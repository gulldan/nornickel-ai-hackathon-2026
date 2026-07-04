//! Cross-community bridge scoring — the discovery-layer substrate for hypothesis
//! generation (Фаза 3 «Фабрика гипотез»).
//!
//! Novelty lives in the edges *between* Leiden communities, not inside them: a
//! pair of themes that is semantically affine yet has few direct links is a
//! structural hole — a candidate non-obvious connection. For every surviving
//! pair we score three facets (maverick recombination, weak-tie vanguard,
//! boundary bridging-centrality), attach ABC mediator documents that ground a
//! hypothesis, and rank by a weighted composite.
//!
//! This module is pure and synchronous: it consumes the same prepared
//! `docs` + [`Graph`] + community partition the cluster pipeline produces and
//! emits a deterministic [`BridgeOutput`]. The betweenness/shortest-path
//! primitives come from the rustworkx-core fork via a `petgraph` view of the
//! graph; the fork is never modified.

use std::cmp::Ordering;
use std::convert::Infallible;

use ahash::{AHashMap, AHashSet};
use petgraph::graph::{NodeIndex, UnGraph};
use rustworkx_core::centrality::betweenness_centrality;
use rustworkx_core::shortest_path::all_shortest_paths;
use serde::{Deserialize, Serialize};

use crate::domain::cluster::ComputeStats;
use crate::domain::fingerprint::cluster_fingerprint;
use crate::domain::metrics::{modularity_terms, round4};
use crate::domain::vector::{collapse_whitespace, l2_normalize, DocVector};
use crate::domain::Graph;

/// `parallel_threshold` for [`betweenness_centrality`]: below this node count the
/// fork runs single-threaded (deterministic); the value is the fork's documented
/// default cut-over point.
const BETWEENNESS_PARALLEL_THRESHOLD: usize = 50;

/// Maximum mediator snippet length, matching `Representative` in the cluster
/// pipeline.
const SNIPPET_CHARS: usize = 360;

/// Optional per-field overrides for [`BridgeConfig`] (`None` = documented
/// default), mirroring [`crate::domain::ConfigOverrides`].
#[derive(Clone, Debug, Default)]
pub struct BridgeOverrides {
    pub top_n: Option<usize>,
    pub min_affinity: Option<f64>,
    pub max_mediators: Option<usize>,
    pub min_convergence: Option<usize>,
    pub w_maverick: Option<f64>,
    pub w_bridging: Option<f64>,
    pub w_vanguard: Option<f64>,
}

/// Fully-resolved bridge-scoring configuration (defaults from the proto comments).
#[derive(Clone, Debug, PartialEq)]
pub struct BridgeConfig {
    /// Max bridges returned.
    pub top_n: usize,
    /// Min centroid cosine for a pair to be considered (bounds the `O(k²)` work).
    pub min_affinity: f64,
    /// ABC mediators returned per bridge.
    pub max_mediators: usize,
    /// Drop bridges with fewer mediating nodes (`>= 2` = convergent).
    pub min_convergence: usize,
    /// Composite weight: distant recombination.
    pub w_maverick: f64,
    /// Composite weight: boundary bridging-centrality.
    pub w_bridging: f64,
    /// Composite weight: weak-tie reinforcement.
    pub w_vanguard: f64,
}

impl BridgeConfig {
    /// Apply documented defaults to a set of overrides.
    #[must_use]
    pub fn resolve(overrides: BridgeOverrides) -> Self {
        Self {
            top_n: overrides.top_n.unwrap_or(50),
            min_affinity: overrides.min_affinity.unwrap_or(0.15).clamp(-1.0, 1.0),
            max_mediators: overrides.max_mediators.unwrap_or(3),
            min_convergence: overrides.min_convergence.unwrap_or(1),
            w_maverick: overrides.w_maverick.unwrap_or(1.0),
            w_bridging: overrides.w_bridging.unwrap_or(1.0),
            w_vanguard: overrides.w_vanguard.unwrap_or(0.5),
        }
    }
}

impl Default for BridgeConfig {
    fn default() -> Self {
        Self::resolve(BridgeOverrides::default())
    }
}

