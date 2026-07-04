#![cfg(feature = "service")]

use crate::support::{
    assert_inflight_running_job_is_recovered_across_restart, assert_single_idempotent_job,
    concurrent_create_job_requests, postgres_fixture, read_json, redis_fixture,
    s3_result_store_fixture_server, sample_zip_archive, wait_for_job_result, ServiceProcess,
};
use reqwest::StatusCode;

#[test]
fn service_e2e_persists_jobs_and_offloaded_results_across_restart() {
    let fixture = sample_zip_archive();
    let runtime_dir = tempfile::tempdir().expect("runtime tempdir should exist");
    let state_path = runtime_dir.path().join("jobs/state.json");
    let result_dir = runtime_dir.path().join("results");
    let env = vec![
        ("ARCHIVE_SCAN_JOB_STORE_BACKEND".to_owned(), "filesystem".to_owned()),
        ("ARCHIVE_SCAN_JOB_STORE_PATH".to_owned(), state_path.display().to_string()),
        ("ARCHIVE_SCAN_RESULT_STORE_BACKEND".to_owned(), "filesystem".to_owned()),
        ("ARCHIVE_SCAN_RESULT_STORE_DIR".to_owned(), result_dir.display().to_string()),
        ("ARCHIVE_SCAN_RESULT_INLINE_MAX_BYTES".to_owned(), "1".to_owned()),
    ];

    let service = ServiceProcess::spawn_with_env(&env);
    let create = service
        .client()
        .post(service.url("/v1/jobs"))
        .header("content-type", "application/json")
        .body(
            serde_json::to_vec(&serde_json::json!({
                "path": fixture.path().display().to_string(),
                "include_entries": true,
                "fast_only": true
            }))
            .expect("request JSON should serialize"),
        )
        .send()
        .expect("service should create persistent job");

    assert_eq!(create.status(), StatusCode::CREATED);
    let create_value = read_json(create);
    let job_id = create_value["job_id"].as_str().expect("job_id should be present").to_owned();

    let result = wait_for_job_result(&service, &job_id);
    assert_eq!(result["total_entries"], 2);
    assert!(state_path.exists(), "job store state file should be persisted");
    assert!(
        std::fs::read_dir(&result_dir).expect("result store dir should exist").any(|entry| {
            entry
                .ok()
                .and_then(|entry| entry.path().extension().map(|ext| ext == "json"))
                .unwrap_or(false)
        }),
        "large job result should be offloaded into external result storage"
    );
    drop(service);

    let restarted = ServiceProcess::spawn_with_env(&env);
    let status = restarted
        .client()
        .get(restarted.url(&format!("/v1/jobs/{job_id}")))
        .send()
        .expect("restarted service should expose persisted job status");
    assert_eq!(status.status(), StatusCode::OK);

    let status_value = read_json(status);
    assert_eq!(status_value["state"], "succeeded");
    assert_eq!(status_value["request"]["path"], fixture.path().display().to_string());

    let result = restarted
        .client()
        .get(restarted.url(&format!("/v1/jobs/{job_id}/result")))
        .send()
        .expect("restarted service should load persisted job result");
    assert_eq!(result.status(), StatusCode::OK);
    let result_value = read_json(result);
    assert_eq!(result_value["total_entries"], 2);

    let metrics = restarted
        .client()
        .get(restarted.url("/v1/jobs/metrics"))
        .send()
        .expect("restarted service should expose storage metrics");
    assert_eq!(metrics.status(), StatusCode::OK);
    let metrics_value = read_json(metrics);
    assert_eq!(metrics_value["storage"]["job_store_backend"], "filesystem");
    assert_eq!(metrics_value["storage"]["result_store_backend"], "filesystem");
    assert_eq!(metrics_value["storage"]["result_inline_max_bytes"], 1);
}

