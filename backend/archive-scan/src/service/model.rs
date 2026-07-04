use super::config::RuntimeStorageConfig;
use crate::row::{EntryKind, EntryRow};
use serde::{
    de::{Deserializer, Error as DeError},
    Deserialize, Serialize,
};

pub(crate) const DEFAULT_HEADER_BYTES: usize = 512;
pub(crate) const DEFAULT_BLOCK_SIZE: usize = 2 * 1024 * 1024;

const fn default_header_bytes() -> usize {
    DEFAULT_HEADER_BYTES
}

const fn default_block_size() -> usize {
    DEFAULT_BLOCK_SIZE
}

#[cfg_attr(feature = "service", derive(utoipa::ToSchema))]
#[derive(Clone, Debug, Deserialize, Serialize)]
pub(crate) struct HealthResponse {
    pub(crate) status: &'static str,
    pub(crate) mode: &'static str,
}

#[cfg_attr(feature = "service", derive(utoipa::ToSchema))]
#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
pub(crate) enum ScanSourceKind {
    LocalPath,
    SharedFilesystemPath,
    ObjectStorageUrl,
}

#[cfg_attr(feature = "service", derive(utoipa::ToSchema))]
#[derive(Clone, Debug, Eq, PartialEq, Serialize)]
pub(crate) struct ScanArchiveSource {
    pub(crate) kind: ScanSourceKind,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub(crate) path: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub(crate) url: Option<String>,
}

#[derive(Deserialize)]
struct RawScanArchiveSource {
    kind: ScanSourceKind,
    #[serde(default)]
    path: Option<String>,
    #[serde(default)]
    url: Option<String>,
}

impl<'de> Deserialize<'de> for ScanArchiveSource {
    fn deserialize<D>(deserializer: D) -> Result<Self, D::Error>
    where
        D: Deserializer<'de>,
    {
        let raw = RawScanArchiveSource::deserialize(deserializer)?;

        match raw.kind {
            ScanSourceKind::LocalPath | ScanSourceKind::SharedFilesystemPath => {
                if raw.url.is_some() {
                    return Err(D::Error::custom(
                        "`source.url` is not allowed for filesystem-backed sources",
                    ));
                }

                let Some(path) = raw.path else {
                    return Err(D::Error::custom(
                        "`source.path` is required for filesystem-backed sources",
                    ));
                };

                if path.trim().is_empty() {
                    return Err(D::Error::custom("`source.path` must not be blank"));
                }

                Ok(Self { kind: raw.kind, path: Some(path), url: None })
            }
            ScanSourceKind::ObjectStorageUrl => {
                if raw.path.is_some() {
                    return Err(D::Error::custom(
                        "`source.path` is not allowed for `object_storage_url`",
                    ));
                }

                let Some(url) = raw.url else {
                    return Err(D::Error::custom(
                        "`source.url` is required for `object_storage_url`",
                    ));
                };

                if url.trim().is_empty() {
                    return Err(D::Error::custom("`source.url` must not be blank"));
                }

                Ok(Self { kind: raw.kind, path: None, url: Some(url) })
            }
        }
    }
}

impl ScanArchiveSource {
    fn source_ref(&self) -> ScanSourceRef<'_> {
        ScanSourceRef { kind: self.kind, path: self.path.as_deref(), url: self.url.as_deref() }
    }
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub(crate) struct ScanSourceRef<'a> {
    pub(crate) kind: ScanSourceKind,
    path: Option<&'a str>,
    url: Option<&'a str>,
}

impl<'a> ScanSourceRef<'a> {
    pub(crate) fn path(self) -> Option<&'a str> {
        self.path
    }

    pub(crate) fn url(self) -> Option<&'a str> {
        self.url
    }
}

#[cfg_attr(feature = "service", derive(utoipa::ToSchema))]
#[derive(Clone, Debug, Eq, PartialEq, Serialize)]
pub(crate) struct ScanArchiveRequest {
    #[serde(skip_serializing_if = "Option::is_none")]
    pub(crate) path: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub(crate) source: Option<ScanArchiveSource>,
    #[serde(default = "default_header_bytes")]
    pub(crate) header_bytes: usize,
    #[serde(default = "default_block_size")]
    pub(crate) block_size: usize,
    #[serde(default)]
    pub(crate) full_hash: bool,
    #[serde(default)]
    pub(crate) fast_only: bool,
    #[serde(default)]
    pub(crate) include_entries: bool,
}

