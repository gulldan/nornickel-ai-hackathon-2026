use anyhow::Result;
use archive_scan::service::{
    ExtractMetadataBackendKind, ExtractMetadataConfig, ExtractStoreBackendKind, ExtractStoreConfig,
    JobStoreBackendKind, JobStoreRuntimeConfig, ResultStoreBackendKind, ResultStoreConfig,
    ServiceConfig, SourceDownloadConfig, DEFAULT_JOB_RETENTION_SECS,
    DEFAULT_OBJECT_SOURCE_MAX_BYTES,
};
use clap::Parser;
use std::{net::SocketAddr, path::PathBuf, time::Duration};
use tracing_subscriber::EnvFilter;

#[derive(Parser, Debug, Clone)]
#[command(name = "archive_scan_server", about = "archive-scan REST service")]
struct Args {
    #[arg(long, env = "ARCHIVE_SCAN_SERVICE_ADDR", default_value = "0.0.0.0:3000")]
    addr: SocketAddr,

    #[arg(long, env = "ARCHIVE_SCAN_JOB_RETENTION_SECS", default_value_t = DEFAULT_JOB_RETENTION_SECS)]
    job_retention_secs: u64,

    #[arg(long, env = "ARCHIVE_SCAN_JOB_STORE_BACKEND", value_enum, default_value_t = JobStoreBackendKind::Filesystem)]
    job_store_backend: JobStoreBackendKind,

    #[arg(long, env = "ARCHIVE_SCAN_JOB_STORE_PATH")]
    job_store_path: Option<PathBuf>,

    #[arg(long, env = "ARCHIVE_SCAN_JOB_STORE_REDIS_URL")]
    job_store_redis_url: Option<String>,

    #[arg(long, env = "ARCHIVE_SCAN_JOB_STORE_KEY_PREFIX", default_value = "archive_scan")]
    job_store_key_prefix: String,

    #[arg(long, env = "ARCHIVE_SCAN_JOB_STORE_REDIS_MAX_CONNECTIONS", default_value_t = 16)]
    job_store_redis_max_connections: u32,

    #[arg(long, env = "ARCHIVE_SCAN_JOB_STORE_REDIS_CLEANUP_BATCH_SIZE", default_value_t = 128)]
    job_store_redis_cleanup_batch_size: usize,

    #[arg(long, env = "ARCHIVE_SCAN_JOB_STORE_POSTGRES_URL")]
    job_store_postgres_url: Option<String>,

