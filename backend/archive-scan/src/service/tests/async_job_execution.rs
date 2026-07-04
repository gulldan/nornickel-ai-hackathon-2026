use super::*;

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn async_job_lifecycle_returns_result() {
    let archive = sample_zip_archive();
    let app = router();

    let create = app
        .clone()
        .oneshot(post_json(
            "/v1/jobs",
            serde_json::json!({
                "path": archive.path().display().to_string(),
                "include_entries": true,
                "fast_only": true
            }),
        ))
        .await
        .expect("router should respond");

    assert_eq!(create.status(), StatusCode::CREATED);
    let create_value = read_json(create).await;
    let job_id = create_value["job_id"].as_str().expect("job_id should be present").to_owned();

    let result = wait_for_job_result(&app, &job_id).await;
    assert_eq!(result["total_entries"], 2);
    assert_eq!(result["total_files"], 2);
    assert_eq!(result["entries"].as_array().map(Vec::len), Some(2));

    let status = app
        .clone()
        .oneshot(
            Request::builder()
                .uri(format!("/v1/jobs/{job_id}"))
                .body(Body::empty())
                .expect("request should build"),
        )
        .await
        .expect("router should respond");
    assert_eq!(status.status(), StatusCode::OK);

    let status_value = read_json(status).await;
    assert_eq!(status_value["state"], "succeeded");
    assert_eq!(status_value["request"]["path"], archive.path().display().to_string());
}
#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn async_job_status_preserves_structured_source_request() {
    let archive = sample_zip_archive();
    let app = router();

    let create = app
        .clone()
        .oneshot(post_json(
            "/v1/jobs",
            serde_json::json!({
                "source": {
                    "kind": "shared_filesystem_path",
                    "path": archive.path().display().to_string()
                },
                "fast_only": true
            }),
        ))
        .await
        .expect("router should respond");

    assert_eq!(create.status(), StatusCode::CREATED);
    let create_value = read_json(create).await;
    let job_id = create_value["job_id"].as_str().expect("job_id should be present").to_owned();

    let status = app
        .clone()
        .oneshot(
            Request::builder()
                .uri(format!("/v1/jobs/{job_id}"))
                .body(Body::empty())
                .expect("request should build"),
        )
        .await
        .expect("router should respond");
    assert_eq!(status.status(), StatusCode::OK);

    let status_value = read_json(status).await;
    assert!(status_value["request"]["path"].is_null());
    assert_eq!(status_value["request"]["source"]["kind"], "shared_filesystem_path");
    assert_eq!(status_value["request"]["source"]["path"], archive.path().display().to_string());

    let result = wait_for_job_result(&app, &job_id).await;
    assert_eq!(result["total_entries"], 2);
}
#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn async_job_status_preserves_object_storage_source_request() {
    let object_store = object_storage_fixture_server().await;
    let app = router_with_private_object_sources();

    let create = app
        .clone()
        .oneshot(post_json(
            "/v1/jobs",
            serde_json::json!({
                "source": {
                    "kind": "object_storage_url",
                    "url": object_store.url.clone()
                },
                "fast_only": true
            }),
        ))
        .await
        .expect("router should respond");

    assert_eq!(create.status(), StatusCode::CREATED);
    let create_value = read_json(create).await;
    let job_id = create_value["job_id"].as_str().expect("job_id should be present").to_owned();

    let status = app
        .clone()
        .oneshot(
            Request::builder()
                .uri(format!("/v1/jobs/{job_id}"))
                .body(Body::empty())
                .expect("request should build"),
        )
        .await
        .expect("router should respond");
    assert_eq!(status.status(), StatusCode::OK);

    let status_value = read_json(status).await;
    assert!(status_value["request"]["path"].is_null());
    assert_eq!(status_value["request"]["source"]["kind"], "object_storage_url");
    assert_eq!(status_value["request"]["source"]["url"], object_store.url);

    let result = wait_for_job_result(&app, &job_id).await;
    assert_eq!(result["archive"]["path"], object_store.url);
    assert_eq!(result["total_entries"], 2);
}
#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn async_job_returns_expected_counts_for_real_archive_fixture() {
    let Some(archive_path) = real_archive_fixture() else {
        eprintln!(
            "skipping real archive async service test because data/asr-master.zip is not present"
        );
        return;
    };

    let app = router();
    let create = app
        .clone()
        .oneshot(post_json(
            "/v1/jobs",
            serde_json::json!({
                "path": archive_path.display().to_string(),
                "include_entries": true,
                "fast_only": true
            }),
        ))
        .await
        .expect("router should respond");

    assert_eq!(create.status(), StatusCode::CREATED);
    let create_value = read_json(create).await;
    let job_id = create_value["job_id"].as_str().expect("job_id should be present").to_owned();

    let result = wait_for_job_result(&app, &job_id).await;
    assert_real_fixture_summary(&result, &archive_path);
}
#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn async_job_reports_full_hash_for_large_entry() {
    let fixture = large_zip_archive();
    let app = router();
    let create = app
        .clone()
        .oneshot(post_json(
            "/v1/jobs",
            serde_json::json!({
                "source": {
                    "kind": "shared_filesystem_path",
                    "path": fixture.archive.path().display().to_string()
                },
                "include_entries": true,
                "fast_only": true,
                "full_hash": true
            }),
        ))
        .await
        .expect("router should respond");

    assert_eq!(create.status(), StatusCode::CREATED);
    let create_value = read_json(create).await;
    let job_id = create_value["job_id"].as_str().expect("job_id should be present").to_owned();

    let result = wait_for_job_result(&app, &job_id).await;
    assert_eq!(result["total_entries"], 1);
    assert_eq!(result["total_files"], 1);
    assert_eq!(result["total_directories"], 0);

    let entry = find_entry(&result, LARGE_ENTRY_NAME);
    assert_large_text_entry(entry, fixture.archive.path(), &fixture.payload, true);
}
