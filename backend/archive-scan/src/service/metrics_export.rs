use super::{
    config::{JobStoreBackendKind, ResultStoreBackendKind},
    jobs::JobMetricsSnapshot,
};
use serde_json::{json, Value};
use std::time::{SystemTime, UNIX_EPOCH};

pub(super) const PROMETHEUS_CONTENT_TYPE: &str = "text/plain; version=0.0.4; charset=utf-8";

pub(super) fn prometheus_text(snapshot: &JobMetricsSnapshot) -> String {
    let common_labels = [
        ("job_store_backend", job_store_backend_label(snapshot.storage.job_store_backend)),
        ("result_store_backend", result_store_backend_label(snapshot.storage.result_store_backend)),
    ];

    let mut output = String::new();
    append_metric(
        &mut output,
        "archive_scan_job_retention_seconds",
        "Configured retention window for terminal jobs before cleanup.",
        "gauge",
        snapshot.retention_secs,
        &common_labels,
    );
    append_metric(
        &mut output,
        "archive_scan_jobs_visible",
        "Current number of visible jobs tracked by the active job store.",
        "gauge",
        snapshot.visible_jobs,
        &common_labels,
    );
    append_metric(
        &mut output,
        "archive_scan_jobs_active",
        "Current number of queued and running jobs.",
        "gauge",
        snapshot.active_jobs,
        &common_labels,
    );
    append_metric(
        &mut output,
        "archive_scan_jobs_terminal",
        "Current number of terminal jobs.",
        "gauge",
        snapshot.terminal_jobs,
        &common_labels,
    );
    append_metric(
        &mut output,
        "archive_scan_jobs_queued",
        "Current number of queued jobs.",
        "gauge",
        snapshot.queued_jobs,
        &common_labels,
    );
    append_metric(
        &mut output,
        "archive_scan_jobs_running",
        "Current number of running jobs.",
        "gauge",
        snapshot.running_jobs,
        &common_labels,
    );
    append_metric(
        &mut output,
        "archive_scan_jobs_succeeded",
        "Current number of succeeded jobs.",
        "gauge",
        snapshot.succeeded_jobs,
        &common_labels,
    );
    append_metric(
        &mut output,
        "archive_scan_jobs_failed",
        "Current number of failed jobs.",
        "gauge",
        snapshot.failed_jobs,
        &common_labels,
    );
    append_metric(
        &mut output,
        "archive_scan_jobs_cancelled",
        "Current number of cancelled jobs.",
        "gauge",
        snapshot.cancelled_jobs,
        &common_labels,
    );
    append_metric(
        &mut output,
        "archive_scan_jobs_created_total",
        "Total number of jobs accepted by the service.",
        "counter",
        snapshot.created_total,
        &common_labels,
    );
    append_metric(
        &mut output,
        "archive_scan_jobs_started_total",
        "Total number of jobs moved into the running state.",
        "counter",
        snapshot.started_total,
        &common_labels,
    );
    append_metric(
        &mut output,
        "archive_scan_jobs_succeeded_total",
        "Total number of jobs completed successfully.",
        "counter",
        snapshot.succeeded_total,
        &common_labels,
    );
    append_metric(
        &mut output,
        "archive_scan_jobs_failed_total",
        "Total number of jobs completed with a failure.",
        "counter",
        snapshot.failed_total,
        &common_labels,
    );
    append_metric(
        &mut output,
        "archive_scan_jobs_cancelled_total",
        "Total number of jobs cancelled before completion.",
        "counter",
        snapshot.cancelled_total,
        &common_labels,
    );
    append_metric(
        &mut output,
        "archive_scan_jobs_expired_total",
        "Total number of terminal jobs removed by retention cleanup.",
        "counter",
        snapshot.expired_total,
        &common_labels,
    );
    append_metric(
        &mut output,
        "archive_scan_jobs_recovery_runs_total",
        "Total number of startup recovery passes over persisted inflight jobs.",
        "counter",
        snapshot.recovery_runs_total,
        &common_labels,
    );
    append_metric(
        &mut output,
        "archive_scan_jobs_recovered_total",
        "Total number of queued and running jobs rescheduled during startup recovery.",
        "counter",
        snapshot.recovered_jobs_total,
        &common_labels,
    );
    append_metric(
        &mut output,
        "archive_scan_jobs_recovered_running_total",
        "Total number of previously running jobs moved back to queued during startup recovery.",
        "counter",
        snapshot.recovered_running_jobs_total,
        &common_labels,
    );
    append_metric(
        &mut output,
        "archive_scan_jobs_recovery_deleted_result_refs_total",
        "Total number of stale result references deleted while reconciling inflight jobs.",
        "counter",
        snapshot.recovery_deleted_result_refs_total,
        &common_labels,
    );
    append_metric(
        &mut output,
        "archive_scan_jobs_cleanup_deleted_result_refs_total",
        "Total number of result references deleted during terminal job cleanup.",
        "counter",
        snapshot.cleanup_deleted_result_refs_total,
        &common_labels,
    );
    append_metric(
        &mut output,
        "archive_scan_result_artifact_gc_runs_total",
        "Total number of result artifact GC passes.",
        "counter",
        snapshot.result_artifact_gc_runs_total,
        &common_labels,
    );
    append_metric(
        &mut output,
        "archive_scan_result_artifact_gc_deleted_total",
        "Total number of filesystem or object-store result artifacts deleted by GC.",
        "counter",
        snapshot.result_artifact_gc_deleted_total,
        &common_labels,
    );
    append_metric(
        &mut output,
        "archive_scan_result_artifact_gc_failures_total",
        "Total number of result artifact GC failures.",
        "counter",
        snapshot.result_artifact_gc_failures_total,
        &common_labels,
    );
    output
}

