use super::*;

#[tokio::test]
async fn job_metrics_counts_expired_terminal_jobs_after_retention_cleanup() {
    let archive = sample_zip_archive();
    let state = AppState::with_job_retention(Duration::ZERO);
    let snapshot = state.jobs.create_job(sample_request(archive.path(), true));

    assert!(state.jobs.mark_running(&snapshot.id));
    assert!(state
        .jobs
        .mark_succeeded(&snapshot.id, scan_archive_fixture_response(archive.path(), true),));

    let app = router_with_state(state);
    let response = app
        .oneshot(
            Request::builder()
                .uri("/v1/jobs/metrics")
                .body(Body::empty())
                .expect("request should build"),
        )
        .await
        .expect("router should respond");

    assert_eq!(response.status(), StatusCode::OK);

    let value = read_json(response).await;
    assert_eq!(value["retention"]["terminal_job_retention_secs"], 0);
    assert_eq!(value["current"]["visible_jobs"], 0);
    assert_eq!(value["current"]["active_jobs"], 0);
    assert_eq!(value["current"]["terminal_jobs"], 0);
    assert_eq!(value["current"]["succeeded_jobs"], 0);
    assert_eq!(value["lifecycle"]["created_total"], 1);
    assert_eq!(value["lifecycle"]["started_total"], 1);
    assert_eq!(value["lifecycle"]["succeeded_total"], 1);
    assert_eq!(value["lifecycle"]["expired_total"], 1);
    assert_eq!(value["maintenance"]["cleanup_deleted_result_refs_total"], 0);
}
#[tokio::test]
async fn get_job_status_returns_not_found_for_unknown_job() {
    let response = router()
        .oneshot(
            Request::builder()
                .uri("/v1/jobs/job-99999999")
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
async fn expired_terminal_job_is_evicted_from_store() {
    let archive = sample_zip_archive();
    let state = AppState::with_job_retention(Duration::from_secs(60));
    let snapshot = state.jobs.create_job(sample_request(archive.path(), true));

    assert!(state.jobs.mark_running(&snapshot.id));
    assert!(state
        .jobs
        .mark_succeeded(&snapshot.id, scan_archive_fixture_response(archive.path(), true),));

    let terminal_snapshot =
        state.jobs.get(&snapshot.id).expect("terminal job should stay visible before cleanup");
    state.jobs.cleanup_expired_with_now(terminal_snapshot.updated_at_unix + 60);

    assert!(state.jobs.get(&snapshot.id).is_none());
}
#[tokio::test]
async fn expired_terminal_job_cleanup_tracks_deleted_result_reference_metrics() {
    let archive = sample_zip_archive();
    let runtime_dir = tempfile::tempdir().expect("tempdir should exist");
    let state = AppState::with_config(&ServiceConfig {
        job_retention: Duration::from_secs(60),
        result_store: ResultStoreConfig {
            backend: ResultStoreBackendKind::Filesystem,
            filesystem_dir: runtime_dir.path().join("results"),
            inline_max_bytes: 1,
            ..ResultStoreConfig::default()
        },
        ..ServiceConfig::default()
    });
    let snapshot = state.jobs.create_job(sample_request(archive.path(), true));

    assert!(state.jobs.mark_running(&snapshot.id));
    assert!(state
        .jobs
        .mark_succeeded(&snapshot.id, scan_archive_fixture_response(archive.path(), true),));

    let terminal_snapshot =
        state.jobs.get(&snapshot.id).expect("terminal job should stay visible before cleanup");
    assert!(
        terminal_snapshot.result_ref.is_some(),
        "small inline threshold should force result offload"
    );
    state.jobs.cleanup_expired_with_now(terminal_snapshot.updated_at_unix + 60);

    let metrics = state.jobs.try_metrics().expect("metrics should load");
    assert_eq!(metrics.expired_total, 1);
    assert_eq!(metrics.cleanup_deleted_result_refs_total, 1);
}
#[tokio::test]
async fn running_job_is_not_evicted_when_retention_is_zero() {
    let archive = sample_zip_archive();
    let state = AppState::with_job_retention(Duration::ZERO);
    let snapshot = state.jobs.create_job(sample_request(archive.path(), true));
    assert!(state.jobs.mark_running(&snapshot.id));
    let app = router_with_state(state);

    let response = app
        .oneshot(
            Request::builder()
                .uri(format!("/v1/jobs/{}", snapshot.id))
                .body(Body::empty())
                .expect("request should build"),
        )
        .await
        .expect("router should respond");

    assert_eq!(response.status(), StatusCode::OK);
    let value = read_json(response).await;
    assert_eq!(value["state"], "running");
}
#[tokio::test]
async fn expired_terminal_job_releases_idempotency_key_for_reuse() {
    let archive = sample_zip_archive();
    let state = AppState::with_job_retention(Duration::ZERO);
    let CreateJobOutcome::Created(first) = state.jobs.create_job_with_idempotency(
        sample_request(archive.path(), true),
        "scan-sample".to_owned(),
    ) else {
        panic!("first insert should create a new job");
    };

    assert!(state.jobs.mark_running(&first.id));
    assert!(state
        .jobs
        .mark_succeeded(&first.id, scan_archive_fixture_response(archive.path(), true),));

    let CreateJobOutcome::Created(second) = state.jobs.create_job_with_idempotency(
        sample_request(archive.path(), true),
        "scan-sample".to_owned(),
    ) else {
        panic!("expired binding should allow creating a new job");
    };

    assert_ne!(first.id, second.id);
}
#[tokio::test]
async fn expired_terminal_job_returns_not_found_from_status_endpoint() {
    let archive = sample_zip_archive();
    let state = AppState::with_job_retention(Duration::ZERO);
    let snapshot = state.jobs.create_job(sample_request(archive.path(), true));
    assert!(state.jobs.mark_running(&snapshot.id));
    assert!(state
        .jobs
        .mark_failed(&snapshot.id, JobFailure::new("scan_failed", "fixture failure for tests"),));
    let app = router_with_state(state);

    let response = app
        .oneshot(
            Request::builder()
                .uri(format!("/v1/jobs/{}", snapshot.id))
                .body(Body::empty())
                .expect("request should build"),
        )
        .await
        .expect("router should respond");

    assert_eq!(response.status(), StatusCode::NOT_FOUND);
    let value = read_json(response).await;
    assert_eq!(value["error"]["code"], "job_not_found");
    assert_eq!(value["error"]["details"]["job_id"], snapshot.id);
}
#[test]
fn startup_recovery_requeues_persisted_queued_and_running_jobs() {
    let archive = sample_zip_archive();
    let runtime_dir = tempfile::tempdir().expect("runtime tempdir should exist");
    let config = ServiceConfig {
        job_store: JobStoreRuntimeConfig {
            backend: JobStoreBackendKind::Filesystem,
            filesystem_path: runtime_dir.path().join("jobs/state.json"),
            ..JobStoreRuntimeConfig::default()
        },
        result_store: ResultStoreConfig {
            backend: ResultStoreBackendKind::Filesystem,
            filesystem_dir: runtime_dir.path().join("results"),
            ..ResultStoreConfig::default()
        },
        ..ServiceConfig::default()
    };

    let initial = AppState::with_config(&config);
    let queued = initial.jobs.create_job(sample_request(archive.path(), false));
    let running = initial.jobs.create_job(sample_request(archive.path(), false));
    assert!(initial.jobs.mark_running(&running.id));
    let succeeded = initial.jobs.create_job(sample_request(archive.path(), false));
    assert!(initial.jobs.mark_running(&succeeded.id));
    assert!(initial
        .jobs
        .mark_succeeded(&succeeded.id, scan_archive_fixture_response(archive.path(), false),));

    let original_running =
        initial.jobs.get(&running.id).expect("running job should remain visible before restart");
    assert_eq!(original_running.state, JobState::Running);

    drop(initial);

    let (recovered, recovered_jobs) =
        AppState::with_config_and_recovery(&config).expect("startup recovery should succeed");
    let recovered_ids: Vec<_> =
        recovered_jobs.iter().map(|snapshot| snapshot.id.as_str()).collect();
    assert_eq!(recovered_ids, vec![queued.id.as_str(), running.id.as_str()]);

    let queued_after_restart =
        recovered.jobs.get(&queued.id).expect("queued job should still exist after restart");
    assert_eq!(queued_after_restart.state, JobState::Queued);
    assert!(
        queued_after_restart.updated_at_unix >= queued.created_at_unix,
        "queued job should receive a fresh timestamp during recovery"
    );

    let running_after_restart =
        recovered.jobs.get(&running.id).expect("running job should still exist after restart");
    assert_eq!(running_after_restart.state, JobState::Queued);
    assert!(
        running_after_restart.updated_at_unix >= original_running.updated_at_unix,
        "running job should be requeued with a newer timestamp"
    );
    assert!(running_after_restart.error.is_none());

    let succeeded_after_restart =
        recovered.jobs.get(&succeeded.id).expect("terminal job should still exist after restart");
    assert_eq!(succeeded_after_restart.state, JobState::Succeeded);

    let metrics = recovered.jobs.try_metrics().expect("metrics should load");
    assert_eq!(metrics.queued_jobs, 2);
    assert_eq!(metrics.running_jobs, 0);
    assert_eq!(metrics.succeeded_jobs, 1);
    assert_eq!(metrics.started_total, 2);
    assert_eq!(metrics.recovery_runs_total, 1);
    assert_eq!(metrics.recovered_jobs_total, 2);
    assert_eq!(metrics.recovered_running_jobs_total, 1);
    assert_eq!(metrics.recovery_deleted_result_refs_total, 0);
}
