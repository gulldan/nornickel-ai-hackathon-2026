//! The clustering pipeline and its output types.
//!
//! Ported from the community-detection and publishing half of `worker.py`
//! (`build_communities`, the `recluster` assembly loop, `MIN_SIZE` filtering and
//! per-cluster metrics).

use std::time::Instant;

use ahash::{AHashMap, AHashSet};
use rayon::prelude::*;
use serde::{Deserialize, Serialize};

use crate::domain::config::DomainConfig;
use crate::domain::fingerprint::cluster_fingerprint;
use crate::domain::graph::build_graph;
use crate::domain::knn::build_knn_maps;
use crate::domain::lineage::{compute_lineage, LineageInfo};
use crate::domain::metrics::{avg_intra_similarity, modularity_terms, round4};
use crate::domain::vector::{collapse_whitespace, l2_normalize, DocVector};
use crate::domain::Graph;

/// Detects communities on a weighted undirected graph.
///
/// Implemented by infrastructure adapters (rustworkx Leiden/Louvain) and mocked
/// in tests. Implementations must return node-index communities covering every
/// node.
pub trait CommunityDetector: Send + Sync {
    /// Partition `graph` into communities (lists of node indices).
    fn detect(&self, graph: &Graph, config: &DomainConfig) -> Vec<Vec<usize>>;
}

/// A previous board cluster used for lineage (`id` + member document ids).
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct PreviousCluster {
    pub id: String,
    pub members: Vec<String>,
}

/// Representative document of a cluster.
#[derive(Clone, Debug, Default, Deserialize, Eq, PartialEq, Serialize)]
pub struct Representative {
    pub document_id: String,
    pub filename: String,
    /// Best prose snippet, `<= 360` chars.
    pub snippet: String,
}

/// Per-cluster quality metrics.
#[derive(Clone, Debug, Default, Deserialize, PartialEq, Serialize)]
pub struct ClusterMetrics {
    pub size: u32,
    pub avg_similarity: f64,
    pub modularity: f64,
    pub modularity_contribution: f64,
}

/// Cluster lineage versus the previous board (mirrors proto `Lineage`).
#[derive(Clone, Debug, Default, Deserialize, PartialEq, Serialize)]
pub struct Lineage {
    pub previous_cluster_id: String,
    pub jaccard: f64,
    pub stability: f64,
    pub merged_from: Vec<String>,
    pub split_from: String,
}

impl From<LineageInfo> for Lineage {
    fn from(info: LineageInfo) -> Self {
        Self {
            previous_cluster_id: info.previous_cluster_id,
            jaccard: info.jaccard,
            stability: info.stability,
            merged_from: info.merged_from,
            split_from: info.split_from.unwrap_or_default(),
        }
    }
}

/// One published cluster.
#[derive(Clone, Debug, Default, Deserialize, PartialEq, Serialize)]
pub struct Cluster {
    /// Document ids, sorted.
    pub members: Vec<String>,
    pub fingerprint: String,
    /// Representative texts for downstream LLM labeling.
    pub signals: Vec<String>,
    pub chunk_count: u32,
    pub metrics: ClusterMetrics,
    pub representatives: Vec<Representative>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub lineage: Option<Lineage>,
}

/// Whole-run statistics.
#[derive(Clone, Debug, Default, Deserialize, PartialEq, Serialize)]
pub struct ComputeStats {
    pub document_count: u32,
    pub edge_count: u32,
    pub modularity: f64,
    pub compute_ms: u64,
}

/// The full clustering response.
#[derive(Clone, Debug, Default, Deserialize, PartialEq, Serialize)]
pub struct ClusterOutput {
    pub clusters: Vec<Cluster>,
    pub stats: ComputeStats,
}

struct PreparedCluster {
    nodes: Vec<usize>,
    contribution: f64,
    member_ids: Vec<Box<str>>,
    fingerprint: String,
}

