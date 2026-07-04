//! Connecting-path reasoning over the kNN graph — the PathRAG retrieval substrate.
//!
//! Given a query's resolved source and target nodes, this enumerates the shortest
//! chains linking the two sets (unweighted BFS via the rustworkx-core fork),
//! scores each by edge-weight flow with a length penalty, dedups undirected
//! twins and returns the top reasoning paths. Those chains are the structural
//! evidence PathRAG feeds an LLM to explain *how* two pieces of context relate.
//!
//! Pure, synchronous and deterministic: enumeration is unweighted (fewest hops),
//! scoring uses the graph's cosine edge weights, and the final order is total
//! (score, then node ids). The fork is used read-only and never modified.

use std::cmp::Ordering;
use std::convert::Infallible;

use ahash::{AHashMap, AHashSet};
use petgraph::graph::{NodeIndex, UnGraph};
use rustworkx_core::shortest_path::all_shortest_paths;
use serde::{Deserialize, Serialize};

use crate::domain::cluster::ComputeStats;
use crate::domain::vector::DocVector;
use crate::domain::Graph;

/// Length penalty per extra hop in [`path_score`]: the mean edge weight is scaled
/// by `LENGTH_DECAY^(hops - 1)`, so a one-hop chain is undamped and each further
/// hop discounts it — PathRAG's distance-aware flow pruning.
const LENGTH_DECAY: f64 = 0.8;

/// Optional per-field overrides for [`PathConfig`] (`None` = documented default).
#[derive(Clone, Debug, Default)]
pub struct PathOverrides {
    pub max_paths: Option<usize>,
    pub max_hops: Option<usize>,
}

/// Fully-resolved path-reasoning configuration (defaults from the proto comments).
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct PathConfig {
    /// Max reasoning paths returned.
    pub max_paths: usize,
    /// Longest path considered, in edges; chains beyond this are pruned.
    pub max_hops: usize,
}

impl PathConfig {
    /// Apply documented defaults to a set of overrides.
    #[must_use]
    pub fn resolve(overrides: PathOverrides) -> Self {
        Self {
            max_paths: overrides.max_paths.unwrap_or(10),
            max_hops: overrides.max_hops.unwrap_or(4).max(1),
        }
    }
}

impl Default for PathConfig {
    fn default() -> Self {
        Self::resolve(PathOverrides::default())
    }
}

/// One connecting chain: the ordered node ids (source → target) and its score.
#[derive(Clone, Debug, Default, Deserialize, PartialEq, Serialize)]
pub struct GraphPath {
    pub node_ids: Vec<String>,
    pub score: f64,
}

/// The full path-reasoning response.
#[derive(Clone, Debug, Default, Deserialize, PartialEq, Serialize)]
pub struct PathOutput {
    pub paths: Vec<GraphPath>,
    pub stats: ComputeStats,
}

/// Find the top connecting paths from any `source_ids` node to any `target_ids`
/// node.
///
/// Ids are matched against `docs[i].id`; unresolved ids are ignored, and an empty
/// resolved source or target set yields no paths. Paths are the unweighted
/// shortest chains per endpoint pair, pruned to `config.max_hops`, scored by
/// [`path_score`], deduped (a chain and its reverse count once) and returned
/// sorted by score descending — ties broken by node ids — truncated to
/// `config.max_paths`. `stats.modularity`/`compute_ms` stay `0` (no partition,
/// timing left to the caller). Deterministic.
#[must_use]
pub fn connecting_paths(
    docs: &[DocVector],
    graph: &Graph,
    source_ids: &[String],
    target_ids: &[String],
    config: &PathConfig,
) -> PathOutput {
    let stats = ComputeStats {
        document_count: docs.len() as u32,
        edge_count: graph.edge_count() as u32,
        modularity: 0.0,
        compute_ms: 0,
    };
    let index: AHashMap<&str, usize> =
        docs.iter().enumerate().map(|(node, doc)| (doc.id.as_ref(), node)).collect();
    let sources = resolve_nodes(source_ids, &index);
    let targets = resolve_nodes(target_ids, &index);
    if sources.is_empty() || targets.is_empty() {
        return PathOutput { paths: Vec::new(), stats };
    }

    let pet = graph.to_petgraph();
    let mut seen: AHashSet<Vec<usize>> = AHashSet::new();
    let mut paths: Vec<GraphPath> = Vec::new();
    for &source in &sources {
        for &target in &targets {
            if source == target {
                continue;
            }
            for indices in shortest_index_paths(&pet, source, target) {
                let hops = indices.len().saturating_sub(1);
                if hops == 0 || hops > config.max_hops || !seen.insert(canonical_key(&indices)) {
                    continue;
                }
                if let Some(score) = path_score(graph, &indices) {
                    let node_ids = indices.iter().map(|&node| docs[node].id.to_string()).collect();
                    paths.push(GraphPath { node_ids, score });
                }
            }
        }
    }

    paths.sort_by(|a, b| {
        b.score
            .partial_cmp(&a.score)
            .unwrap_or(Ordering::Equal)
            .then_with(|| a.node_ids.cmp(&b.node_ids))
    });
    paths.truncate(config.max_paths);
    PathOutput { paths, stats }
}