    #[arg(
        long,
        env = "ARCHIVE_SCAN_JOB_STORE_POSTGRES_TABLE_PREFIX",
        default_value = "archive_scan"
    )]
    job_store_postgres_table_prefix: String,

    #[arg(long, env = "ARCHIVE_SCAN_JOB_STORE_POSTGRES_MAX_CONNECTIONS", default_value_t = 16)]
    job_store_postgres_max_connections: u32,

    #[arg(long, env = "ARCHIVE_SCAN_JOB_STORE_LOCK_TIMEOUT_SECS", default_value_t = 5)]
    job_store_lock_timeout_secs: u64,

    #[arg(long, env = "ARCHIVE_SCAN_JOB_STORE_LOCK_RETRY_MILLIS", default_value_t = 25)]
    job_store_lock_retry_millis: u64,

    #[arg(long, env = "ARCHIVE_SCAN_RESULT_STORE_BACKEND", value_enum, default_value_t = ResultStoreBackendKind::Filesystem)]
    result_store_backend: ResultStoreBackendKind,

    #[arg(long, env = "ARCHIVE_SCAN_RESULT_STORE_DIR")]
    result_store_dir: Option<PathBuf>,

    #[arg(long, env = "ARCHIVE_SCAN_RESULT_STORE_S3_ENDPOINT")]
    result_store_s3_endpoint: Option<String>,

    #[arg(long, env = "ARCHIVE_SCAN_RESULT_STORE_S3_REGION", default_value = "us-east-1")]
    result_store_s3_region: String,

    #[arg(long, env = "ARCHIVE_SCAN_RESULT_STORE_S3_BUCKET")]
    result_store_s3_bucket: Option<String>,

    #[arg(
        long,
        env = "ARCHIVE_SCAN_RESULT_STORE_S3_KEY_PREFIX",
        default_value = "archive-scan/results"
    )]
    result_store_s3_key_prefix: String,

    #[arg(long, env = "ARCHIVE_SCAN_RESULT_STORE_S3_ACCESS_KEY_ID")]
    result_store_s3_access_key_id: Option<String>,

    #[arg(long, env = "ARCHIVE_SCAN_RESULT_STORE_S3_SECRET_ACCESS_KEY")]
    result_store_s3_secret_access_key: Option<String>,

    #[arg(long, env = "ARCHIVE_SCAN_RESULT_STORE_S3_SESSION_TOKEN")]
    result_store_s3_session_token: Option<String>,

    #[arg(
        long,
        env = "ARCHIVE_SCAN_RESULT_STORE_S3_PATH_STYLE",
        default_value_t = false,
        num_args = 0..=1,
        default_missing_value = "true",
        value_parser = clap::builder::BoolishValueParser::new()
    )]
    result_store_s3_path_style: bool,

    #[arg(long, env = "ARCHIVE_SCAN_RESULT_INLINE_MAX_BYTES", default_value_t = 256 * 1024)]
    result_inline_max_bytes: usize,

    #[arg(
        long,
        env = "ARCHIVE_SCAN_RESULT_ARTIFACT_RETENTION_SECS",
        default_value_t = DEFAULT_JOB_RETENTION_SECS
    )]
    result_artifact_retention_secs: u64,

    #[arg(
        long,
        env = "ARCHIVE_SCAN_RESULT_ARTIFACT_GC_INTERVAL_SECS",
        default_value_t = 5 * 60
    )]
    result_artifact_gc_interval_secs: u64,

    #[arg(long, env = "ARCHIVE_SCAN_OBJECT_SOURCE_CONNECT_TIMEOUT_SECS", default_value_t = 10)]
    object_source_connect_timeout_secs: u64,

    #[arg(long, env = "ARCHIVE_SCAN_OBJECT_SOURCE_TIMEOUT_SECS", default_value_t = 300)]
    object_source_timeout_secs: u64,

    #[arg(
        long,
        env = "ARCHIVE_SCAN_OBJECT_SOURCE_MAX_BYTES",
        default_value_t = DEFAULT_OBJECT_SOURCE_MAX_BYTES
    )]
    object_source_max_bytes: u64,

    #[arg(long, env = "ARCHIVE_SCAN_OBJECT_SOURCE_TEMP_DIR")]
    object_source_temp_dir: Option<PathBuf>,

    #[arg(
        long,
        env = "ARCHIVE_SCAN_OBJECT_SOURCE_ALLOW_PRIVATE_NETWORKS",
        default_value_t = false,
        num_args = 0..=1,
        default_missing_value = "true",
        value_parser = clap::builder::BoolishValueParser::new()
    )]
    object_source_allow_private_networks: bool,

    #[arg(long, env = "ARCHIVE_SCAN_EXTRACT_STORE_BACKEND", value_enum, default_value_t = ExtractStoreBackendKind::Filesystem)]
    extract_store_backend: ExtractStoreBackendKind,

    #[arg(long, env = "ARCHIVE_SCAN_EXTRACT_STORE_DIR")]
    extract_store_dir: Option<PathBuf>,

    #[arg(long, env = "ARCHIVE_SCAN_EXTRACT_STORE_S3_ENDPOINT")]
    extract_store_s3_endpoint: Option<String>,

    #[arg(long, env = "ARCHIVE_SCAN_EXTRACT_STORE_S3_REGION", default_value = "us-east-1")]
    extract_store_s3_region: String,

    #[arg(long, env = "ARCHIVE_SCAN_EXTRACT_STORE_S3_BUCKET")]
    extract_store_s3_bucket: Option<String>,

    #[arg(
        long,
        env = "ARCHIVE_SCAN_EXTRACT_STORE_S3_KEY_PREFIX",
        default_value = "archive-scan/extracted"
    )]
    extract_store_s3_key_prefix: String,

    #[arg(long, env = "ARCHIVE_SCAN_EXTRACT_STORE_S3_ACCESS_KEY_ID")]
    extract_store_s3_access_key_id: Option<String>,

    #[arg(long, env = "ARCHIVE_SCAN_EXTRACT_STORE_S3_SECRET_ACCESS_KEY")]
    extract_store_s3_secret_access_key: Option<String>,

    #[arg(long, env = "ARCHIVE_SCAN_EXTRACT_STORE_S3_SESSION_TOKEN")]
    extract_store_s3_session_token: Option<String>,

    #[arg(
        long,
        env = "ARCHIVE_SCAN_EXTRACT_STORE_S3_PATH_STYLE",
        default_value_t = false,
        num_args = 0..=1,
        default_missing_value = "true",
        value_parser = clap::builder::BoolishValueParser::new()
    )]
    extract_store_s3_path_style: bool,

    #[arg(long, env = "ARCHIVE_SCAN_EXTRACT_STORE_TEMP_DIR")]
    extract_store_temp_dir: Option<PathBuf>,

    #[arg(long, env = "ARCHIVE_SCAN_EXTRACT_METADATA_BACKEND", value_enum, default_value_t = ExtractMetadataBackendKind::Filesystem)]
    extract_metadata_backend: ExtractMetadataBackendKind,

    #[arg(long, env = "ARCHIVE_SCAN_EXTRACT_METADATA_DIR")]
    extract_metadata_dir: Option<PathBuf>,

    #[arg(long, env = "ARCHIVE_SCAN_EXTRACT_METADATA_POSTGRES_URL")]
    extract_metadata_postgres_url: Option<String>,

    #[arg(
        long,
        env = "ARCHIVE_SCAN_EXTRACT_METADATA_POSTGRES_TABLE_PREFIX",
        default_value = "archive_scan"
    )]
    extract_metadata_postgres_table_prefix: String,

    #[arg(
        long,
        env = "ARCHIVE_SCAN_EXTRACT_METADATA_POSTGRES_MAX_CONNECTIONS",
        default_value_t = 16
    )]
    extract_metadata_postgres_max_connections: u32,

    #[arg(long, env = "ARCHIVE_SCAN_EXTRACT_METADATA_BATCH_SIZE", default_value_t = 500)]
    extract_metadata_batch_size: usize,
}