#[derive(Deserialize)]
struct RawScanArchiveRequest {
    #[serde(default)]
    path: Option<String>,
    #[serde(default)]
    source: Option<ScanArchiveSource>,
    #[serde(default = "default_header_bytes")]
    header_bytes: usize,
    #[serde(default = "default_block_size")]
    block_size: usize,
    #[serde(default)]
    full_hash: bool,
    #[serde(default)]
    fast_only: bool,
    #[serde(default)]
    include_entries: bool,
}

impl<'de> Deserialize<'de> for ScanArchiveRequest {
    fn deserialize<D>(deserializer: D) -> Result<Self, D::Error>
    where
        D: Deserializer<'de>,
    {
        let raw = RawScanArchiveRequest::deserialize(deserializer)?;

        match (&raw.path, &raw.source) {
            (Some(_), Some(_)) => {
                return Err(D::Error::custom("exactly one of `path` or `source` must be provided"));
            }
            (None, None) => {
                return Err(D::Error::custom("either `path` or `source` must be provided"));
            }
            _ => {}
        }

        if raw.path.as_deref().is_some_and(|path| path.trim().is_empty()) {
            return Err(D::Error::custom("`path` must not be blank"));
        }

        Ok(Self {
            path: raw.path,
            source: raw.source,
            header_bytes: raw.header_bytes,
            block_size: raw.block_size,
            full_hash: raw.full_hash,
            fast_only: raw.fast_only,
            include_entries: raw.include_entries,
        })
    }
}

impl ScanArchiveRequest {
    pub(crate) fn source_ref(&self) -> ScanSourceRef<'_> {
        match (&self.path, &self.source) {
            (Some(path), None) => {
                ScanSourceRef { kind: ScanSourceKind::LocalPath, path: Some(path), url: None }
            }
            (None, Some(source)) => source.source_ref(),
            _ => unreachable!("request deserialization guarantees a single archive source"),
        }
    }
}

#[cfg_attr(feature = "service", derive(utoipa::ToSchema))]
#[derive(Clone, Debug, Deserialize, Serialize)]
pub(crate) struct ScanArchiveResponse {
    pub(crate) archive: ArchiveSummary,
    pub(crate) total_entries: u64,
    pub(crate) total_files: u64,
    pub(crate) total_directories: u64,
    pub(crate) total_other_entries: u64,
    pub(crate) entry_kinds: Vec<EntryKindCount>,
    pub(crate) types: Vec<TypeCount>,
    pub(crate) mimes: Vec<MimeCount>,
    pub(crate) entries: Option<Vec<EntryRow>>,
}

#[cfg_attr(feature = "service", derive(utoipa::ToSchema))]
#[derive(Clone, Debug, Deserialize, Serialize)]
pub(crate) struct ArchiveSummary {
    pub(crate) path: String,
    pub(crate) name: String,
    pub(crate) size_bytes: u64,
    pub(crate) mtime_unix: i64,
}

#[cfg_attr(feature = "service", derive(utoipa::ToSchema))]
#[derive(Clone, Debug, Deserialize, Serialize)]
pub(crate) struct TypeCount {
    #[cfg_attr(feature = "service", schema(value_type = String))]
    pub(crate) label: Box<str>,
    pub(crate) count: u64,
}

#[cfg_attr(feature = "service", derive(utoipa::ToSchema))]
#[derive(Clone, Debug, Deserialize, Serialize)]
pub(crate) struct MimeCount {
    #[cfg_attr(feature = "service", schema(value_type = String))]
    pub(crate) mime: Box<str>,
    pub(crate) count: u64,
}

#[cfg_attr(feature = "service", derive(utoipa::ToSchema))]
#[derive(Clone, Debug, Deserialize, Serialize)]
pub(crate) struct EntryKindCount {
    pub(crate) kind: EntryKind,
    pub(crate) count: u64,
}

#[cfg_attr(feature = "service", derive(utoipa::ToSchema))]
#[derive(Clone, Debug, Serialize)]
pub(crate) struct ErrorEnvelope {
    pub(crate) error: ErrorObject,
}

#[cfg_attr(feature = "service", derive(utoipa::ToSchema))]
#[derive(Clone, Debug, Serialize)]
pub(crate) struct ErrorObject {
    pub(crate) code: &'static str,
    pub(crate) message: String,
    pub(crate) status: u16,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub(crate) details: Option<serde_json::Value>,
}

#[cfg_attr(feature = "service", derive(utoipa::ToSchema))]
#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
pub(crate) enum JobState {
    Queued,
    Running,
    Cancelled,
    Succeeded,
    Failed,
}

impl JobState {
    pub(crate) const fn is_terminal(self) -> bool {
        matches!(self, Self::Cancelled | Self::Succeeded | Self::Failed)
    }
}

