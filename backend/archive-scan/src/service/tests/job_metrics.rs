use super::*;

#[tokio::test]
async fn job_metrics_returns_current_state_counts_and_lifecycle_totals() {
    let archive = sample_zip_archive();
    let state = AppState::with_job_retention(Duration::from_secs(60));

    let queued = state.jobs.create_job(sample_request(archive.path(), true));

    let running = state.jobs.create_job(sample_request(archive.path(), true));
    assert!(state.jobs.mark_running(&running.id));

    let succeeded = state.jobs.create_job(sample_request(archive.path(), true));
    assert!(state.jobs.mark_running(&succeeded.id));
    assert!(state
        .jobs
        .mark_succeeded(&succeeded.id, scan_archive_fixture_response(archive.path(), true),));

    let failed = state.jobs.create_job(sample_request(archive.path(), true));
    assert!(state.jobs.mark_running(&failed.id));
    assert!(state
        .jobs
        .mark_failed(&failed.id, JobFailure::new("scan_failed", "fixture failure for tests"),));

    let cancelled = state.jobs.create_job(sample_request(archive.path(), true));
    assert!(matches!(state.jobs.cancel(&cancelled.id), Some(CancelJobOutcome::Cancelled(_))));

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
    assert_eq!(value["retention"]["terminal_job_retention_secs"], 60);
    assert_eq!(value["current"]["visible_jobs"], 5);
    assert_eq!(value["current"]["active_jobs"], 2);
    assert_eq!(value["current"]["terminal_jobs"], 3);
    assert_eq!(value["current"]["queued_jobs"], 1);
    assert_eq!(value["current"]["running_jobs"], 1);
    assert_eq!(value["current"]["succeeded_jobs"], 1);
    assert_eq!(value["current"]["failed_jobs"], 1);
    assert_eq!(value["current"]["cancelled_jobs"], 1);
    assert_eq!(value["lifecycle"]["created_total"], 5);
    assert_eq!(value["lifecycle"]["started_total"], 3);
    assert_eq!(value["lifecycle"]["succeeded_total"], 1);
    assert_eq!(value["lifecycle"]["failed_total"], 1);
    assert_eq!(value["lifecycle"]["cancelled_total"], 1);
    assert_eq!(value["lifecycle"]["expired_total"], 0);
    assert_eq!(value["maintenance"]["recovery_runs_total"], 0);
    assert_eq!(value["maintenance"]["recovered_jobs_total"], 0);
    assert_eq!(value["maintenance"]["recovered_running_jobs_total"], 0);
    assert_eq!(value["maintenance"]["recovery_deleted_result_refs_total"], 0);
    assert_eq!(value["maintenance"]["cleanup_deleted_result_refs_total"], 0);
    assert_eq!(value["maintenance"]["result_artifact_gc_runs_total"], 0);
    assert_eq!(value["maintenance"]["result_artifact_gc_deleted_total"], 0);
    assert_eq!(value["maintenance"]["result_artifact_gc_failures_total"], 0);

    let queued_id = queued.id;
    assert_eq!(
        value["current"]["visible_jobs"].as_u64(),
        Some(5),
        "queued job {queued_id} should keep the store non-terminal"
    );
}
#[tokio::test]
async fn job_metrics_reports_active_storage_configuration() {
    let runtime_dir = tempfile::tempdir().expect("tempdir should exist");
    let config = ServiceConfig {
        job_store: JobStoreRuntimeConfig {
            backend: JobStoreBackendKind::Filesystem,
            filesystem_path: runtime_dir.path().join("jobs/state.json"),
            ..JobStoreRuntimeConfig::default()
        },
        result_store: ResultStoreConfig {
            backend: ResultStoreBackendKind::Filesystem,
            filesystem_dir: runtime_dir.path().join("results"),
            inline_max_bytes: 1234,
            ..ResultStoreConfig::default()
        },
        ..ServiceConfig::default()
    };
    let app = router_with_state(AppState::with_config(&config));

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
    assert_eq!(value["storage"]["job_store_backend"], "filesystem");
    assert_eq!(value["storage"]["result_store_backend"], "filesystem");
    assert_eq!(
        value["storage"]["job_store_filesystem_path"],
        runtime_dir.path().join("jobs/state.json").display().to_string()
    );
    assert_eq!(
        value["storage"]["result_store_filesystem_dir"],
        runtime_dir.path().join("results").display().to_string()
    );
    assert_eq!(value["storage"]["result_store_s3_endpoint"], Value::Null);
    assert_eq!(value["storage"]["result_store_s3_bucket"], Value::Null);
    assert_eq!(value["storage"]["result_store_s3_region"], "us-east-1");
    assert_eq!(value["storage"]["result_store_s3_key_prefix"], "archive-scan/results");
    assert_eq!(value["storage"]["result_store_s3_path_style"], false);
    assert_eq!(value["storage"]["job_store_postgres_url"], Value::Null);
    assert_eq!(value["storage"]["job_store_redis_max_connections"], 16);
    assert_eq!(value["storage"]["job_store_redis_cleanup_batch_size"], 128);
    assert_eq!(value["storage"]["job_store_postgres_table_prefix"], "archive_scan");
    assert_eq!(value["storage"]["job_store_postgres_max_connections"], 16);
    assert_eq!(value["storage"]["result_inline_max_bytes"], 1234);
    assert_eq!(value["storage"]["result_artifact_retention_secs"], 3600);
    assert_eq!(value["storage"]["result_artifact_gc_interval_secs"], 300);
    assert_eq!(value["maintenance"]["result_artifact_gc_runs_total"], 0);
    assert_eq!(value["maintenance"]["result_artifact_gc_deleted_total"], 0);
    assert_eq!(value["maintenance"]["result_artifact_gc_failures_total"], 0);
}
#[tokio::test]
async fn job_metrics_reports_s3_result_store_configuration() {
    let config = ServiceConfig {
        result_store: ResultStoreConfig {
            backend: ResultStoreBackendKind::S3,
            s3_endpoint: Some("http://127.0.0.1:9000".to_owned()),
            s3_region: "eu-central-1".to_owned(),
            s3_bucket: Some("result-bucket".to_owned()),
            s3_key_prefix: "prod/results".to_owned(),
            s3_access_key_id: Some("access".to_owned()),
            s3_secret_access_key: Some("secret".to_owned()),
            s3_path_style: true,
            inline_max_bytes: 2048,
            artifact_retention: Duration::from_secs(7200),
            gc_interval: Duration::from_secs(60),
            ..ResultStoreConfig::default()
        },
        ..ServiceConfig::default()
    };
    let app = router_with_state(AppState::with_config(&config));

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
    assert_eq!(value["storage"]["result_store_backend"], "s3");
    assert_eq!(value["storage"]["result_store_filesystem_dir"], Value::Null);
    assert_eq!(value["storage"]["result_store_s3_endpoint"], "http://127.0.0.1:9000");
    assert_eq!(value["storage"]["result_store_s3_bucket"], "result-bucket");
    assert_eq!(value["storage"]["result_store_s3_region"], "eu-central-1");
    assert_eq!(value["storage"]["result_store_s3_key_prefix"], "prod/results");
    assert_eq!(value["storage"]["result_store_s3_path_style"], true);
    assert_eq!(value["storage"]["result_inline_max_bytes"], 2048);
    assert_eq!(value["storage"]["result_artifact_retention_secs"], 7200);
    assert_eq!(value["storage"]["result_artifact_gc_interval_secs"], 60);
}
#[tokio::test]
async fn prometheus_metrics_route_exports_job_and_maintenance_counters() {
    let archive = sample_zip_archive();
    let state = AppState::with_job_retention(Duration::from_secs(90));
    state.jobs.create_job(sample_request(archive.path(), true));
    let succeeded = state.jobs.create_job(sample_request(archive.path(), true));

    assert!(state.jobs.mark_running(&succeeded.id));
    assert!(state
        .jobs
        .mark_succeeded(&succeeded.id, scan_archive_fixture_response(archive.path(), true),));

    let response = router_with_state(state)
        .oneshot(
            Request::builder().uri("/metrics").body(Body::empty()).expect("request should build"),
        )
        .await
        .expect("router should respond");

    assert_eq!(response.status(), StatusCode::OK);
    assert_eq!(response.headers()[header::CONTENT_TYPE], metrics_export::PROMETHEUS_CONTENT_TYPE);

    let body = read_text(response).await;
    assert!(body.contains("# TYPE archive_scan_job_retention_seconds gauge"));
    assert!(body.contains(
        "archive_scan_job_retention_seconds{job_store_backend=\"in_memory\",result_store_backend=\"in_memory\"} 90"
    ));
    assert!(body.contains("# TYPE archive_scan_jobs_created_total counter"));
    assert!(body.contains(
        "archive_scan_jobs_created_total{job_store_backend=\"in_memory\",result_store_backend=\"in_memory\"} 2"
    ));
    assert!(body.contains(
        "archive_scan_jobs_succeeded{job_store_backend=\"in_memory\",result_store_backend=\"in_memory\"} 1"
    ));
    assert!(body.contains(
        "archive_scan_result_artifact_gc_runs_total{job_store_backend=\"in_memory\",result_store_backend=\"in_memory\"} 0"
    ));
    assert!(
        !body.contains("job_store_filesystem_path"),
        "prometheus export should stay low-cardinality and avoid filesystem paths"
    );
}
#[tokio::test]
async fn otlp_metrics_route_exports_otlp_json_snapshot() {
    let archive = sample_zip_archive();
    let state = AppState::with_job_retention(Duration::from_secs(75));
    let succeeded = state.jobs.create_job(sample_request(archive.path(), true));

    assert!(state.jobs.mark_running(&succeeded.id));
    assert!(state
        .jobs
        .mark_succeeded(&succeeded.id, scan_archive_fixture_response(archive.path(), true),));

    let response = router_with_state(state)
        .oneshot(
            Request::builder()
                .uri("/v1/jobs/metrics/otlp")
                .body(Body::empty())
                .expect("request should build"),
        )
        .await
        .expect("router should respond");

    assert_eq!(response.status(), StatusCode::OK);

    let value = read_json(response).await;
    let resource_metrics =
        value["resourceMetrics"].as_array().expect("resourceMetrics should be an array");
    assert_eq!(resource_metrics.len(), 1);
    let metrics = resource_metrics[0]["scopeMetrics"][0]["metrics"]
        .as_array()
        .expect("metrics should be an array");
    let by_name = metrics
        .iter()
        .filter_map(|metric| metric["name"].as_str().map(|name| (name.to_owned(), metric.clone())))
        .collect::<HashMap<_, _>>();

    assert_eq!(
        by_name["archive_scan_job_retention_seconds"]["gauge"]["dataPoints"][0]["asInt"],
        "75"
    );
    assert_eq!(by_name["archive_scan_jobs_created_total"]["sum"]["aggregationTemporality"], 2);
    assert_eq!(by_name["archive_scan_jobs_created_total"]["sum"]["isMonotonic"], true);
    assert_eq!(by_name["archive_scan_jobs_succeeded"]["gauge"]["dataPoints"][0]["asInt"], "1");
    assert_eq!(
        value["resourceMetrics"][0]["resource"]["attributes"][3]["value"]["stringValue"],
        "in_memory"
    );
    assert_eq!(
        value["resourceMetrics"][0]["resource"]["attributes"][4]["value"]["stringValue"],
        "in_memory"
    );
}
