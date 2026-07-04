use super::{
    config::{
        ExtractMetadataConfig, ExtractStoreConfig, JobStoreBackendKind, JobStoreRuntimeConfig,
        ResultStoreBackendKind, ResultStoreConfig, SourceDownloadConfig,
    },
    extract_runtime::metadata::ExtractMetadataStore,
    jobs::{JobSnapshot, JobStore, JobStoreConfig},
    DEFAULT_JOB_RETENTION_SECS,
};
use anyhow::Result;
use std::{net::SocketAddr, time::Duration};

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct ServiceConfig {
    pub addr: SocketAddr,
    pub job_retention: Duration,
    pub job_store: JobStoreRuntimeConfig,
    pub result_store: ResultStoreConfig,
    pub source_download: SourceDownloadConfig,
    pub extract_store: ExtractStoreConfig,
    pub extract_metadata: ExtractMetadataConfig,
}

#[derive(Clone)]
pub(super) struct AppState {
    pub(super) jobs: JobStore,
    pub(super) source_download: SourceDownloadConfig,
    pub(super) extract_store: ExtractStoreConfig,
    pub(super) extract_metadata: ExtractMetadataStore,
}

impl AppState {
    #[cfg(test)]
    pub(super) fn with_job_retention(job_retention: Duration) -> Self {
        Self::with_config(&ServiceConfig { job_retention, ..ServiceConfig::default() })
    }

    pub(super) fn with_config(config: &ServiceConfig) -> Self {
        Self {
            jobs: JobStore::new(JobStoreConfig {
                terminal_job_retention: config.job_retention,
                runtime: config.job_store.clone(),
                result_store: config.result_store.clone(),
            }),
            source_download: config.source_download.clone(),
            extract_store: config.extract_store.clone(),
            extract_metadata: ExtractMetadataStore::new(&config.extract_metadata),
        }
    }

    pub(super) fn with_config_and_recovery(
        config: &ServiceConfig,
    ) -> Result<(Self, Vec<JobSnapshot>)> {
        let state = Self::with_config(config);
        let recovered_jobs = state.jobs.try_reconcile_inflight()?;
        Ok((state, recovered_jobs))
    }

    pub(super) fn upload_limit_bytes(&self) -> usize {
        self.source_download
            .max_bytes
            .and_then(|value| usize::try_from(value).ok())
            .unwrap_or(usize::MAX)
    }
}

impl Default for AppState {
    fn default() -> Self {
        Self::with_config(&ServiceConfig::default())
    }
}

impl Default for ServiceConfig {
    fn default() -> Self {
        Self {
            addr: "0.0.0.0:3000".parse().expect("default service listen address should parse"),
            job_retention: Duration::from_secs(DEFAULT_JOB_RETENTION_SECS),
            job_store: JobStoreRuntimeConfig {
                backend: JobStoreBackendKind::InMemory,
                ..JobStoreRuntimeConfig::default()
            },
            result_store: ResultStoreConfig {
                backend: ResultStoreBackendKind::InMemory,
                ..ResultStoreConfig::default()
            },
            source_download: SourceDownloadConfig::default(),
            extract_store: ExtractStoreConfig::default(),
            extract_metadata: ExtractMetadataConfig::default(),
        }
    }
}