#[cfg_attr(feature = "service", derive(utoipa::ToSchema))]
#[derive(Clone, Debug, Deserialize, Serialize)]
pub(crate) struct JobFailure {
    #[cfg_attr(feature = "service", schema(value_type = String))]
    pub(crate) code: Box<str>,
    pub(crate) message: String,
}

impl JobFailure {
    pub(crate) fn new(code: &'static str, message: impl Into<String>) -> Self {
        Self { code: code.into(), message: message.into() }
    }
}

#[cfg_attr(feature = "service", derive(utoipa::ToSchema))]
#[derive(Clone, Debug, Serialize)]
pub(crate) struct CreateJobResponse {
    pub(crate) job_id: String,
    pub(crate) state: JobState,
    pub(crate) status_url: String,
    pub(crate) result_url: String,
}

#[cfg_attr(feature = "service", derive(utoipa::ToSchema))]
#[derive(Clone, Debug, Serialize)]
pub(crate) struct JobStatusResponse {
    pub(crate) job_id: String,
    pub(crate) state: JobState,
    pub(crate) created_at_unix: i64,
    pub(crate) updated_at_unix: i64,
    pub(crate) status_url: String,
    pub(crate) result_url: String,
    pub(crate) request: ScanArchiveRequest,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub(crate) error: Option<JobFailure>,
}

#[cfg_attr(feature = "service", derive(utoipa::ToSchema))]
#[derive(Clone, Debug, Serialize)]
pub(crate) struct JobResultPendingResponse {
    pub(crate) job_id: String,
    pub(crate) state: JobState,
    pub(crate) message: &'static str,
    pub(crate) status_url: String,
}

#[cfg_attr(feature = "service", derive(utoipa::ToSchema))]
#[derive(Clone, Debug, Serialize)]
pub(crate) struct JobMetricsResponse {
    pub(crate) retention: JobRetentionPolicy,
    pub(crate) storage: RuntimeStorageConfig,
    pub(crate) current: JobStateCounts,
    pub(crate) lifecycle: JobLifecycleTotals,
    pub(crate) maintenance: JobMaintenanceTotals,
}

#[cfg_attr(feature = "service", derive(utoipa::ToSchema))]
#[derive(Clone, Debug, Serialize)]
pub(crate) struct JobRetentionPolicy {
    pub(crate) terminal_job_retention_secs: u64,
}

#[cfg_attr(feature = "service", derive(utoipa::ToSchema))]
#[derive(Clone, Debug, Serialize)]
pub(crate) struct JobStateCounts {
    pub(crate) visible_jobs: u64,
    pub(crate) active_jobs: u64,
    pub(crate) terminal_jobs: u64,
    pub(crate) queued_jobs: u64,
    pub(crate) running_jobs: u64,
    pub(crate) succeeded_jobs: u64,
    pub(crate) failed_jobs: u64,
    pub(crate) cancelled_jobs: u64,
}

#[cfg_attr(feature = "service", derive(utoipa::ToSchema))]
#[derive(Clone, Debug, Serialize)]
pub(crate) struct JobLifecycleTotals {
    pub(crate) created_total: u64,
    pub(crate) started_total: u64,
    pub(crate) succeeded_total: u64,
    pub(crate) failed_total: u64,
    pub(crate) cancelled_total: u64,
    pub(crate) expired_total: u64,
}

#[cfg_attr(feature = "service", derive(utoipa::ToSchema))]
#[derive(Clone, Debug, Serialize)]
pub(crate) struct JobMaintenanceTotals {
    pub(crate) recovery_runs_total: u64,
    pub(crate) recovered_jobs_total: u64,
    pub(crate) recovered_running_jobs_total: u64,
    pub(crate) recovery_deleted_result_refs_total: u64,
    pub(crate) cleanup_deleted_result_refs_total: u64,
    pub(crate) result_artifact_gc_runs_total: u64,
    pub(crate) result_artifact_gc_deleted_total: u64,
    pub(crate) result_artifact_gc_failures_total: u64,
}

impl CreateJobResponse {
    pub(crate) fn new(
        job_id: &str,
        state: JobState,
        status_url: String,
        result_url: String,
    ) -> Self {
        Self { job_id: job_id.to_owned(), state, status_url, result_url }
    }
}

impl JobResultPendingResponse {
    pub(crate) fn new(
        job_id: &str,
        state: JobState,
        status_url: String,
        message: &'static str,
    ) -> Self {
        Self { job_id: job_id.to_owned(), state, message, status_url }
    }
}
