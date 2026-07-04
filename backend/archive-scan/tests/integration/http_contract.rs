#![cfg(feature = "service")]

use crate::support::{read_json, sample_zip_archive, wait_for_job_result, ServiceProcess};
use reqwest::StatusCode;
use std::process::Command;

#[test]
fn service_e2e_exercises_all_declared_routes() {
    let archive = sample_zip_archive();
    let service = ServiceProcess::spawn();

    let healthz = service
        .client()
        .get(service.url("/healthz"))
        .send()
        .expect("service should expose healthz");
    assert_eq!(healthz.status(), StatusCode::OK);

    let readyz =
        service.client().get(service.url("/readyz")).send().expect("service should expose readyz");
    assert_eq!(readyz.status(), StatusCode::OK);

    let prometheus = service
        .client()
        .get(service.url("/metrics"))
        .send()
        .expect("service should expose prometheus metrics");
    assert_eq!(prometheus.status(), StatusCode::OK);
    assert!(prometheus
        .headers()
        .get("content-type")
        .and_then(|value| value.to_str().ok())
        .is_some_and(|value| value.starts_with("text/plain")));
    let prometheus_text = prometheus.text().expect("prometheus response body should be readable");
    assert!(prometheus_text.contains("archive_scan_jobs_created_total"));

    let openapi = service
        .client()
        .get(service.url("/openapi.json"))
        .send()
        .expect("service should expose openapi");
    assert_eq!(openapi.status(), StatusCode::OK);

    let docs =
        service.client().get(service.url("/docs")).send().expect("service should expose docs");
    assert_eq!(docs.status(), StatusCode::OK);
    let docs_html = docs.text().expect("docs body should be readable");
    assert!(docs_html.contains("POST /v1/extract/upload"));
    assert!(docs_html.contains("generated from Rust route metadata"));

    let legacy_scan = service
        .client()
        .post(service.url("/v1/scan/local-path"))
        .header("content-type", "application/json")
        .body(
            serde_json::to_vec(&serde_json::json!({
                "path": archive.path().display().to_string(),
                "fast_only": true,
                "include_entries": true
            }))
            .expect("legacy scan request should serialize"),
        )
        .send()
        .expect("service should accept legacy scan route");
    assert_eq!(legacy_scan.status(), StatusCode::OK);
    assert_eq!(read_json(legacy_scan)["total_entries"], 2);

    let source_scan = service
        .client()
        .post(service.url("/v1/scan/source"))
        .header("content-type", "application/json")
        .body(
            serde_json::to_vec(&serde_json::json!({
                "source": {
                    "kind": "shared_filesystem_path",
                    "path": archive.path().display().to_string()
                },
                "fast_only": true
            }))
            .expect("structured scan request should serialize"),
        )
        .send()
        .expect("service should accept structured scan route");
    assert_eq!(source_scan.status(), StatusCode::OK);
    assert_eq!(read_json(source_scan)["total_entries"], 2);

    let extract_form = reqwest::blocking::multipart::Form::new()
        .file("archive", archive.path())
        .expect("multipart archive field should be created")
        .text("fast_only", "true")
        .text("include_entries", "true");
    let extract = service
        .client()
        .post(service.url("/v1/extract/upload"))
        .multipart(extract_form)
        .send()
        .expect("service should accept upload extract route");
    assert_eq!(extract.status(), StatusCode::OK);
    let extract_value = read_json(extract);
    assert_eq!(extract_value["total_entries"], 2);
    assert_eq!(extract_value["stored_files"], 2);

    let metrics = service
        .client()
        .get(service.url("/v1/jobs/metrics"))
        .send()
        .expect("service should expose metrics");
    assert_eq!(metrics.status(), StatusCode::OK);

    let otlp = service
        .client()
        .get(service.url("/v1/jobs/metrics/otlp"))
        .send()
        .expect("service should expose otlp metrics");
    assert_eq!(otlp.status(), StatusCode::OK);
    let otlp_value = read_json(otlp);
    assert!(otlp_value["resourceMetrics"].is_array());

    let create = service
        .client()
        .post(service.url("/v1/jobs"))
        .header("content-type", "application/json")
        .body(
            serde_json::to_vec(&serde_json::json!({
                "path": archive.path().display().to_string(),
                "fast_only": true
            }))
            .expect("create job request should serialize"),
        )
        .send()
        .expect("service should create async job");
    assert_eq!(create.status(), StatusCode::CREATED);
    let create_value = read_json(create);
    let job_id = create_value["job_id"].as_str().expect("job_id should be present");

    let status = service
        .client()
        .get(service.url(&format!("/v1/jobs/{job_id}")))
        .send()
        .expect("service should expose job status");
    assert_eq!(status.status(), StatusCode::OK);

    let result = wait_for_job_result(&service, job_id);
    assert_eq!(result["total_entries"], 2);

    let cancel = service
        .client()
        .post(service.url(&format!("/v1/jobs/{job_id}/cancel")))
        .send()
        .expect("service should expose cancel route");
    assert_eq!(cancel.status(), StatusCode::CONFLICT);
}

