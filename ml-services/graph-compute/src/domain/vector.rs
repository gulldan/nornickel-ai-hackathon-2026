//! Per-chunk parsing, heuristics and weighted pooling into document vectors.
//!
//! Ported from `extract_vector`, `chunk_index_of`, the
//! `_RESULTS_CUE`/`looks_like_*` heuristics, `chunk_weight` and `aggregate_docs`
//! in the Python `cluster-worker`.

use std::sync::LazyLock;

use ahash::AHashMap;
use regex::Regex;
use serde_json::Value;

use crate::domain::config::{DomainConfig, EDGE_EPS};

// Cues that a chunk carries the paper's findings (boosted) vs. its tail-matter
// (references / acknowledgements, down-weighted). Same patterns as `worker.py`.
static RESULTS_CUE: LazyLock<Regex> = LazyLock::new(|| {
    Regex::new(
        r"(?i)\b(results?|conclusions?|discussion|findings?|we (?:show|find|propose|present|demonstrate)|in summary|to summari[sz]e|our (?:results|method|approach|model)|результат|вывод|заключени|обсуждени|в итоге|мы предлага|мы показыва)\b",
    )
    .expect("RESULTS_CUE regex is valid")
});

static REFS_HEADING: LazyLock<Regex> = LazyLock::new(|| {
    Regex::new(
        r"(?i)^\W{0,6}(references|bibliography|works cited|acknowledge?ments?|список\s+литератур|литература|библиографи|благодарност)\b",
    )
    .expect("REFS_HEADING regex is valid")
});

static REF_MARKER: LazyLock<Regex> =
    LazyLock::new(|| Regex::new(r"\[\s*\d{1,3}\s*\]").expect("REF_MARKER regex is valid"));

static DOI: LazyLock<Regex> = LazyLock::new(|| {
    Regex::new(r"(?i)\b(?:doi\s*[:.]?\s*|https?://(?:dx\.)?doi\.org/)?10\.\d{4,9}/\S+")
        .expect("DOI regex is valid")
});

static WORD: LazyLock<Regex> =
    LazyLock::new(|| Regex::new(r"[A-Za-zА-Яа-я]{2,}").expect("WORD regex is valid"));

static SENTENCE: LazyLock<Regex> =
    LazyLock::new(|| Regex::new(r"[.!?](?:\s|$)").expect("SENTENCE regex is valid"));

/// A single Qdrant point reduced to the fields clustering needs.
#[derive(Clone, Debug)]
pub struct ChunkPoint {
    /// Qdrant point id. Empty for sources that do not carry it (document-level
    /// pooling ignores it); chunk-granularity clustering uses it as the node id so
    /// the returned cluster members are chunk point ids.
    pub id: Box<str>,
    pub document_id: Box<str>,
    pub vector: Vec<f32>,
    pub chunk_index: i64,
    pub text: Box<str>,
    pub filename: Option<Box<str>>,
}

/// One document after pooling its chunk vectors into a single direction.
#[derive(Clone, Debug)]
pub struct DocVector {
    pub id: Box<str>,
    pub vector: Vec<f32>,
    /// Raw text of the chosen representative chunk (abstract-like prose if any).
    pub rep_text: Box<str>,
    pub chunk_count: u32,
    /// Filename taken from the chunk payload, if present.
    pub filename: Option<Box<str>>,
}

/// Coerce a Qdrant `vector` field into a 1-D `f32` vector.
///
/// Qdrant returns a plain list for a single unnamed vector but a map
/// (`{"name": [...]}`) for named/multi-vector collections. Pick the first dense
/// list-like value, reject empties or any non-finite component.
#[must_use]
pub fn extract_vector(raw: &Value) -> Option<Vec<f32>> {
    match raw {
        Value::Object(map) => map.values().find_map(extract_vector),
        Value::Array(items) => {
            if items.is_empty() {
                return None;
            }
            let mut out = Vec::with_capacity(items.len());
            for item in items {
                let component = item.as_f64()? as f32;
                if !component.is_finite() {
                    return None;
                }
                out.push(component);
            }
            Some(out)
        }
        _ => None,
    }
}

/// Best-effort integer chunk index. Missing payload key ⇒ `0`; a present but
/// non-numeric value ⇒ `1_000_000` so it sorts last (mirrors the Python
/// `except (TypeError, ValueError)` fallback).
#[must_use]
pub fn chunk_index_of(payload: &Value) -> i64 {
    const FALLBACK: i64 = 1_000_000;
    let Some(value) = payload.get("chunk_index") else {
        return 0;
    };
    match value {
        Value::Number(number) => number
            .as_i64()
            .or_else(|| number.as_u64().and_then(|u| i64::try_from(u).ok()))
            .or_else(|| number.as_f64().map(|f| f.trunc() as i64))
            .unwrap_or(FALLBACK),
        Value::String(text) => text.trim().parse::<i64>().unwrap_or(FALLBACK),
        Value::Bool(flag) => i64::from(*flag),
        _ => FALLBACK,
    }
}