impl Args {
    fn into_service_config(self) -> ServiceConfig {
        let default_job_store_path = std::env::temp_dir().join("archive-scan/jobs/state.json");
        let default_result_store_dir = std::env::temp_dir().join("archive-scan/results");
        let default_extract_store_dir = std::env::temp_dir().join("archive-scan/extracted");
        let default_extract_metadata_dir =
            std::env::temp_dir().join("archive-scan/extract-metadata");

        ServiceConfig {
            addr: self.addr,
            job_retention: Duration::from_secs(self.job_retention_secs),
            job_store: JobStoreRuntimeConfig {
                backend: self.job_store_backend,
                filesystem_path: self.job_store_path.unwrap_or(default_job_store_path),
                redis_url: self.job_store_redis_url,
                redis_key_prefix: self.job_store_key_prefix,
                redis_max_connections: self.job_store_redis_max_connections,
                redis_cleanup_batch_size: self.job_store_redis_cleanup_batch_size,
                postgres_url: self.job_store_postgres_url,
                postgres_table_prefix: self.job_store_postgres_table_prefix,
                postgres_max_connections: self.job_store_postgres_max_connections,
                lock_timeout: Duration::from_secs(self.job_store_lock_timeout_secs),
                lock_retry_interval: Duration::from_millis(self.job_store_lock_retry_millis),
            },
            result_store: ResultStoreConfig {
                backend: self.result_store_backend,
                filesystem_dir: self.result_store_dir.unwrap_or(default_result_store_dir),
                s3_endpoint: self.result_store_s3_endpoint,
                s3_region: self.result_store_s3_region,
                s3_bucket: self.result_store_s3_bucket,
                s3_key_prefix: self.result_store_s3_key_prefix,
                s3_access_key_id: self.result_store_s3_access_key_id,
                s3_secret_access_key: self.result_store_s3_secret_access_key,
                s3_session_token: self.result_store_s3_session_token,
                s3_path_style: self.result_store_s3_path_style,
                inline_max_bytes: self.result_inline_max_bytes,
                artifact_retention: Duration::from_secs(self.result_artifact_retention_secs),
                gc_interval: Duration::from_secs(self.result_artifact_gc_interval_secs),
            },
            source_download: SourceDownloadConfig {
                connect_timeout: Duration::from_secs(self.object_source_connect_timeout_secs),
                request_timeout: Duration::from_secs(self.object_source_timeout_secs),
                max_bytes: Some(self.object_source_max_bytes),
                temp_dir: self.object_source_temp_dir,
                allow_private_networks: self.object_source_allow_private_networks,
            },
            extract_store: ExtractStoreConfig {
                backend: self.extract_store_backend,
                filesystem_dir: self.extract_store_dir.unwrap_or(default_extract_store_dir),
                s3_endpoint: self.extract_store_s3_endpoint,
                s3_region: self.extract_store_s3_region,
                s3_bucket: self.extract_store_s3_bucket,
                s3_key_prefix: self.extract_store_s3_key_prefix,
                s3_access_key_id: self.extract_store_s3_access_key_id,
                s3_secret_access_key: self.extract_store_s3_secret_access_key,
                s3_session_token: self.extract_store_s3_session_token,
                s3_path_style: self.extract_store_s3_path_style,
                temp_dir: self.extract_store_temp_dir,
            },
            extract_metadata: ExtractMetadataConfig {
                backend: self.extract_metadata_backend,
                filesystem_dir: self.extract_metadata_dir.unwrap_or(default_extract_metadata_dir),
                postgres_url: self.extract_metadata_postgres_url,
                postgres_table_prefix: self.extract_metadata_postgres_table_prefix,
                postgres_max_connections: self.extract_metadata_postgres_max_connections,
                batch_size: self.extract_metadata_batch_size,
            },
        }
    }
}

#[tokio::main]
async fn main() -> Result<()> {
    let args = Args::parse();
    init_tracing();
    tracing::info!(listen_addr = %args.addr, "starting archive_scan_server");
    archive_scan::service::run(args.into_service_config()).await
}

fn init_tracing() {
    let filter = EnvFilter::try_from_env("ARCHIVE_SCAN_LOG")
        .or_else(|_| EnvFilter::try_from_default_env())
        .unwrap_or_else(|_| EnvFilter::new("info,archive_scan=info"));
    tracing_subscriber::fmt().with_env_filter(filter).json().flatten_event(true).init();
}
