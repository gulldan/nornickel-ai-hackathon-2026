#![allow(dead_code)]

use super::{
    config::{
        ExtractMetadataBackendKind, RuntimeExtractConfig, RuntimeSourceConfig, RuntimeStorageConfig,
    },
    model::{
        ArchiveSummary, CreateJobResponse, EntryKindCount, ErrorEnvelope, HealthResponse,
        JobFailure, JobLifecycleTotals, JobMaintenanceTotals, JobMetricsResponse,
        JobResultPendingResponse, JobRetentionPolicy, JobState, JobStateCounts, JobStatusResponse,
        MimeCount, ScanArchiveRequest, ScanArchiveResponse, ScanArchiveSource, ScanSourceKind,
        TypeCount,
    },
};
use crate::{
    extract::{
        ExtractArchiveResult, ExtractArchiveSummary, ExtractDestinationSummary,
        ExtractEntryKindCount, ExtractMimeCount, ExtractStorageKind, ExtractTypeCount,
        ExtractedEntry, StoredObject,
    },
    row::{ArchiveMeta, DetectionSource, EntryKind, EntryRow},
};
use serde_json::Value;
use utoipa::{OpenApi, ToSchema};

#[derive(ToSchema)]
struct UploadExtractMultipart {
    #[schema(value_type = String, format = Binary)]
    archive: String,
    extraction_id: Option<String>,
    header_bytes: Option<usize>,
    block_size: Option<usize>,
    full_hash: Option<bool>,
    fast_only: Option<bool>,
    include_entries: Option<bool>,
}

#[derive(ToSchema)]
struct OtlpMetricsExport {
    #[serde(rename = "resourceMetrics")]
    #[schema(value_type = Object)]
    resource_metrics: Value,
}

#[derive(OpenApi)]
#[openapi(
    info(
        title = "archive_scan service API",
        version = env!("CARGO_PKG_VERSION"),
        description = "Archive scan and extraction service."
    ),
    paths(
        healthz_doc,
        readyz_doc,
        prometheus_metrics_doc,
        openapi_doc,
        docs_doc,
        scan_source_doc,
        scan_local_path_doc,
        extract_upload_doc,
        create_job_doc,
        get_job_metrics_doc,
        get_otlp_metrics_doc,
        get_job_status_doc,
        cancel_job_doc,
        get_job_result_doc
    ),
    components(schemas(
        ArchiveMeta,
        ArchiveSummary,
        CreateJobResponse,
        DetectionSource,
        EntryKind,
        EntryKindCount,
        EntryRow,
        ErrorEnvelope,
        ExtractArchiveResult,
        ExtractArchiveSummary,
        ExtractDestinationSummary,
        ExtractEntryKindCount,
        ExtractedEntry,
        ExtractMetadataBackendKind,
        ExtractMimeCount,
        ExtractStorageKind,
        ExtractTypeCount,
        HealthResponse,
        JobFailure,
        JobLifecycleTotals,
        JobMaintenanceTotals,
        JobMetricsResponse,
        JobResultPendingResponse,
        JobRetentionPolicy,
        JobState,
        JobStateCounts,
        JobStatusResponse,
        MimeCount,
        OtlpMetricsExport,
        RuntimeExtractConfig,
        RuntimeSourceConfig,
        RuntimeStorageConfig,
        ScanArchiveRequest,
        ScanArchiveResponse,
        ScanArchiveSource,
        ScanSourceKind,
        StoredObject,
        TypeCount,
        UploadExtractMultipart
    )),
    tags(
        (name = "health", description = "Liveness and readiness endpoints"),
        (name = "scan", description = "Synchronous archive scanning"),
        (name = "extract", description = "Archive upload extraction"),
        (name = "jobs", description = "Asynchronous scan jobs"),
        (name = "observability", description = "Metrics and API docs")
    )
)]
struct ApiDoc;

pub(super) fn openapi_document() -> Value {
    serde_json::to_value(ApiDoc::openapi()).expect("generated OpenAPI document should serialize")
}

pub(super) fn docs_html() -> &'static str {
    r#"<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8">
    <title>archive_scan service docs</title>
    <style>
      body { font-family: ui-sans-serif, system-ui, sans-serif; margin: 2rem auto; max-width: 960px; line-height: 1.6; color: #111827; }
      code, pre { background: #f3f4f6; border-radius: 0.35rem; }
      code { padding: 0.15rem 0.35rem; }
      pre { padding: 1rem; overflow-x: auto; }
      h1, h2 { line-height: 1.25; }
    </style>
  </head>
  <body>
    <h1>archive_scan service</h1>
    <p>OpenAPI specification: <a href="/openapi.json"><code>/openapi.json</code></a>. The document is generated from Rust route metadata and DTO schemas at build time.</p>
    <h2>Endpoints</h2>
    <ul>
      <li><code>GET /healthz</code></li>
      <li><code>GET /readyz</code></li>
      <li><code>GET /metrics</code></li>
      <li><code>GET /openapi.json</code></li>
      <li><code>GET /docs</code></li>
      <li><code>POST /v1/scan/source</code></li>
      <li><code>POST /v1/scan/local-path</code></li>
      <li><code>POST /v1/extract/upload</code></li>
      <li><code>POST /v1/jobs</code></li>
      <li><code>GET /v1/jobs/metrics</code></li>
      <li><code>GET /v1/jobs/metrics/otlp</code></li>
      <li><code>GET /v1/jobs/{job_id}</code></li>
      <li><code>POST /v1/jobs/{job_id}/cancel</code></li>
      <li><code>GET /v1/jobs/{job_id}/result</code></li>
    </ul>
    <h2>Upload extract</h2>
    <pre><code>curl -sS http://127.0.0.1:3000/v1/extract/upload \
  -F archive=@sample.zip \
  -F fast_only=true \
  -F include_entries=false | jq .</code></pre>
    <h2>Sync scan</h2>
    <pre><code>curl -sS http://127.0.0.1:3000/v1/scan/source \
  -H 'content-type: application/json' \
  -d '{"source":{"kind":"shared_filesystem_path","path":"/mnt/archive/sample.zip"},"fast_only":true}' | jq .</code></pre>
  </body>
</html>"#
}

#[utoipa::path(
    get,
    path = "/healthz",
    tag = "health",
    responses((status = 200, description = "Service is alive", body = HealthResponse))
)]
fn healthz_doc() {}

