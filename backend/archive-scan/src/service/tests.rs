use super::*;
use crate::cancel::scan_cancelled_io_error;
use axum::{
    body::{to_bytes, Body},
    http::{header, Method, Request},
    routing::get,
};
use serde_json::Value;
use std::{
    collections::HashMap,
    fs::{File, OpenOptions},
    io::Write,
    path::{Path, PathBuf},
    sync::{
        atomic::{AtomicBool, Ordering},
        Arc,
    },
    thread,
    time::Duration,
};
use tempfile::{Builder, NamedTempFile};
use tower::util::ServiceExt;
use zip::{write::SimpleFileOptions, CompressionMethod, ZipWriter};

const LARGE_ENTRY_NAME: &str = "large.txt";
const LARGE_ENTRY_SIZE: usize = 64 * 1024;
const TRUNCATED_ENTRY_BYTES_SCANNED: u64 = 8 * 1024;

#[path = "tests/api_contract.rs"]
mod api_contract;
#[path = "tests/async_job_execution.rs"]
mod async_job_execution;
#[path = "tests/health_readiness.rs"]
mod health_readiness;
#[path = "tests/job_cancellation.rs"]
mod job_cancellation;
#[path = "tests/job_creation.rs"]
mod job_creation;
#[path = "tests/job_metrics.rs"]
mod job_metrics;
#[path = "tests/job_results.rs"]
mod job_results;
#[path = "tests/job_retention_recovery.rs"]
mod job_retention_recovery;
#[path = "tests/scan_routes.rs"]
mod scan_routes;
#[path = "tests/validation_errors.rs"]
mod validation_errors;

fn post_json(uri: &str, payload: Value) -> Request<Body> {
    Request::builder()
        .method("POST")
        .uri(uri)
        .header("content-type", "application/json")
        .body(Body::from(serde_json::to_vec(&payload).expect("request payload should serialize")))
        .expect("request should build")
}

fn router_with_private_object_sources() -> Router {
    let config = ServiceConfig {
        source_download: SourceDownloadConfig {
            allow_private_networks: true,
            ..SourceDownloadConfig::default()
        },
        ..ServiceConfig::default()
    };
    router_with_state(AppState::with_config(&config))
}

fn post_json_with_idempotency_key(
    uri: &str,
    payload: Value,
    idempotency_key: &str,
) -> Request<Body> {
    Request::builder()
        .method("POST")
        .uri(uri)
        .header("content-type", "application/json")
        .header("idempotency-key", idempotency_key)
        .body(Body::from(serde_json::to_vec(&payload).expect("request payload should serialize")))
        .expect("request should build")
}

fn sample_request(path: &Path, include_entries: bool) -> ScanArchiveRequest {
    ScanArchiveRequest {
        path: Some(path.display().to_string()),
        source: None,
        header_bytes: 512,
        block_size: 2 * 1024 * 1024,
        full_hash: false,
        fast_only: true,
        include_entries,
    }
}

fn shared_source_request(path: &Path, include_entries: bool) -> ScanArchiveRequest {
    ScanArchiveRequest {
        path: None,
        source: Some(model::ScanArchiveSource {
            kind: model::ScanSourceKind::SharedFilesystemPath,
            path: Some(path.display().to_string()),
            url: None,
        }),
        header_bytes: 512,
        block_size: 2 * 1024 * 1024,
        full_hash: false,
        fast_only: true,
        include_entries,
    }
}

fn object_storage_source_request(url: &str, include_entries: bool) -> ScanArchiveRequest {
    ScanArchiveRequest {
        path: None,
        source: Some(model::ScanArchiveSource {
            kind: model::ScanSourceKind::ObjectStorageUrl,
            path: None,
            url: Some(url.to_owned()),
        }),
        header_bytes: 512,
        block_size: 2 * 1024 * 1024,
        full_hash: false,
        fast_only: true,
        include_entries,
    }
}

fn scan_archive_fixture_response(path: &Path, include_entries: bool) -> ScanArchiveResponse {
    scan_archive_at_path(path, sample_request(path, include_entries))
        .expect("fixture archive should scan successfully")
}

async fn wait_for_job_result(app: &Router, job_id: &str) -> Value {
    for _ in 0..100 {
        let response = app
            .clone()
            .oneshot(
                Request::builder()
                    .uri(format!("/v1/jobs/{job_id}/result"))
                    .body(Body::empty())
                    .expect("request should build"),
            )
            .await
            .expect("router should respond");

        let status = response.status();
        let value = read_json(response).await;

        if status == StatusCode::OK {
            return value;
        }

        assert_eq!(status, StatusCode::ACCEPTED, "job should either complete or still be pending");
        thread::sleep(Duration::from_millis(10));
    }

    panic!("job did not complete in time");
}

