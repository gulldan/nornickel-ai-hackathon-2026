//! Weighted undirected kNN graph construction.
//!
//! Ported from the graph half of `build_communities` in `worker.py`:
//! cosine→`(0,1]` edge weights, an optional similarity floor, mutual- or
//! union-kNN edges, undirected dedup keeping the max cosine, and isolated-node
//! reattachment to the single best neighbour.

use ahash::AHashMap;
use petgraph::graph::{NodeIndex, UnGraph};

use crate::domain::config::{DomainConfig, EDGE_EPS};
use crate::domain::knn::{neighbors_contains, Neighbors};

/// Undirected weighted graph with symmetric adjacency and precomputed degrees so
/// modularity can be evaluated without rebuilding anything.
#[derive(Clone, Debug)]
pub struct Graph {
    node_count: usize,
    edges: Vec<(usize, usize, f64)>,
    adjacency: Vec<Vec<(usize, f64)>>,
    degrees: Vec<f64>,
    total_weight: f64,
}

impl Graph {
    /// Build the graph from canonical undirected edges (`a < b`, unique, no
    /// self-loops).
    #[must_use]
    pub fn from_edges(node_count: usize, edges: Vec<(usize, usize, f64)>) -> Self {
        let mut adjacency = vec![Vec::new(); node_count];
        let mut degrees = vec![0.0; node_count];
        let mut total_weight = 0.0;
        for &(a, b, weight) in &edges {
            adjacency[a].push((b, weight));
            adjacency[b].push((a, weight));
            degrees[a] += weight;
            degrees[b] += weight;
            total_weight += weight;
        }
        Self { node_count, edges, adjacency, degrees, total_weight }
    }

    #[must_use]
    #[inline]
    pub fn node_count(&self) -> usize {
        self.node_count
    }

    #[must_use]
    #[inline]
    pub fn edge_count(&self) -> usize {
        self.edges.len()
    }

    #[must_use]
    #[inline]
    pub fn edges(&self) -> &[(usize, usize, f64)] {
        &self.edges
    }

    #[must_use]
    #[inline]
    pub fn neighbors(&self, node: usize) -> &[(usize, f64)] {
        &self.adjacency[node]
    }

    #[must_use]
    #[inline]
    pub fn degree(&self, node: usize) -> f64 {
        self.degrees[node]
    }

    /// Sum of edge weights (each undirected edge once); the `m` in modularity.
    #[must_use]
    #[inline]
    pub fn total_weight(&self) -> f64 {
        self.total_weight
    }

    /// A `petgraph` undirected view (node indices `0..n`, edge weights from this
    /// graph) for the rustworkx-core centrality / shortest-path routines. Shared
    /// by bridge scoring and path reasoning; the fork itself is never modified.
    #[must_use]
    pub(crate) fn to_petgraph(&self) -> UnGraph<(), f64> {
        let mut pet = UnGraph::<(), f64>::with_capacity(self.node_count, self.edges.len());
        for _ in 0..self.node_count {
            pet.add_node(());
        }
        for &(a, b, weight) in &self.edges {
            pet.add_edge(NodeIndex::new(a), NodeIndex::new(b), weight);
        }
        pet
    }
}

/// Map cosine `[-1, 1]` to `(0, 1]` so weights stay strictly positive (Louvain
/// modularity is only defined for non-negative weights).
#[must_use]
#[inline]
pub fn cosine_weight(cosine: f64) -> f64 {
    EDGE_EPS.max((cosine + 1.0) / 2.0)
}

/// Assemble the weighted graph from per-node kNN maps. Edges are sorted by
/// endpoints for fully deterministic construction.
#[must_use]
pub fn build_graph(knn: &[Neighbors], node_count: usize, config: &DomainConfig) -> Graph {
    let mut candidates: AHashMap<(usize, usize), f64> = AHashMap::new();
    for (i, neighbors) in knn.iter().enumerate() {
        for &(j, cosine) in neighbors {
            if j == i {
                continue;
            }
            if config.mutual_knn && !neighbors_contains(&knn[j], i) {
                continue;
            }
            if cosine < config.sim_threshold {
                continue;
            }
            let key = if i < j { (i, j) } else { (j, i) };
            let entry = candidates.entry(key).or_insert(f64::NEG_INFINITY);
            if cosine > *entry {
                *entry = cosine;
            }
        }
    }

    let mut edges: Vec<(usize, usize, f64)> =
        candidates.into_iter().map(|((a, b), cosine)| (a, b, cosine_weight(cosine))).collect();

    // Re-attach any isolated node to its single best (finite) neighbour so it is
    // never dropped downstream as a singleton.
    let mut has_edge = vec![false; node_count];
    for &(a, b, _) in &edges {
        has_edge[a] = true;
        has_edge[b] = true;
    }
    for (i, neighbors) in knn.iter().enumerate() {
        if has_edge[i] {
            continue;
        }
        let Some(&(j, cosine)) = neighbors.first() else {
            continue;
        };
        if !cosine.is_finite() {
            continue;
        }
        let key = if i < j { (i, j) } else { (j, i) };
        edges.push((key.0, key.1, cosine_weight(cosine)));
        has_edge[i] = true;
        has_edge[j] = true;
    }

    edges.sort_unstable_by_key(|&(a, b, _)| (a, b));
    Graph::from_edges(node_count, edges)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::domain::knn::build_knn_maps;

    fn normalize(vectors: &[Vec<f32>]) -> Vec<Vec<f32>> {
        vectors
            .iter()
            .map(|v| {
                let norm = v.iter().map(|&x| f64::from(x) * f64::from(x)).sum::<f64>().sqrt();
                v.iter().map(|&x| (f64::from(x) / norm) as f32).collect()
            })
            .collect()
    }

    #[test]
    fn cosine_weight_maps_to_positive() {
        assert!((cosine_weight(1.0) - 1.0).abs() < f64::EPSILON);
        assert!((cosine_weight(0.0) - 0.5).abs() < f64::EPSILON);
        assert!(cosine_weight(-1.0) >= EDGE_EPS);
    }

    #[test]
    fn two_clusters_form_expected_edges() {
        let vectors =
            normalize(&[vec![1.0, 0.0], vec![0.95, 0.05], vec![0.0, 1.0], vec![0.05, 0.95]]);
        let knn = build_knn_maps(&vectors, 2, 512);
        let config = DomainConfig::default();
        let graph = build_graph(&knn, 4, &config);
        assert_eq!(graph.node_count(), 4);
        assert!(graph.edge_count() >= 2);
        // Node 0 and node 1 are mutual nearest neighbours → an edge with weight
        // close to 1.0.
        let n0 = graph.neighbors(0);
        assert!(n0.iter().any(|&(j, w)| j == 1 && w > 0.9));
        assert!(graph.total_weight() > 0.0);
    }
}
