use super::*;

#[tokio::test]
async fn cancel_job_returns_not_found_for_unknown_job() {
    let response = router()
        .oneshot(
            Request::builder()
                .method(Method::POST)
                .uri("/v1/jobs/job-99999999/cancel")
                .body(Body::empty())
                .expect("request should build"),
        )
        .await
        .expect("router should respond");

    assert_eq!(response.status(), StatusCode::NOT_FOUND);

    let value = read_json(response).await;
    assert_eq!(value["error"]["code"], "job_not_found");
    assert_eq!(value["error"]["details"]["job_id"], "job-99999999");
}
#[tokio::test]
async fn cancel_job_transitions_queued_job_to_cancelled() {
    let archive = sample_zip_archive();
    let state = AppState::default();
    let snapshot = state.jobs.create_job(sample_request(archive.path(), true));
    let app = router_with_state(state.clone());

    let response = app
        .oneshot(
            Request::builder()
                .method(Method::POST)
                .uri(format!("/v1/jobs/{}/cancel", snapshot.id))
                .body(Body::empty())
                .expect("request should build"),
        )
        .await
        .expect("router should respond");

    assert_eq!(response.status(), StatusCode::OK);

    let value = read_json(response).await;
    assert_eq!(value["job_id"], snapshot.id);
    assert_eq!(value["state"], "cancelled");
    assert_eq!(value["error"]["code"], "job_cancelled");

    let stored = state.jobs.get(&snapshot.id).expect("cancelled job should remain in store");
    assert_eq!(stored.state, JobState::Cancelled);
}
#[tokio::test]
async fn cancel_job_marks_cancellation_signal() {
    let archive = sample_zip_archive();
    let state = AppState::default();
    let snapshot = state.jobs.create_job(sample_request(archive.path(), true));
    let cancellation =
        state.jobs.cancellation(&snapshot.id).expect("job should expose cancellation handle");

    assert!(!cancellation.is_cancelled());

    let outcome = state.jobs.cancel(&snapshot.id).expect("job should exist for cancellation");

    assert!(matches!(outcome, CancelJobOutcome::Cancelled(_)));
    assert!(cancellation.is_cancelled());
}
#[tokio::test]
async fn cancel_job_is_idempotent_for_already_cancelled_job() {
    let archive = sample_zip_archive();
    let state = AppState::default();
    let snapshot = state.jobs.create_job(sample_request(archive.path(), true));
    let first_cancel = state.jobs.cancel(&snapshot.id).expect("job should exist for cancellation");
    assert!(matches!(first_cancel, CancelJobOutcome::Cancelled(_)));
    let app = router_with_state(state);

    let response = app
        .oneshot(
            Request::builder()
                .method(Method::POST)
                .uri(format!("/v1/jobs/{}/cancel", snapshot.id))
                .body(Body::empty())
                .expect("request should build"),
        )
        .await
        .expect("router should respond");

    assert_eq!(response.status(), StatusCode::OK);

    let value = read_json(response).await;
    assert_eq!(value["state"], "cancelled");
    assert_eq!(value["error"]["code"], "job_cancelled");
}
#[tokio::test]
async fn cancel_job_returns_conflict_for_succeeded_job() {
    let archive = sample_zip_archive();
    let state = AppState::default();
    let snapshot = state.jobs.create_job(sample_request(archive.path(), true));
    state.jobs.mark_running(&snapshot.id);
    state.jobs.mark_succeeded(&snapshot.id, scan_archive_fixture_response(archive.path(), true));
    let app = router_with_state(state);

    let response = app
        .oneshot(
            Request::builder()
                .method(Method::POST)
                .uri(format!("/v1/jobs/{}/cancel", snapshot.id))
                .body(Body::empty())
                .expect("request should build"),
        )
        .await
        .expect("router should respond");

    assert_eq!(response.status(), StatusCode::CONFLICT);

    let value = read_json(response).await;
    assert_eq!(value["error"]["code"], "job_not_cancellable");
    assert_eq!(value["error"]["details"]["job_id"], snapshot.id);
    assert_eq!(value["error"]["details"]["state"], "succeeded");
}
#[tokio::test]
async fn cancelled_job_keeps_terminal_state_even_if_worker_reports_afterwards() {
    let archive = sample_zip_archive();
    let state = AppState::default();
    let snapshot = state.jobs.create_job(sample_request(archive.path(), true));

    assert!(state.jobs.mark_running(&snapshot.id));
    let cancelled = state.jobs.cancel(&snapshot.id).expect("job should exist for cancellation");
    assert!(matches!(cancelled, CancelJobOutcome::Cancelled(_)));

    assert!(!state
        .jobs
        .mark_succeeded(&snapshot.id, scan_archive_fixture_response(archive.path(), true),));
    assert!(!state.jobs.mark_failed(&snapshot.id, JobFailure::new("scan_failed", "late failure")));

    let stored = state.jobs.get(&snapshot.id).expect("job should remain in store");
    assert_eq!(stored.state, JobState::Cancelled);
    assert!(stored.result.is_none());
    assert_eq!(stored.error.as_ref().map(|failure| failure.code.as_ref()), Some("job_cancelled"));
}
#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn cancel_job_stops_running_worker_cooperatively() {
    let archive = sample_zip_archive();
    let state = AppState::default();
    let snapshot = state.jobs.create_job(sample_request(archive.path(), true));
    let cancellation =
        state.jobs.cancellation(&snapshot.id).expect("job should expose cancellation handle");
    let worker_stopped = Arc::new(AtomicBool::new(false));
    let worker_stopped_for_runner = Arc::clone(&worker_stopped);

    let handle = spawn_scan_job_with_runner(
        state.clone(),
        snapshot.id.clone(),
        sample_request(archive.path(), true),
        cancellation,
        move |_request, cancellation| {
            while !cancellation.is_cancelled() {
                thread::sleep(Duration::from_millis(5));
            }
            worker_stopped_for_runner.store(true, Ordering::Relaxed);
            Err(scan_cancelled_io_error().into())
        },
    );

    wait_for_job_state(&state, &snapshot.id, JobState::Running).await;

    let app = router_with_state(state.clone());
    let response = app
        .oneshot(
            Request::builder()
                .method(Method::POST)
                .uri(format!("/v1/jobs/{}/cancel", snapshot.id))
                .body(Body::empty())
                .expect("request should build"),
        )
        .await
        .expect("router should respond");

    assert_eq!(response.status(), StatusCode::OK);
    let value = read_json(response).await;
    assert_eq!(value["state"], "cancelled");

    tokio::time::timeout(Duration::from_secs(1), handle)
        .await
        .expect("worker should stop after cancellation")
        .expect("join should succeed");

    assert!(worker_stopped.load(Ordering::Relaxed));

    let stored = state.jobs.get(&snapshot.id).expect("cancelled job should remain in store");
    assert_eq!(stored.state, JobState::Cancelled);
    assert_eq!(stored.error.as_ref().map(|failure| failure.code.as_ref()), Some("job_cancelled"));
}
