//! Partition quality metrics, ported from `modularity_terms` and
//! `avg_intra_similarity` in `worker.py`.

use crate::domain::graph::Graph;
use crate::domain::vector::l2_normalize;

/// Round to 4 decimals, matching the Python `round(x, 4)` used throughout the
/// worker's published `params`/`metrics`.
#[must_use]
#[inline]
pub fn round4(value: f64) -> f64 {
    (value * 10_000.0).round() / 10_000.0
}

/// Newman weighted modularity for a partition: `Q = Σ_c [L_c/m − (k_c/2m)²]`.
///
/// Returns `(Q, per-community contribution)`. `Q` is rounded from the unrounded
/// sum; each contribution is rounded independently (same as the Python). An
/// edgeless graph yields `(0.0, [0.0, …])`.
#[must_use]
pub fn modularity_terms(graph: &Graph, communities: &[Vec<usize>]) -> (f64, Vec<f64>) {
    let m = graph.total_weight();
    if m <= 0.0 {
        return (0.0, vec![0.0; communities.len()]);
    }

    let mut member = vec![false; graph.node_count()];
    let mut contributions = Vec::with_capacity(communities.len());
    for nodes in communities {
        for &u in nodes {
            member[u] = true;
        }
        let mut internal = 0.0;
        let mut degree_sum = 0.0;
        for &u in nodes {
            for &(neighbor, weight) in graph.neighbors(u) {
                // Count each internal edge once (the `neighbor >= u` guard).
                if neighbor >= u && member[neighbor] {
                    internal += weight;
                }
            }
            degree_sum += graph.degree(u);
        }
        let fraction = degree_sum / (2.0 * m);
        let internal_fraction = internal / m;
        let expected_fraction = fraction * fraction;
        contributions.push(internal_fraction - expected_fraction);
        for &u in nodes {
            member[u] = false;
        }
    }

    let total = round4(contributions.iter().sum());
    let rounded = contributions.iter().map(|&x| round4(x)).collect();
    (total, rounded)
}

/// Mean pairwise cosine similarity inside a cluster.
///
/// Vectors are L2-normalized here so the Gram matrix is cosine. A single member
/// is perfectly self-similar (`1.0`); the result is clamped to `[-1, 1]` and
/// rounded to 4 decimals.
#[must_use]
pub fn avg_intra_similarity(member_vectors: &[&[f32]]) -> f64 {
    let count = member_vectors.len();
    if count <= 1 {
        return 1.0;
    }
    let normalized: Vec<Vec<f32>> = member_vectors.iter().map(|v| l2_normalize(v)).collect();
    let mut sum = 0.0;
    let mut pairs = 0u64;
    for i in 0..count {
        for j in (i + 1)..count {
            sum += dot(&normalized[i], &normalized[j]);
            pairs += 1;
        }
    }
    if pairs == 0 {
        return 1.0;
    }
    round4((sum / pairs as f64).clamp(-1.0, 1.0))
}

#[inline]
fn dot(a: &[f32], b: &[f32]) -> f64 {
    a.iter().zip(b.iter()).map(|(&x, &y)| f64::from(x) * f64::from(y)).sum()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn round4_rounds_to_four_decimals() {
        assert!((round4(0.123_456) - 0.1235).abs() < f64::EPSILON);
        assert!((round4(1.0) - 1.0).abs() < f64::EPSILON);
    }

    #[test]
    fn modularity_of_two_disjoint_pairs() {
        // Two disjoint edges {0-1} and {2-3}, equal weights. Each community is a
        // pair; modularity is positive for this clearly-modular partition.
        let graph = Graph::from_edges(4, vec![(0, 1, 1.0), (2, 3, 1.0)]);
        let (q, contribs) = modularity_terms(&graph, &[vec![0, 1], vec![2, 3]]);
        assert_eq!(contribs.len(), 2);
        assert!(q > 0.0);
        assert!((contribs[0] - contribs[1]).abs() < 1e-9);
    }

    #[test]
    fn modularity_edgeless_is_zero() {
        let graph = Graph::from_edges(3, vec![]);
        let (q, contribs) = modularity_terms(&graph, &[vec![0], vec![1], vec![2]]);
        assert!((q - 0.0).abs() < f64::EPSILON);
        assert_eq!(contribs, vec![0.0, 0.0, 0.0]);
    }

    #[test]
    fn avg_intra_similarity_basics() {
        assert!((avg_intra_similarity(&[&[1.0, 0.0]]) - 1.0).abs() < f64::EPSILON);
        let identical: Vec<&[f32]> = vec![&[1.0, 0.0], &[1.0, 0.0]];
        assert!((avg_intra_similarity(&identical) - 1.0).abs() < f64::EPSILON);
        let orthogonal: Vec<&[f32]> = vec![&[1.0, 0.0], &[0.0, 1.0]];
        assert!((avg_intra_similarity(&orthogonal) - 0.0).abs() < f64::EPSILON);
    }
}
