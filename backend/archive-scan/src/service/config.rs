use clap::ValueEnum;
use serde::{Deserialize, Serialize};
use std::{path::PathBuf, time::Duration};
use url::{form_urlencoded, Url};

pub const DEFAULT_OBJECT_SOURCE_MAX_BYTES: u64 = 1024 * 1024 * 1024;

#[cfg_attr(feature = "service", derive(utoipa::ToSchema))]
#[derive(Clone, Copy, Debug, Default, Deserialize, Eq, PartialEq, Serialize, ValueEnum)]
#[serde(rename_all = "snake_case")]
#[value(rename_all = "snake_case")]
pub enum JobStoreBackendKind {
    #[default]
    InMemory,
    Filesystem,
    Redis,
    Postgres,
}

#[cfg_attr(feature = "service", derive(utoipa::ToSchema))]
#[derive(Clone, Copy, Debug, Default, Deserialize, Eq, PartialEq, Serialize, ValueEnum)]
#[serde(rename_all = "snake_case")]
#[value(rename_all = "snake_case")]
pub enum ResultStoreBackendKind {
    #[default]
    InMemory,
    Filesystem,
    S3,
}

#[cfg_attr(feature = "service", derive(utoipa::ToSchema))]
#[derive(Clone, Copy, Debug, Default, Deserialize, Eq, PartialEq, Serialize, ValueEnum)]
#[serde(rename_all = "snake_case")]
#[value(rename_all = "snake_case")]
pub enum ExtractStoreBackendKind {
    #[default]
    Filesystem,
    S3,
}

