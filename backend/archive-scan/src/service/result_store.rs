#[path = "result_store/filesystem.rs"]
mod filesystem;
#[path = "result_store/s3.rs"]
mod s3;
#[cfg(test)]
#[path = "result_store/tests.rs"]
mod tests;

use self::{filesystem::FilesystemResultStore, s3::S3ResultStore};
use super::{
    config::{ResultStoreBackendKind, ResultStoreConfig},
    model::ScanArchiveResponse,
};
use anyhow::{Context, Result};
use serde::{Deserialize, Serialize};
use std::{
    fs,
    path::Path,
    sync::atomic::{AtomicU64, Ordering},
    time::{Duration, SystemTime},
};

#[derive(Clone)]
pub(super) enum ResultStore {
    InMemory(InMemoryResultStore),
    Filesystem(FilesystemResultStore),
    S3(S3ResultStore),
}

#[derive(Clone, Debug)]
pub(super) struct PersistedResult {
    pub(super) inline_result: Option<ScanArchiveResponse>,
    pub(super) reference: Option<JobResultReference>,
}

#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
pub(super) enum JobResultReferenceKind {
    Filesystem,
    S3,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub(super) struct JobResultReference {
    pub(super) kind: JobResultReferenceKind,
    pub(super) location: String,
    pub(super) size_bytes: usize,
    #[serde(default)]
    pub(super) created_at_unix: i64,
}

#[derive(Clone)]
pub(in crate::service) struct InMemoryResultStore {
    _inline_max_bytes: usize,
}

#[derive(Clone, Copy, Debug)]
struct ResultArtifactGcPolicy {
    retention: Duration,
    interval: Duration,
}

#[derive(Clone, Copy, Debug, Default)]
pub(super) struct ResultStoreMaintenanceStats {
    pub(super) gc_runs_total: u64,
    pub(super) gc_deleted_artifacts_total: u64,
    pub(super) gc_failures_total: u64,
}

#[derive(Debug, Default)]
pub(super) struct ResultStoreGcMetrics {
    runs_total: AtomicU64,
    deleted_artifacts_total: AtomicU64,
    failures_total: AtomicU64,
}

impl ResultStore {
    pub(super) fn new(config: &ResultStoreConfig) -> Self {
        let gc = ResultArtifactGcPolicy {
            retention: config.artifact_retention,
            interval: config.gc_interval,
        };
        match config.backend {
            ResultStoreBackendKind::InMemory => {
                Self::InMemory(InMemoryResultStore { _inline_max_bytes: config.inline_max_bytes })
            }
            ResultStoreBackendKind::Filesystem => {
                Self::Filesystem(FilesystemResultStore::new(config, gc))
            }
            ResultStoreBackendKind::S3 => Self::S3(S3ResultStore::new(config, gc)),
        }
    }

    pub(super) fn persist(
        &self,
        job_id: &str,
        result: &ScanArchiveResponse,
    ) -> Result<PersistedResult> {
        let persisted = match self {
            Self::InMemory(store) => store.persist(result)?,
            Self::Filesystem(store) => store.persist(job_id, result)?,
            Self::S3(store) => store.persist(job_id, result)?,
        };
        let _ = self.maybe_run_gc();
        Ok(persisted)
    }

    pub(super) fn load(
        &self,
        reference: &JobResultReference,
    ) -> Result<Option<ScanArchiveResponse>> {
        let result = match self {
            Self::InMemory(_) => None,
            Self::Filesystem(store) => store.load(reference)?,
            Self::S3(store) => store.load(reference)?,
        };
        let _ = self.maybe_run_gc();
        Ok(result)
    }

    pub(super) fn delete(&self, reference: &JobResultReference) -> Result<()> {
        match self {
            Self::InMemory(_) => Ok(()),
            Self::Filesystem(store) => store.delete(reference),
            Self::S3(store) => store.delete(reference),
        }?;
        let _ = self.maybe_run_gc();
        Ok(())
    }

    pub(super) fn readiness_check(&self) -> Result<()> {
        match self {
            Self::InMemory(_) => Ok(()),
            Self::Filesystem(store) => store.readiness_check(),
            Self::S3(store) => store.readiness_check(),
        }
    }

    #[cfg(test)]
    pub(super) fn run_gc_with_now(&self, now_unix: i64) -> Result<()> {
        match self {
            Self::InMemory(_) => Ok(()),
            Self::Filesystem(store) => store.run_gc(now_unix).map(|_| ()),
            Self::S3(store) => store.run_gc(now_unix).map(|_| ()),
        }
    }

