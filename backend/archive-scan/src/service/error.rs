use super::model::{ErrorEnvelope, ErrorObject, JobFailure, JobState, ScanSourceKind};
use axum::{
    extract::rejection::JsonRejection,
    http::{Method, StatusCode, Uri},
    response::{IntoResponse, Response},
    Json,
};
use serde_json::{json, Value};
use std::path::Path;

pub(super) type ServiceResult<T> = Result<T, ServiceError>;

pub(super) struct ServiceError {
    status: StatusCode,
    code: &'static str,
    message: String,
    details: Option<Value>,
}

impl IntoResponse for ServiceError {
    fn into_response(self) -> Response {
        let Self { status, code, message, details } = self;
        if status.is_server_error() {
            tracing::error!(status = status.as_u16(), code, message = %message, "service request failed");
        } else {
            tracing::warn!(status = status.as_u16(), code, message = %message, "service request rejected");
        }
        (
            status,
            Json(ErrorEnvelope {
                error: ErrorObject { code, message, status: status.as_u16(), details },
            }),
        )
            .into_response()
    }
}

impl ServiceError {
    fn new(status: StatusCode, code: &'static str, message: impl Into<String>) -> Self {
        Self { status, code, message: message.into(), details: None }
    }

    fn with_details(mut self, details: Value) -> Self {
        self.details = Some(details);
        self
    }

    pub(super) fn route_not_found(uri: &Uri) -> Self {
        Self::new(StatusCode::NOT_FOUND, "route_not_found", "route does not exist")
            .with_details(json!({ "path": uri.path() }))
    }

    pub(super) fn method_not_allowed(method: &Method, uri: &Uri) -> Self {
        Self::new(
            StatusCode::METHOD_NOT_ALLOWED,
            "method_not_allowed",
            "method is not allowed for this route",
        )
        .with_details(json!({
            "method": method.as_str(),
            "path": uri.path(),
        }))
    }

    pub(super) fn unsupported_media_type() -> Self {
        Self::new(
            StatusCode::UNSUPPORTED_MEDIA_TYPE,
            "unsupported_media_type",
            "expected request with `Content-Type: application/json`",
        )
    }

    pub(super) fn invalid_json_syntax(reason: impl Into<String>) -> Self {
        Self::new(
            StatusCode::BAD_REQUEST,
            "invalid_json_syntax",
            "request body contains malformed JSON",
        )
        .with_details(json!({ "reason": reason.into() }))
    }

    pub(super) fn invalid_request_body(reason: impl Into<String>) -> Self {
        Self::new(
            StatusCode::UNPROCESSABLE_ENTITY,
            "invalid_request_body",
            "request JSON does not match the expected schema",
        )
        .with_details(json!({ "reason": reason.into() }))
    }

    pub(super) fn invalid_idempotency_key(reason: impl Into<String>) -> Self {
        Self::new(
            StatusCode::BAD_REQUEST,
            "invalid_idempotency_key",
            "Idempotency-Key header is invalid",
        )
        .with_details(json!({ "reason": reason.into() }))
    }

    pub(super) fn request_body_read_failed(reason: impl Into<String>) -> Self {
        Self::new(
            StatusCode::BAD_REQUEST,
            "request_body_read_failed",
            "failed to read request body",
        )
        .with_details(json!({ "reason": reason.into() }))
    }

    pub(super) fn invalid_multipart(reason: impl Into<String>) -> Self {
        Self::new(StatusCode::BAD_REQUEST, "invalid_multipart", "multipart request is invalid")
            .with_details(json!({ "reason": reason.into() }))
    }

    pub(super) fn request_too_large(max_bytes: u64) -> Self {
        Self::new(
            StatusCode::PAYLOAD_TOO_LARGE,
            "request_too_large",
            "request archive payload exceeds the configured size limit",
        )
        .with_details(json!({ "max_bytes": max_bytes }))
    }

    pub(super) fn archive_path_not_found(source_kind: ScanSourceKind, path: &Path) -> Self {
        Self::new(
            StatusCode::NOT_FOUND,
            "archive_path_not_found",
            "archive source path does not exist",
        )
        .with_details(json!({
            "path": path.display().to_string(),
            "source_kind": source_kind,
        }))
    }

