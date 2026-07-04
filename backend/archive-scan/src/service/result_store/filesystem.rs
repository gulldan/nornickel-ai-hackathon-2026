use super::{
    artifact_file_name, artifact_is_expired, duration_to_i64_secs, ensure_reference_kind,
    is_json_file, remove_file_if_exists, should_run_gc, system_time_to_unix, unix_now,
    JobResultReference, JobResultReferenceKind, PersistedResult, ResultArtifactGcPolicy,
    ResultStoreGcMetrics, ResultStoreMaintenanceStats, ScanArchiveResponse,
};
use anyhow::{anyhow, Context, Result};
use std::{
    fs,
    io::Write,
    path::{Path, PathBuf},
    sync::{atomic::AtomicU64, Arc},
    time::SystemTime,
};
use tempfile::NamedTempFile;

#[derive(Clone, Debug)]
pub(in crate::service) struct FilesystemResultStore {
    dir: PathBuf,
    inline_max_bytes: usize,
    gc: ResultArtifactGcPolicy,
    last_gc_unix: Arc<AtomicU64>,
    gc_metrics: Arc<ResultStoreGcMetrics>,
}

impl FilesystemResultStore {
    pub(super) fn new(
        config: &super::super::config::ResultStoreConfig,
        gc: ResultArtifactGcPolicy,
    ) -> Self {
        Self {
            dir: config.filesystem_dir.clone(),
            inline_max_bytes: config.inline_max_bytes,
            gc,
            last_gc_unix: Arc::new(AtomicU64::new(0)),
            gc_metrics: Arc::new(ResultStoreGcMetrics::default()),
        }
    }

    pub(super) fn persist(
        &self,
        job_id: &str,
        result: &ScanArchiveResponse,
    ) -> Result<PersistedResult> {
        let payload = serde_json::to_vec(result).context("failed to serialize job result")?;
        if payload.len() <= self.inline_max_bytes {
            return Ok(PersistedResult { inline_result: Some(result.clone()), reference: None });
        }

        fs::create_dir_all(&self.dir).with_context(|| {
            format!("failed to create result store directory {}", self.dir.display())
        })?;
        let created_at_unix = unix_now();
        let path = self.dir.join(artifact_file_name(job_id, created_at_unix));
        let mut staging_file = NamedTempFile::new_in(&self.dir).with_context(|| {
            format!("failed to allocate temp result file in {}", self.dir.display())
        })?;
        staging_file
            .as_file_mut()
            .write_all(&payload)
            .with_context(|| format!("failed to write result payload for {job_id}"))?;
        staging_file
            .as_file_mut()
            .flush()
            .with_context(|| format!("failed to flush result payload for {job_id}"))?;
        staging_file.persist(&path).map_err(|err| {
            anyhow!("failed to persist result payload {}: {}", path.display(), err)
        })?;

        Ok(PersistedResult {
            inline_result: None,
            reference: Some(JobResultReference {
                kind: JobResultReferenceKind::Filesystem,
                location: path.display().to_string(),
                size_bytes: payload.len(),
                created_at_unix,
            }),
        })
    }

    pub(super) fn load(
        &self,
        reference: &JobResultReference,
    ) -> Result<Option<ScanArchiveResponse>> {
        ensure_reference_kind(reference, JobResultReferenceKind::Filesystem, "filesystem")?;
        let path = Path::new(&reference.location);
        if !path.exists() {
            return Ok(None);
        }
        let bytes = fs::read(path)
            .with_context(|| format!("failed to read result payload {}", path.display()))?;
        let result = serde_json::from_slice(&bytes)
            .with_context(|| format!("failed to deserialize result payload {}", path.display()))?;
        Ok(Some(result))
    }

    pub(super) fn delete(&self, reference: &JobResultReference) -> Result<()> {
        ensure_reference_kind(reference, JobResultReferenceKind::Filesystem, "filesystem")?;
        let path = Path::new(&reference.location);
        match fs::remove_file(path) {
            Ok(()) => Ok(()),
            Err(err) if err.kind() == std::io::ErrorKind::NotFound => Ok(()),
            Err(err) => Err(err)
                .with_context(|| format!("failed to delete result payload {}", path.display())),
        }
    }

    pub(super) fn readiness_check(&self) -> Result<()> {
        fs::create_dir_all(&self.dir).with_context(|| {
            format!("failed to create result store directory {}", self.dir.display())
        })?;
        let _ = NamedTempFile::new_in(&self.dir)
            .with_context(|| format!("failed to allocate temp file in {}", self.dir.display()))?;
        Ok(())
    }

    pub(super) fn maybe_run_gc(&self, now_unix: i64) -> Result<usize> {
        if !should_run_gc(&self.last_gc_unix, self.gc.interval, now_unix) {
            return Ok(0);
        }
        self.run_gc(now_unix)
    }

    pub(super) fn run_gc(&self, now_unix: i64) -> Result<usize> {
        match self.run_gc_inner(now_unix) {
            Ok(deleted) => {
                self.gc_metrics.record_run(deleted);
                Ok(deleted)
            }
            Err(err) => {
                self.gc_metrics.record_failure();
                Err(err)
            }
        }
    }

    pub(super) fn maintenance_stats(&self) -> ResultStoreMaintenanceStats {
        self.gc_metrics.snapshot()
    }

    fn run_gc_inner(&self, now_unix: i64) -> Result<usize> {
        if !self.dir.exists() {
            return Ok(0);
        }

        let cutoff = now_unix.saturating_sub(duration_to_i64_secs(self.gc.retention));
        let mut deleted = 0_usize;
        for entry in fs::read_dir(&self.dir).with_context(|| {
            format!("failed to read result store directory {}", self.dir.display())
        })? {
            let entry = entry.with_context(|| {
                format!("failed to iterate result store directory {}", self.dir.display())
            })?;
            let path = entry.path();
            if !path.is_file() {
                continue;
            }

            if artifact_is_expired(path.file_name().and_then(|name| name.to_str()), cutoff) {
                remove_file_if_exists(&path)?;
                deleted = deleted.saturating_add(1);
                continue;
            }

            if !is_json_file(&path) {
                continue;
            }

            let modified = path
                .metadata()
                .and_then(|metadata| metadata.modified())
                .unwrap_or(SystemTime::UNIX_EPOCH);
            let modified_at = system_time_to_unix(modified);
            if modified_at < cutoff {
                remove_file_if_exists(&path)?;
                deleted = deleted.saturating_add(1);
            }
        }
        Ok(deleted)
    }
}