#[cfg(feature = "service")]
#[test]
fn service_e2e_persists_jobs_and_offloaded_results_across_restart_with_s3() {
    let fixture = sample_zip_archive();
    let s3 = s3_result_store_fixture_server();
    let runtime_dir = tempfile::tempdir().expect("runtime tempdir should exist");
    let state_path = runtime_dir.path().join("jobs/state.json");
    let env = vec![
        ("ARCHIVE_SCAN_JOB_STORE_BACKEND".to_owned(), "filesystem".to_owned()),
        ("ARCHIVE_SCAN_JOB_STORE_PATH".to_owned(), state_path.display().to_string()),
        ("ARCHIVE_SCAN_RESULT_STORE_BACKEND".to_owned(), "s3".to_owned()),
        ("ARCHIVE_SCAN_RESULT_STORE_S3_ENDPOINT".to_owned(), s3.endpoint.clone()),
        ("ARCHIVE_SCAN_RESULT_STORE_S3_REGION".to_owned(), "us-east-1".to_owned()),
        ("ARCHIVE_SCAN_RESULT_STORE_S3_BUCKET".to_owned(), "result-bucket".to_owned()),
        ("ARCHIVE_SCAN_RESULT_STORE_S3_KEY_PREFIX".to_owned(), "integration/results".to_owned()),
        ("ARCHIVE_SCAN_RESULT_STORE_S3_ACCESS_KEY_ID".to_owned(), "access".to_owned()),
        ("ARCHIVE_SCAN_RESULT_STORE_S3_SECRET_ACCESS_KEY".to_owned(), "secret".to_owned()),
        ("ARCHIVE_SCAN_RESULT_STORE_S3_PATH_STYLE".to_owned(), "true".to_owned()),
        ("ARCHIVE_SCAN_RESULT_INLINE_MAX_BYTES".to_owned(), "1".to_owned()),
    ];

    let service = ServiceProcess::spawn_with_env(&env);
    let readyz =
        service.client().get(service.url("/readyz")).send().expect("service should expose readyz");
    assert_eq!(readyz.status(), StatusCode::OK);

    let create = service
        .client()
        .post(service.url("/v1/jobs"))
        .header("content-type", "application/json")
        .body(
            serde_json::to_vec(&serde_json::json!({
                "path": fixture.path().display().to_string(),
                "include_entries": true,
                "fast_only": true
            }))
            .expect("request JSON should serialize"),
        )
        .send()
        .expect("service should create persistent job");

    assert_eq!(create.status(), StatusCode::CREATED);
    let create_value = read_json(create);
    let job_id = create_value["job_id"].as_str().expect("job_id should be present").to_owned();

    let result = wait_for_job_result(&service, &job_id);
    assert_eq!(result["total_entries"], 2);
    assert!(
        s3.objects()
            .iter()
            .any(|key| key.starts_with("integration/results/result-job-") && key.ends_with(".json")),
        "large job result should be offloaded into the s3 result store"
    );
    drop(service);

    let restarted = ServiceProcess::spawn_with_env(&env);
    let result = restarted
        .client()
        .get(restarted.url(&format!("/v1/jobs/{job_id}/result")))
        .send()
        .expect("restarted service should load persisted s3-backed result");
    assert_eq!(result.status(), StatusCode::OK);
    let result_value = read_json(result);
    assert_eq!(result_value["total_entries"], 2);

    let metrics = restarted
        .client()
        .get(restarted.url("/v1/jobs/metrics"))
        .send()
        .expect("restarted service should expose storage metrics");
    assert_eq!(metrics.status(), StatusCode::OK);
    let metrics_value = read_json(metrics);
    assert_eq!(metrics_value["storage"]["result_store_backend"], "s3");
    assert_eq!(metrics_value["storage"]["result_store_s3_endpoint"], s3.endpoint);
    assert_eq!(metrics_value["storage"]["result_store_s3_bucket"], "result-bucket");
    assert_eq!(metrics_value["storage"]["result_store_s3_key_prefix"], "integration/results");
    assert_eq!(metrics_value["storage"]["result_store_s3_path_style"], true);
    assert_eq!(metrics_value["storage"]["result_inline_max_bytes"], 1);
}