/// Resolve a list of ids to a sorted, deduped list of node indices.
fn resolve_nodes(ids: &[String], index: &AHashMap<&str, usize>) -> Vec<usize> {
    let mut nodes: Vec<usize> =
        ids.iter().filter_map(|id| index.get(id.as_str()).copied()).collect();
    nodes.sort_unstable();
    nodes.dedup();
    nodes
}

/// Every unweighted shortest path from `source` to `target` as node-index
/// sequences (endpoints inclusive); empty when the target is unreachable.
fn shortest_index_paths(pet: &UnGraph<(), f64>, source: usize, target: usize) -> Vec<Vec<usize>> {
    all_shortest_paths(pet, NodeIndex::new(source), NodeIndex::new(target), |_| {
        Ok::<usize, Infallible>(1)
    })
    .unwrap_or_default()
    .into_iter()
    .map(|path| path.iter().map(|node| node.index()).collect())
    .collect()
}

/// Direction-independent dedup key: a path and its reverse share one key, since a
/// connecting chain is undirected.
fn canonical_key(seq: &[usize]) -> Vec<usize> {
    let reversed: Vec<usize> = seq.iter().rev().copied().collect();
    if reversed.as_slice() < seq {
        reversed
    } else {
        seq.to_vec()
    }
}

/// Flow/length score: the mean edge weight along the path times
/// `LENGTH_DECAY^(hops - 1)`, so short, high-weight chains rank highest. `None`
/// if a consecutive pair is not adjacent (defensive; shortest paths always are).
fn path_score(graph: &Graph, path: &[usize]) -> Option<f64> {
    if path.len() < 2 {
        return None;
    }
    let weight_sum: f64 =
        path.windows(2).map(|pair| edge_weight(graph, pair[0], pair[1])).sum::<Option<f64>>()?;
    let hops = (path.len() - 1) as f64;
    let decay = LENGTH_DECAY.powi((path.len() - 2) as i32);
    Some((weight_sum / hops) * decay)
}

/// Weight of the undirected edge `a-b`, if present.
fn edge_weight(graph: &Graph, a: usize, b: usize) -> Option<f64> {
    graph.neighbors(a).iter().find_map(|&(node, weight)| (node == b).then_some(weight))
}

#[cfg(test)]
mod tests {
    use super::*;

    fn doc(id: &str) -> DocVector {
        DocVector {
            id: id.into(),
            vector: vec![1.0, 0.0],
            rep_text: String::new().into(),
            chunk_count: 1,
            filename: None,
        }
    }

    fn docs(ids: &[&str]) -> Vec<DocVector> {
        ids.iter().map(|id| doc(id)).collect()
    }

    fn ids(values: &[&str]) -> Vec<String> {
        values.iter().map(|value| (*value).to_owned()).collect()
    }

    /// Triangle A {0,1,2} and triangle B {3,4,5} joined by the single bridge 2-3.
    fn barbell() -> (Vec<DocVector>, Graph) {
        let edges = vec![
            (0, 1, 1.0),
            (0, 2, 1.0),
            (1, 2, 1.0),
            (3, 4, 1.0),
            (3, 5, 1.0),
            (4, 5, 1.0),
            (2, 3, 1.0),
        ];
        (docs(&["n0", "n1", "n2", "n3", "n4", "n5"]), Graph::from_edges(6, edges))
    }