#[cfg_attr(feature = "service", derive(utoipa::ToSchema))]
#[derive(Clone, Copy, Debug, Default, Deserialize, Eq, PartialEq, Serialize, ValueEnum)]
#[serde(rename_all = "snake_case")]
#[value(rename_all = "snake_case")]
pub enum ExtractMetadataBackendKind {
    None,
    #[default]
    Filesystem,
    Postgres,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct JobStoreRuntimeConfig {
    pub backend: JobStoreBackendKind,
    pub filesystem_path: PathBuf,
    pub redis_url: Option<String>,
    pub redis_key_prefix: String,
    pub redis_max_connections: u32,
    pub redis_cleanup_batch_size: usize,
    pub postgres_url: Option<String>,
    pub postgres_table_prefix: String,
    pub postgres_max_connections: u32,
    pub lock_timeout: Duration,
    pub lock_retry_interval: Duration,
}

impl Default for JobStoreRuntimeConfig {
    fn default() -> Self {
        Self {
            backend: JobStoreBackendKind::Filesystem,
            filesystem_path: std::env::temp_dir().join("archive-scan/jobs/state.json"),
            redis_url: None,
            redis_key_prefix: "archive_scan".to_owned(),
            redis_max_connections: 16,
            redis_cleanup_batch_size: 128,
            postgres_url: None,
            postgres_table_prefix: "archive_scan".to_owned(),
            postgres_max_connections: 16,
            lock_timeout: Duration::from_secs(5),
            lock_retry_interval: Duration::from_millis(25),
        }
    }
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct ResultStoreConfig {
    pub backend: ResultStoreBackendKind,
    pub filesystem_dir: PathBuf,
    pub s3_endpoint: Option<String>,
    pub s3_region: String,
    pub s3_bucket: Option<String>,
    pub s3_key_prefix: String,
    pub s3_access_key_id: Option<String>,
    pub s3_secret_access_key: Option<String>,
    pub s3_session_token: Option<String>,
    pub s3_path_style: bool,
    pub inline_max_bytes: usize,
    pub artifact_retention: Duration,
    pub gc_interval: Duration,
}

impl Default for ResultStoreConfig {
    fn default() -> Self {
        Self {
            backend: ResultStoreBackendKind::Filesystem,
            filesystem_dir: std::env::temp_dir().join("archive-scan/results"),
            s3_endpoint: None,
            s3_region: "us-east-1".to_owned(),
            s3_bucket: None,
            s3_key_prefix: "archive-scan/results".to_owned(),
            s3_access_key_id: None,
            s3_secret_access_key: None,
            s3_session_token: None,
            s3_path_style: false,
            inline_max_bytes: 256 * 1024,
            artifact_retention: Duration::from_secs(60 * 60),
            gc_interval: Duration::from_secs(5 * 60),
        }
    }
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct ExtractStoreConfig {
    pub backend: ExtractStoreBackendKind,
    pub filesystem_dir: PathBuf,
    pub s3_endpoint: Option<String>,
    pub s3_region: String,
    pub s3_bucket: Option<String>,
    pub s3_key_prefix: String,
    pub s3_access_key_id: Option<String>,
    pub s3_secret_access_key: Option<String>,
    pub s3_session_token: Option<String>,
    pub s3_path_style: bool,
    pub temp_dir: Option<PathBuf>,
}

impl Default for ExtractStoreConfig {
    fn default() -> Self {
        Self {
            backend: ExtractStoreBackendKind::Filesystem,
            filesystem_dir: std::env::temp_dir().join("archive-scan/extracted"),
            s3_endpoint: None,
            s3_region: "us-east-1".to_owned(),
            s3_bucket: None,
            s3_key_prefix: "archive-scan/extracted".to_owned(),
            s3_access_key_id: None,
            s3_secret_access_key: None,
            s3_session_token: None,
            s3_path_style: false,
            temp_dir: None,
        }
    }
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct ExtractMetadataConfig {
    pub backend: ExtractMetadataBackendKind,
    pub filesystem_dir: PathBuf,
    pub postgres_url: Option<String>,
    pub postgres_table_prefix: String,
    pub postgres_max_connections: u32,
    pub batch_size: usize,
}

impl Default for ExtractMetadataConfig {
    fn default() -> Self {
        Self {
            backend: ExtractMetadataBackendKind::Filesystem,
            filesystem_dir: std::env::temp_dir().join("archive-scan/extract-metadata"),
            postgres_url: None,
            postgres_table_prefix: "archive_scan".to_owned(),
            postgres_max_connections: 16,
            batch_size: 500,
        }
    }
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct SourceDownloadConfig {
    pub connect_timeout: Duration,
    pub request_timeout: Duration,
    pub max_bytes: Option<u64>,
    pub temp_dir: Option<PathBuf>,
    pub allow_private_networks: bool,
}

impl Default for SourceDownloadConfig {
    fn default() -> Self {
        Self {
            connect_timeout: Duration::from_secs(10),
            request_timeout: Duration::from_secs(300),
            max_bytes: Some(DEFAULT_OBJECT_SOURCE_MAX_BYTES),
            temp_dir: None,
            allow_private_networks: false,
        }
    }
}

#[cfg_attr(feature = "service", derive(utoipa::ToSchema))]
#[derive(Clone, Debug, Default, Deserialize, Eq, PartialEq, Serialize)]
pub struct RuntimeStorageConfig {
    pub job_store_backend: JobStoreBackendKind,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub job_store_filesystem_path: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub job_store_redis_url: Option<String>,
    pub job_store_redis_key_prefix: String,
    pub job_store_redis_max_connections: u32,
    pub job_store_redis_cleanup_batch_size: usize,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub job_store_postgres_url: Option<String>,
    pub job_store_postgres_table_prefix: String,
    pub job_store_postgres_max_connections: u32,
    pub job_store_lock_timeout_secs: u64,
    pub job_store_lock_retry_millis: u64,
    pub result_store_backend: ResultStoreBackendKind,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub result_store_filesystem_dir: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub result_store_s3_endpoint: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub result_store_s3_bucket: Option<String>,
    pub result_store_s3_region: String,
    pub result_store_s3_key_prefix: String,
    pub result_store_s3_path_style: bool,
    pub result_inline_max_bytes: usize,
    pub result_artifact_retention_secs: u64,
    pub result_artifact_gc_interval_secs: u64,
}

#[cfg_attr(feature = "service", derive(utoipa::ToSchema))]
#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub struct RuntimeSourceConfig {
    pub connect_timeout_secs: u64,
    pub request_timeout_secs: u64,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub max_bytes: Option<u64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub temp_dir: Option<String>,
    pub allow_private_networks: bool,
}

#[cfg_attr(feature = "service", derive(utoipa::ToSchema))]
#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub struct RuntimeExtractConfig {
    pub extract_store_backend: ExtractStoreBackendKind,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub extract_store_filesystem_dir: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub extract_store_s3_endpoint: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub extract_store_s3_bucket: Option<String>,
    pub extract_store_s3_region: String,
    pub extract_store_s3_key_prefix: String,
    pub extract_store_s3_path_style: bool,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub extract_store_temp_dir: Option<String>,
    pub extract_metadata_backend: ExtractMetadataBackendKind,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub extract_metadata_filesystem_dir: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub extract_metadata_postgres_url: Option<String>,
    pub extract_metadata_postgres_table_prefix: String,
    pub extract_metadata_postgres_max_connections: u32,
    pub extract_metadata_batch_size: usize,
}

impl JobStoreRuntimeConfig {
    #[must_use]
    pub fn describe(&self) -> RuntimeStorageConfig {
        RuntimeStorageConfig {
            job_store_backend: self.backend,
            job_store_filesystem_path: (self.backend == JobStoreBackendKind::Filesystem)
                .then(|| self.filesystem_path.display().to_string()),
            job_store_redis_url: (self.backend == JobStoreBackendKind::Redis)
                .then(|| self.redis_url.as_deref().map(redact_runtime_connection_string))
                .flatten(),
            job_store_redis_key_prefix: self.redis_key_prefix.clone(),
            job_store_redis_max_connections: self.redis_max_connections,
            job_store_redis_cleanup_batch_size: self.redis_cleanup_batch_size,
            job_store_postgres_url: (self.backend == JobStoreBackendKind::Postgres)
                .then(|| self.postgres_url.as_deref().map(redact_runtime_connection_string))
                .flatten(),
            job_store_postgres_table_prefix: self.postgres_table_prefix.clone(),
            job_store_postgres_max_connections: self.postgres_max_connections,
            job_store_lock_timeout_secs: self.lock_timeout.as_secs(),
            job_store_lock_retry_millis: u64::try_from(self.lock_retry_interval.as_millis())
                .unwrap_or(u64::MAX),
            result_store_backend: ResultStoreBackendKind::InMemory,
            result_store_filesystem_dir: None,
            result_store_s3_endpoint: None,
            result_store_s3_bucket: None,
            result_store_s3_region: String::new(),
            result_store_s3_key_prefix: String::new(),
            result_store_s3_path_style: false,
            result_inline_max_bytes: 0,
            result_artifact_retention_secs: 0,
            result_artifact_gc_interval_secs: 0,
        }
    }
}

impl ResultStoreConfig {
    pub fn apply_to_runtime(&self, runtime: &mut RuntimeStorageConfig) {
        runtime.result_store_backend = self.backend;
        runtime.result_store_filesystem_dir = (self.backend == ResultStoreBackendKind::Filesystem)
            .then(|| self.filesystem_dir.display().to_string());
        runtime.result_store_s3_endpoint = (self.backend == ResultStoreBackendKind::S3)
            .then(|| self.s3_endpoint.clone())
            .flatten();
        runtime.result_store_s3_bucket =
            (self.backend == ResultStoreBackendKind::S3).then(|| self.s3_bucket.clone()).flatten();
        runtime.result_store_s3_region = self.s3_region.clone();
        runtime.result_store_s3_key_prefix = self.s3_key_prefix.clone();
        runtime.result_store_s3_path_style = self.s3_path_style;
        runtime.result_inline_max_bytes = self.inline_max_bytes;
        runtime.result_artifact_retention_secs = self.artifact_retention.as_secs();
        runtime.result_artifact_gc_interval_secs = self.gc_interval.as_secs();
    }
}

impl SourceDownloadConfig {
    #[must_use]
    pub fn describe(&self) -> RuntimeSourceConfig {
        RuntimeSourceConfig {
            connect_timeout_secs: self.connect_timeout.as_secs(),
            request_timeout_secs: self.request_timeout.as_secs(),
            max_bytes: self.max_bytes,
            temp_dir: self.temp_dir.as_ref().map(|path| path.display().to_string()),
            allow_private_networks: self.allow_private_networks,
        }
    }
}

impl ExtractStoreConfig {
    #[must_use]
    pub fn describe(&self, metadata: &ExtractMetadataConfig) -> RuntimeExtractConfig {
        RuntimeExtractConfig {
            extract_store_backend: self.backend,
            extract_store_filesystem_dir: (self.backend == ExtractStoreBackendKind::Filesystem)
                .then(|| self.filesystem_dir.display().to_string()),
            extract_store_s3_endpoint: (self.backend == ExtractStoreBackendKind::S3)
                .then(|| self.s3_endpoint.clone())
                .flatten(),
            extract_store_s3_bucket: (self.backend == ExtractStoreBackendKind::S3)
                .then(|| self.s3_bucket.clone())
                .flatten(),
            extract_store_s3_region: self.s3_region.clone(),
            extract_store_s3_key_prefix: self.s3_key_prefix.clone(),
            extract_store_s3_path_style: self.s3_path_style,
            extract_store_temp_dir: self.temp_dir.as_ref().map(|path| path.display().to_string()),
            extract_metadata_backend: metadata.backend,
            extract_metadata_filesystem_dir: (metadata.backend
                == ExtractMetadataBackendKind::Filesystem)
                .then(|| metadata.filesystem_dir.display().to_string()),
            extract_metadata_postgres_url: (metadata.backend
                == ExtractMetadataBackendKind::Postgres)
                .then(|| metadata.postgres_url.as_deref().map(redact_runtime_connection_string))
                .flatten(),
            extract_metadata_postgres_table_prefix: metadata.postgres_table_prefix.clone(),
            extract_metadata_postgres_max_connections: metadata.postgres_max_connections,
            extract_metadata_batch_size: metadata.batch_size,
        }
    }
}

fn redact_runtime_connection_string(value: &str) -> String {
    let Ok(mut url) = Url::parse(value) else {
        return "<redacted>".to_owned();
    };

    let mut redacted = false;
    if !url.username().is_empty() || url.password().is_some() {
        redacted = true;
        let _ = url.set_username("redacted");
        let _ = url.set_password(Some("redacted"));
    }

    if let Some(query) = url.query() {
        let mut serializer = form_urlencoded::Serializer::new(String::new());
        let mut query_redacted = false;
        for (key, value) in form_urlencoded::parse(query.as_bytes()) {
            if query_value_is_sensitive(&key) {
                serializer.append_pair(&key, "redacted");
                query_redacted = true;
            } else {
                serializer.append_pair(&key, &value);
            }
        }
        if query_redacted {
            url.set_query(Some(&serializer.finish()));
            redacted = true;
        }
    }

    if redacted {
        url.to_string()
    } else {
        value.to_owned()
    }
}

fn query_value_is_sensitive(key: &str) -> bool {
    let key = key.to_ascii_lowercase();
    key.contains("password") || key.contains("secret") || key.contains("token")
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn source_download_defaults_are_bounded_and_public_only() {
        let config = SourceDownloadConfig::default();

        assert_eq!(config.max_bytes, Some(DEFAULT_OBJECT_SOURCE_MAX_BYTES));
        assert!(!config.allow_private_networks);
    }

    #[test]
    fn redact_runtime_connection_string_masks_credentials() {
        assert_eq!(
            redact_runtime_connection_string("postgresql://user:sample@db.invalid:5432/db"),
            "postgresql://redacted:redacted@db.invalid:5432/db"
        );
        assert_eq!(
            redact_runtime_connection_string("redis://:sample@cache.invalid:6379/0"),
            "redis://redacted:redacted@cache.invalid:6379/0"
        );
        assert_eq!(
            redact_runtime_connection_string(
                "postgresql://example.invalid/db?sslmode=require&password=sample-value"
            ),
            "postgresql://example.invalid/db?sslmode=require&password=redacted"
        );
    }
}
