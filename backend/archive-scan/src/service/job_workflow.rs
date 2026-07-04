use super::{
    error::{ServiceError, ServiceResult},
    jobs::{CreateJobOutcome, JobCancellation, JobMetricsSnapshot, JobSnapshot},
    model::{
        CreateJobResponse, JobFailure, JobLifecycleTotals, JobMaintenanceTotals,
        JobMetricsResponse, JobRetentionPolicy, JobStateCounts, JobStatusResponse,
        ScanArchiveRequest, ScanArchiveResponse,
    },
    scanning::scan_archive_request_with_interrupt,
    state::AppState,
};
use crate::cancel::is_scan_cancelled_error;
use anyhow::Result;
use axum::{
    http::{HeaderMap, StatusCode},
    Json,
};

const IDEMPOTENCY_KEY_HEADER: &str = "idempotency-key";

pub(super) fn spawn_scan_job(
    state: AppState,
    job_id: String,
    request: ScanArchiveRequest,
) -> Option<tokio::task::JoinHandle<()>> {
    let cancellation = match state.jobs.try_cancellation(&job_id) {
        Ok(Some(cancellation)) => cancellation,
        Ok(None) => {
            tracing::warn!(job_id = %job_id, "scan job has no cancellation state");
            return None;
        }
        Err(err) => {
            tracing::error!(job_id = %job_id, error = %err, "failed to load scan job cancellation state");
            return None;
        }
    };
    let source_download = state.source_download.clone();
    Some(spawn_scan_job_with_runner(
        state,
        job_id,
        request,
        cancellation,
        move |request, cancellation| {
            scan_archive_request_with_interrupt(request, &source_download, move || {
                cancellation.is_cancelled()
            })
        },
    ))
}

pub(super) fn spawn_scan_job_with_runner<RunScan>(
    state: AppState,
    job_id: String,
    request: ScanArchiveRequest,
    cancellation: JobCancellation,
    run_scan: RunScan,
) -> tokio::task::JoinHandle<()>
where
    RunScan:
        FnOnce(ScanArchiveRequest, JobCancellation) -> Result<ScanArchiveResponse> + Send + 'static,
{
    tokio::spawn(async move {
        if !state.jobs.try_mark_running(&job_id).unwrap_or(false) {
            tracing::warn!(job_id = %job_id, "scan job was not marked running");
            return;
        }
        tracing::info!(job_id = %job_id, "scan job started");

        match tokio::task::spawn_blocking(move || run_scan(request, cancellation)).await {
            Ok(Ok(response)) => {
                tracing::info!(
                    job_id = %job_id,
                    total_entries = response.total_entries,
                    total_files = response.total_files,
                    "scan job succeeded"
                );
                let _ = state.jobs.try_mark_succeeded(&job_id, response);
            }
            Ok(Err(err)) if is_scan_cancelled_error(&err) => {
                tracing::info!(job_id = %job_id, "scan job cancelled");
            }
            Ok(Err(err)) => {
                tracing::error!(job_id = %job_id, error = %err, "scan job failed");
                let _ = state
                    .jobs
                    .try_mark_failed(&job_id, JobFailure::new("scan_failed", err.to_string()));
            }
            Err(err) => {
                tracing::error!(job_id = %job_id, error = %err, "scan job task failed");
                let _ = state
                    .jobs
                    .try_mark_failed(&job_id, JobFailure::new("scan_task_failed", err.to_string()));
            }
        }
    })
}

pub(super) fn job_status_response(snapshot: &JobSnapshot) -> JobStatusResponse {
    let (status_url, result_url) = job_urls(&snapshot.id);
    JobStatusResponse {
        job_id: snapshot.id.clone(),
        state: snapshot.state,
        created_at_unix: snapshot.created_at_unix,
        updated_at_unix: snapshot.updated_at_unix,
        status_url,
        result_url,
        request: snapshot.request.clone(),
        error: snapshot.error.clone(),
    }
}

