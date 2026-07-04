use super::*;
use axum::{
    body::Bytes,
    extract::State,
    http::{header, HeaderMap, Method, StatusCode, Uri},
    response::{IntoResponse, Response},
    Router,
};
use std::{
    collections::HashMap,
    fs,
    path::Path,
    sync::{Arc, Mutex},
    time::Duration,
};
use tokio::{net::TcpListener, sync::oneshot};

use super::super::config::{ResultStoreBackendKind, ResultStoreConfig};

#[test]
fn filesystem_result_store_roundtrips_and_gc_orphans() {
    let runtime_dir = tempfile::tempdir().expect("tempdir should exist");
    let store = ResultStore::new(&ResultStoreConfig {
        backend: ResultStoreBackendKind::Filesystem,
        filesystem_dir: runtime_dir.path().to_path_buf(),
        inline_max_bytes: 1,
        artifact_retention: Duration::ZERO,
        gc_interval: Duration::from_secs(60),
        ..ResultStoreConfig::default()
    });
    let result = sample_result();

    let persisted = store.persist("job-00000001", &result).expect("persist should succeed");
    let reference = persisted.reference.expect("result should be offloaded to filesystem");
    assert!(reference.location.ends_with(".json"));
    assert!(Path::new(&reference.location).exists());

    let loaded = store
        .load(&reference)
        .expect("load should succeed")
        .expect("persisted result should exist");
    assert_eq!(loaded.total_entries, 2);

    let stale_path =
        runtime_dir.path().join(artifact_file_name("job-00000002", unix_now().saturating_sub(10)));
    fs::write(&stale_path, b"{}").expect("stale artifact should write");
    store.run_gc_with_now(unix_now()).expect("gc should remove orphaned filesystem artifacts");
    assert!(!stale_path.exists(), "stale artifact should be deleted by gc");
    let maintenance = store.maintenance_stats();
    assert_eq!(maintenance.gc_runs_total, 2);
    assert_eq!(maintenance.gc_deleted_artifacts_total, 1);
    assert_eq!(maintenance.gc_failures_total, 0);

    store.delete(&reference).expect("delete should succeed");
    assert!(!Path::new(&reference.location).exists());
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn s3_result_store_roundtrips_and_gc_orphans() {
    let fixture = S3FixtureServer::start().await;
    let store = ResultStore::new(&ResultStoreConfig {
        backend: ResultStoreBackendKind::S3,
        s3_endpoint: Some(fixture.endpoint.clone()),
        s3_region: "us-east-1".to_owned(),
        s3_bucket: Some("result-bucket".to_owned()),
        s3_key_prefix: "test/results".to_owned(),
        s3_access_key_id: Some("test-access-key".to_owned()),
        s3_secret_access_key: Some("test-secret-key".to_owned()),
        s3_session_token: Some("test-session-token".to_owned()),
        s3_path_style: true,
        inline_max_bytes: 1,
        artifact_retention: Duration::ZERO,
        gc_interval: Duration::from_secs(60),
        ..ResultStoreConfig::default()
    });
    let result = sample_result();

    store.readiness_check().expect("s3 readiness should succeed");
    let persisted = store.persist("job-00000001", &result).expect("persist should upload to s3");
    let reference = persisted.reference.expect("result should be offloaded to s3");
    assert_eq!(reference.kind, JobResultReferenceKind::S3);
    assert!(fixture
        .request_log()
        .iter()
        .any(|request| request.authorization.starts_with("AWS4-HMAC-SHA256 Credential=")));
    assert!(fixture
        .request_log()
        .iter()
        .any(|request| request.session_token.as_deref() == Some("test-session-token")));
    assert!(fixture
        .request_log()
        .iter()
        .all(|request| request.host.as_deref() == Some("127.0.0.1")));

    let loaded = store
        .load(&reference)
        .expect("load should succeed")
        .expect("persisted s3 result should exist");
    assert_eq!(loaded.total_entries, 2);

    let stale_key = format!(
        "test/results/{}",
        artifact_file_name("job-00000002", unix_now().saturating_sub(10))
    );
    fixture.put_object(&stale_key, b"{}");
    store.run_gc_with_now(unix_now()).expect("gc should remove orphaned s3 artifacts");
    assert!(fixture.get_object(&stale_key).is_none(), "stale s3 artifact should be deleted by gc");
    let maintenance = store.maintenance_stats();
    assert_eq!(maintenance.gc_runs_total, 2);
    assert_eq!(maintenance.gc_deleted_artifacts_total, 1);
    assert_eq!(maintenance.gc_failures_total, 0);

    store.delete(&reference).expect("delete should remove s3 object");
    assert!(fixture.get_object(&reference.location).is_none());
}

fn sample_result() -> ScanArchiveResponse {
    ScanArchiveResponse {
        archive: super::super::model::ArchiveSummary {
            path: "/tmp/sample.zip".to_owned(),
            name: "sample.zip".to_owned(),
            size_bytes: 123,
            mtime_unix: 456,
        },
        total_entries: 2,
        total_files: 2,
        total_directories: 0,
        total_other_entries: 0,
        entry_kinds: vec![],
        types: vec![],
        mimes: vec![],
        entries: None,
    }
}

#[derive(Clone, Debug)]
struct LoggedRequest {
    authorization: String,
    session_token: Option<String>,
    host: Option<String>,
}

struct S3FixtureServer {
    endpoint: String,
    state: Arc<Mutex<HashMap<String, Vec<u8>>>>,
    requests: Arc<Mutex<Vec<LoggedRequest>>>,
    shutdown: Option<oneshot::Sender<()>>,
    handle: tokio::task::JoinHandle<()>,
}

#[derive(Clone)]
struct S3FixtureState {
    objects: Arc<Mutex<HashMap<String, Vec<u8>>>>,
    requests: Arc<Mutex<Vec<LoggedRequest>>>,
}

impl S3FixtureServer {
    async fn start() -> Self {
        let state = Arc::new(Mutex::new(HashMap::new()));
        let requests = Arc::new(Mutex::new(Vec::new()));
        let listener =
            TcpListener::bind("127.0.0.1:0").await.expect("fixture listener should bind");
        let addr = listener.local_addr().expect("fixture listener should expose local addr");
        let (shutdown_tx, shutdown_rx) = oneshot::channel();

        let handle = {
            let fixture_state =
                S3FixtureState { objects: Arc::clone(&state), requests: Arc::clone(&requests) };
            tokio::spawn(async move {
                let app = Router::new().fallback(handle_fixture_request).with_state(fixture_state);
                axum::serve(listener, app)
                    .with_graceful_shutdown(async move {
                        let _ = shutdown_rx.await;
                    })
                    .await
                    .expect("fixture server should stay available");
            })
        };

        Self {
            endpoint: format!("http://{addr}"),
            state,
            requests,
            shutdown: Some(shutdown_tx),
            handle,
        }
    }

    fn put_object(&self, key: &str, payload: &[u8]) {
        self.state
            .lock()
            .expect("fixture state should not be poisoned")
            .insert(key.to_owned(), payload.to_vec());
    }

    fn get_object(&self, key: &str) -> Option<Vec<u8>> {
        self.state.lock().expect("fixture state should not be poisoned").get(key).cloned()
    }

    fn request_log(&self) -> Vec<LoggedRequest> {
        self.requests.lock().expect("fixture request log should not be poisoned").clone()
    }
}

impl Drop for S3FixtureServer {
    fn drop(&mut self) {
        if let Some(shutdown) = self.shutdown.take() {
            let _ = shutdown.send(());
        }
        self.handle.abort();
    }
}

async fn handle_fixture_request(
    State(fixture): State<S3FixtureState>,
    method: Method,
    uri: Uri,
    headers: HeaderMap,
    body: Bytes,
) -> Response {
    fixture.requests.lock().expect("fixture request log should not be poisoned").push(
        LoggedRequest {
            authorization: header_value(&headers, header::AUTHORIZATION).unwrap_or_default(),
            session_token: header_value(&headers, "x-amz-security-token"),
            host: header_value(&headers, header::HOST)
                .map(|value| value.split(':').next().unwrap_or_default().to_owned()),
        },
    );

    let path = uri.path();
    let query = uri.query().unwrap_or_default();
    let key =
        path.trim_start_matches('/').strip_prefix("result-bucket/").unwrap_or_default().to_owned();

    match method {
        Method::PUT => {
            fixture
                .objects
                .lock()
                .expect("fixture state should not be poisoned")
                .insert(key, body.to_vec());
            StatusCode::OK.into_response()
        }
        Method::GET if query.contains("list-type=2") => {
            let prefix = query
                .split('&')
                .find_map(|pair| pair.split_once('='))
                .filter(|(name, _)| *name == "prefix")
                .map_or_else(String::new, |(_, value)| {
                    value.replace("%2F", "/").replace("%2f", "/")
                });
            let keys = fixture
                .objects
                .lock()
                .expect("fixture state should not be poisoned")
                .keys()
                .filter(|key| key.starts_with(&prefix))
                .cloned()
                .collect::<Vec<_>>();
            let body = format!(
                "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\
                <ListBucketResult>\
                  <IsTruncated>false</IsTruncated>\
                  {}\
                </ListBucketResult>",
                keys.iter()
                    .map(|key| format!("<Contents><Key>{key}</Key></Contents>"))
                    .collect::<String>()
            )
            .into_bytes();
            (StatusCode::OK, [(header::CONTENT_TYPE, "application/xml")], body).into_response()
        }
        Method::GET => {
            let payload = fixture
                .objects
                .lock()
                .expect("fixture state should not be poisoned")
                .get(&key)
                .cloned();
            if let Some(payload) = payload {
                (StatusCode::OK, [(header::CONTENT_TYPE, "application/json")], payload)
                    .into_response()
            } else {
                StatusCode::NOT_FOUND.into_response()
            }
        }
        Method::DELETE => {
            fixture.objects.lock().expect("fixture state should not be poisoned").remove(&key);
            StatusCode::NO_CONTENT.into_response()
        }
        _ => StatusCode::METHOD_NOT_ALLOWED.into_response(),
    }
}

fn header_value(headers: &HeaderMap, name: impl header::AsHeaderName) -> Option<String> {
    headers.get(name).and_then(|value| value.to_str().ok()).map(str::to_owned)
}