pub(super) fn otlp_json(snapshot: &JobMetricsSnapshot) -> Value {
    let timestamp_unix_nano = unix_timestamp_nanos();
    let resource_attributes = vec![
        string_attribute("service.name", "archive_scan_server"),
        string_attribute("service.namespace", "archive_scan"),
        string_attribute("service.version", env!("CARGO_PKG_VERSION")),
        string_attribute(
            "archive_scan.job_store.backend",
            job_store_backend_label(snapshot.storage.job_store_backend),
        ),
        string_attribute(
            "archive_scan.result_store.backend",
            result_store_backend_label(snapshot.storage.result_store_backend),
        ),
    ];
    let metrics = vec![
        otlp_gauge(
            "archive_scan_job_retention_seconds",
            "Configured retention window for terminal jobs before cleanup.",
            "s",
            snapshot.retention_secs,
            &timestamp_unix_nano,
        ),
        otlp_gauge(
            "archive_scan_jobs_visible",
            "Current number of visible jobs tracked by the active job store.",
            "1",
            snapshot.visible_jobs,
            &timestamp_unix_nano,
        ),
        otlp_gauge(
            "archive_scan_jobs_active",
            "Current number of queued and running jobs.",
            "1",
            snapshot.active_jobs,
            &timestamp_unix_nano,
        ),
        otlp_gauge(
            "archive_scan_jobs_terminal",
            "Current number of terminal jobs.",
            "1",
            snapshot.terminal_jobs,
            &timestamp_unix_nano,
        ),
        otlp_gauge(
            "archive_scan_jobs_queued",
            "Current number of queued jobs.",
            "1",
            snapshot.queued_jobs,
            &timestamp_unix_nano,
        ),
        otlp_gauge(
            "archive_scan_jobs_running",
            "Current number of running jobs.",
            "1",
            snapshot.running_jobs,
            &timestamp_unix_nano,
        ),
        otlp_gauge(
            "archive_scan_jobs_succeeded",
            "Current number of succeeded jobs.",
            "1",
            snapshot.succeeded_jobs,
            &timestamp_unix_nano,
        ),
        otlp_gauge(
            "archive_scan_jobs_failed",
            "Current number of failed jobs.",
            "1",
            snapshot.failed_jobs,
            &timestamp_unix_nano,
        ),
        otlp_gauge(
            "archive_scan_jobs_cancelled",
            "Current number of cancelled jobs.",
            "1",
            snapshot.cancelled_jobs,
            &timestamp_unix_nano,
        ),
        otlp_sum(
            "archive_scan_jobs_created_total",
            "Total number of jobs accepted by the service.",
            "1",
            snapshot.created_total,
            &timestamp_unix_nano,
        ),
        otlp_sum(
            "archive_scan_jobs_started_total",
            "Total number of jobs moved into the running state.",
            "1",
            snapshot.started_total,
            &timestamp_unix_nano,
        ),
        otlp_sum(
            "archive_scan_jobs_succeeded_total",
            "Total number of jobs completed successfully.",
            "1",
            snapshot.succeeded_total,
            &timestamp_unix_nano,
        ),
        otlp_sum(
            "archive_scan_jobs_failed_total",
            "Total number of jobs completed with a failure.",
            "1",
            snapshot.failed_total,
            &timestamp_unix_nano,
        ),
        otlp_sum(
            "archive_scan_jobs_cancelled_total",
            "Total number of jobs cancelled before completion.",
            "1",
            snapshot.cancelled_total,
            &timestamp_unix_nano,
        ),
        otlp_sum(
            "archive_scan_jobs_expired_total",
            "Total number of terminal jobs removed by retention cleanup.",
            "1",
            snapshot.expired_total,
            &timestamp_unix_nano,
        ),
        otlp_sum(
            "archive_scan_jobs_recovery_runs_total",
            "Total number of startup recovery passes over persisted inflight jobs.",
            "1",
            snapshot.recovery_runs_total,
            &timestamp_unix_nano,
        ),
        otlp_sum(
            "archive_scan_jobs_recovered_total",
            "Total number of queued and running jobs rescheduled during startup recovery.",
            "1",
            snapshot.recovered_jobs_total,
            &timestamp_unix_nano,
        ),
        otlp_sum(
            "archive_scan_jobs_recovered_running_total",
            "Total number of previously running jobs moved back to queued during startup recovery.",
            "1",
            snapshot.recovered_running_jobs_total,
            &timestamp_unix_nano,
        ),
        otlp_sum(
            "archive_scan_jobs_recovery_deleted_result_refs_total",
            "Total number of stale result references deleted while reconciling inflight jobs.",
            "1",
            snapshot.recovery_deleted_result_refs_total,
            &timestamp_unix_nano,
        ),
        otlp_sum(
            "archive_scan_jobs_cleanup_deleted_result_refs_total",
            "Total number of result references deleted during terminal job cleanup.",
            "1",
            snapshot.cleanup_deleted_result_refs_total,
            &timestamp_unix_nano,
        ),
        otlp_sum(
            "archive_scan_result_artifact_gc_runs_total",
            "Total number of result artifact GC passes.",
            "1",
            snapshot.result_artifact_gc_runs_total,
            &timestamp_unix_nano,
        ),
        otlp_sum(
            "archive_scan_result_artifact_gc_deleted_total",
            "Total number of filesystem or object-store result artifacts deleted by GC.",
            "1",
            snapshot.result_artifact_gc_deleted_total,
            &timestamp_unix_nano,
        ),
        otlp_sum(
            "archive_scan_result_artifact_gc_failures_total",
            "Total number of result artifact GC failures.",
            "1",
            snapshot.result_artifact_gc_failures_total,
            &timestamp_unix_nano,
        ),
    ];

    json!({
        "resourceMetrics": [{
            "resource": {
                "attributes": resource_attributes
            },
            "scopeMetrics": [{
                "scope": {
                    "name": "archive_scan.service.metrics",
                    "version": env!("CARGO_PKG_VERSION")
                },
                "metrics": metrics
            }]
        }]
    })
}