/// An ABC "B" document linking the two endpoints of a bridge.
#[derive(Clone, Debug, Default, Deserialize, Eq, PartialEq, Serialize)]
pub struct Mediator {
    pub document_id: String,
    pub filename: String,
    pub snippet: String,
}

/// Per-bridge facet scores (rounded to 4 decimals, like the cluster metrics).
#[derive(Clone, Debug, Default, Deserialize, PartialEq, Serialize)]
pub struct BridgeScores {
    /// Centroid cosine between the two communities.
    pub affinity: f64,
    /// Realized cross edges / (`|A|*|B|`).
    pub link_density: f64,
    /// `max(0, affinity) * (1 - link_density)`: structural-hole recombination.
    pub maverick: f64,
    /// Weak-tie reinforcement (affine pair with only a few weak existing links).
    pub vanguard: f64,
    /// Mean boundary betweenness × bridging coefficient.
    pub bridging_centrality: f64,
    /// Number of distinct mediating documents found.
    pub convergence: u32,
    /// Weighted composite used for ranking.
    pub composite: f64,
}

/// One scored cross-community bridge.
#[derive(Clone, Debug, Default, Deserialize, PartialEq, Serialize)]
pub struct Bridge {
    /// `sha1[:16]` of the sorted pair of community fingerprints.
    pub fingerprint: String,
    /// Fingerprint of community A.
    pub community_a: String,
    /// Fingerprint of community B.
    pub community_b: String,
    /// Document ids in A (sorted).
    pub members_a: Vec<String>,
    /// Document ids in B (sorted).
    pub members_b: Vec<String>,
    /// Anchor document in A (closest to B's centroid).
    pub endpoint_a: String,
    /// Anchor document in B (closest to A's centroid).
    pub endpoint_b: String,
    pub mediators: Vec<Mediator>,
    pub scores: BridgeScores,
}

/// The full bridge-scoring response.
#[derive(Clone, Debug, Default, Deserialize, PartialEq, Serialize)]
pub struct BridgeOutput {
    pub bridges: Vec<Bridge>,
    pub stats: ComputeStats,
}

/// Per-pair realized-edge aggregate, accumulated in a single pass over the graph.
#[derive(Default)]
struct CrossAgg {
    edges: u64,
    weight: f64,
    /// Nodes incident to a cross edge between the two communities.
    boundary: AHashSet<usize>,
}

/// Precomputed, partition-wide context shared by every pair so the inner loop
/// stays small and allocation-free beyond what each bridge needs.
struct Ctx<'a> {
    docs: &'a [DocVector],
    doc_filenames: &'a AHashMap<Box<str>, Box<str>>,
    graph: &'a Graph,
    pet: &'a UnGraph<(), f64>,
    normalized: &'a [Vec<f32>],
    centroids: &'a [Vec<f32>],
    communities: &'a [Vec<usize>],
    cross: &'a AHashMap<(usize, usize), CrossAgg>,
    betweenness: &'a [f64],
    bridging_coeff: &'a [f64],
    comm_members: &'a [Vec<String>],
    comm_fp: &'a [String],
    config: &'a BridgeConfig,
}