#[cfg(feature = "service")]
#[test]
fn service_e2e_persists_jobs_and_idempotency_across_restart_with_redis() {
    let Some(redis) = redis_fixture() else {
        return;
    };

    let fixture = sample_zip_archive();
    let runtime_dir = tempfile::tempdir().expect("runtime tempdir should exist");
    let result_dir = runtime_dir.path().join("results");
    let env = vec![
        ("ARCHIVE_SCAN_JOB_STORE_BACKEND".to_owned(), "redis".to_owned()),
        ("ARCHIVE_SCAN_JOB_STORE_REDIS_URL".to_owned(), redis.url.clone()),
        ("ARCHIVE_SCAN_JOB_STORE_KEY_PREFIX".to_owned(), redis.key_prefix.clone()),
        ("ARCHIVE_SCAN_JOB_STORE_REDIS_MAX_CONNECTIONS".to_owned(), "8".to_owned()),
        ("ARCHIVE_SCAN_JOB_STORE_REDIS_CLEANUP_BATCH_SIZE".to_owned(), "1".to_owned()),
        ("ARCHIVE_SCAN_RESULT_STORE_BACKEND".to_owned(), "filesystem".to_owned()),
        ("ARCHIVE_SCAN_RESULT_STORE_DIR".to_owned(), result_dir.display().to_string()),
        ("ARCHIVE_SCAN_RESULT_INLINE_MAX_BYTES".to_owned(), "1".to_owned()),
    ];

    let request = serde_json::json!({
        "path": fixture.path().display().to_string(),
        "include_entries": true,
        "fast_only": true
    });
    let service = ServiceProcess::spawn_with_env(&env);
    let create = service
        .client()
        .post(service.url("/v1/jobs"))
        .header("content-type", "application/json")
        .header("Idempotency-Key", "redis-restart-scan")
        .body(serde_json::to_vec(&request).expect("request JSON should serialize"))
        .send()
        .expect("service should create redis-backed job");

    assert_eq!(create.status(), StatusCode::CREATED);
    let create_value = read_json(create);
    let job_id = create_value["job_id"].as_str().expect("job_id should be present").to_owned();

    let result = wait_for_job_result(&service, &job_id);
    assert_eq!(result["total_entries"], 2);
    assert!(
        std::fs::read_dir(&result_dir).expect("result store dir should exist").any(|entry| {
            entry
                .ok()
                .and_then(|entry| entry.path().extension().map(|ext| ext == "json"))
                .unwrap_or(false)
        }),
        "large job result should be offloaded into the filesystem result store"
    );

    let replayed = service
        .client()
        .post(service.url("/v1/jobs"))
        .header("content-type", "application/json")
        .header("Idempotency-Key", "redis-restart-scan")
        .body(serde_json::to_vec(&request).expect("request JSON should serialize"))
        .send()
        .expect("service should replay idempotent redis-backed job");
    assert_eq!(replayed.status(), StatusCode::OK);
    assert_eq!(read_json(replayed)["job_id"], job_id);

    drop(service);

    let restarted = ServiceProcess::spawn_with_env(&env);
    let status = restarted
        .client()
        .get(restarted.url(&format!("/v1/jobs/{job_id}")))
        .send()
        .expect("restarted redis-backed service should expose job status");
    assert_eq!(status.status(), StatusCode::OK);
    assert_eq!(read_json(status)["state"], "succeeded");

    let result = restarted
        .client()
        .get(restarted.url(&format!("/v1/jobs/{job_id}/result")))
        .send()
        .expect("restarted redis-backed service should expose job result");
    assert_eq!(result.status(), StatusCode::OK);
    assert_eq!(read_json(result)["total_entries"], 2);

    let replayed = restarted
        .client()
        .post(restarted.url("/v1/jobs"))
        .header("content-type", "application/json")
        .header("Idempotency-Key", "redis-restart-scan")
        .body(serde_json::to_vec(&request).expect("request JSON should serialize"))
        .send()
        .expect("restarted service should preserve redis idempotency keys");
    assert_eq!(replayed.status(), StatusCode::OK);
    assert_eq!(read_json(replayed)["job_id"], job_id);

    let conflict = restarted
        .client()
        .post(restarted.url("/v1/jobs"))
        .header("content-type", "application/json")
        .header("Idempotency-Key", "redis-restart-scan")
        .body(
            serde_json::to_vec(&serde_json::json!({
                "path": fixture.path().display().to_string(),
                "include_entries": false,
                "fast_only": true
            }))
            .expect("conflict request JSON should serialize"),
        )
        .send()
        .expect("restarted service should reject conflicting redis idempotency key reuse");
    assert_eq!(conflict.status(), StatusCode::CONFLICT);

    let metrics = restarted
        .client()
        .get(restarted.url("/v1/jobs/metrics"))
        .send()
        .expect("restarted redis-backed service should expose metrics");
    assert_eq!(metrics.status(), StatusCode::OK);
    let metrics_value = read_json(metrics);
    assert_eq!(metrics_value["storage"]["job_store_backend"], "redis");
    assert_eq!(metrics_value["storage"]["job_store_redis_url"], redis.url);
    assert_eq!(metrics_value["storage"]["job_store_redis_key_prefix"], redis.key_prefix);
    assert_eq!(metrics_value["storage"]["job_store_redis_max_connections"], 8);
    assert_eq!(metrics_value["storage"]["job_store_redis_cleanup_batch_size"], 1);
    assert_eq!(metrics_value["storage"]["result_store_backend"], "filesystem");
    assert_eq!(metrics_value["storage"]["result_inline_max_bytes"], 1);
}