pub(super) fn job_metrics_response(snapshot: &JobMetricsSnapshot) -> JobMetricsResponse {
    JobMetricsResponse {
        retention: JobRetentionPolicy { terminal_job_retention_secs: snapshot.retention_secs },
        storage: snapshot.storage.clone(),
        current: JobStateCounts {
            visible_jobs: snapshot.visible_jobs,
            active_jobs: snapshot.active_jobs,
            terminal_jobs: snapshot.terminal_jobs,
            queued_jobs: snapshot.queued_jobs,
            running_jobs: snapshot.running_jobs,
            succeeded_jobs: snapshot.succeeded_jobs,
            failed_jobs: snapshot.failed_jobs,
            cancelled_jobs: snapshot.cancelled_jobs,
        },
        lifecycle: JobLifecycleTotals {
            created_total: snapshot.created_total,
            started_total: snapshot.started_total,
            succeeded_total: snapshot.succeeded_total,
            failed_total: snapshot.failed_total,
            cancelled_total: snapshot.cancelled_total,
            expired_total: snapshot.expired_total,
        },
        maintenance: JobMaintenanceTotals {
            recovery_runs_total: snapshot.recovery_runs_total,
            recovered_jobs_total: snapshot.recovered_jobs_total,
            recovered_running_jobs_total: snapshot.recovered_running_jobs_total,
            recovery_deleted_result_refs_total: snapshot.recovery_deleted_result_refs_total,
            cleanup_deleted_result_refs_total: snapshot.cleanup_deleted_result_refs_total,
            result_artifact_gc_runs_total: snapshot.result_artifact_gc_runs_total,
            result_artifact_gc_deleted_total: snapshot.result_artifact_gc_deleted_total,
            result_artifact_gc_failures_total: snapshot.result_artifact_gc_failures_total,
        },
    }
}

pub(super) fn load_job_metrics_snapshot(state: &AppState) -> ServiceResult<JobMetricsSnapshot> {
    state.jobs.try_metrics().map_err(|err| ServiceError::job_store_failed(err.to_string()))
}

pub(super) fn job_urls(job_id: &str) -> (String, String) {
    (format!("/v1/jobs/{job_id}"), format!("/v1/jobs/{job_id}/result"))
}

fn create_job_response(
    snapshot: JobSnapshot,
    status: StatusCode,
) -> (StatusCode, Json<CreateJobResponse>) {
    let (status_url, result_url) = job_urls(&snapshot.id);
    (status, Json(CreateJobResponse::new(&snapshot.id, snapshot.state, status_url, result_url)))
}

pub(super) fn create_job_response_from_outcome(
    state: AppState,
    idempotency_key: &str,
    request: ScanArchiveRequest,
    outcome: CreateJobOutcome,
) -> ServiceResult<(StatusCode, Json<CreateJobResponse>)> {
    match outcome {
        CreateJobOutcome::Created(snapshot) => {
            let _ = spawn_scan_job(state, snapshot.id.clone(), request);
            Ok(create_job_response(snapshot, StatusCode::CREATED))
        }
        CreateJobOutcome::Existing(snapshot) => Ok(create_job_response(snapshot, StatusCode::OK)),
        CreateJobOutcome::Conflict(job_id) => {
            Err(ServiceError::idempotency_key_conflict(idempotency_key, &job_id))
        }
    }
}

pub(super) fn idempotency_key_from_headers(headers: &HeaderMap) -> ServiceResult<Option<String>> {
    let Some(value) = headers.get(IDEMPOTENCY_KEY_HEADER) else {
        return Ok(None);
    };

    let value = value
        .to_str()
        .map_err(|_| ServiceError::invalid_idempotency_key("header value must be valid UTF-8"))?;
    let key = value.trim();
    if key.is_empty() {
        return Err(ServiceError::invalid_idempotency_key("header value must not be empty"));
    }

    Ok(Some(key.to_owned()))
}