    /// Two equal 2-hop chains from n0 to n3: n0-n1-n3 and n0-n2-n3.
    fn diamond() -> (Vec<DocVector>, Graph) {
        let edges = vec![(0, 1, 1.0), (0, 2, 1.0), (1, 3, 1.0), (2, 3, 1.0)];
        (docs(&["n0", "n1", "n2", "n3"]), Graph::from_edges(4, edges))
    }

    #[test]
    fn finds_the_connecting_path_between_two_clusters() {
        let (documents, graph) = barbell();
        let out = connecting_paths(
            &documents,
            &graph,
            &ids(&["n0"]),
            &ids(&["n5"]),
            &PathConfig::default(),
        );

        assert_eq!(out.stats.document_count, 6);
        assert_eq!(out.paths.len(), 1, "one shortest chain crosses the single bridge");
        let path = &out.paths[0];
        assert_eq!(path.node_ids, vec!["n0", "n2", "n3", "n5"]);
        // 3 hops, all weights 1.0: mean 1.0 * 0.8^2 = 0.64.
        assert!((path.score - 0.64).abs() < 1e-9);
    }

    #[test]
    fn max_hops_prunes_long_paths() {
        let (documents, graph) = barbell();
        let config = PathConfig { max_hops: 2, ..PathConfig::default() };
        let out = connecting_paths(&documents, &graph, &ids(&["n0"]), &ids(&["n5"]), &config);
        assert!(out.paths.is_empty(), "the only chain is 3 hops, beyond the 2-hop cap");
    }

    #[test]
    fn unreachable_targets_yield_no_paths() {
        let documents = docs(&["n0", "n1", "n2", "n3"]);
        let graph = Graph::from_edges(4, vec![(0, 1, 1.0), (2, 3, 1.0)]); // two components
        let out = connecting_paths(
            &documents,
            &graph,
            &ids(&["n0"]),
            &ids(&["n3"]),
            &PathConfig::default(),
        );
        assert!(out.paths.is_empty());
        assert_eq!(out.stats.edge_count, 2);
    }

    #[test]
    fn enumerates_all_shortest_paths_and_breaks_ties_by_id() {
        let (documents, graph) = diamond();
        let out = connecting_paths(
            &documents,
            &graph,
            &ids(&["n0"]),
            &ids(&["n3"]),
            &PathConfig::default(),
        );

        assert_eq!(out.paths.len(), 2, "both equal-length chains are returned");
        // Equal score (mean 1.0 * 0.8 = 0.8); ties broken by node ids ascending.
        assert_eq!(out.paths[0].node_ids, vec!["n0", "n1", "n3"]);
        assert_eq!(out.paths[1].node_ids, vec!["n0", "n2", "n3"]);
        assert!((out.paths[0].score - 0.8).abs() < 1e-9);
    }

    #[test]
    fn max_paths_truncates_to_top_k() {
        let (documents, graph) = diamond();
        let config = PathConfig { max_paths: 1, ..PathConfig::default() };
        let out = connecting_paths(&documents, &graph, &ids(&["n0"]), &ids(&["n3"]), &config);
        assert_eq!(out.paths.len(), 1);
        assert_eq!(out.paths[0].node_ids, vec!["n0", "n1", "n3"]);
    }

    #[test]
    fn overlapping_source_target_sets_dedup_reversed_paths() {
        let (documents, graph) = diamond();
        let both = ids(&["n0", "n3"]);
        let out = connecting_paths(&documents, &graph, &both, &both, &PathConfig::default());
        // (n0→n3) and (n3→n0) describe the same two undirected chains, not four.
        assert_eq!(out.paths.len(), 2);
    }

    #[test]
    fn paths_are_deterministic() {
        let (documents, graph) = diamond();
        let a = connecting_paths(
            &documents,
            &graph,
            &ids(&["n0"]),
            &ids(&["n3"]),
            &PathConfig::default(),
        );
        let b = connecting_paths(
            &documents,
            &graph,
            &ids(&["n0"]),
            &ids(&["n3"]),
            &PathConfig::default(),
        );
        assert_eq!(a, b);
    }
}
