use super::*;

#[tokio::test]
async fn healthz_route_is_available() {
    let response = router()
        .oneshot(
            Request::builder().uri("/healthz").body(Body::empty()).expect("request should build"),
        )
        .await
        .expect("router should respond");

    assert_eq!(response.status(), StatusCode::OK);
}
#[tokio::test]
async fn readyz_route_is_available() {
    let response = router()
        .oneshot(
            Request::builder().uri("/readyz").body(Body::empty()).expect("request should build"),
        )
        .await
        .expect("router should respond");

    assert_eq!(response.status(), StatusCode::OK);
}
#[tokio::test]
async fn readyz_returns_service_unavailable_when_storage_is_misconfigured() {
    let invalid_root = NamedTempFile::new().expect("temp file should exist");
    let config = ServiceConfig {
        job_store: JobStoreRuntimeConfig {
            backend: JobStoreBackendKind::Filesystem,
            filesystem_path: invalid_root.path().join("jobs/state.json"),
            ..JobStoreRuntimeConfig::default()
        },
        result_store: ResultStoreConfig {
            backend: ResultStoreBackendKind::Filesystem,
            filesystem_dir: invalid_root.path().join("results"),
            ..ResultStoreConfig::default()
        },
        ..ServiceConfig::default()
    };
    let response = router_with_state(AppState::with_config(&config))
        .oneshot(
            Request::builder().uri("/readyz").body(Body::empty()).expect("request should build"),
        )
        .await
        .expect("router should respond");

    assert_eq!(response.status(), StatusCode::SERVICE_UNAVAILABLE);
    let value = read_json(response).await;
    assert_eq!(value["error"]["code"], "service_not_ready");
}
#[tokio::test]
async fn readyz_returns_service_unavailable_when_s3_result_store_is_misconfigured() {
    let config = ServiceConfig {
        result_store: ResultStoreConfig {
            backend: ResultStoreBackendKind::S3,
            s3_endpoint: Some("http://127.0.0.1:9".to_owned()),
            s3_region: "us-east-1".to_owned(),
            s3_bucket: Some("result-bucket".to_owned()),
            s3_access_key_id: Some("access".to_owned()),
            s3_secret_access_key: Some("secret".to_owned()),
            ..ResultStoreConfig::default()
        },
        ..ServiceConfig::default()
    };
    let response = router_with_state(AppState::with_config(&config))
        .oneshot(
            Request::builder().uri("/readyz").body(Body::empty()).expect("request should build"),
        )
        .await
        .expect("router should respond");

    assert_eq!(response.status(), StatusCode::SERVICE_UNAVAILABLE);
    let value = read_json(response).await;
    assert_eq!(value["error"]["code"], "service_not_ready");
}
#[tokio::test]
async fn readyz_returns_service_unavailable_when_job_store_lock_is_held() {
    let runtime_dir = tempfile::tempdir().expect("tempdir should exist");
    let state_path = runtime_dir.path().join("jobs/state.json");
    let lock_path = state_path.with_file_name("state.json.lock");
    std::fs::create_dir_all(lock_path.parent().expect("lock file should have a parent"))
        .expect("lock directory should exist");
    let lock = OpenOptions::new()
        .read(true)
        .write(true)
        .create(true)
        .truncate(false)
        .open(&lock_path)
        .expect("lock file should open");
    lock.lock().expect("lock file should be acquired");

    let config = ServiceConfig {
        job_store: JobStoreRuntimeConfig {
            backend: JobStoreBackendKind::Filesystem,
            filesystem_path: state_path,
            lock_timeout: Duration::from_millis(5),
            lock_retry_interval: Duration::from_millis(1),
            ..JobStoreRuntimeConfig::default()
        },
        result_store: ResultStoreConfig {
            backend: ResultStoreBackendKind::Filesystem,
            filesystem_dir: runtime_dir.path().join("results"),
            ..ResultStoreConfig::default()
        },
        ..ServiceConfig::default()
    };

    let response = router_with_state(AppState::with_config(&config))
        .oneshot(
            Request::builder().uri("/readyz").body(Body::empty()).expect("request should build"),
        )
        .await
        .expect("router should respond");

    assert_eq!(response.status(), StatusCode::SERVICE_UNAVAILABLE);
    let value = read_json(response).await;
    assert_eq!(value["error"]["code"], "service_not_ready");
    assert!(value["error"]["details"]["reason"]
        .as_str()
        .is_some_and(|reason| reason.contains("timed out waiting for filesystem job store lock")));
}
#[tokio::test]
async fn readyz_returns_service_unavailable_when_job_store_state_is_corrupted() {
    let runtime_dir = tempfile::tempdir().expect("tempdir should exist");
    let state_path = runtime_dir.path().join("jobs/state.json");
    std::fs::create_dir_all(state_path.parent().expect("state file should have a parent"))
        .expect("job store directory should exist");
    std::fs::write(&state_path, b"{not-json").expect("corrupted state should be written");

    let config = ServiceConfig {
        job_store: JobStoreRuntimeConfig {
            backend: JobStoreBackendKind::Filesystem,
            filesystem_path: state_path,
            ..JobStoreRuntimeConfig::default()
        },
        result_store: ResultStoreConfig {
            backend: ResultStoreBackendKind::Filesystem,
            filesystem_dir: runtime_dir.path().join("results"),
            ..ResultStoreConfig::default()
        },
        ..ServiceConfig::default()
    };

    let response = router_with_state(AppState::with_config(&config))
        .oneshot(
            Request::builder().uri("/readyz").body(Body::empty()).expect("request should build"),
        )
        .await
        .expect("router should respond");

    assert_eq!(response.status(), StatusCode::SERVICE_UNAVAILABLE);
    let value = read_json(response).await;
    assert_eq!(value["error"]["code"], "service_not_ready");
    assert!(value["error"]["details"]["reason"]
        .as_str()
        .is_some_and(|reason| reason.contains("failed to deserialize job store state")));
}
#[tokio::test]
async fn readyz_tolerates_existing_unlocked_job_store_lockfile() {
    let runtime_dir = tempfile::tempdir().expect("tempdir should exist");
    let state_path = runtime_dir.path().join("jobs/state.json");
    let lock_path = state_path.with_file_name("state.json.lock");
    std::fs::create_dir_all(lock_path.parent().expect("lock file should have a parent"))
        .expect("lock directory should exist");
    std::fs::write(&lock_path, b"leftover").expect("stale lock marker should be writable");

    let config = ServiceConfig {
        job_store: JobStoreRuntimeConfig {
            backend: JobStoreBackendKind::Filesystem,
            filesystem_path: state_path,
            ..JobStoreRuntimeConfig::default()
        },
        result_store: ResultStoreConfig {
            backend: ResultStoreBackendKind::Filesystem,
            filesystem_dir: runtime_dir.path().join("results"),
            ..ResultStoreConfig::default()
        },
        ..ServiceConfig::default()
    };

    let response = router_with_state(AppState::with_config(&config))
        .oneshot(
            Request::builder().uri("/readyz").body(Body::empty()).expect("request should build"),
        )
        .await
        .expect("router should respond");

    assert_eq!(response.status(), StatusCode::OK);
}