/// Score every affine cross-community pair and return the top bridges.
///
/// `communities` are node-index lists over `docs`/`graph` (same index space the
/// cluster pipeline uses); `doc_filenames` backfills filenames the chunk payload
/// lacked. The result is deterministic for a fixed input.
#[must_use]
pub fn score_bridges(
    docs: &[DocVector],
    doc_filenames: &AHashMap<Box<str>, Box<str>>,
    graph: &Graph,
    communities: &[Vec<usize>],
    config: &BridgeConfig,
) -> BridgeOutput {
    let n = docs.len();
    let stats = ComputeStats {
        document_count: n as u32,
        edge_count: graph.edge_count() as u32,
        modularity: modularity_terms(graph, communities).0,
        compute_ms: 0,
    };
    if communities.len() < 2 || n == 0 {
        return BridgeOutput { bridges: Vec::new(), stats };
    }

    let normalized: Vec<Vec<f32>> = docs.iter().map(|doc| l2_normalize(&doc.vector)).collect();
    let dim = normalized.first().map_or(0, Vec::len);
    let centroids: Vec<Vec<f32>> =
        communities.iter().map(|comm| centroid(comm, &normalized, dim)).collect();

    let mut node_comm = vec![usize::MAX; n];
    for (ci, comm) in communities.iter().enumerate() {
        for &node in comm {
            if node < n {
                node_comm[node] = ci;
            }
        }
    }
    let cross = cross_aggregates(graph, &node_comm);

    let pet = graph.to_petgraph();
    let betweenness: Vec<f64> =
        betweenness_centrality(&pet, false, true, BETWEENNESS_PARALLEL_THRESHOLD)
            .into_iter()
            .map(|score| score.unwrap_or(0.0))
            .collect();
    let bridging_coeff: Vec<f64> = (0..n).map(|node| bridging_coefficient(graph, node)).collect();

    let comm_members: Vec<Vec<String>> =
        communities.iter().map(|comm| sorted_member_ids(comm, docs)).collect();
    let comm_fp: Vec<String> = comm_members.iter().map(|ids| cluster_fingerprint(ids)).collect();

    let ctx = Ctx {
        docs,
        doc_filenames,
        graph,
        pet: &pet,
        normalized: &normalized,
        centroids: &centroids,
        communities,
        cross: &cross,
        betweenness: &betweenness,
        bridging_coeff: &bridging_coeff,
        comm_members: &comm_members,
        comm_fp: &comm_fp,
        config,
    };

    let mut bridges = Vec::new();
    for i in 0..communities.len() {
        for j in (i + 1)..communities.len() {
            if let Some(bridge) = score_pair(&ctx, i, j) {
                bridges.push(bridge);
            }
        }
    }

    bridges.sort_by(|a, b| {
        b.scores
            .composite
            .partial_cmp(&a.scores.composite)
            .unwrap_or(Ordering::Equal)
            .then_with(|| a.fingerprint.cmp(&b.fingerprint))
    });
    bridges.truncate(config.top_n);
    BridgeOutput { bridges, stats }
}

/// Score one ordered pair `(i, j)` (A = `i`, B = `j`); `None` when the pair is
/// below the affinity floor or fails the convergence gate.
fn score_pair(ctx: &Ctx<'_>, i: usize, j: usize) -> Option<Bridge> {
    let affinity = dot(&ctx.centroids[i], &ctx.centroids[j]).clamp(-1.0, 1.0);
    if affinity < ctx.config.min_affinity {
        return None;
    }

    let default_agg = CrossAgg::default();
    let agg = ctx.cross.get(&(i, j)).unwrap_or(&default_agg);
    let positive_affinity = affinity.max(0.0);

    let denom = (ctx.communities[i].len() * ctx.communities[j].len()) as f64;
    let link_density = if denom > 0.0 { (agg.edges as f64 / denom).min(1.0) } else { 0.0 };
    let maverick = positive_affinity * (1.0 - link_density);
    // vanguard rewards an affine pair held together only by a few *weak* ties:
    // `1/(1+cross_weight)` decays as those existing links strengthen, so it is a
    // proxy for "almost a structural hole" rather than a realized connection.
    let vanguard =
        if agg.edges == 0 { 0.0 } else { positive_affinity * (1.0 / (1.0 + agg.weight)) };
    let bridging_centrality = boundary_bridging(ctx, &agg.boundary);

    let endpoint_a = best_endpoint(&ctx.communities[i], ctx.normalized, &ctx.centroids[j])?;
    let endpoint_b = best_endpoint(&ctx.communities[j], ctx.normalized, &ctx.centroids[i])?;

    let mediator_nodes = find_mediators(ctx, endpoint_a, endpoint_b);
    let convergence = mediator_nodes.len();
    if convergence < ctx.config.min_convergence {
        return None;
    }
    let mediators: Vec<Mediator> = mediator_nodes
        .iter()
        .take(ctx.config.max_mediators)
        .map(|&node| make_mediator(node, ctx.docs, ctx.doc_filenames))
        .collect();

    let composite = ctx.config.w_maverick.mul_add(
        maverick,
        ctx.config.w_bridging.mul_add(bridging_centrality, ctx.config.w_vanguard * vanguard),
    );

    let fp_a = ctx.comm_fp[i].clone();
    let fp_b = ctx.comm_fp[j].clone();
    let fingerprint = cluster_fingerprint(&[fp_a.clone(), fp_b.clone()]);

    Some(Bridge {
        fingerprint,
        community_a: fp_a,
        community_b: fp_b,
        members_a: ctx.comm_members[i].clone(),
        members_b: ctx.comm_members[j].clone(),
        endpoint_a: ctx.docs[endpoint_a].id.to_string(),
        endpoint_b: ctx.docs[endpoint_b].id.to_string(),
        mediators,
        scores: BridgeScores {
            affinity: round4(affinity),
            link_density: round4(link_density),
            maverick: round4(maverick),
            vanguard: round4(vanguard),
            bridging_centrality: round4(bridging_centrality),
            convergence: convergence as u32,
            composite: round4(composite),
        },
    })
}

