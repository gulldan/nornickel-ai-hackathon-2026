use super::*;

#[tokio::test]
async fn create_job_returns_created_and_status_links() {
    let archive = sample_zip_archive();
    let response = router()
        .oneshot(post_json(
            "/v1/jobs",
            serde_json::json!({
                "path": archive.path().display().to_string(),
                "fast_only": true
            }),
        ))
        .await
        .expect("router should respond");

    assert_eq!(response.status(), StatusCode::CREATED);

    let value = read_json(response).await;
    assert_eq!(value["state"], "queued");
    assert!(value["job_id"].as_str().is_some_and(|job_id| job_id.starts_with("job-")));
    assert!(value["status_url"].as_str().is_some_and(|url| url.starts_with("/v1/jobs/job-")));
    assert!(value["result_url"].as_str().is_some_and(|url| url.ends_with("/result")));
}
#[tokio::test]
async fn create_job_reuses_existing_job_for_same_idempotency_key() {
    let archive = sample_zip_archive();
    let state = AppState::default();
    let app = router_with_state(state.clone());
    let payload = serde_json::json!({
        "path": archive.path().display().to_string(),
        "fast_only": true
    });

    let created = app
        .clone()
        .oneshot(post_json_with_idempotency_key("/v1/jobs", payload.clone(), "scan-sample"))
        .await
        .expect("router should respond");

    assert_eq!(created.status(), StatusCode::CREATED);
    let created_value = read_json(created).await;
    let created_job_id =
        created_value["job_id"].as_str().expect("job_id should be present").to_owned();

    let replayed = app
        .clone()
        .oneshot(post_json_with_idempotency_key("/v1/jobs", payload, "scan-sample"))
        .await
        .expect("router should respond");

    assert_eq!(replayed.status(), StatusCode::OK);
    let replayed_value = read_json(replayed).await;
    assert_eq!(replayed_value["job_id"], created_job_id);
    assert_eq!(replayed_value["status_url"], format!("/v1/jobs/{created_job_id}"));
    assert_eq!(replayed_value["result_url"], format!("/v1/jobs/{created_job_id}/result"));

    let metrics = app
        .oneshot(
            Request::builder()
                .uri("/v1/jobs/metrics")
                .body(Body::empty())
                .expect("request should build"),
        )
        .await
        .expect("router should respond");
    let metrics_value = read_json(metrics).await;
    assert_eq!(metrics_value["lifecycle"]["created_total"], 1);
}
#[tokio::test]
async fn create_job_returns_conflict_when_idempotency_key_is_reused_for_different_request() {
    let archive = sample_zip_archive();
    let state = AppState::default();
    let app = router_with_state(state.clone());
    let first_payload = serde_json::json!({
        "path": archive.path().display().to_string(),
        "fast_only": true,
        "include_entries": false
    });
    let second_payload = serde_json::json!({
        "path": archive.path().display().to_string(),
        "fast_only": true,
        "include_entries": true
    });

    let created = app
        .clone()
        .oneshot(post_json_with_idempotency_key("/v1/jobs", first_payload, "scan-sample"))
        .await
        .expect("router should respond");
    let created_value = read_json(created).await;
    let created_job_id = created_value["job_id"].clone();

    let conflict = app
        .clone()
        .oneshot(post_json_with_idempotency_key("/v1/jobs", second_payload, "scan-sample"))
        .await
        .expect("router should respond");

    assert_eq!(conflict.status(), StatusCode::CONFLICT);
    let conflict_value = read_json(conflict).await;
    assert_eq!(conflict_value["error"]["code"], "idempotency_key_conflict");
    assert_eq!(conflict_value["error"]["details"]["idempotency_key"], "scan-sample");
    assert_eq!(conflict_value["error"]["details"]["job_id"], created_job_id);

    let metrics = app
        .oneshot(
            Request::builder()
                .uri("/v1/jobs/metrics")
                .body(Body::empty())
                .expect("request should build"),
        )
        .await
        .expect("router should respond");
    let metrics_value = read_json(metrics).await;
    assert_eq!(metrics_value["lifecycle"]["created_total"], 1);
}
#[tokio::test]
async fn create_job_returns_existing_job_for_same_idempotency_key_even_after_source_disappears() {
    let archive = sample_zip_archive();
    let request = sample_request(archive.path(), true);
    let state = AppState::default();
    let CreateJobOutcome::Created(snapshot) =
        state.jobs.create_job_with_idempotency(request.clone(), "scan-sample".to_owned())
    else {
        panic!("first idempotent insert should create a new job");
    };
    archive.close().expect("temp archive should be removable");

    let response = router_with_state(state)
        .oneshot(post_json_with_idempotency_key(
            "/v1/jobs",
            serde_json::json!({
                "path": request
                    .path
                    .clone()
                    .expect("legacy request should keep a top-level path"),
                "header_bytes": request.header_bytes,
                "block_size": request.block_size,
                "full_hash": request.full_hash,
                "fast_only": request.fast_only,
                "include_entries": request.include_entries
            }),
            "scan-sample",
        ))
        .await
        .expect("router should respond");

    assert_eq!(response.status(), StatusCode::OK);
    let value = read_json(response).await;
    assert_eq!(value["job_id"], snapshot.id);
}
