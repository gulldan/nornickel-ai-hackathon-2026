//! Personalized PageRank over the kNN graph — the HippoRAG-2 retrieval substrate.
//!
//! The caller resolves a query to seed nodes; PageRank with a teleport vector
//! personalized toward those seeds propagates relevance across the weighted graph
//! and ranks the whole corpus by graph-aware importance (HippoRAG-2). With no
//! seeds it degrades to ordinary PageRank, whose stationary mass on an undirected
//! graph is proportional to weighted degree.
//!
//! Hand-implemented power iteration over the [`Graph`] adjacency — no extra
//! dependency, the rustworkx fork is untouched. Pure, synchronous and
//! deterministic for fixed inputs; the rank vector is a probability distribution
//! (sums to 1), so scores are directly comparable across a request.

use std::cmp::Ordering;

use ahash::AHashMap;
use serde::{Deserialize, Serialize};

use crate::domain::cluster::ComputeStats;
use crate::domain::vector::DocVector;
use crate::domain::Graph;

/// Power-iteration stop threshold: halt once the L1 change between iterations
/// falls below this (well before `max_iterations` for typical graphs).
const CONVERGENCE_TOL: f64 = 1e-9;

/// Optional per-field overrides for [`RankConfig`] (`None` = documented default),
/// mirroring [`crate::domain::ConfigOverrides`].
#[derive(Clone, Debug, Default)]
pub struct RankOverrides {
    pub damping: Option<f64>,
    pub max_iterations: Option<usize>,
    pub top_n: Option<usize>,
}

/// Fully-resolved PageRank configuration (defaults from the proto comments).
#[derive(Clone, Debug, PartialEq)]
pub struct RankConfig {
    /// Damping/teleport factor: probability of following an edge vs. teleporting
    /// to the personalization set. Clamped to `[0, 1]`.
    pub damping: f64,
    /// Power-iteration cap (the loop also stops early on convergence).
    pub max_iterations: usize,
    /// Max ranked nodes returned; `0` = all.
    pub top_n: usize,
}

impl RankConfig {
    /// Apply documented defaults to a set of overrides.
    #[must_use]
    pub fn resolve(overrides: RankOverrides) -> Self {
        Self {
            damping: overrides.damping.unwrap_or(0.85).clamp(0.0, 1.0),
            max_iterations: overrides.max_iterations.unwrap_or(100).max(1),
            top_n: overrides.top_n.unwrap_or(0),
        }
    }
}

impl Default for RankConfig {
    fn default() -> Self {
        Self::resolve(RankOverrides::default())
    }
}

/// One ranked node: its id and Personalized PageRank mass.
#[derive(Clone, Debug, Default, Deserialize, PartialEq, Serialize)]
pub struct RankedNode {
    pub id: String,
    pub score: f64,
}

/// The full ranking response.
#[derive(Clone, Debug, Default, Deserialize, PartialEq, Serialize)]
pub struct RankOutput {
    pub nodes: Vec<RankedNode>,
    pub stats: ComputeStats,
}

/// Rank every node by Personalized PageRank seeded at `seed_ids`.
///
/// `seed_ids` are matched against `docs[i].id` (document ids, or chunk point ids
/// under chunk granularity); unresolved ids are ignored. The result is sorted by
/// score descending, ties broken by id, and truncated to `config.top_n`
/// (`0` = all). `stats.modularity`/`compute_ms` are left at `0` — ranking
/// computes no partition and leaves timing to the caller. Deterministic.
#[must_use]
pub fn rank_nodes(
    docs: &[DocVector],
    graph: &Graph,
    seed_ids: &[String],
    config: &RankConfig,
) -> RankOutput {
    let n = docs.len();
    let stats = ComputeStats {
        document_count: n as u32,
        edge_count: graph.edge_count() as u32,
        modularity: 0.0,
        compute_ms: 0,
    };
    if n == 0 {
        return RankOutput { nodes: Vec::new(), stats };
    }

    let personalization = personalization_vector(docs, n, seed_ids);
    let ranks =
        personalized_pagerank(graph, &personalization, config.damping, config.max_iterations);

    let mut nodes: Vec<RankedNode> = docs
        .iter()
        .zip(&ranks)
        .map(|(doc, &score)| RankedNode { id: doc.id.to_string(), score })
        .collect();
    nodes.sort_by(|a, b| {
        b.score.partial_cmp(&a.score).unwrap_or(Ordering::Equal).then_with(|| a.id.cmp(&b.id))
    });
    if config.top_n > 0 {
        nodes.truncate(config.top_n);
    }
    RankOutput { nodes, stats }
}