/// Mean of `betweenness × bridging_coefficient` over the boundary nodes (`0`
/// when the pair has no realized cross edges).
fn boundary_bridging(ctx: &Ctx<'_>, boundary: &AHashSet<usize>) -> f64 {
    if boundary.is_empty() {
        return 0.0;
    }
    let sum: f64 =
        boundary.iter().map(|&node| ctx.betweenness[node] * ctx.bridging_coeff[node]).sum();
    sum / boundary.len() as f64
}

/// Distinct ABC mediator nodes between two endpoints, ordered by betweenness
/// (then index) descending: graph common-neighbours first, else the internal
/// nodes of the shortest path(s) between them.
fn find_mediators(ctx: &Ctx<'_>, endpoint_a: usize, endpoint_b: usize) -> Vec<usize> {
    let mut nodes = common_neighbors(ctx.graph, endpoint_a, endpoint_b);
    if nodes.is_empty() {
        nodes = shortest_path_internal(ctx.pet, endpoint_a, endpoint_b);
    }
    nodes.sort_by(|&x, &y| {
        ctx.betweenness[y]
            .partial_cmp(&ctx.betweenness[x])
            .unwrap_or(Ordering::Equal)
            .then(x.cmp(&y))
    });
    nodes
}

/// Graph common neighbours of `a` and `b`, excluding the endpoints themselves.
fn common_neighbors(graph: &Graph, a: usize, b: usize) -> Vec<usize> {
    let nb: AHashSet<usize> = graph.neighbors(b).iter().map(|&(node, _)| node).collect();
    let mut out: Vec<usize> = graph
        .neighbors(a)
        .iter()
        .map(|&(node, _)| node)
        .filter(|&node| node != a && node != b && nb.contains(&node))
        .collect();
    out.sort_unstable();
    out.dedup();
    out
}

/// Internal nodes (endpoints excluded) of every unweighted shortest path between
/// `a` and `b`; empty when they are unreachable.
fn shortest_path_internal(pet: &UnGraph<(), f64>, a: usize, b: usize) -> Vec<usize> {
    let paths = all_shortest_paths(pet, NodeIndex::new(a), NodeIndex::new(b), |_| {
        Ok::<usize, Infallible>(1)
    })
    .unwrap_or_default();
    let mut seen: AHashSet<usize> = AHashSet::new();
    for path in &paths {
        if path.len() <= 2 {
            continue;
        }
        for node in &path[1..path.len() - 1] {
            seen.insert(node.index());
        }
    }
    seen.into_iter().collect()
}

