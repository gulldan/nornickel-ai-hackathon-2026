//! Stable cluster fingerprint, ported from `cluster_fingerprint` in `worker.py`:
//! the first 16 hex chars of `sha1` over the sorted, newline-joined member ids.

use std::fmt::Write as _;

use sha1::{Digest, Sha1};

/// First 16 hex chars of `sha1("\n".join(sorted(nonempty member ids)))`. Returns
/// an empty string when there are no non-empty ids.
#[must_use]
pub fn cluster_fingerprint<S: AsRef<str>>(members: &[S]) -> String {
    let mut ids: Vec<&str> = members.iter().map(AsRef::as_ref).filter(|s| !s.is_empty()).collect();
    if ids.is_empty() {
        return String::new();
    }
    ids.sort_unstable();
    let joined = ids.join("\n");

    let mut hasher = Sha1::new();
    hasher.update(joined.as_bytes());
    let digest = hasher.finalize();

    let mut out = String::with_capacity(16);
    for byte in digest.iter().take(8) {
        let _ = write!(out, "{byte:02x}");
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn fingerprint_is_order_independent_and_16_hex() {
        let a = cluster_fingerprint(&["doc-2", "doc-1"]);
        let b = cluster_fingerprint(&["doc-1", "doc-2"]);
        assert_eq!(a, b);
        assert_eq!(a.len(), 16);
        assert!(a.chars().all(|c| c.is_ascii_hexdigit()));
    }

    #[test]
    fn empty_members_give_empty_fingerprint() {
        let empty: [&str; 0] = [];
        assert_eq!(cluster_fingerprint(&empty), "");
        assert_eq!(cluster_fingerprint(&[""]), "");
    }

    #[test]
    fn known_value_matches_sha1_prefix() {
        // sha1("a\nb")[:16]
        assert_eq!(cluster_fingerprint(&["b", "a"]), "fcd127ffa1016069");
    }
}