/// Run the full pipeline: build the kNN graph, detect communities, drop those
/// below `min_size`, compute metrics/representatives/lineage and return the
/// board sorted by size (largest first).
#[must_use]
pub fn cluster_documents<D: CommunityDetector + ?Sized>(
    docs: &[DocVector],
    doc_filenames: &AHashMap<Box<str>, Box<str>>,
    previous_clusters: &[PreviousCluster],
    config: &DomainConfig,
    detector: &D,
) -> ClusterOutput {
    let (graph, communities) = build_communities(docs, config, detector);
    cluster_from_prepared(docs, doc_filenames, &graph, communities, previous_clusters, config)
}

/// Assemble the published board from an already-built graph and partition.
///
/// Computes the partition modularity, drops communities below `min_size`,
/// attaches metrics/representatives/lineage and returns them sorted by size
/// (largest first). Shares the prepared graph/partition with bridge scoring.
#[must_use]
pub fn cluster_from_prepared(
    docs: &[DocVector],
    doc_filenames: &AHashMap<Box<str>, Box<str>>,
    graph: &Graph,
    communities: Vec<Vec<usize>>,
    previous_clusters: &[PreviousCluster],
    config: &DomainConfig,
) -> ClusterOutput {
    let start = Instant::now();
    let document_count = docs.len() as u32;
    let (modularity, contributions) = modularity_terms(graph, &communities);
    let edge_count = graph.edge_count();

    let mut surviving: Vec<(Vec<usize>, f64)> = communities
        .into_iter()
        .zip(contributions)
        .filter(|(nodes, _)| nodes.len() >= config.min_size)
        .collect();
    // Stable sort by descending size (mirrors `comms.sort(key=len, reverse=True)`).
    surviving.sort_by_key(|(nodes, _)| std::cmp::Reverse(nodes.len()));

    let prepared: Vec<PreparedCluster> = surviving
        .into_iter()
        .map(|(nodes, contribution)| {
            let member_ids: Vec<Box<str>> = nodes.iter().map(|&i| docs[i].id.clone()).collect();
            let fingerprint = cluster_fingerprint(&member_ids);
            PreparedCluster { nodes, contribution, member_ids, fingerprint }
        })
        .collect();

    let stats = ComputeStats {
        document_count,
        edge_count: edge_count as u32,
        modularity,
        compute_ms: start.elapsed().as_millis() as u64,
    };

    if prepared.is_empty() {
        return ClusterOutput { clusters: Vec::new(), stats };
    }

    let lineage_map = build_lineage(&prepared, previous_clusters, config);

    let clusters: Vec<Cluster> = prepared
        .par_iter()
        .map(|cluster| assemble_cluster(cluster, docs, doc_filenames, &lineage_map, modularity))
        .collect();

    ClusterOutput {
        clusters,
        stats: ComputeStats { compute_ms: start.elapsed().as_millis() as u64, ..stats },
    }
}

/// Build the weighted kNN graph and detect its communities (no `min_size`
/// filtering yet). Returns the graph alongside the partition so callers can reuse
/// both — the cluster board and the bridge scorer run over the identical graph.
pub(crate) fn build_communities<D: CommunityDetector + ?Sized>(
    docs: &[DocVector],
    config: &DomainConfig,
    detector: &D,
) -> (Graph, Vec<Vec<usize>>) {
    let n = docs.len();
    if n == 0 {
        return (Graph::from_edges(0, Vec::new()), Vec::new());
    }
    if n <= 2 {
        // 1 doc → itself; 2 docs → one pair (MIN_SIZE filtering decides later).
        return (Graph::from_edges(n, Vec::new()), vec![(0..n).collect()]);
    }

    let normalized: Vec<Vec<f32>> = docs.iter().map(|doc| l2_normalize(&doc.vector)).collect();
    let k = config.knn_k.min(n - 1).max(1);
    let knn = build_knn_maps(&normalized, k, config.knn_block_size);
    let graph = build_graph(&knn, n, config);

    if graph.edge_count() == 0 {
        let singletons: Vec<Vec<usize>> = (0..n).map(|i| vec![i]).collect();
        return (graph, singletons);
    }

    let communities = detector.detect(&graph, config);
    (graph, communities)
}

