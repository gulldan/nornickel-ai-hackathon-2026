#[path = "service/config.rs"]
mod config;
#[path = "service/error.rs"]
mod error;
#[path = "service/extract/mod.rs"]
mod extract_runtime;
#[path = "service/job_workflow.rs"]
mod job_workflow;
#[path = "service/jobs.rs"]
mod jobs;
#[path = "service/metrics_export.rs"]
mod metrics_export;
#[path = "service/model.rs"]
mod model;
#[path = "service/openapi.rs"]
mod openapi;
#[path = "service/result_store.rs"]
mod result_store;
#[path = "service/scanning.rs"]
mod scanning;
#[path = "service/source.rs"]
mod source;
#[path = "service/state.rs"]
mod state;
#[cfg(test)]
#[path = "service/tests.rs"]
mod tests;
#[path = "service/upload.rs"]
mod upload;

use crate::extract::ExtractArchiveResult;
use anyhow::{Context, Result};
use axum::{
    extract::{
        multipart::MultipartRejection, rejection::JsonRejection, DefaultBodyLimit,
        Json as ExtractJson, Multipart, Path as AxumPath, State,
    },
    http::{header, HeaderMap, Method, StatusCode, Uri},
    response::{Html, IntoResponse, Response},
    routing::{get, post},
    Json, Router,
};
pub use config::{
    ExtractMetadataBackendKind, ExtractMetadataConfig, ExtractStoreBackendKind, ExtractStoreConfig,
    JobStoreBackendKind, JobStoreRuntimeConfig, ResultStoreBackendKind, ResultStoreConfig,
    SourceDownloadConfig, DEFAULT_OBJECT_SOURCE_MAX_BYTES,
};
use error::{ServiceError, ServiceResult};
use extract_runtime::store::RuntimeExtractStore;
#[cfg(test)]
use job_workflow::spawn_scan_job_with_runner;
use job_workflow::{
    create_job_response_from_outcome, idempotency_key_from_headers, job_metrics_response,
    job_status_response, job_urls, load_job_metrics_snapshot, spawn_scan_job,
};
use jobs::{CancelJobOutcome, CreateJobOutcome, DEFAULT_TERMINAL_JOB_RETENTION_SECS};
#[cfg(test)]
use model::JobFailure;
use model::{
    CreateJobResponse, HealthResponse, JobMetricsResponse, JobResultPendingResponse, JobState,
    JobStatusResponse, ScanArchiveRequest, ScanArchiveResponse,
};
#[cfg(test)]
use scanning::scan_archive_at_path;
use scanning::scan_archive_request;
use state::AppState;
pub use state::ServiceConfig;
use upload::{extract_uploaded_archive, read_extract_upload};

pub const DEFAULT_JOB_RETENTION_SECS: u64 = DEFAULT_TERMINAL_JOB_RETENTION_SECS;

pub fn router() -> Router {
    router_with_state(AppState::default())
}

fn router_with_state(state: AppState) -> Router {
    let upload_limit = state.upload_limit_bytes();
    Router::new()
        .route("/healthz", get(healthz))
        .route("/readyz", get(readyz))
        .route("/metrics", get(get_prometheus_metrics))
        .route("/openapi.json", get(openapi_spec))
        .route("/docs", get(docs))
        .route("/v1/scan/source", post(scan_source))
        .route("/v1/scan/local-path", post(scan_source))
        .route("/v1/extract/upload", post(extract_upload))
        .route("/v1/jobs", post(create_job))
        .route("/v1/jobs/metrics", get(get_job_metrics))
        .route("/v1/jobs/metrics/otlp", get(get_otlp_metrics))
        .route("/v1/jobs/{job_id}", get(get_job_status))
        .route("/v1/jobs/{job_id}/cancel", post(cancel_job))
        .route("/v1/jobs/{job_id}/result", get(get_job_result))
        .method_not_allowed_fallback(method_not_allowed)
        .fallback(not_found)
        .layer(DefaultBodyLimit::max(upload_limit))
        .with_state(state)
}