fn append_metric(
    output: &mut String,
    name: &str,
    help: &str,
    metric_type: &str,
    value: u64,
    labels: &[(&str, &str)],
) {
    output.push_str("# HELP ");
    output.push_str(name);
    output.push(' ');
    output.push_str(help);
    output.push('\n');
    output.push_str("# TYPE ");
    output.push_str(name);
    output.push(' ');
    output.push_str(metric_type);
    output.push('\n');
    output.push_str(name);
    if !labels.is_empty() {
        output.push('{');
        for (index, (key, value)) in labels.iter().enumerate() {
            if index > 0 {
                output.push(',');
            }
            output.push_str(key);
            output.push_str("=\"");
            output.push_str(&escape_prometheus_label_value(value));
            output.push('"');
        }
        output.push('}');
    }
    output.push(' ');
    output.push_str(&value.to_string());
    output.push('\n');
}

fn otlp_gauge(
    name: &str,
    description: &str,
    unit: &str,
    value: u64,
    time_unix_nano: &str,
) -> Value {
    json!({
        "name": name,
        "description": description,
        "unit": unit,
        "gauge": {
            "dataPoints": [{
                "asInt": value.to_string(),
                "timeUnixNano": time_unix_nano,
                "attributes": []
            }]
        }
    })
}

fn otlp_sum(name: &str, description: &str, unit: &str, value: u64, time_unix_nano: &str) -> Value {
    json!({
        "name": name,
        "description": description,
        "unit": unit,
        "sum": {
            "aggregationTemporality": 2,
            "isMonotonic": true,
            "dataPoints": [{
                "asInt": value.to_string(),
                "timeUnixNano": time_unix_nano,
                "attributes": []
            }]
        }
    })
}

fn string_attribute(key: &str, value: &str) -> Value {
    json!({
        "key": key,
        "value": {
            "stringValue": value
        }
    })
}

fn escape_prometheus_label_value(value: &str) -> String {
    value.replace('\\', "\\\\").replace('\n', "\\n").replace('"', "\\\"")
}

fn unix_timestamp_nanos() -> String {
    let now = SystemTime::now().duration_since(UNIX_EPOCH).unwrap_or_default();
    now.as_nanos().to_string()
}

fn job_store_backend_label(kind: JobStoreBackendKind) -> &'static str {
    match kind {
        JobStoreBackendKind::InMemory => "in_memory",
        JobStoreBackendKind::Filesystem => "filesystem",
        JobStoreBackendKind::Redis => "redis",
        JobStoreBackendKind::Postgres => "postgres",
    }
}

fn result_store_backend_label(kind: ResultStoreBackendKind) -> &'static str {
    match kind {
        ResultStoreBackendKind::InMemory => "in_memory",
        ResultStoreBackendKind::Filesystem => "filesystem",
        ResultStoreBackendKind::S3 => "s3",
    }
}