#[utoipa::path(
    get,
    path = "/readyz",
    tag = "health",
    responses(
        (status = 200, description = "Service dependencies are ready", body = HealthResponse),
        (status = 503, description = "Service dependency is not ready", body = ErrorEnvelope)
    )
)]
fn readyz_doc() {}

#[utoipa::path(
    get,
    path = "/metrics",
    tag = "observability",
    responses((status = 200, description = "Prometheus text exposition", body = String, content_type = "text/plain"))
)]
fn prometheus_metrics_doc() {}

#[utoipa::path(
    get,
    path = "/openapi.json",
    tag = "observability",
    responses((status = 200, description = "Generated OpenAPI document", body = Object))
)]
fn openapi_doc() {}

#[utoipa::path(
    get,
    path = "/docs",
    tag = "observability",
    responses((status = 200, description = "HTML docs", body = String, content_type = "text/html"))
)]
fn docs_doc() {}

#[utoipa::path(
    post,
    path = "/v1/scan/source",
    tag = "scan",
    request_body = ScanArchiveRequest,
    responses(
        (status = 200, description = "Archive scan completed", body = ScanArchiveResponse),
        (status = 400, description = "Invalid request", body = ErrorEnvelope),
        (status = 404, description = "Archive path not found", body = ErrorEnvelope),
        (status = 422, description = "Invalid JSON body", body = ErrorEnvelope),
        (status = 500, description = "Scan failed", body = ErrorEnvelope)
    )
)]
fn scan_source_doc() {}

#[utoipa::path(
    post,
    path = "/v1/scan/local-path",
    tag = "scan",
    request_body = ScanArchiveRequest,
    responses(
        (status = 200, description = "Archive scan completed", body = ScanArchiveResponse),
        (status = 400, description = "Invalid request", body = ErrorEnvelope),
        (status = 404, description = "Archive path not found", body = ErrorEnvelope),
        (status = 422, description = "Invalid JSON body", body = ErrorEnvelope),
        (status = 500, description = "Scan failed", body = ErrorEnvelope)
    )
)]
fn scan_local_path_doc() {}

#[utoipa::path(
    post,
    path = "/v1/extract/upload",
    tag = "extract",
    request_body(content = UploadExtractMultipart, content_type = "multipart/form-data"),
    responses(
        (status = 200, description = "Archive extraction completed", body = ExtractArchiveResult),
        (status = 400, description = "Invalid multipart request", body = ErrorEnvelope),
        (status = 413, description = "Archive upload exceeds configured limit", body = ErrorEnvelope),
        (status = 500, description = "Extraction failed", body = ErrorEnvelope)
    )
)]
fn extract_upload_doc() {}

#[utoipa::path(
    post,
    path = "/v1/jobs",
    tag = "jobs",
    params(("Idempotency-Key" = Option<String>, Header, description = "Optional retry key")),
    request_body = ScanArchiveRequest,
    responses(
        (status = 200, description = "Existing idempotent job", body = CreateJobResponse),
        (status = 201, description = "Job created", body = CreateJobResponse),
        (status = 409, description = "Idempotency conflict", body = ErrorEnvelope)
    )
)]
fn create_job_doc() {}

#[utoipa::path(
    get,
    path = "/v1/jobs/metrics",
    tag = "jobs",
    responses((status = 200, description = "Job metrics", body = JobMetricsResponse))
)]
fn get_job_metrics_doc() {}

#[utoipa::path(
    get,
    path = "/v1/jobs/metrics/otlp",
    tag = "observability",
    responses((status = 200, description = "OTLP JSON metrics export", body = OtlpMetricsExport))
)]
fn get_otlp_metrics_doc() {}

#[utoipa::path(
    get,
    path = "/v1/jobs/{job_id}",
    tag = "jobs",
    params(("job_id" = String, Path, description = "Job id")),
    responses(
        (status = 200, description = "Job status", body = JobStatusResponse),
        (status = 404, description = "Job not found", body = ErrorEnvelope)
    )
)]
fn get_job_status_doc() {}

#[utoipa::path(
    post,
    path = "/v1/jobs/{job_id}/cancel",
    tag = "jobs",
    params(("job_id" = String, Path, description = "Job id")),
    responses(
        (status = 200, description = "Job cancelled", body = JobStatusResponse),
        (status = 404, description = "Job not found", body = ErrorEnvelope),
        (status = 409, description = "Job cannot be cancelled", body = ErrorEnvelope)
    )
)]
fn cancel_job_doc() {}

#[utoipa::path(
    get,
    path = "/v1/jobs/{job_id}/result",
    tag = "jobs",
    params(("job_id" = String, Path, description = "Job id")),
    responses(
        (status = 200, description = "Completed job result", body = ScanArchiveResponse),
        (status = 202, description = "Job is not complete", body = JobResultPendingResponse),
        (status = 404, description = "Job not found", body = ErrorEnvelope),
        (status = 409, description = "Job failed or was cancelled", body = ErrorEnvelope)
    )
)]
fn get_job_result_doc() {}