#[cfg(feature = "service")]
#[test]
fn server_help_lists_storage_runtime_flags() {
    let binary = std::env::var_os("CARGO_BIN_EXE_archive_scan_server")
        .expect("cargo should expose server binary path");
    let output = Command::new(binary).arg("--help").output().expect("server help should start");

    assert!(output.status.success());
    let stdout = String::from_utf8_lossy(&output.stdout);
    assert!(stdout.contains("--job-store-backend"));
    assert!(stdout.contains("--job-store-path"));
    assert!(stdout.contains("--job-store-redis-url"));
    assert!(stdout.contains("--job-store-redis-max-connections"));
    assert!(stdout.contains("--job-store-redis-cleanup-batch-size"));
    assert!(stdout.contains("--job-store-postgres-url"));
    assert!(stdout.contains("--job-store-postgres-table-prefix"));
    assert!(stdout.contains("--job-store-postgres-max-connections"));
    assert!(stdout.contains("--result-store-backend"));
    assert!(stdout.contains("--result-store-dir"));
    assert!(stdout.contains("--result-store-s3-endpoint"));
    assert!(stdout.contains("--result-store-s3-region"));
    assert!(stdout.contains("--result-store-s3-bucket"));
    assert!(stdout.contains("--result-store-s3-key-prefix"));
    assert!(stdout.contains("--result-store-s3-access-key-id"));
    assert!(stdout.contains("--result-store-s3-secret-access-key"));
    assert!(stdout.contains("--result-store-s3-session-token"));
    assert!(stdout.contains("--result-store-s3-path-style"));
    assert!(stdout.contains("--result-inline-max-bytes"));
    assert!(stdout.contains("--result-artifact-retention-secs"));
    assert!(stdout.contains("--result-artifact-gc-interval-secs"));
    assert!(stdout.contains("--object-source-max-bytes"));
    assert!(stdout.contains("--object-source-timeout-secs"));
    assert!(stdout.contains("--object-source-allow-private-networks"));
    assert!(stdout.contains("--extract-store-backend"));
    assert!(stdout.contains("--extract-store-dir"));
    assert!(stdout.contains("--extract-store-s3-endpoint"));
    assert!(stdout.contains("--extract-store-s3-bucket"));
    assert!(stdout.contains("--extract-store-s3-key-prefix"));
    assert!(stdout.contains("--extract-store-s3-access-key-id"));
    assert!(stdout.contains("--extract-store-s3-secret-access-key"));
    assert!(stdout.contains("--extract-store-s3-path-style"));
    assert!(stdout.contains("--extract-metadata-backend"));
    assert!(stdout.contains("--extract-metadata-dir"));
    assert!(stdout.contains("--extract-metadata-postgres-url"));
    assert!(stdout.contains("--extract-metadata-postgres-table-prefix"));
    assert!(stdout.contains("--extract-metadata-batch-size"));
}