#[cfg(feature = "service")]
#[test]
fn service_e2e_persists_jobs_and_idempotency_across_restart_with_postgres() {
    let Some(postgres) = postgres_fixture() else {
        return;
    };

    let fixture = sample_zip_archive();
    let runtime_dir = tempfile::tempdir().expect("runtime tempdir should exist");
    let result_dir = runtime_dir.path().join("results");
    let env = vec![
        ("ARCHIVE_SCAN_JOB_STORE_BACKEND".to_owned(), "postgres".to_owned()),
        ("ARCHIVE_SCAN_JOB_STORE_POSTGRES_URL".to_owned(), postgres.url.clone()),
        ("ARCHIVE_SCAN_JOB_STORE_POSTGRES_TABLE_PREFIX".to_owned(), postgres.table_prefix.clone()),
        ("ARCHIVE_SCAN_JOB_STORE_POSTGRES_MAX_CONNECTIONS".to_owned(), "8".to_owned()),
        ("ARCHIVE_SCAN_RESULT_STORE_BACKEND".to_owned(), "filesystem".to_owned()),
        ("ARCHIVE_SCAN_RESULT_STORE_DIR".to_owned(), result_dir.display().to_string()),
        ("ARCHIVE_SCAN_RESULT_INLINE_MAX_BYTES".to_owned(), "1".to_owned()),
    ];

    let request = serde_json::json!({
        "path": fixture.path().display().to_string(),
        "include_entries": true,
        "fast_only": true
    });
    let service = ServiceProcess::spawn_with_env(&env);
    let create = service
        .client()
        .post(service.url("/v1/jobs"))
        .header("content-type", "application/json")
        .header("Idempotency-Key", "postgres-restart-scan")
        .body(serde_json::to_vec(&request).expect("request JSON should serialize"))
        .send()
        .expect("service should create postgres-backed job");

    assert_eq!(create.status(), StatusCode::CREATED);
    let create_value = read_json(create);
    let job_id = create_value["job_id"].as_str().expect("job_id should be present").to_owned();

    let result = wait_for_job_result(&service, &job_id);
    assert_eq!(result["total_entries"], 2);
    assert!(
        std::fs::read_dir(&result_dir).expect("result store dir should exist").any(|entry| {
            entry
                .ok()
                .and_then(|entry| entry.path().extension().map(|ext| ext == "json"))
                .unwrap_or(false)
        }),
        "large job result should be offloaded into the filesystem result store"
    );

    let replayed = service
        .client()
        .post(service.url("/v1/jobs"))
        .header("content-type", "application/json")
        .header("Idempotency-Key", "postgres-restart-scan")
        .body(serde_json::to_vec(&request).expect("request JSON should serialize"))
        .send()
        .expect("service should replay idempotent postgres-backed job");
    assert_eq!(replayed.status(), StatusCode::OK);
    assert_eq!(read_json(replayed)["job_id"], job_id);

    drop(service);

    let restarted = ServiceProcess::spawn_with_env(&env);
    let status = restarted
        .client()
        .get(restarted.url(&format!("/v1/jobs/{job_id}")))
        .send()
        .expect("restarted postgres-backed service should expose job status");
    assert_eq!(status.status(), StatusCode::OK);
    assert_eq!(read_json(status)["state"], "succeeded");

    let result = restarted
        .client()
        .get(restarted.url(&format!("/v1/jobs/{job_id}/result")))
        .send()
        .expect("restarted postgres-backed service should expose job result");
    assert_eq!(result.status(), StatusCode::OK);
    assert_eq!(read_json(result)["total_entries"], 2);

    let replayed = restarted
        .client()
        .post(restarted.url("/v1/jobs"))
        .header("content-type", "application/json")
        .header("Idempotency-Key", "postgres-restart-scan")
        .body(serde_json::to_vec(&request).expect("request JSON should serialize"))
        .send()
        .expect("restarted service should preserve idempotency keys");
    assert_eq!(replayed.status(), StatusCode::OK);
    assert_eq!(read_json(replayed)["job_id"], job_id);

    let conflict = restarted
        .client()
        .post(restarted.url("/v1/jobs"))
        .header("content-type", "application/json")
        .header("Idempotency-Key", "postgres-restart-scan")
        .body(
            serde_json::to_vec(&serde_json::json!({
                "path": fixture.path().display().to_string(),
                "include_entries": false,
                "fast_only": true
            }))
            .expect("conflict request JSON should serialize"),
        )
        .send()
        .expect("restarted service should reject conflicting idempotency key reuse");
    assert_eq!(conflict.status(), StatusCode::CONFLICT);

    let metrics = restarted
        .client()
        .get(restarted.url("/v1/jobs/metrics"))
        .send()
        .expect("restarted postgres-backed service should expose metrics");
    assert_eq!(metrics.status(), StatusCode::OK);
    let metrics_value = read_json(metrics);
    assert_eq!(metrics_value["storage"]["job_store_backend"], "postgres");
    assert_eq!(
        metrics_value["storage"]["job_store_postgres_url"],
        postgres.url.replacen("postgres:postgres@", "redacted:redacted@", 1)
    );
    assert_eq!(metrics_value["storage"]["job_store_postgres_table_prefix"], postgres.table_prefix);
    assert_eq!(metrics_value["storage"]["job_store_postgres_max_connections"], 8);
    assert_eq!(metrics_value["storage"]["result_store_backend"], "filesystem");
    assert_eq!(metrics_value["storage"]["result_inline_max_bytes"], 1);
}

