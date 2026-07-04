//! Exact blocked top-k cosine neighbours (ported from `build_knn_maps`).
//!
//! Vectors are expected to be L2-normalized by the caller so a dot product is
//! the cosine. Selection is deterministic: highest cosine first, ties broken by
//! the smaller node index.

use rayon::prelude::*;
use smallvec::SmallVec;

/// Top-k neighbour list for one node: `(neighbour_index, cosine)`.
///
/// Sorted by descending cosine (ties by ascending index). `k` defaults to 6, so
/// the inline capacity of 8 keeps the common case off the heap.
pub type Neighbors = SmallVec<[(usize, f64); 8]>;

/// Build the per-node top-k cosine neighbour maps.
///
/// Avoids materialising the full `n × n` similarity matrix. Rows are processed
/// in blocks of `block_size` (memory locality only — the result is independent
/// of the block size).
#[must_use]
pub fn build_knn_maps(vectors: &[Vec<f32>], k: usize, block_size: usize) -> Vec<Neighbors> {
    let n = vectors.len();
    let block = block_size.max(1);
    let mut maps: Vec<Neighbors> = vec![SmallVec::new(); n];
    maps.par_chunks_mut(block).enumerate().for_each(|(chunk_idx, chunk)| {
        let start = chunk_idx * block;
        for (offset, slot) in chunk.iter_mut().enumerate() {
            *slot = top_k_for_row(vectors, start + offset, k);
        }
    });
    maps
}

fn top_k_for_row(vectors: &[Vec<f32>], i: usize, k: usize) -> Neighbors {
    let mut best: Neighbors = SmallVec::new();
    let row = &vectors[i];
    for (j, other) in vectors.iter().enumerate() {
        if j == i {
            continue;
        }
        let cosine = cosine(row, other).clamp(-1.0, 1.0);
        if !cosine.is_finite() {
            continue;
        }
        insert_top_k(&mut best, k, j, cosine);
    }
    best
}

fn insert_top_k(best: &mut Neighbors, k: usize, j: usize, cosine: f64) {
    if best.len() >= k {
        let (worst_j, worst_c) = best[best.len() - 1];
        if cosine < worst_c || (cosine == worst_c && j > worst_j) {
            return;
        }
        best.pop();
    }
    let position = best
        .iter()
        .position(|&(ej, ec)| cosine > ec || (cosine == ec && j < ej))
        .unwrap_or(best.len());
    best.insert(position, (j, cosine));
}

#[inline]
fn cosine(a: &[f32], b: &[f32]) -> f64 {
    f64::from(a.iter().zip(b.iter()).fold(0.0_f32, |sum, (&x, &y)| x.mul_add(y, sum)))
}

/// Whether `target` is among `neighbors` (used for the mutual-kNN test).
#[must_use]
#[inline]
pub fn neighbors_contains(neighbors: &[(usize, f64)], target: usize) -> bool {
    neighbors.iter().any(|&(j, _)| j == target)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn top_k_excludes_self_and_orders_by_cosine() {
        // a ~ b (close), c orthogonal-ish.
        let raw: Vec<Vec<f32>> = vec![vec![1.0, 0.0], vec![0.9, 0.1], vec![0.0, 1.0]];
        let normed: Vec<Vec<f32>> =
            raw.iter().map(|v| crate::domain::vector::l2_normalize(v)).collect();
        let maps = build_knn_maps(&normed, 2, 512);
        assert_eq!(maps.len(), 3);
        // Node 0's nearest is node 1.
        assert_eq!(maps[0][0].0, 1);
        assert!(!neighbors_contains(&maps[0], 0));
        assert!(neighbors_contains(&maps[0], 1));
    }

    #[test]
    fn block_size_does_not_change_result() {
        let vectors = vec![
            vec![1.0, 0.0, 0.0],
            vec![0.8, 0.2, 0.0],
            vec![0.0, 1.0, 0.0],
            vec![0.0, 0.9, 0.1],
        ];
        let a = build_knn_maps(&vectors, 2, 512);
        let b = build_knn_maps(&vectors, 2, 1);
        assert_eq!(a, b);
    }
}