    fn maybe_run_gc(&self) -> Result<()> {
        let now_unix = unix_now();
        match self {
            Self::InMemory(_) => Ok(()),
            Self::Filesystem(store) => store.maybe_run_gc(now_unix).map(|_| ()),
            Self::S3(store) => store.maybe_run_gc(now_unix).map(|_| ()),
        }
    }

    pub(super) fn maintenance_stats(&self) -> ResultStoreMaintenanceStats {
        match self {
            Self::InMemory(_) => ResultStoreMaintenanceStats::default(),
            Self::Filesystem(store) => store.maintenance_stats(),
            Self::S3(store) => store.maintenance_stats(),
        }
    }
}

impl InMemoryResultStore {
    fn persist(&self, result: &ScanArchiveResponse) -> Result<PersistedResult> {
        let _serialized = serde_json::to_vec(result).context("failed to serialize job result")?;
        Ok(PersistedResult { inline_result: Some(result.clone()), reference: None })
    }
}

fn duration_to_i64_secs(value: Duration) -> i64 {
    i64::try_from(value.as_secs()).unwrap_or(i64::MAX)
}

fn should_run_gc(last_gc_unix: &AtomicU64, interval: Duration, now_unix: i64) -> bool {
    let now_unix = u64::try_from(now_unix.max(0)).unwrap_or(u64::MAX);
    let interval = interval.as_secs();
    let previous = last_gc_unix.load(Ordering::Relaxed);
    if interval > 0 && now_unix.saturating_sub(previous) < interval {
        return false;
    }
    last_gc_unix.compare_exchange(previous, now_unix, Ordering::SeqCst, Ordering::Relaxed).is_ok()
}

fn ensure_reference_kind(
    reference: &JobResultReference,
    expected: JobResultReferenceKind,
    backend_name: &str,
) -> Result<()> {
    if reference.kind == expected {
        return Ok(());
    }
    Err(anyhow::anyhow!(
        "result reference kind {:?} does not match active {backend_name} result store",
        reference.kind
    ))
}

impl ResultStoreGcMetrics {
    pub(super) fn record_run(&self, deleted_artifacts: usize) {
        self.runs_total.fetch_add(1, Ordering::Relaxed);
        self.deleted_artifacts_total
            .fetch_add(u64::try_from(deleted_artifacts).unwrap_or(u64::MAX), Ordering::Relaxed);
    }

    pub(super) fn record_failure(&self) {
        self.failures_total.fetch_add(1, Ordering::Relaxed);
    }

    pub(super) fn snapshot(&self) -> ResultStoreMaintenanceStats {
        ResultStoreMaintenanceStats {
            gc_runs_total: self.runs_total.load(Ordering::Relaxed),
            gc_deleted_artifacts_total: self.deleted_artifacts_total.load(Ordering::Relaxed),
            gc_failures_total: self.failures_total.load(Ordering::Relaxed),
        }
    }
}

fn remove_file_if_exists(path: &Path) -> Result<()> {
    match fs::remove_file(path) {
        Ok(()) => Ok(()),
        Err(err) if err.kind() == std::io::ErrorKind::NotFound => Ok(()),
        Err(err) => Err(err).with_context(|| format!("failed to delete {}", path.display())),
    }
}

fn is_json_file(path: &Path) -> bool {
    path.extension()
        .and_then(|ext| ext.to_str())
        .is_some_and(|ext| ext.eq_ignore_ascii_case("json"))
}

fn artifact_file_name(job_id: &str, created_at_unix: i64) -> String {
    format!("result-{job_id}-{created_at_unix}.json")
}

fn artifact_is_expired(location: Option<&str>, cutoff_unix: i64) -> bool {
    artifact_created_at_unix(location).is_some_and(|created_at| created_at < cutoff_unix)
}

fn artifact_created_at_unix(location: Option<&str>) -> Option<i64> {
    let location = location?;
    let name = location
        .rsplit(['/', '\\'])
        .next()
        .filter(|name| name.starts_with("result-") && name.ends_with(".json"))?;
    let stem = name.strip_suffix(".json")?;
    let (_, timestamp) = stem.rsplit_once('-')?;
    timestamp.parse::<i64>().ok()
}

fn unix_now() -> i64 {
    system_time_to_unix(SystemTime::now())
}

fn system_time_to_unix(value: SystemTime) -> i64 {
    value
        .duration_since(SystemTime::UNIX_EPOCH)
        .map_or(0, |duration| i64::try_from(duration.as_secs()).unwrap_or(i64::MAX))
}