#[cfg(feature = "service")]
#[test]
fn service_e2e_recovers_inflight_running_jobs_across_restart_with_filesystem() {
    let runtime_dir = tempfile::tempdir().expect("runtime tempdir should exist");
    let env = vec![
        ("ARCHIVE_SCAN_JOB_STORE_BACKEND".to_owned(), "filesystem".to_owned()),
        (
            "ARCHIVE_SCAN_JOB_STORE_PATH".to_owned(),
            runtime_dir.path().join("jobs/state.json").display().to_string(),
        ),
        ("ARCHIVE_SCAN_RESULT_STORE_BACKEND".to_owned(), "filesystem".to_owned()),
        (
            "ARCHIVE_SCAN_RESULT_STORE_DIR".to_owned(),
            runtime_dir.path().join("results").display().to_string(),
        ),
        ("ARCHIVE_SCAN_OBJECT_SOURCE_ALLOW_PRIVATE_NETWORKS".to_owned(), "1".to_owned()),
    ];

    assert_inflight_running_job_is_recovered_across_restart(&env);
}

#[cfg(feature = "service")]
#[test]
fn service_e2e_recovers_inflight_running_jobs_across_restart_with_redis() {
    let Some(redis) = redis_fixture() else {
        return;
    };

    let runtime_dir = tempfile::tempdir().expect("runtime tempdir should exist");
    let env = vec![
        ("ARCHIVE_SCAN_JOB_STORE_BACKEND".to_owned(), "redis".to_owned()),
        ("ARCHIVE_SCAN_JOB_STORE_REDIS_URL".to_owned(), redis.url.clone()),
        ("ARCHIVE_SCAN_JOB_STORE_KEY_PREFIX".to_owned(), redis.key_prefix.clone()),
        ("ARCHIVE_SCAN_JOB_STORE_REDIS_MAX_CONNECTIONS".to_owned(), "8".to_owned()),
        ("ARCHIVE_SCAN_RESULT_STORE_BACKEND".to_owned(), "filesystem".to_owned()),
        (
            "ARCHIVE_SCAN_RESULT_STORE_DIR".to_owned(),
            runtime_dir.path().join("results").display().to_string(),
        ),
        ("ARCHIVE_SCAN_OBJECT_SOURCE_ALLOW_PRIVATE_NETWORKS".to_owned(), "1".to_owned()),
    ];

    assert_inflight_running_job_is_recovered_across_restart(&env);
}