fn build_lineage(
    prepared: &[PreparedCluster],
    previous_clusters: &[PreviousCluster],
    config: &DomainConfig,
) -> AHashMap<String, LineageInfo> {
    let new_sets: Vec<(String, AHashSet<&str>)> = prepared
        .iter()
        .map(|cluster| {
            let members: AHashSet<&str> = cluster.member_ids.iter().map(AsRef::as_ref).collect();
            (cluster.fingerprint.clone(), members)
        })
        .collect();
    let prev_sets: Vec<(String, AHashSet<&str>)> = previous_clusters
        .iter()
        .filter(|prev| !prev.members.is_empty())
        .map(|prev| {
            let members: AHashSet<&str> =
                prev.members.iter().map(String::as_str).filter(|s| !s.is_empty()).collect();
            (prev.id.clone(), members)
        })
        .collect();
    compute_lineage(&new_sets, &prev_sets, config.lineage_overlap_min)
}

fn assemble_cluster(
    cluster: &PreparedCluster,
    docs: &[DocVector],
    doc_filenames: &AHashMap<Box<str>, Box<str>>,
    lineage_map: &AHashMap<String, LineageInfo>,
    modularity: f64,
) -> Cluster {
    let mut members: Vec<String> = cluster.member_ids.iter().map(ToString::to_string).collect();
    members.sort_unstable();

    let chunk_count = cluster.nodes.iter().map(|&i| docs[i].chunk_count).sum();
    let member_vectors: Vec<&[f32]> =
        cluster.nodes.iter().map(|&i| docs[i].vector.as_slice()).collect();
    let avg_similarity = avg_intra_similarity(&member_vectors);

    let signals: Vec<String> =
        cluster.nodes.iter().take(8).map(|&i| collapse_whitespace(&docs[i].rep_text)).collect();

    let representatives: Vec<Representative> = cluster
        .nodes
        .iter()
        .take(5)
        .map(|&i| {
            let doc = &docs[i];
            let filename = doc
                .filename
                .as_ref()
                .map(ToString::to_string)
                .or_else(|| doc_filenames.get(&doc.id).map(ToString::to_string))
                .unwrap_or_default();
            let snippet = truncate_chars(&collapse_whitespace(&doc.rep_text), 360);
            Representative { document_id: doc.id.to_string(), filename, snippet }
        })
        .collect();

    let lineage = lineage_map.get(&cluster.fingerprint).cloned().map(Lineage::from);

    Cluster {
        members,
        fingerprint: cluster.fingerprint.clone(),
        signals,
        chunk_count,
        metrics: ClusterMetrics {
            size: cluster.nodes.len() as u32,
            avg_similarity,
            modularity,
            modularity_contribution: round4(cluster.contribution),
        },
        representatives,
        lineage,
    }
}