/// Count cross edges, sum their weight and collect boundary nodes per community
/// pair in one pass. Edges touching an uncovered node (`usize::MAX`) are skipped.
fn cross_aggregates(graph: &Graph, node_comm: &[usize]) -> AHashMap<(usize, usize), CrossAgg> {
    let mut cross: AHashMap<(usize, usize), CrossAgg> = AHashMap::new();
    for &(a, b, weight) in graph.edges() {
        let (ca, cb) = (node_comm[a], node_comm[b]);
        if ca == usize::MAX || cb == usize::MAX || ca == cb {
            continue;
        }
        let key = if ca < cb { (ca, cb) } else { (cb, ca) };
        let agg = cross.entry(key).or_default();
        agg.edges += 1;
        agg.weight += weight;
        agg.boundary.insert(a);
        agg.boundary.insert(b);
    }
    cross
}

/// L2-normalized sum of the (already normalized) member vectors.
fn centroid(comm: &[usize], normalized: &[Vec<f32>], dim: usize) -> Vec<f32> {
    let mut sum = vec![0.0_f64; dim];
    for &node in comm {
        for (slot, &value) in sum.iter_mut().zip(normalized[node].iter()) {
            *slot += f64::from(value);
        }
    }
    let raw: Vec<f32> = sum.iter().map(|&value| value as f32).collect();
    l2_normalize(&raw)
}

/// Node in `comm` whose vector is most cosine-similar to `target` (ties broken by
/// the smaller index for determinism).
fn best_endpoint(comm: &[usize], normalized: &[Vec<f32>], target: &[f32]) -> Option<usize> {
    comm.iter()
        .copied()
        .map(|node| (node, dot(&normalized[node], target)))
        .reduce(
            |best, cur| {
                if cur.1 > best.1 || (cur.1 == best.1 && cur.0 < best.0) {
                    cur
                } else {
                    best
                }
            },
        )
        .map(|(node, _)| node)
}

/// Hwang bridging coefficient: `(1/deg) / Σ_nbr (1/deg_nbr)` on the unweighted
/// (topological) degree; `0` when the node is isolated or every neighbour is.
fn bridging_coefficient(graph: &Graph, node: usize) -> f64 {
    let deg = graph.neighbors(node).len();
    if deg == 0 {
        return 0.0;
    }
    let denom: f64 = graph
        .neighbors(node)
        .iter()
        .map(|&(nbr, _)| {
            let nbr_deg = graph.neighbors(nbr).len();
            if nbr_deg == 0 {
                0.0
            } else {
                1.0 / nbr_deg as f64
            }
        })
        .sum();
    if denom == 0.0 {
        0.0
    } else {
        (1.0 / deg as f64) / denom
    }
}

/// Sorted document ids of a community.
fn sorted_member_ids(comm: &[usize], docs: &[DocVector]) -> Vec<String> {
    let mut ids: Vec<String> = comm.iter().map(|&node| docs[node].id.to_string()).collect();
    ids.sort_unstable();
    ids
}

/// Build a [`Mediator`] for a node, reusing the cluster snippet conventions.
fn make_mediator(
    node: usize,
    docs: &[DocVector],
    doc_filenames: &AHashMap<Box<str>, Box<str>>,
) -> Mediator {
    let doc = &docs[node];
    let filename = doc
        .filename
        .as_ref()
        .map(ToString::to_string)
        .or_else(|| doc_filenames.get(&doc.id).map(ToString::to_string))
        .unwrap_or_default();
    let snippet = collapse_whitespace(&doc.rep_text).chars().take(SNIPPET_CHARS).collect();
    Mediator { document_id: doc.id.to_string(), filename, snippet }
}

/// Dot product of two `f32` slices accumulated in `f64` (cosine for unit inputs).
fn dot(a: &[f32], b: &[f32]) -> f64 {
    a.iter().zip(b.iter()).map(|(&x, &y)| f64::from(x) * f64::from(y)).sum()
}

#[cfg(test)]
mod tests {
    use super::*;

    fn doc(id: &str, vector: &[f32]) -> DocVector {
        DocVector {
            id: id.into(),
            vector: vector.to_vec(),
            rep_text: format!("representative prose for {id}").into(),
            chunk_count: 1,
            filename: Some(format!("{id}.pdf").into()),
        }
    }

