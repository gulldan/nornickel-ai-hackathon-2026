//! Cluster lineage versus the previous board, ported from `jaccard` and
//! `compute_lineage` in `worker.py`.

use ahash::{AHashMap, AHashSet};

use crate::domain::metrics::round4;

/// Lineage of one new cluster relative to the previous board.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct LineageInfo {
    /// Best Jaccard match among previous clusters (empty if none).
    pub previous_cluster_id: String,
    pub jaccard: f64,
    /// Equals `jaccard` (`1.0` ⇒ membership unchanged vs. its predecessor).
    pub stability: f64,
    /// `>= 2` previous clusters that each handed most of their members here.
    pub merged_from: Vec<String>,
    /// Set when the best previous match also fed `>= 2` new clusters.
    pub split_from: Option<String>,
}

/// Jaccard overlap of two id sets; `0.0` when either is empty or disjoint.
#[must_use]
pub fn jaccard(a: &AHashSet<&str>, b: &AHashSet<&str>) -> f64 {
    if a.is_empty() || b.is_empty() {
        return 0.0;
    }
    let intersection = intersection_size(a, b);
    if intersection == 0 {
        return 0.0;
    }
    let union = a.len() + b.len() - intersection;
    intersection as f64 / union as f64
}

fn intersection_size(a: &AHashSet<&str>, b: &AHashSet<&str>) -> usize {
    let (small, large) = if a.len() <= b.len() { (a, b) } else { (b, a) };
    small.iter().filter(|&&x| large.contains(x)).count()
}

/// Per new cluster, lineage versus the previous board. Empty `prev_sets` (no
/// prior board) ⇒ empty result so the caller leaves lineage unset.
#[must_use]
pub fn compute_lineage<'a>(
    new_sets: &[(String, AHashSet<&'a str>)],
    prev_sets: &[(String, AHashSet<&'a str>)],
    overlap_min: f64,
) -> AHashMap<String, LineageInfo> {
    let mut lineage = AHashMap::new();
    if prev_sets.is_empty() || new_sets.is_empty() {
        return lineage;
    }

    // How many new clusters each previous cluster substantially feeds (splits).
    let mut prev_fanout: AHashMap<&str, u32> = AHashMap::with_capacity(prev_sets.len());
    for (pid, _) in prev_sets {
        prev_fanout.entry(pid.as_str()).or_insert(0);
    }
    for (_, new_members) in new_sets {
        for (pid, prev_members) in prev_sets {
            if feeds(new_members, prev_members, overlap_min) {
                if let Some(count) = prev_fanout.get_mut(pid.as_str()) {
                    *count += 1;
                }
            }
        }
    }

    for (key, new_members) in new_sets {
        let mut best_id = "";
        let mut best_jaccard = 0.0;
        let mut merged: Vec<&str> = Vec::new();
        for (pid, prev_members) in prev_sets {
            let score = jaccard(new_members, prev_members);
            if score > best_jaccard {
                best_jaccard = score;
                best_id = pid.as_str();
            }
            if feeds(new_members, prev_members, overlap_min) {
                merged.push(pid.as_str());
            }
        }

        let mut info = LineageInfo {
            previous_cluster_id: best_id.to_owned(),
            jaccard: round4(best_jaccard),
            stability: round4(best_jaccard),
            merged_from: Vec::new(),
            split_from: None,
        };
        if merged.len() >= 2 {
            let mut sources: Vec<String> = merged.iter().map(|s| (*s).to_owned()).collect();
            sources.sort_unstable();
            info.merged_from = sources;
        }
        if !best_id.is_empty() && prev_fanout.get(best_id).copied().unwrap_or(0) >= 2 {
            info.split_from = Some(best_id.to_owned());
        }
        lineage.insert(key.clone(), info);
    }

    lineage
}

/// Whether `new_members` absorbed at least `overlap_min` of `prev_members`.
fn feeds(new_members: &AHashSet<&str>, prev_members: &AHashSet<&str>, overlap_min: f64) -> bool {
    !prev_members.is_empty()
        && intersection_size(new_members, prev_members) as f64 / prev_members.len() as f64
            >= overlap_min
}

#[cfg(test)]
mod tests {
    use super::*;

    fn set<'a>(items: &[&'a str]) -> AHashSet<&'a str> {
        items.iter().copied().collect()
    }

    #[test]
    fn jaccard_basic() {
        assert!((jaccard(&set(&["a", "b"]), &set(&["a", "b"])) - 1.0).abs() < f64::EPSILON);
        assert!((jaccard(&set(&["a", "b"]), &set(&["b", "c"])) - 1.0 / 3.0).abs() < 1e-9);
        assert!((jaccard(&set(&["a"]), &set(&["x"])) - 0.0).abs() < f64::EPSILON);
        assert!((jaccard(&set(&[]), &set(&["x"])) - 0.0).abs() < f64::EPSILON);
    }

    #[test]
    fn no_previous_board_yields_empty() {
        let new = vec![("fp".to_owned(), set(&["a", "b"]))];
        assert!(compute_lineage(&new, &[], 0.3).is_empty());
    }

    #[test]
    fn detects_merge_and_split() {
        // New cluster N1 absorbs prev P1 and P2 fully → merged_from = [P1, P2].
        let n1 = ("N1".to_owned(), set(&["a", "b", "c", "d"]));
        let new = vec![n1];
        let prev = vec![("P1".to_owned(), set(&["a", "b"])), ("P2".to_owned(), set(&["c", "d"]))];
        let lineage = compute_lineage(&new, &prev, 0.3);
        let info = &lineage["N1"];
        assert_eq!(info.merged_from, vec!["P1".to_owned(), "P2".to_owned()]);

        // One prev P fed two new clusters → split_from set on both.
        let new2 = vec![("A".to_owned(), set(&["a", "b"])), ("B".to_owned(), set(&["c", "d"]))];
        let prev2 = vec![("P".to_owned(), set(&["a", "b", "c", "d"]))];
        let lineage2 = compute_lineage(&new2, &prev2, 0.3);
        assert_eq!(lineage2["A"].split_from.as_deref(), Some("P"));
        assert_eq!(lineage2["B"].split_from.as_deref(), Some("P"));
    }
}
