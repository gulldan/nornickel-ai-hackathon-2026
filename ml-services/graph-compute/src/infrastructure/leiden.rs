//! [`CommunityDetector`] backed by the rustworkx-core fork (branch
//! `extract-community-core`). Leiden is the default; Louvain is selected by
//! `config.algorithm`. The fork itself is never modified.

use rustworkx_core::community::leiden::{run_leiden_core, GraphState as LeidenGraph};
use rustworkx_core::community::louvain::{run_louvain_core, GraphState as LouvainGraph};

use crate::domain::config::Algorithm;
use crate::domain::{CommunityDetector, DomainConfig, Graph};

/// NetworkX/`python-louvain` default modularity-gain threshold for one Louvain
/// level. Leiden does not use it.
const LOUVAIN_THRESHOLD: f64 = 1e-7;

/// Detector wrapping rustworkx-core's Leiden and Louvain cores.
#[derive(Clone, Copy, Debug, Default)]
pub struct LeidenDetector;

impl CommunityDetector for LeidenDetector {
    fn detect(&self, graph: &Graph, config: &DomainConfig) -> Vec<Vec<usize>> {
        let n = graph.node_count();
        if n == 0 {
            return Vec::new();
        }
        let edges = graph.edges().to_vec();
        let original_nodes: Vec<usize> = (0..n).collect();

        let communities = match config.algorithm {
            Algorithm::Leiden => {
                let state =
                    LeidenGraph::from_weighted_edges_with_metadata(n, edges, original_nodes);
                run_leiden_core(
                    state,
                    config.resolution,
                    Some(config.seed),
                    config.max_iterations,
                    None,
                )
            }
            Algorithm::Louvain => {
                let state =
                    LouvainGraph::from_weighted_edges_with_metadata(n, edges, original_nodes);
                let hierarchy = run_louvain_core(
                    state,
                    config.resolution,
                    LOUVAIN_THRESHOLD,
                    Some(config.seed),
                    None,
                );
                hierarchy.into_iter().next_back().unwrap_or_default()
            }
        };

        complete_coverage(communities, n)
    }
}

/// Ensure every node appears in exactly one community by appending a singleton
/// for any node the detector left unassigned (defensive; the Python Louvain
/// partition always covered all nodes).
fn complete_coverage(mut communities: Vec<Vec<usize>>, n: usize) -> Vec<Vec<usize>> {
    let mut seen = vec![false; n];
    for community in &communities {
        for &node in community {
            if node < n {
                seen[node] = true;
            }
        }
    }
    for (node, assigned) in seen.iter().enumerate() {
        if !assigned {
            communities.push(vec![node]);
        }
    }
    communities
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::domain::graph::Graph;

    #[test]
    fn leiden_separates_two_cliques() {
        // Two triangles {0,1,2} and {3,4,5} joined by a single weak bridge.
        let edges = vec![
            (0, 1, 1.0),
            (0, 2, 1.0),
            (1, 2, 1.0),
            (3, 4, 1.0),
            (3, 5, 1.0),
            (4, 5, 1.0),
            (2, 3, 0.01),
        ];
        let graph = Graph::from_edges(6, edges);
        let config = DomainConfig::default();
        let communities = LeidenDetector.detect(&graph, &config);
        // Every node covered exactly once.
        let total: usize = communities.iter().map(Vec::len).sum();
        assert_eq!(total, 6);
        assert!(communities.len() >= 2);
    }

    #[test]
    fn louvain_path_runs_and_covers_nodes() {
        let edges = vec![(0, 1, 1.0), (1, 2, 1.0), (2, 3, 1.0)];
        let graph = Graph::from_edges(4, edges);
        let config = DomainConfig { algorithm: Algorithm::Louvain, ..DomainConfig::default() };
        let communities = LeidenDetector.detect(&graph, &config);
        let mut covered: Vec<usize> = communities.into_iter().flatten().collect();
        covered.sort_unstable();
        assert_eq!(covered, vec![0, 1, 2, 3]);
    }
}