    /// Two affine communities {0,1,2} (A) and {3,4,5} (B) with a single weak
    /// direct link (2-3) plus a mediator node 6 adjacent to each side's endpoint.
    fn fixture() -> (Vec<DocVector>, Graph, Vec<Vec<usize>>) {
        let docs = vec![
            doc("a0", &[1.0, 0.5]),
            doc("a1", &[1.0, 0.55]),
            doc("a2", &[1.0, 0.8]), // leans toward B → endpoint_a
            doc("b0", &[0.8, 1.0]), // leans toward A → endpoint_b
            doc("b1", &[0.55, 1.0]),
            doc("b2", &[0.5, 1.0]),
            doc("med", &[0.7, 0.7]),
        ];
        let edges = vec![
            (0, 1, 1.0),
            (1, 2, 1.0),
            (3, 4, 1.0),
            (4, 5, 1.0),
            (2, 6, 1.0), // mediator ↔ endpoint_a
            (3, 6, 1.0), // mediator ↔ endpoint_b
            (2, 3, 0.1), // single weak cross link
        ];
        let graph = Graph::from_edges(7, edges);
        let communities = vec![vec![0, 1, 2], vec![3, 4, 5]]; // node 6 uncovered
        (docs, graph, communities)
    }

    #[test]
    fn structural_hole_bridge_is_scored_with_mediator() {
        let (docs, graph, communities) = fixture();
        let out =
            score_bridges(&docs, &AHashMap::new(), &graph, &communities, &BridgeConfig::default());

        assert_eq!(out.stats.document_count, 7);
        assert_eq!(out.bridges.len(), 1, "one affine A↔B bridge");
        let bridge = &out.bridges[0];

        assert!(bridge.scores.maverick > 0.0, "structural-hole novelty is positive");
        assert!(bridge.scores.affinity > 0.15);
        assert!(bridge.scores.convergence >= 1);
        assert_eq!(bridge.endpoint_a, "a2");
        assert_eq!(bridge.endpoint_b, "b0");
        assert_eq!(bridge.members_a, vec!["a0", "a1", "a2"]);
        assert_eq!(bridge.members_b, vec!["b0", "b1", "b2"]);
        assert!(
            bridge.mediators.iter().any(|m| m.document_id == "med"),
            "the common-neighbour mediator appears"
        );

        // Fingerprint is the sorted pair of community fingerprints, deterministic.
        let fp_a = cluster_fingerprint(&["a0", "a1", "a2"]);
        let fp_b = cluster_fingerprint(&["b0", "b1", "b2"]);
        assert_eq!(bridge.fingerprint, cluster_fingerprint(&[fp_a, fp_b]));
        let again =
            score_bridges(&docs, &AHashMap::new(), &graph, &communities, &BridgeConfig::default());
        assert_eq!(again.bridges[0].fingerprint, bridge.fingerprint);
    }

    #[test]
    fn convergence_gate_drops_single_mediator_bridge() {
        let (docs, graph, communities) = fixture();
        // The only bridge has exactly one mediator → require 2 ⇒ no bridge.
        let config = BridgeConfig { min_convergence: 2, ..BridgeConfig::default() };
        let out = score_bridges(&docs, &AHashMap::new(), &graph, &communities, &config);
        assert!(out.bridges.is_empty());
        assert_eq!(out.stats.document_count, 7);
    }

    #[test]
    fn affinity_floor_skips_distant_pairs() {
        let (docs, graph, communities) = fixture();
        // Centroid cosine ≈ 0.89 < 0.95 ⇒ the pair is never considered.
        let config = BridgeConfig { min_affinity: 0.95, ..BridgeConfig::default() };
        let out = score_bridges(&docs, &AHashMap::new(), &graph, &communities, &config);
        assert!(out.bridges.is_empty());
    }

    #[test]
    fn fewer_than_two_communities_yields_no_bridges() {
        let (docs, graph, _) = fixture();
        let out = score_bridges(
            &docs,
            &AHashMap::new(),
            &graph,
            &[vec![0, 1, 2, 3, 4, 5, 6]],
            &BridgeConfig::default(),
        );
        assert!(out.bridges.is_empty());
        assert_eq!(out.stats.edge_count, graph.edge_count() as u32);
    }
}