/// Runs the HTTP service until the process receives a shutdown signal.
///
/// # Errors
///
/// Returns an error if the listener cannot bind or the server exits with an I/O failure.
pub async fn run(config: ServiceConfig) -> Result<()> {
    let listen_addr = config.addr;
    tracing::info!(%listen_addr, "binding archive scan service");
    let listener = tokio::net::TcpListener::bind(config.addr).await?;
    let (state, recovered_jobs) =
        tokio::task::spawn_blocking(move || AppState::with_config_and_recovery(&config))
            .await
            .context("service startup state initialization task failed")??;
    tracing::info!(
        %listen_addr,
        recovered_jobs = recovered_jobs.len(),
        "archive scan service started"
    );
    for snapshot in recovered_jobs {
        tracing::info!(job_id = %snapshot.id, state = ?snapshot.state, "requeueing recovered scan job");
        let request = snapshot.request.clone();
        let _ = spawn_scan_job(state.clone(), snapshot.id, request);
    }

    let result = axum::serve(listener, router_with_state(state))
        .with_graceful_shutdown(shutdown_signal())
        .await;
    match &result {
        Ok(()) => tracing::info!(%listen_addr, "archive scan service stopped"),
        Err(err) => {
            tracing::error!(%listen_addr, error = %err, "archive scan service stopped with error");
        }
    }
    result?;
    Ok(())
}

async fn shutdown_signal() {
    #[cfg(unix)]
    {
        let mut terminate =
            tokio::signal::unix::signal(tokio::signal::unix::SignalKind::terminate())
                .expect("installing SIGTERM handler should succeed");
        tokio::select! {
            _ = tokio::signal::ctrl_c() => {},
            _ = terminate.recv() => {},
        }
    }

    #[cfg(not(unix))]
    {
        let _ = tokio::signal::ctrl_c().await;
    }
}

async fn healthz() -> Json<HealthResponse> {
    Json(HealthResponse { status: "ok", mode: "service" })
}

async fn readyz(State(state): State<AppState>) -> ServiceResult<Json<HealthResponse>> {
    tokio::task::spawn_blocking(move || readiness_check(&state))
        .await
        .map_err(|err| ServiceError::service_not_ready(err.to_string()))?
        .map_err(|err| ServiceError::service_not_ready(err.to_string()))?;
    Ok(Json(HealthResponse { status: "ready", mode: "service" }))
}

fn readiness_check(state: &AppState) -> Result<()> {
    state.jobs.readiness_check()?;
    source::validate_runtime_source_config(&state.source_download).map_err(anyhow::Error::msg)?;
    RuntimeExtractStore::readiness_check(&state.extract_store)?;
    state.extract_metadata.readiness_check()
}

async fn openapi_spec() -> Json<serde_json::Value> {
    Json(openapi::openapi_document())
}

async fn docs() -> Html<&'static str> {
    Html(openapi::docs_html())
}

async fn not_found(uri: Uri) -> ServiceError {
    ServiceError::route_not_found(&uri)
}

async fn method_not_allowed(method: Method, uri: Uri) -> ServiceError {
    ServiceError::method_not_allowed(&method, &uri)
}

async fn scan_source(
    State(state): State<AppState>,
    payload: std::result::Result<ExtractJson<ScanArchiveRequest>, JsonRejection>,
) -> ServiceResult<Json<ScanArchiveResponse>> {
    let ExtractJson(request) = payload.map_err(ServiceError::from_json_rejection)?;
    source::validate_scan_source(request.source_ref(), &state.source_download).await?;
    let source_download = state.source_download.clone();

    let response =
        tokio::task::spawn_blocking(move || scan_archive_request(request, &source_download))
            .await
            .map_err(|err| ServiceError::scan_task_failed(err.to_string()))?
            .map_err(|err| ServiceError::scan_failed(err.to_string()))?;

    Ok(Json(response))
}

async fn extract_upload(
    State(state): State<AppState>,
    payload: std::result::Result<Multipart, MultipartRejection>,
) -> ServiceResult<Json<ExtractArchiveResult>> {
    let mut multipart = payload.map_err(|err| ServiceError::invalid_multipart(err.to_string()))?;
    let upload = read_extract_upload(&mut multipart, &state.source_download).await?;
    let extract_store = state.extract_store.clone();
    let metadata_store = state.extract_metadata.clone();

    let response = tokio::task::spawn_blocking(move || {
        extract_uploaded_archive(upload, extract_store, metadata_store)
    })
    .await
    .map_err(|err| ServiceError::scan_task_failed(err.to_string()))?
    .map_err(|err| ServiceError::scan_failed(err.to_string()))?;

    Ok(Json(response))
}