async fn wait_for_job_state(state: &AppState, job_id: &str, expected: JobState) {
    for _ in 0..100 {
        if state.jobs.get(job_id).is_some_and(|job| job.state == expected) {
            return;
        }
        tokio::time::sleep(Duration::from_millis(10)).await;
    }

    panic!("job did not reach expected state in time");
}

async fn read_json(response: Response) -> Value {
    let body = to_bytes(response.into_body(), usize::MAX).await.expect("body should read");
    serde_json::from_slice(&body).expect("response should be json")
}

async fn read_text(response: Response) -> String {
    let body = to_bytes(response.into_body(), usize::MAX).await.expect("body should read");
    String::from_utf8(body.to_vec()).expect("response should be utf-8")
}

fn assert_real_fixture_summary(value: &Value, archive_path: &Path) {
    assert_eq!(value["archive"]["name"], "asr-master.zip");
    assert_eq!(value["archive"]["path"], archive_path.display().to_string());
    assert_eq!(value["total_entries"], 35);
    assert_eq!(value["total_files"], 28);
    assert_eq!(value["total_directories"], 7);
    assert_eq!(value["total_other_entries"], 0);

    let counts = type_counts(value);
    assert_eq!(counts.get("python"), Some(&10));
    assert_eq!(counts.get("txt"), Some(&5));
    assert_eq!(counts.get("yaml"), Some(&1));
    assert_eq!(counts.get("html"), Some(&1));
    assert_eq!(counts.get("wav"), Some(&2));

    let entry_kind_counts = entry_kind_counts(value);
    assert_eq!(entry_kind_counts.get("file"), Some(&28));
    assert_eq!(entry_kind_counts.get("directory"), Some(&7));

    let mimes = mime_counts(value);
    assert_eq!(mimes.get("text/x-python"), Some(&10));
    assert_eq!(mimes.get("text/plain"), Some(&5));
    assert_eq!(mimes.get("audio/wav"), Some(&2));

    let entries =
        value["entries"].as_array().expect("entries should be present for include_entries=true");
    assert_eq!(entries.len(), 35);
    assert!(
        entries.iter().all(|entry| entry["archive_name"] == "asr-master.zip"),
        "every row should reference the real archive fixture"
    );

    let entries_with_head_hash = entries
        .iter()
        .filter(|entry| entry["head_b3"].as_str().is_some_and(|hash| hash.len() == 64))
        .count();
    let entries_without_head_hash =
        entries.iter().filter(|entry| entry.get("head_b3").is_none()).count();
    let empty_entries =
        entries.iter().filter(|entry| entry["bytes_scanned"].as_u64() == Some(0)).count();

    assert_eq!(entries_without_head_hash, empty_entries);
    assert_eq!(entries_without_head_hash, 7);
    assert_eq!(entries_with_head_hash + entries_without_head_hash, entries.len());
    assert_eq!(entries.iter().filter(|entry| entry["entry_kind"] == "directory").count(), 7);
    assert!(
        entries.iter().all(|entry| entry.get("full_b3").is_none()),
        "full hashes should stay absent when full_hash=false"
    );
}

fn find_entry<'a>(value: &'a Value, entry_name: &str) -> &'a Value {
    value["entries"]
        .as_array()
        .expect("entries should be present")
        .iter()
        .find(|entry| entry["entry_name"] == entry_name)
        .expect("requested entry should be present")
}

fn assert_large_text_entry(
    entry: &Value,
    archive_path: &Path,
    payload: &[u8],
    expect_full_hash: bool,
) {
    let expected_head = blake3::hash(&payload[..model::DEFAULT_HEADER_BYTES]).to_hex().to_string();
    let archive_name = archive_path
        .file_name()
        .and_then(|name| name.to_str())
        .expect("archive path should have a file name");

    assert_eq!(entry["archive_name"], archive_name);
    assert_eq!(entry["archive_path"], archive_path.display().to_string());
    assert_eq!(entry["entry_name"], LARGE_ENTRY_NAME);
    assert_eq!(entry["entry_kind"], "file");
    assert_eq!(entry["label"], "txt");
    assert_eq!(entry["mime"], "text/plain");
    assert_eq!(entry["detected_by"], "heuristic");
    assert_eq!(entry["header_len"], model::DEFAULT_HEADER_BYTES as u64);
    assert_eq!(entry["head_b3"], expected_head);
    assert_eq!(
        entry["bytes_scanned"],
        if expect_full_hash { payload.len() as u64 } else { TRUNCATED_ENTRY_BYTES_SCANNED }
    );
    assert_eq!(entry["truncated_scan"], !expect_full_hash);

    if expect_full_hash {
        let expected_full = blake3::hash(payload).to_hex().to_string();
        assert_eq!(entry["full_b3"], expected_full);
    } else {
        assert!(entry.get("full_b3").is_none());
    }
}