/// Unit-length copy of `vector`; a zero/non-finite-norm vector is returned as-is.
#[must_use]
pub fn l2_normalize(vector: &[f32]) -> Vec<f32> {
    let norm = vector.iter().map(|&x| f64::from(x) * f64::from(x)).sum::<f64>().sqrt();
    if norm == 0.0 || !norm.is_finite() {
        return vector.to_vec();
    }
    vector.iter().map(|&x| (f64::from(x) / norm) as f32).collect()
}

/// True when a chunk is dominated by a bibliography / acknowledgements tail.
#[must_use]
pub fn looks_like_references(text: &str) -> bool {
    let stripped = text.trim();
    if stripped.is_empty() {
        return false;
    }
    let head_src: String = stripped.chars().take(120).collect();
    let head_collapsed = collapse_whitespace(&head_src);
    let head =
        head_collapsed.trim_start_matches(|c: char| matches!(c, ' ' | '.' | '#' | '0'..='9'));
    if REFS_HEADING.is_match(head) {
        return true;
    }
    let markers = REF_MARKER.find_iter(stripped).count();
    let dois = DOI.find_iter(stripped).count();
    let words = WORD.find_iter(stripped).count().max(1);
    markers >= 5 || (dois >= 3 && (dois as f64 / words as f64) > 0.025)
}

/// True when a chunk reads like body text (an abstract), not a title block.
#[must_use]
pub fn looks_like_prose(text: &str) -> bool {
    let collapsed = collapse_whitespace(text);
    if collapsed.chars().count() < 200 {
        return false;
    }
    if looks_like_references(&collapsed) {
        return false;
    }
    let words = WORD.find_iter(&collapsed).count();
    let sentences = SENTENCE.find_iter(&collapsed).count();
    words >= 30 && sentences >= 2
}

/// Section/position weight for a chunk before pooling (`>= 0`).
#[must_use]
pub fn chunk_weight(idx: i64, text: &str, lead_cutoff: i64, config: &DomainConfig) -> f64 {
    if looks_like_references(text) {
        return config.chunk_w_refs;
    }
    if idx <= lead_cutoff {
        return config.chunk_w_lead;
    }
    if RESULTS_CUE.is_match(text) {
        return config.chunk_w_results;
    }
    1.0
}

/// Collapse runs of whitespace to single spaces and trim, mirroring
/// `re.sub(r"\s+", " ", value).strip()`.
#[must_use]
pub fn collapse_whitespace(value: &str) -> String {
    value.split_whitespace().collect::<Vec<_>>().join(" ")
}

struct DocAccumulator {
    id: Box<str>,
    weighted_sum: Vec<f64>,
    plain_sum: Vec<f64>,
    weight_total: f64,
    count: u32,
    dim: usize,
    filename: Option<Box<str>>,
    /// Best representative so far: `(is_prose, chunk_index, point_index)`.
    rep: Option<(bool, i64, usize)>,
}

impl DocAccumulator {
    fn new(id: Box<str>, dim: usize) -> Self {
        Self {
            id,
            weighted_sum: vec![0.0; dim],
            plain_sum: vec![0.0; dim],
            weight_total: 0.0,
            count: 0,
            dim,
            filename: None,
            rep: None,
        }
    }

    fn direction(&self) -> Vec<f32> {
        if self.weight_total > EDGE_EPS && self.weighted_sum.iter().any(|&x| x != 0.0) {
            self.weighted_sum.iter().map(|&x| (x / self.weight_total) as f32).collect()
        } else {
            let count = f64::from(self.count);
            self.plain_sum.iter().map(|&x| (x / count) as f32).collect()
        }
    }
}

