use super::*;

#[tokio::test]
async fn get_job_result_returns_pending_for_running_job() {
    let archive = sample_zip_archive();
    let state = AppState::default();
    let snapshot = state.jobs.create_job(sample_request(archive.path(), true));
    state.jobs.mark_running(&snapshot.id);
    let app = router_with_state(state);

    let response = app
        .oneshot(
            Request::builder()
                .uri(format!("/v1/jobs/{}/result", snapshot.id))
                .body(Body::empty())
                .expect("request should build"),
        )
        .await
        .expect("router should respond");

    assert_eq!(response.status(), StatusCode::ACCEPTED);

    let value = read_json(response).await;
    assert_eq!(value["state"], "running");
    assert_eq!(value["job_id"], snapshot.id);
}
#[tokio::test]
async fn get_job_result_returns_conflict_for_cancelled_job() {
    let archive = sample_zip_archive();
    let state = AppState::default();
    let snapshot = state.jobs.create_job(sample_request(archive.path(), true));
    state.jobs.mark_running(&snapshot.id);
    state.jobs.cancel(&snapshot.id).expect("job should exist for cancellation");
    let app = router_with_state(state);

    let response = app
        .oneshot(
            Request::builder()
                .uri(format!("/v1/jobs/{}/result", snapshot.id))
                .body(Body::empty())
                .expect("request should build"),
        )
        .await
        .expect("router should respond");

    assert_eq!(response.status(), StatusCode::CONFLICT);

    let value = read_json(response).await;
    assert_eq!(value["error"]["code"], "job_cancelled");
    assert_eq!(value["error"]["details"]["job_id"], snapshot.id);
}
#[tokio::test]
async fn get_job_result_returns_conflict_for_failed_job() {
    let archive = sample_zip_archive();
    let state = AppState::default();
    let snapshot = state.jobs.create_job(sample_request(archive.path(), true));
    state.jobs.mark_running(&snapshot.id);
    state
        .jobs
        .mark_failed(&snapshot.id, JobFailure::new("scan_failed", "fixture failure for tests"));
    let app = router_with_state(state);

    let response = app
        .oneshot(
            Request::builder()
                .uri(format!("/v1/jobs/{}/result", snapshot.id))
                .body(Body::empty())
                .expect("request should build"),
        )
        .await
        .expect("router should respond");

    assert_eq!(response.status(), StatusCode::CONFLICT);

    let value = read_json(response).await;
    assert_eq!(value["error"]["code"], "job_failed");
    assert_eq!(value["error"]["details"]["job_id"], snapshot.id);
    assert_eq!(value["error"]["details"]["failure_code"], "scan_failed");
}
#[tokio::test]
async fn in_memory_result_store_keeps_result_when_inline_limit_is_zero() {
    let archive = sample_zip_archive();
    let state = AppState::with_config(&ServiceConfig {
        result_store: ResultStoreConfig {
            backend: ResultStoreBackendKind::InMemory,
            inline_max_bytes: 0,
            ..ResultStoreConfig::default()
        },
        ..ServiceConfig::default()
    });
    let snapshot = state.jobs.create_job(sample_request(archive.path(), true));
    assert!(state.jobs.mark_running(&snapshot.id));
    assert!(state
        .jobs
        .mark_succeeded(&snapshot.id, scan_archive_fixture_response(archive.path(), true),));

    let response = router_with_state(state)
        .oneshot(
            Request::builder()
                .uri(format!("/v1/jobs/{}/result", snapshot.id))
                .body(Body::empty())
                .expect("request should build"),
        )
        .await
        .expect("router should respond");

    assert_eq!(response.status(), StatusCode::OK);
    assert_eq!(read_json(response).await["total_entries"], 2);
}
