//! [`VectorSource`] backed by the Qdrant REST scroll API.
//!
//! Ported from `scroll_qdrant` in `worker.py`: paginate
//! `POST /collections/{c}/points/scroll` with `with_vector`/`with_payload`,
//! filtering server-side by `owner_id` when an owner is given.

use std::future::Future;

use anyhow::{Context, Result};
use serde_json::{json, Value};

use crate::application::VectorSource;
use crate::domain::vector::{chunk_index_of, extract_vector, ChunkPoint};

/// Page size for the Qdrant scroll (matches the Python worker).
const SCROLL_LIMIT: u32 = 256;

/// Qdrant-backed vector source.
#[derive(Clone, Debug)]
pub struct QdrantVectorSource {
    client: reqwest::Client,
    base_url: String,
}

impl QdrantVectorSource {
    /// Build a source for the given Qdrant base URL (e.g. `http://qdrant:6333`).
    pub fn new(qdrant_url: impl Into<String>) -> Result<Self> {
        let client = reqwest::Client::builder().build().context("build qdrant http client")?;
        let base_url = qdrant_url.into().trim_end_matches('/').to_owned();
        Ok(Self { client, base_url })
    }
}

impl VectorSource for QdrantVectorSource {
    fn fetch(
        &self,
        owner_id: &str,
        collection: &str,
    ) -> impl Future<Output = Result<Vec<ChunkPoint>>> + Send {
        let url = format!("{}/collections/{collection}/points/scroll", self.base_url);
        let owner = owner_id.to_owned();
        let client = self.client.clone();
        async move {
            let mut points = Vec::new();
            let mut offset: Option<Value> = None;
            loop {
                let mut body = json!({
                    "limit": SCROLL_LIMIT,
                    "with_vector": true,
                    "with_payload": true,
                });
                if !owner.is_empty() {
                    body["filter"] =
                        json!({"must": [{"key": "owner_id", "match": {"value": owner}}]});
                }
                if let Some(cursor) = &offset {
                    body["offset"] = cursor.clone();
                }

                let response = client
                    .post(&url)
                    .json(&body)
                    .send()
                    .await
                    .context("qdrant scroll request failed")?
                    .error_for_status()
                    .context("qdrant scroll returned an error status")?;
                let envelope: Value =
                    response.json().await.context("decode qdrant scroll response")?;
                let result =
                    envelope.get("result").context("qdrant scroll response missing `result`")?;

                if let Some(batch) = result.get("points").and_then(Value::as_array) {
                    points.extend(batch.iter().filter_map(parse_point));
                }

                match result.get("next_page_offset") {
                    Some(next) if !next.is_null() => offset = Some(next.clone()),
                    _ => break,
                }
            }
            Ok(points)
        }
    }
}

/// Parse one Qdrant point into a [`ChunkPoint`], skipping points with no usable
/// document id or dense vector.
fn parse_point(point: &Value) -> Option<ChunkPoint> {
    let payload = point.get("payload")?;
    let document_id = payload.get("document_id").and_then(Value::as_str)?;
    if document_id.is_empty() {
        return None;
    }
    let vector = extract_vector(point.get("vector")?)?;
    if vector.is_empty() {
        return None;
    }
    let text = payload.get("text").and_then(Value::as_str).unwrap_or_default();
    let filename = payload.get("filename").and_then(Value::as_str).filter(|name| !name.is_empty());
    Some(ChunkPoint {
        id: point_id(point),
        document_id: document_id.into(),
        vector,
        chunk_index: chunk_index_of(payload),
        text: text.into(),
        filename: filename.map(Into::into),
    })
}

/// Stringify the Qdrant point id. Qdrant ids are either UUID strings (what
/// chunk-splitter writes) or unsigned integers; both map to a `Box<str>` so
/// chunk-granularity clustering can key nodes by the point id. Missing/odd ⇒ "".
fn point_id(point: &Value) -> Box<str> {
    match point.get("id") {
        Some(Value::String(text)) => text.as_str().into(),
        Some(Value::Number(number)) => number.to_string().into(),
        _ => Box::default(),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parse_point_extracts_fields() {
        let point = json!({
            "id": "point-9",
            "vector": [0.1, 0.2, 0.3],
            "payload": {"document_id": "doc-1", "chunk_index": 2, "text": "hello", "filename": "a.pdf"}
        });
        let parsed = parse_point(&point).expect("valid point");
        assert_eq!(parsed.id.as_ref(), "point-9");
        assert_eq!(parsed.document_id.as_ref(), "doc-1");
        assert_eq!(parsed.chunk_index, 2);
        assert_eq!(parsed.vector, vec![0.1, 0.2, 0.3]);
        assert_eq!(parsed.filename.as_deref(), Some("a.pdf"));
        // Integer ids are stringified.
        let numeric = json!({"id": 42, "vector": [1.0], "payload": {"document_id": "d"}});
        assert_eq!(parse_point(&numeric).expect("valid").id.as_ref(), "42");
    }

    #[test]
    fn parse_point_rejects_missing_vector_or_id() {
        assert!(parse_point(&json!({"payload": {"document_id": "x"}})).is_none());
        assert!(parse_point(&json!({"vector": [1.0], "payload": {}})).is_none());
        assert!(parse_point(&json!({"vector": [1.0], "payload": {"document_id": ""}})).is_none());
    }
}