#[cfg(feature = "service")]
#[test]
fn service_e2e_recovers_inflight_running_jobs_across_restart_with_postgres() {
    let Some(postgres) = postgres_fixture() else {
        return;
    };

    let runtime_dir = tempfile::tempdir().expect("runtime tempdir should exist");
    let env = vec![
        ("ARCHIVE_SCAN_JOB_STORE_BACKEND".to_owned(), "postgres".to_owned()),
        ("ARCHIVE_SCAN_JOB_STORE_POSTGRES_URL".to_owned(), postgres.url.clone()),
        ("ARCHIVE_SCAN_JOB_STORE_POSTGRES_TABLE_PREFIX".to_owned(), postgres.table_prefix.clone()),
        ("ARCHIVE_SCAN_JOB_STORE_POSTGRES_MAX_CONNECTIONS".to_owned(), "8".to_owned()),
        ("ARCHIVE_SCAN_RESULT_STORE_BACKEND".to_owned(), "filesystem".to_owned()),
        (
            "ARCHIVE_SCAN_RESULT_STORE_DIR".to_owned(),
            runtime_dir.path().join("results").display().to_string(),
        ),
        ("ARCHIVE_SCAN_OBJECT_SOURCE_ALLOW_PRIVATE_NETWORKS".to_owned(), "1".to_owned()),
    ];

    assert_inflight_running_job_is_recovered_across_restart(&env);
}

#[cfg(feature = "service")]
#[test]
fn service_e2e_deduplicates_concurrent_idempotent_creates_with_redis() {
    let Some(redis) = redis_fixture() else {
        return;
    };

    let fixture = sample_zip_archive();
    let env = vec![
        ("ARCHIVE_SCAN_JOB_STORE_BACKEND".to_owned(), "redis".to_owned()),
        ("ARCHIVE_SCAN_JOB_STORE_REDIS_URL".to_owned(), redis.url.clone()),
        ("ARCHIVE_SCAN_JOB_STORE_KEY_PREFIX".to_owned(), redis.key_prefix.clone()),
        ("ARCHIVE_SCAN_JOB_STORE_REDIS_MAX_CONNECTIONS".to_owned(), "8".to_owned()),
        ("ARCHIVE_SCAN_JOB_STORE_REDIS_CLEANUP_BATCH_SIZE".to_owned(), "1".to_owned()),
    ];
    let request = serde_json::json!({
        "path": fixture.path().display().to_string(),
        "fast_only": true
    });

    let service = ServiceProcess::spawn_with_env(&env);
    let responses = concurrent_create_job_requests(&service, &request, "redis-race", 12);
    assert_single_idempotent_job(responses, &service);
}

#[cfg(feature = "service")]
#[test]
fn service_e2e_deduplicates_concurrent_idempotent_creates_with_postgres() {
    let Some(postgres) = postgres_fixture() else {
        return;
    };

    let fixture = sample_zip_archive();
    let env = vec![
        ("ARCHIVE_SCAN_JOB_STORE_BACKEND".to_owned(), "postgres".to_owned()),
        ("ARCHIVE_SCAN_JOB_STORE_POSTGRES_URL".to_owned(), postgres.url.clone()),
        ("ARCHIVE_SCAN_JOB_STORE_POSTGRES_TABLE_PREFIX".to_owned(), postgres.table_prefix.clone()),
        ("ARCHIVE_SCAN_JOB_STORE_POSTGRES_MAX_CONNECTIONS".to_owned(), "8".to_owned()),
    ];
    let request = serde_json::json!({
        "path": fixture.path().display().to_string(),
        "fast_only": true
    });

    let service = ServiceProcess::spawn_with_env(&env);
    let responses = concurrent_create_job_requests(&service, &request, "postgres-race", 12);
    assert_single_idempotent_job(responses, &service);
}