/// Teleport distribution: uniform over the resolved `seed_ids`, or uniform over
/// every node when none resolve (ordinary PageRank). Always sums to 1.
fn personalization_vector(docs: &[DocVector], n: usize, seed_ids: &[String]) -> Vec<f64> {
    let index: AHashMap<&str, usize> =
        docs.iter().enumerate().map(|(node, doc)| (doc.id.as_ref(), node)).collect();
    let mut seeds: Vec<usize> =
        seed_ids.iter().filter_map(|id| index.get(id.as_str()).copied()).collect();
    seeds.sort_unstable();
    seeds.dedup();

    if seeds.is_empty() {
        return vec![1.0 / n as f64; n];
    }
    let share = 1.0 / seeds.len() as f64;
    let mut vector = vec![0.0; n];
    for &node in &seeds {
        vector[node] = share;
    }
    vector
}

/// Personalized PageRank by power iteration over the weighted undirected
/// adjacency.
///
/// Each step: every node pushes a `damping` share of its mass to neighbours
/// (split by edge weight / weighted degree); the remaining `1 - damping`, plus
/// any dangling node's mass, teleports back to `personalization`. Mass is
/// conserved, so the vector stays a distribution. Iterates until the L1 change
/// drops below [`CONVERGENCE_TOL`] or `max_iterations` is hit.
fn personalized_pagerank(
    graph: &Graph,
    personalization: &[f64],
    damping: f64,
    max_iterations: usize,
) -> Vec<f64> {
    let n = graph.node_count();
    if n == 0 {
        return Vec::new();
    }
    let degrees: Vec<f64> = (0..n).map(|node| graph.degree(node)).collect();
    let mut rank = personalization.to_vec();

    for _ in 0..max_iterations {
        let dangling: f64 = rank
            .iter()
            .zip(&degrees)
            .filter(|(_, &degree)| degree <= 0.0)
            .map(|(&mass, _)| mass)
            .sum();
        let teleport = damping.mul_add(dangling, 1.0 - damping);
        let mut next: Vec<f64> = personalization.iter().map(|&share| teleport * share).collect();

        // One pass over the undirected edges pushes mass both ways; every node
        // touched by an edge has degree > 0, so the divisions are safe.
        for &(a, b, weight) in graph.edges() {
            next[b] += damping * rank[a] * weight / degrees[a];
            next[a] += damping * rank[b] * weight / degrees[b];
        }

        let delta: f64 = next.iter().zip(&rank).map(|(&x, &y)| (x - y).abs()).sum();
        rank = next;
        if delta < CONVERGENCE_TOL {
            break;
        }
    }
    rank
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

    fn score_of(out: &RankOutput, id: &str) -> f64 {
        out.nodes.iter().find(|node| node.id == id).map_or(0.0, |node| node.score)
    }

    #[test]
    fn uniform_pagerank_ranks_the_hub_highest() {
        // Star: hub n0 linked to four leaves. Uniform PageRank mass on an
        // undirected graph tracks weighted degree, so the hub leads.
        let documents = docs(&["n0", "n1", "n2", "n3", "n4"]);
        let graph = Graph::from_edges(5, vec![(0, 1, 1.0), (0, 2, 1.0), (0, 3, 1.0), (0, 4, 1.0)]);

        let out = rank_nodes(&documents, &graph, &[], &RankConfig::default());

        assert_eq!(out.nodes.len(), 5);
        assert_eq!(out.nodes[0].id, "n0", "the hub ranks first");
        let hub = score_of(&out, "n0");
        for leaf in ["n1", "n2", "n3", "n4"] {
            assert!(hub > score_of(&out, leaf), "hub outranks every leaf");
        }
        // Mass is a probability distribution.
        let total: f64 = out.nodes.iter().map(|node| node.score).sum();
        assert!((total - 1.0).abs() < 1e-9);
    }

    #[test]
    fn seeded_ppr_concentrates_in_the_seed_neighborhood() {
        // Triangle A {0,1,2} and triangle B {3,4,5} joined by the weak bridge
        // 2-3. Seeding A keeps almost all mass on A's nodes.
        let documents = docs(&["n0", "n1", "n2", "n3", "n4", "n5"]);
        let graph = Graph::from_edges(
            6,
            vec![
                (0, 1, 1.0),
                (0, 2, 1.0),
                (1, 2, 1.0),
                (3, 4, 1.0),
                (3, 5, 1.0),
                (4, 5, 1.0),
                (2, 3, 0.1),
            ],
        );

        let out = rank_nodes(&documents, &graph, &["n0".to_owned()], &RankConfig::default());

        assert_eq!(out.nodes[0].id, "n0", "the seed ranks first");
        let a_min =
            ["n0", "n1", "n2"].into_iter().map(|id| score_of(&out, id)).fold(f64::MAX, f64::min);
        let b_max = ["n3", "n4", "n5"].into_iter().map(|id| score_of(&out, id)).fold(0.0, f64::max);
        assert!(a_min > b_max, "every node in the seed's community outranks the far community");

        // Seeding lifts the seed far above its uniform-PageRank mass.
        let uniform = rank_nodes(&documents, &graph, &[], &RankConfig::default());
        assert!(score_of(&out, "n0") > score_of(&uniform, "n0"));
    }

    #[test]
    fn dangling_seed_keeps_all_mass_on_the_seed() {
        // Edgeless graph: every node is dangling, so all teleport mass returns to
        // the personalization set — a seeded run leaves the seed with mass 1.
        let documents = docs(&["n0", "n1", "n2"]);
        let graph = Graph::from_edges(3, Vec::new());

        let out = rank_nodes(&documents, &graph, &["n1".to_owned()], &RankConfig::default());
        assert!((score_of(&out, "n1") - 1.0).abs() < 1e-9);
        assert!(score_of(&out, "n0") < 1e-9);
        assert!(score_of(&out, "n2") < 1e-9);
    }

    #[test]
    fn unresolved_seeds_fall_back_to_uniform() {
        let documents = docs(&["n0", "n1", "n2", "n3"]);
        let graph = Graph::from_edges(4, vec![(0, 1, 1.0), (1, 2, 1.0), (2, 3, 1.0)]);

        let seeded =
            rank_nodes(&documents, &graph, &["missing".to_owned()], &RankConfig::default());
        let uniform = rank_nodes(&documents, &graph, &[], &RankConfig::default());
        for id in ["n0", "n1", "n2", "n3"] {
            assert!((score_of(&seeded, id) - score_of(&uniform, id)).abs() < 1e-12);
        }
    }

    #[test]
    fn top_n_truncates_and_run_is_deterministic() {
        let documents = docs(&["n0", "n1", "n2", "n3", "n4"]);
        let graph = Graph::from_edges(5, vec![(0, 1, 1.0), (0, 2, 1.0), (0, 3, 1.0), (0, 4, 1.0)]);
        let config = RankConfig { top_n: 2, ..RankConfig::default() };

        let first = rank_nodes(&documents, &graph, &["n0".to_owned()], &config);
        let second = rank_nodes(&documents, &graph, &["n0".to_owned()], &config);
        assert_eq!(first.nodes.len(), 2, "top_n caps the returned nodes");
        assert_eq!(first, second, "ranking is deterministic");
    }
}
