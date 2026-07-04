use super::*;

#[tokio::test]
async fn scan_local_path_returns_not_found_for_missing_archive() {
    let response = router()
        .oneshot(post_json(
            "/v1/scan/local-path",
            serde_json::json!({ "path": "/tmp/definitely-missing-archive.zip" }),
        ))
        .await
        .expect("router should respond");

    assert_eq!(response.status(), StatusCode::NOT_FOUND);

    let value = read_json(response).await;
    assert_eq!(value["error"]["code"], "archive_path_not_found");
    assert_eq!(value["error"]["status"], 404);
    assert_eq!(value["error"]["details"]["source_kind"], "local_path");
}
#[tokio::test]
async fn scan_local_path_returns_bad_request_for_directory_path() {
    let temp_dir = tempfile::tempdir().expect("tempdir should exist");
    let response = router()
        .oneshot(post_json(
            "/v1/scan/local-path",
            serde_json::json!({ "path": temp_dir.path().display().to_string() }),
        ))
        .await
        .expect("router should respond");

    assert_eq!(response.status(), StatusCode::BAD_REQUEST);

    let value = read_json(response).await;
    assert_eq!(value["error"]["code"], "archive_path_not_file");
    assert_eq!(value["error"]["status"], 400);
    assert_eq!(value["error"]["details"]["source_kind"], "local_path");
}
#[tokio::test]
async fn scan_source_returns_source_kind_in_not_found_error() {
    let response = router()
        .oneshot(post_json(
            "/v1/scan/source",
            serde_json::json!({
                "source": {
                    "kind": "shared_filesystem_path",
                    "path": "/tmp/definitely-missing-shared-archive.zip"
                }
            }),
        ))
        .await
        .expect("router should respond");

    assert_eq!(response.status(), StatusCode::NOT_FOUND);

    let value = read_json(response).await;
    assert_eq!(value["error"]["code"], "archive_path_not_found");
    assert_eq!(value["error"]["details"]["source_kind"], "shared_filesystem_path");
}
#[tokio::test]
async fn scan_source_returns_bad_request_for_invalid_object_storage_url() {
    let response = router()
        .oneshot(post_json(
            "/v1/scan/source",
            serde_json::json!({
                "source": {
                    "kind": "object_storage_url",
                    "url": "s3://archive-bucket/sample.zip"
                }
            }),
        ))
        .await
        .expect("router should respond");

    assert_eq!(response.status(), StatusCode::BAD_REQUEST);

    let value = read_json(response).await;
    assert_eq!(value["error"]["code"], "invalid_source_reference");
    assert_eq!(value["error"]["details"]["source_kind"], "object_storage_url");
}
#[tokio::test]
async fn scan_source_rejects_private_object_storage_url_by_default() {
    let response = router()
        .oneshot(post_json(
            "/v1/scan/source",
            serde_json::json!({
                "source": {
                    "kind": "object_storage_url",
                    "url": "http://127.0.0.1/archive.zip"
                }
            }),
        ))
        .await
        .expect("router should respond");

    assert_eq!(response.status(), StatusCode::BAD_REQUEST);

    let value = read_json(response).await;
    assert_eq!(value["error"]["code"], "invalid_source_reference");
    assert!(value["error"]["details"]["reason"]
        .as_str()
        .is_some_and(|reason| reason.contains("non-public address")));
}
#[tokio::test]
async fn create_job_rejects_private_object_storage_url_by_default() {
    let response = router()
        .oneshot(post_json(
            "/v1/jobs",
            serde_json::json!({
                "source": {
                    "kind": "object_storage_url",
                    "url": "http://127.0.0.1/archive.zip"
                }
            }),
        ))
        .await
        .expect("router should respond");

    assert_eq!(response.status(), StatusCode::BAD_REQUEST);

    let value = read_json(response).await;
    assert_eq!(value["error"]["code"], "invalid_source_reference");
    assert!(value["error"]["details"]["reason"]
        .as_str()
        .is_some_and(|reason| reason.contains("non-public address")));
}
#[tokio::test]
async fn unknown_route_returns_json_not_found_error() {
    let response = router()
        .oneshot(Request::builder().uri("/nope").body(Body::empty()).expect("request should build"))
        .await
        .expect("router should respond");

    assert_eq!(response.status(), StatusCode::NOT_FOUND);

    let value = read_json(response).await;
    assert_eq!(value["error"]["code"], "route_not_found");
    assert_eq!(value["error"]["details"]["path"], "/nope");
}
#[tokio::test]
async fn method_not_allowed_returns_json_error() {
    let response = router()
        .oneshot(
            Request::builder()
                .method(Method::POST)
                .uri("/healthz")
                .body(Body::empty())
                .expect("request should build"),
        )
        .await
        .expect("router should respond");

    assert_eq!(response.status(), StatusCode::METHOD_NOT_ALLOWED);

    let value = read_json(response).await;
    assert_eq!(value["error"]["code"], "method_not_allowed");
    assert_eq!(value["error"]["details"]["method"], "POST");
    assert_eq!(value["error"]["details"]["path"], "/healthz");
}
#[tokio::test]
async fn create_job_rejects_wrong_content_type_as_json_error() {
    let response = router()
        .oneshot(
            Request::builder()
                .method("POST")
                .uri("/v1/jobs")
                .header("content-type", "text/plain")
                .body(Body::from("{\"path\":\"/tmp/x.zip\"}"))
                .expect("request should build"),
        )
        .await
        .expect("router should respond");

    assert_eq!(response.status(), StatusCode::UNSUPPORTED_MEDIA_TYPE);

    let value = read_json(response).await;
    assert_eq!(value["error"]["code"], "unsupported_media_type");
    assert_eq!(value["error"]["status"], 415);
}
#[tokio::test]
async fn create_job_rejects_empty_idempotency_key() {
    let archive = sample_zip_archive();
    let response = router()
        .oneshot(post_json_with_idempotency_key(
            "/v1/jobs",
            serde_json::json!({
                "path": archive.path().display().to_string()
            }),
            "   ",
        ))
        .await
        .expect("router should respond");

    assert_eq!(response.status(), StatusCode::BAD_REQUEST);

    let value = read_json(response).await;
    assert_eq!(value["error"]["code"], "invalid_idempotency_key");
    assert_eq!(value["error"]["status"], 400);
}
#[tokio::test]
async fn scan_local_path_rejects_malformed_json_as_json_error() {
    let response = router()
        .oneshot(
            Request::builder()
                .method("POST")
                .uri("/v1/scan/local-path")
                .header("content-type", "application/json")
                .body(Body::from("{"))
                .expect("request should build"),
        )
        .await
        .expect("router should respond");

    assert_eq!(response.status(), StatusCode::BAD_REQUEST);

    let value = read_json(response).await;
    assert_eq!(value["error"]["code"], "invalid_json_syntax");
    assert_eq!(value["error"]["status"], 400);
}
#[tokio::test]
async fn create_job_rejects_missing_required_field_as_json_error() {
    let response = router()
        .oneshot(
            Request::builder()
                .method("POST")
                .uri("/v1/jobs")
                .header("content-type", "application/json")
                .body(Body::from("{}"))
                .expect("request should build"),
        )
        .await
        .expect("router should respond");

    assert_eq!(response.status(), StatusCode::UNPROCESSABLE_ENTITY);

    let value = read_json(response).await;
    assert_eq!(value["error"]["code"], "invalid_request_body");
    assert_eq!(value["error"]["status"], 422);
}
#[tokio::test]
async fn create_job_rejects_requests_with_both_path_and_source() {
    let archive = sample_zip_archive();
    let response = router()
        .oneshot(post_json(
            "/v1/jobs",
            serde_json::json!({
                "path": archive.path().display().to_string(),
                "source": {
                    "kind": "shared_filesystem_path",
                    "path": archive.path().display().to_string()
                }
            }),
        ))
        .await
        .expect("router should respond");

    assert_eq!(response.status(), StatusCode::UNPROCESSABLE_ENTITY);

    let value = read_json(response).await;
    assert_eq!(value["error"]["code"], "invalid_request_body");
    assert!(value["error"]["details"]["reason"]
        .as_str()
        .is_some_and(|reason| reason.contains("exactly one of `path` or `source`")));
}
#[tokio::test]
async fn create_job_rejects_object_storage_source_with_path_field() {
    let response = router()
        .oneshot(post_json(
            "/v1/jobs",
            serde_json::json!({
                "source": {
                    "kind": "object_storage_url",
                    "path": "/tmp/should-not-be-used.zip"
                }
            }),
        ))
        .await
        .expect("router should respond");

    assert_eq!(response.status(), StatusCode::UNPROCESSABLE_ENTITY);

    let value = read_json(response).await;
    assert_eq!(value["error"]["code"], "invalid_request_body");
    assert!(value["error"]["details"]["reason"]
        .as_str()
        .is_some_and(|reason| reason.contains("`source.path` is not allowed")));
}
#[test]
fn shared_source_request_exposes_structured_source_ref() {
    let request = shared_source_request(Path::new("/mnt/archive-share/sample.zip"), false);
    let source = request.source_ref();

    assert_eq!(source.kind, model::ScanSourceKind::SharedFilesystemPath);
    assert_eq!(source.path(), Some("/mnt/archive-share/sample.zip"));
    assert_eq!(source.url(), None);
}
#[test]
fn object_storage_source_request_exposes_structured_source_ref() {
    let request = object_storage_source_request("https://storage.example.com/sample.zip", false);
    let source = request.source_ref();

    assert_eq!(source.kind, model::ScanSourceKind::ObjectStorageUrl);
    assert_eq!(source.path(), None);
    assert_eq!(source.url(), Some("https://storage.example.com/sample.zip"));
}