/// Pool chunk vectors per `document_id` into one direction and pick a rep chunk.
///
/// Chunk vectors are L2-normalized before pooling so the document vector is a
/// direction average; with weighting on, the abstract/intro and
/// results/conclusion lead while the references tail collapses to near zero.
#[must_use]
pub fn aggregate_docs(points: &[ChunkPoint], config: &DomainConfig) -> Vec<DocVector> {
    let lead_cutoff = config.lead_cutoff();
    let mut index: AHashMap<Box<str>, usize> = AHashMap::new();
    let mut accs: Vec<DocAccumulator> = Vec::new();

    for (point_index, point) in points.iter().enumerate() {
        if point.document_id.is_empty() || point.vector.is_empty() {
            continue;
        }
        let slot = match index.get(point.document_id.as_ref()) {
            Some(&slot) => slot,
            None => {
                let slot = accs.len();
                index.insert(point.document_id.clone(), slot);
                accs.push(DocAccumulator::new(point.document_id.clone(), point.vector.len()));
                slot
            }
        };
        let acc = &mut accs[slot];
        if point.vector.len() != acc.dim {
            continue;
        }
        if acc.filename.is_none() {
            if let Some(name) = &point.filename {
                if !name.is_empty() {
                    acc.filename = Some(name.clone());
                }
            }
        }
        let inv_norm = unit_scale(&point.vector);
        let weight = if config.chunk_weighting {
            chunk_weight(point.chunk_index, &point.text, lead_cutoff, config)
        } else {
            1.0
        };
        for ((weighted, plain), &raw) in
            acc.weighted_sum.iter_mut().zip(acc.plain_sum.iter_mut()).zip(point.vector.iter())
        {
            let scaled = f64::from(raw) * inv_norm;
            *plain += scaled;
            *weighted = weight.mul_add(scaled, *weighted);
        }
        acc.weight_total += weight;
        acc.count += 1;

        let is_prose = looks_like_prose(&point.text);
        let better =
            acc.rep.is_none_or(|(prose, idx, _)| (is_prose, -point.chunk_index) > (prose, -idx));
        if better {
            acc.rep = Some((is_prose, point.chunk_index, point_index));
        }
    }

    accs.into_iter()
        .filter(|acc| acc.count > 0)
        .map(|acc| {
            let vector = acc.direction();
            let rep_text = acc
                .rep
                .map_or_else(Box::default, |(_, _, point_index)| points[point_index].text.clone());
            DocVector {
                id: acc.id,
                vector,
                rep_text,
                chunk_count: acc.count,
                filename: acc.filename,
            }
        })
        .collect()
}

/// Build one graph node per raw chunk (RAPTOR chunk-granularity clustering).
///
/// Unlike [`aggregate_docs`], this performs NO per-document pooling: each Qdrant
/// point becomes its own [`DocVector`] keyed by the point id, so the cluster
/// `members` the pipeline returns are chunk point ids. Points with no id or no
/// vector are dropped (nothing to cluster on). Vectors are passed through raw;
/// `build_communities` L2-normalizes them before the kNN build, exactly as for the
/// document path.
#[must_use]
pub fn chunk_nodes(points: &[ChunkPoint]) -> Vec<DocVector> {
    points
        .iter()
        .filter(|point| !point.id.is_empty() && !point.vector.is_empty())
        .map(|point| DocVector {
            id: point.id.clone(),
            vector: point.vector.clone(),
            rep_text: point.text.clone(),
            chunk_count: 1,
            filename: point.filename.clone(),
        })
        .collect()
}