    pub(super) fn archive_path_not_file(source_kind: ScanSourceKind, path: &Path) -> Self {
        Self::new(
            StatusCode::BAD_REQUEST,
            "archive_path_not_file",
            "archive source path is not a regular file",
        )
        .with_details(json!({
            "path": path.display().to_string(),
            "source_kind": source_kind,
        }))
    }

    pub(super) fn invalid_source_reference(
        source_kind: ScanSourceKind,
        reason: impl Into<String>,
    ) -> Self {
        Self::new(
            StatusCode::BAD_REQUEST,
            "invalid_source_reference",
            "archive source reference is invalid",
        )
        .with_details(json!({
            "source_kind": source_kind,
            "reason": reason.into(),
        }))
    }

    pub(super) fn scan_task_failed(reason: impl Into<String>) -> Self {
        Self::new(
            StatusCode::INTERNAL_SERVER_ERROR,
            "scan_task_failed",
            "scan task failed unexpectedly",
        )
        .with_details(json!({ "reason": reason.into() }))
    }

    pub(super) fn service_not_ready(reason: impl Into<String>) -> Self {
        Self::new(
            StatusCode::SERVICE_UNAVAILABLE,
            "service_not_ready",
            "service dependencies are not ready",
        )
        .with_details(json!({ "reason": reason.into() }))
    }

    pub(super) fn job_store_failed(reason: impl Into<String>) -> Self {
        Self::new(
            StatusCode::INTERNAL_SERVER_ERROR,
            "job_store_failed",
            "job store operation failed",
        )
        .with_details(json!({ "reason": reason.into() }))
    }

    pub(super) fn scan_failed(reason: impl Into<String>) -> Self {
        Self::new(StatusCode::INTERNAL_SERVER_ERROR, "scan_failed", "failed to scan archive")
            .with_details(json!({ "reason": reason.into() }))
    }

    pub(super) fn job_not_found(job_id: &str) -> Self {
        Self::new(StatusCode::NOT_FOUND, "job_not_found", "job does not exist")
            .with_details(json!({ "job_id": job_id }))
    }

    pub(super) fn job_failed(job_id: &str, failure: &JobFailure) -> Self {
        Self::new(
            StatusCode::CONFLICT,
            "job_failed",
            "job finished with an error, result is unavailable",
        )
        .with_details(json!({
            "job_id": job_id,
            "failure_code": failure.code,
            "failure_message": failure.message,
        }))
    }

    pub(super) fn job_cancelled(job_id: &str) -> Self {
        Self::new(StatusCode::CONFLICT, "job_cancelled", "job was cancelled, result is unavailable")
            .with_details(json!({ "job_id": job_id }))
    }

    pub(super) fn job_result_unavailable(job_id: &str, reason: impl Into<String>) -> Self {
        Self::new(
            StatusCode::INTERNAL_SERVER_ERROR,
            "job_result_unavailable",
            "job result is unavailable in result storage",
        )
        .with_details(json!({
            "job_id": job_id,
            "reason": reason.into(),
        }))
    }

    pub(super) fn job_not_cancellable(job_id: &str, state: JobState) -> Self {
        Self::new(
            StatusCode::CONFLICT,
            "job_not_cancellable",
            "job is already in a terminal state and cannot be cancelled",
        )
        .with_details(json!({
            "job_id": job_id,
            "state": state,
        }))
    }

    pub(super) fn idempotency_key_conflict(idempotency_key: &str, job_id: &str) -> Self {
        Self::new(
            StatusCode::CONFLICT,
            "idempotency_key_conflict",
            "Idempotency-Key is already bound to a different request payload",
        )
        .with_details(json!({
            "idempotency_key": idempotency_key,
            "job_id": job_id,
        }))
    }

    pub(super) fn from_json_rejection(rejection: JsonRejection) -> Self {
        match rejection {
            JsonRejection::MissingJsonContentType(_) => Self::unsupported_media_type(),
            JsonRejection::JsonSyntaxError(err) => Self::invalid_json_syntax(err.to_string()),
            JsonRejection::JsonDataError(err) => Self::invalid_request_body(err.to_string()),
            JsonRejection::BytesRejection(err) => Self::request_body_read_failed(err.to_string()),
            _ => Self::invalid_request_body(rejection.to_string()),
        }
    }
}