async fn create_job(
    State(state): State<AppState>,
    headers: HeaderMap,
    payload: std::result::Result<ExtractJson<ScanArchiveRequest>, JsonRejection>,
) -> ServiceResult<(StatusCode, Json<CreateJobResponse>)> {
    let ExtractJson(request) = payload.map_err(ServiceError::from_json_rejection)?;
    let idempotency_key = idempotency_key_from_headers(&headers)?;

    if let Some(key) = idempotency_key.as_deref() {
        if let Some(outcome) = state
            .jobs
            .try_resolve_idempotent_job(key, &request)
            .map_err(|err| ServiceError::job_store_failed(err.to_string()))?
        {
            return create_job_response_from_outcome(state, key, request, outcome);
        }
    }

    source::validate_scan_source(request.source_ref(), &state.source_download).await?;

    let outcome = match idempotency_key.as_ref() {
        Some(key) => state
            .jobs
            .try_create_job_with_idempotency(request.clone(), key.clone())
            .map_err(|err| ServiceError::job_store_failed(err.to_string()))?,
        None => CreateJobOutcome::Created(
            state
                .jobs
                .try_create_job(request.clone())
                .map_err(|err| ServiceError::job_store_failed(err.to_string()))?,
        ),
    };

    create_job_response_from_outcome(
        state,
        idempotency_key.as_deref().unwrap_or_default(),
        request,
        outcome,
    )
}

async fn get_job_metrics(State(state): State<AppState>) -> ServiceResult<Json<JobMetricsResponse>> {
    let metrics = load_job_metrics_snapshot(&state)?;
    Ok(Json(job_metrics_response(&metrics)))
}

async fn get_prometheus_metrics(State(state): State<AppState>) -> ServiceResult<Response> {
    let metrics = load_job_metrics_snapshot(&state)?;
    Ok((
        [(header::CONTENT_TYPE, metrics_export::PROMETHEUS_CONTENT_TYPE)],
        metrics_export::prometheus_text(&metrics),
    )
        .into_response())
}

async fn get_otlp_metrics(State(state): State<AppState>) -> ServiceResult<Json<serde_json::Value>> {
    let metrics = load_job_metrics_snapshot(&state)?;
    Ok(Json(metrics_export::otlp_json(&metrics)))
}

async fn get_job_status(
    State(state): State<AppState>,
    AxumPath(job_id): AxumPath<String>,
) -> ServiceResult<Json<JobStatusResponse>> {
    let snapshot = state
        .jobs
        .try_get(&job_id)
        .map_err(|err| ServiceError::job_store_failed(err.to_string()))?
        .ok_or_else(|| ServiceError::job_not_found(&job_id))?;
    Ok(Json(job_status_response(&snapshot)))
}

async fn get_job_result(
    State(state): State<AppState>,
    AxumPath(job_id): AxumPath<String>,
) -> ServiceResult<Response> {
    let snapshot = state
        .jobs
        .try_get(&job_id)
        .map_err(|err| ServiceError::job_store_failed(err.to_string()))?
        .ok_or_else(|| ServiceError::job_not_found(&job_id))?;

    match snapshot.state {
        JobState::Queued | JobState::Running => {
            let (status_url, _) = job_urls(&snapshot.id);
            Ok((
                StatusCode::ACCEPTED,
                Json(JobResultPendingResponse::new(
                    &snapshot.id,
                    snapshot.state,
                    status_url,
                    "job has not finished yet",
                )),
            )
                .into_response())
        }
        JobState::Succeeded => {
            let result = state
                .jobs
                .load_result(&snapshot)
                .map_err(|err| ServiceError::job_result_unavailable(&snapshot.id, err.to_string()))?
                .ok_or_else(|| {
                    ServiceError::job_result_unavailable(
                        &snapshot.id,
                        "successful job is missing persisted result",
                    )
                })?;
            Ok((StatusCode::OK, Json(result)).into_response())
        }
        JobState::Cancelled => Err(ServiceError::job_cancelled(&snapshot.id)),
        JobState::Failed => {
            let failure = snapshot.error.as_ref().expect("failed job must contain failure details");
            Err(ServiceError::job_failed(&snapshot.id, failure))
        }
    }
}

async fn cancel_job(
    State(state): State<AppState>,
    AxumPath(job_id): AxumPath<String>,
) -> ServiceResult<Json<JobStatusResponse>> {
    let snapshot = match state
        .jobs
        .try_cancel(&job_id)
        .map_err(|err| ServiceError::job_store_failed(err.to_string()))?
    {
        Some(
            CancelJobOutcome::Cancelled(snapshot) | CancelJobOutcome::AlreadyCancelled(snapshot),
        ) => snapshot,
        Some(CancelJobOutcome::NotCancellable(snapshot)) => {
            return Err(ServiceError::job_not_cancellable(&snapshot.id, snapshot.state));
        }
        None => return Err(ServiceError::job_not_found(&job_id)),
    };

    Ok(Json(job_status_response(&snapshot)))
}
