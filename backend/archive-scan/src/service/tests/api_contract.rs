use super::*;

#[tokio::test]
async fn openapi_route_is_available() {
    let response = router()
        .oneshot(
            Request::builder()
                .uri("/openapi.json")
                .body(Body::empty())
                .expect("request should build"),
        )
        .await
        .expect("router should respond");

    assert_eq!(response.status(), StatusCode::OK);

    let value = read_json(response).await;
    assert_eq!(value["openapi"], "3.1.0");
    assert!(value["paths"]["/v1/scan/source"]["post"]["responses"]["200"].is_object());
    assert!(value["paths"]["/v1/scan/local-path"]["post"]["responses"]["200"].is_object());
    assert!(value["paths"]["/v1/extract/upload"]["post"]["responses"]["200"].is_object());
    assert_eq!(
        value["paths"]["/v1/extract/upload"]["post"]["requestBody"]["content"]
            ["multipart/form-data"]["schema"]["$ref"],
        "#/components/schemas/UploadExtractMultipart"
    );
    assert!(value["paths"]["/metrics"]["get"]["responses"]["200"].is_object());
    assert_eq!(value["paths"]["/v1/jobs"]["post"]["parameters"][0]["name"], "Idempotency-Key");
    assert!(value["paths"]["/v1/jobs"]["post"]["responses"]["200"].is_object());
    assert!(value["paths"]["/v1/jobs"]["post"]["responses"]["201"].is_object());
    assert!(value["paths"]["/v1/jobs"]["post"]["responses"]["409"].is_object());
    assert!(value["paths"]["/v1/jobs/metrics"]["get"]["responses"]["200"].is_object());
    assert!(value["paths"]["/v1/jobs/metrics/otlp"]["get"]["responses"]["200"].is_object());
    assert!(value["paths"]["/v1/jobs/{job_id}/cancel"]["post"]["responses"]["200"].is_object());
    assert!(value["paths"]["/v1/jobs/{job_id}/result"]["get"]["responses"]["202"].is_object());
    assert!(value["paths"]["/readyz"]["get"]["responses"]["503"].is_object());
    assert_eq!(value["components"]["schemas"]["ScanSourceKind"]["enum"][2], "object_storage_url");
    assert!(value["components"]["schemas"]["ScanArchiveRequest"].is_object());
    assert!(value["components"]["schemas"]["ScanArchiveResponse"].is_object());
    assert!(value["components"]["schemas"]["ExtractArchiveResult"].is_object());
    assert!(value["components"]["schemas"]["EntryRow"].is_object());
    assert!(value["components"]["schemas"]["RuntimeStorageConfig"].is_object());
    assert!(value["components"]["schemas"]["RuntimeExtractConfig"].is_object());
    assert!(value["components"]["schemas"]["JobMaintenanceTotals"]["properties"]
        ["result_artifact_gc_failures_total"]
        .is_object());
    assert!(value["components"]["schemas"]["OtlpMetricsExport"].is_object());
}
#[tokio::test]
async fn docs_route_is_available() {
    let response = router()
        .oneshot(Request::builder().uri("/docs").body(Body::empty()).expect("request should build"))
        .await
        .expect("router should respond");

    assert_eq!(response.status(), StatusCode::OK);

    let body = to_bytes(response.into_body(), usize::MAX).await.expect("body should read");
    let html = String::from_utf8(body.to_vec()).expect("docs body should be utf-8");
    assert!(html.contains("/openapi.json"));
    assert!(html.contains("GET /metrics"));
    assert!(html.contains("GET /openapi.json"));
    assert!(html.contains("GET /docs"));
    assert!(html.contains("POST /v1/scan/source"));
    assert!(html.contains("POST /v1/scan/local-path"));
    assert!(html.contains("POST /v1/extract/upload"));
    assert!(html.contains("POST /v1/jobs"));
    assert!(html.contains("GET /v1/jobs/metrics"));
    assert!(html.contains("GET /v1/jobs/metrics/otlp"));
    assert!(html.contains("POST /v1/jobs/{job_id}/cancel"));
    assert!(html.contains("GET /v1/jobs/{job_id}/result"));
}