fn sample_zip_archive() -> NamedTempFile {
    let archive = Builder::new()
        .prefix("sample-")
        .suffix(".zip")
        .tempfile()
        .expect("temp zip should be created");
    let file = File::create(archive.path()).expect("zip file should open");
    let mut writer = ZipWriter::new(file);
    let options = SimpleFileOptions::default().compression_method(CompressionMethod::Stored);

    writer.start_file("alpha.zip", options).expect("should start alpha entry");
    writer.write_all(b"PK\x03\x04payload").expect("should write alpha entry");
    writer.start_file("nested/bravo.pdf", options).expect("should start bravo entry");
    writer.write_all(b"%PDF-1.7 payload").expect("should write bravo entry");
    writer.finish().expect("zip archive should finish");

    archive
}

struct LargeArchiveFixture {
    archive: NamedTempFile,
    payload: Vec<u8>,
}

struct ObjectStorageFixtureServer {
    url: String,
    handle: tokio::task::JoinHandle<()>,
}

impl Drop for ObjectStorageFixtureServer {
    fn drop(&mut self) {
        self.handle.abort();
    }
}

fn large_text_payload() -> Vec<u8> {
    let line = b"large archive scan fixture line 0123456789abcdefghijklmnopqrstuvwxyz\n";
    let repeats = LARGE_ENTRY_SIZE / line.len() + 1;
    let mut payload = line.repeat(repeats);
    payload.truncate(LARGE_ENTRY_SIZE);
    payload
}

fn large_zip_archive() -> LargeArchiveFixture {
    let archive = Builder::new()
        .prefix("large-")
        .suffix(".zip")
        .tempfile()
        .expect("temp zip should be created");
    let payload = large_text_payload();
    let file = File::create(archive.path()).expect("zip file should open");
    let mut writer = ZipWriter::new(file);
    let options = SimpleFileOptions::default().compression_method(CompressionMethod::Stored);

    writer.start_file(LARGE_ENTRY_NAME, options).expect("should start large entry");
    writer.write_all(&payload).expect("should write large entry payload");
    writer.finish().expect("zip archive should finish");

    LargeArchiveFixture { archive, payload }
}

async fn object_storage_fixture_server() -> ObjectStorageFixtureServer {
    let archive = sample_zip_archive();
    let payload = Arc::new(
        std::fs::read(archive.path()).expect("object storage fixture archive should read"),
    );
    let listener =
        tokio::net::TcpListener::bind("127.0.0.1:0").await.expect("fixture listener should bind");
    let addr = listener.local_addr().expect("fixture listener should expose local address");
    let app = Router::new().route(
        "/fixtures/remote-sample.zip",
        get({
            let payload = Arc::clone(&payload);
            move || {
                let payload = Arc::clone(&payload);
                async move { ([("content-type", "application/zip")], payload.as_ref().clone()) }
            }
        }),
    );
    let handle = tokio::spawn(async move {
        axum::serve(listener, app).await.expect("fixture server should stay available");
    });

    ObjectStorageFixtureServer { url: format!("http://{addr}/fixtures/remote-sample.zip"), handle }
}

fn real_archive_fixture() -> Option<PathBuf> {
    let path = PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("data/asr-master.zip");
    path.exists().then_some(path)
}

fn type_counts(value: &Value) -> HashMap<String, u64> {
    value["types"]
        .as_array()
        .expect("types should be an array")
        .iter()
        .map(|entry| {
            (
                entry["label"].as_str().expect("label should be a string").to_owned(),
                entry["count"].as_u64().expect("count should be a u64"),
            )
        })
        .collect()
}

fn mime_counts(value: &Value) -> HashMap<String, u64> {
    value["mimes"]
        .as_array()
        .expect("mimes should be an array")
        .iter()
        .map(|entry| {
            (
                entry["mime"].as_str().expect("mime should be a string").to_owned(),
                entry["count"].as_u64().expect("count should be a u64"),
            )
        })
        .collect()
}

fn entry_kind_counts(value: &Value) -> HashMap<String, u64> {
    value["entry_kinds"]
        .as_array()
        .expect("entry_kinds should be an array")
        .iter()
        .map(|entry| {
            (
                entry["kind"].as_str().expect("kind should be a string").to_owned(),
                entry["count"].as_u64().expect("count should be a u64"),
            )
        })
        .collect()
}