/// `1/‖v‖` when usable, else `1.0` (so a zero/non-finite-norm vector is pooled
/// unscaled, matching [`l2_normalize`]).
fn unit_scale(vector: &[f32]) -> f64 {
    let norm = vector.iter().map(|&x| f64::from(x) * f64::from(x)).sum::<f64>().sqrt();
    if norm == 0.0 || !norm.is_finite() {
        1.0
    } else {
        1.0 / norm
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    fn point(id: &str, vector: &[f32], idx: i64, text: &str) -> ChunkPoint {
        ChunkPoint {
            id: format!("{id}-{idx}").into(),
            document_id: id.into(),
            vector: vector.to_vec(),
            chunk_index: idx,
            text: text.into(),
            filename: None,
        }
    }

    #[test]
    fn extract_vector_handles_lists_and_named_maps() {
        assert_eq!(extract_vector(&json!([1.0, 2.0, 3.0])), Some(vec![1.0, 2.0, 3.0]));
        assert_eq!(extract_vector(&json!({"dense": [0.5, 0.5]})), Some(vec![0.5, 0.5]));
        assert_eq!(extract_vector(&json!([])), None);
        assert_eq!(extract_vector(&json!("nope")), None);
        assert_eq!(extract_vector(&json!([1.0, "x"])), None);
    }

    #[test]
    fn chunk_index_coercion() {
        assert_eq!(chunk_index_of(&json!({"chunk_index": 3})), 3);
        assert_eq!(chunk_index_of(&json!({"chunk_index": 2.9})), 2);
        assert_eq!(chunk_index_of(&json!({"chunk_index": "7"})), 7);
        assert_eq!(chunk_index_of(&json!({})), 0);
        assert_eq!(chunk_index_of(&json!({"chunk_index": "x"})), 1_000_000);
        assert_eq!(chunk_index_of(&json!({"chunk_index": null})), 1_000_000);
    }

    #[test]
    fn heuristics_classify_text() {
        let refs = "References [1] A. Author. [2] B. Author. [3] C. [4] D. [5] E. doi.org/10.1/x";
        assert!(looks_like_references(refs));
        let prose = "This abstract describes a careful method for pooling chunk vectors into a \
            single direction per document. We show that normalizing each chunk before averaging \
            consistently improves the resulting clusters across our experiments. The proposed \
            approach is simple, efficient, and effective over several different corpora and tasks.";
        assert!(looks_like_prose(prose));
        assert!(!looks_like_prose("Short title only"));
    }

    #[test]
    fn chunk_weight_tiers() {
        let config = DomainConfig::default();
        let cutoff = config.lead_cutoff();
        assert!((chunk_weight(0, "Title page", cutoff, &config) - 2.0).abs() < f64::EPSILON);
        assert!(
            (chunk_weight(9, "We present results and conclusions here.", cutoff, &config) - 1.5)
                .abs()
                < f64::EPSILON
        );
        assert!(
            (chunk_weight(9, "plain middle body chunk", cutoff, &config) - 1.0).abs()
                < f64::EPSILON
        );
        let refs = "References [1] x [2] y [3] z [4] w [5] v";
        assert!((chunk_weight(9, refs, cutoff, &config) - 0.05).abs() < f64::EPSILON);
    }

    #[test]
    fn aggregate_pools_per_document() {
        let config = DomainConfig::default();
        let points = vec![
            point("a", &[1.0, 0.0], 0, "lead chunk of doc a"),
            point("a", &[0.0, 1.0], 5, "second chunk of doc a"),
            point("b", &[1.0, 1.0], 0, "only chunk of doc b"),
        ];
        let docs = aggregate_docs(&points, &config);
        assert_eq!(docs.len(), 2);
        assert_eq!(docs[0].id.as_ref(), "a");
        assert_eq!(docs[0].chunk_count, 2);
        assert_eq!(docs[1].id.as_ref(), "b");
        assert_eq!(docs[1].chunk_count, 1);
        // Lead chunk of "a" has weight 2.0 → direction biased toward [1, 0].
        assert!(docs[0].vector[0] > docs[0].vector[1]);
    }

    #[test]
    fn chunk_nodes_keeps_one_node_per_point() {
        // No pooling: two chunks of the same document stay two nodes, keyed by id.
        let points = vec![
            point("a", &[1.0, 0.0], 0, "first chunk of a"),
            point("a", &[0.0, 1.0], 1, "second chunk of a"),
            point("b", &[1.0, 1.0], 0, "only chunk of b"),
        ];
        let nodes = chunk_nodes(&points);
        assert_eq!(nodes.len(), 3);
        assert_eq!(nodes[0].id.as_ref(), "a-0");
        assert_eq!(nodes[1].id.as_ref(), "a-1");
        assert_eq!(nodes[2].id.as_ref(), "b-0");
        assert!(nodes.iter().all(|node| node.chunk_count == 1));
        assert_eq!(nodes[0].rep_text.as_ref(), "first chunk of a");
    }

    #[test]
    fn chunk_nodes_drops_idless_or_empty_points() {
        let points = vec![
            ChunkPoint {
                id: String::new().into(),
                document_id: "a".into(),
                vector: vec![1.0, 0.0],
                chunk_index: 0,
                text: "no id".into(),
                filename: None,
            },
            ChunkPoint {
                id: "has-id".into(),
                document_id: "a".into(),
                vector: Vec::new(),
                chunk_index: 1,
                text: "no vector".into(),
                filename: None,
            },
            point("b", &[1.0, 1.0], 0, "kept"),
        ];
        let nodes = chunk_nodes(&points);
        assert_eq!(nodes.len(), 1);
        assert_eq!(nodes[0].id.as_ref(), "b-0");
    }

    #[test]
    fn aggregate_skips_mixed_dimensions() {
        let config = DomainConfig::default();
        let points = vec![point("a", &[1.0, 0.0], 0, "x"), point("a", &[1.0, 0.0, 0.0], 1, "y")];
        let docs = aggregate_docs(&points, &config);
        assert_eq!(docs.len(), 1);
        assert_eq!(docs[0].chunk_count, 1);
    }
}