fn truncate_chars(text: &str, limit: usize) -> String {
    text.chars().take(limit).collect()
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::domain::vector::{aggregate_docs, ChunkPoint};

    /// Trivial detector: connected components via union-find, so the pipeline is
    /// testable without the rustworkx dependency.
    struct ComponentDetector;

    impl CommunityDetector for ComponentDetector {
        fn detect(&self, graph: &Graph, _config: &DomainConfig) -> Vec<Vec<usize>> {
            let n = graph.node_count();
            let mut parent: Vec<usize> = (0..n).collect();
            fn find(parent: &mut [usize], x: usize) -> usize {
                let mut root = x;
                while parent[root] != root {
                    root = parent[root];
                }
                let mut cur = x;
                while parent[cur] != root {
                    let next = parent[cur];
                    parent[cur] = root;
                    cur = next;
                }
                root
            }
            for &(a, b, _) in graph.edges() {
                let ra = find(&mut parent, a);
                let rb = find(&mut parent, b);
                if ra != rb {
                    parent[ra] = rb;
                }
            }
            let mut groups: AHashMap<usize, Vec<usize>> = AHashMap::new();
            for node in 0..n {
                let root = find(&mut parent, node);
                groups.entry(root).or_default().push(node);
            }
            let mut out: Vec<Vec<usize>> = groups.into_values().collect();
            for group in &mut out {
                group.sort_unstable();
            }
            out.sort_by(|l, r| r.len().cmp(&l.len()).then_with(|| l[0].cmp(&r[0])));
            out
        }
    }

    fn doc(id: &str, vector: &[f32]) -> DocVector {
        DocVector {
            id: id.into(),
            vector: vector.to_vec(),
            rep_text: format!("representative prose for {id}").into(),
            chunk_count: 1,
            filename: Some(format!("{id}.pdf").into()),
        }
    }

    #[test]
    fn pipeline_finds_two_clusters_and_is_deterministic() {
        // Two tight groups: {a,b,c} around [1,0], {d,e,f} around [0,1].
        let docs = vec![
            doc("a", &[1.0, 0.0]),
            doc("b", &[0.98, 0.02]),
            doc("c", &[0.95, 0.05]),
            doc("d", &[0.0, 1.0]),
            doc("e", &[0.02, 0.98]),
            doc("f", &[0.05, 0.95]),
        ];
        let filenames = AHashMap::new();
        // k=2 so each node links only to its two same-group neighbours; the
        // connected-components detector then yields the two groups.
        let config = DomainConfig::resolve(crate::domain::config::ConfigOverrides {
            knn_k: Some(2),
            ..Default::default()
        });
        let detector = ComponentDetector;

        let out = cluster_documents(&docs, &filenames, &[], &config, &detector);
        assert_eq!(out.clusters.len(), 2);
        assert_eq!(out.stats.document_count, 6);
        for cluster in &out.clusters {
            assert_eq!(cluster.metrics.size, 3);
            assert_eq!(cluster.members.len(), 3);
            assert!(cluster.lineage.is_none());
            assert!(!cluster.fingerprint.is_empty());
        }
        // Determinism: same fingerprints on a second run.
        let again = cluster_documents(&docs, &filenames, &[], &config, &detector);
        let fps_a: Vec<&str> = out.clusters.iter().map(|c| c.fingerprint.as_str()).collect();
        let fps_b: Vec<&str> = again.clusters.iter().map(|c| c.fingerprint.as_str()).collect();
        assert_eq!(fps_a, fps_b);
    }

    #[test]
    fn min_size_drops_small_communities() {
        let docs = vec![doc("solo", &[1.0, 0.0])];
        let filenames = AHashMap::new();
        let config = DomainConfig::default();
        let out = cluster_documents(&docs, &filenames, &[], &config, &ComponentDetector);
        assert!(out.clusters.is_empty());
        assert_eq!(out.stats.document_count, 1);
    }

    #[test]
    fn aggregate_then_cluster_roundtrip() {
        let config = DomainConfig::default();
        let points = vec![
            ChunkPoint {
                id: "a-0".into(),
                document_id: "a".into(),
                vector: vec![1.0, 0.0],
                chunk_index: 0,
                text: "lead chunk".into(),
                filename: None,
            },
            ChunkPoint {
                id: "b-0".into(),
                document_id: "b".into(),
                vector: vec![0.9, 0.1],
                chunk_index: 0,
                text: "lead chunk".into(),
                filename: None,
            },
        ];
        let docs = aggregate_docs(&points, &config);
        let out = cluster_documents(&docs, &AHashMap::new(), &[], &config, &ComponentDetector);
        // 2 docs → single pair community of size 2.
        assert_eq!(out.clusters.len(), 1);
        assert_eq!(out.clusters[0].metrics.size, 2);
    }
}
