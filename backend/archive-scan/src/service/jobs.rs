#[path = "jobs/postgres_backend.rs"]
mod postgres_backend;
#[path = "jobs/redis_backend.rs"]
mod redis_backend;
#[path = "jobs/state_backend.rs"]
mod state_backend;

use self::{
    postgres_backend::{run_postgres_blocking, PostgresJobStoreBackend},
    redis_backend::RedisJobStoreBackend,
    state_backend::{FilesystemJobStoreBackend, InMemoryJobStoreBackend},
};
use super::{
    config::{
        JobStoreBackendKind, JobStoreRuntimeConfig, ResultStoreBackendKind, ResultStoreConfig,
        RuntimeStorageConfig,
    },
    model::{JobFailure, JobState, ScanArchiveRequest, ScanArchiveResponse},
    result_store::{JobResultReference, ResultStore},
};
use anyhow::{anyhow, Result};
use serde::{Deserialize, Serialize};
use std::{
    collections::HashMap,
    sync::{
        atomic::{AtomicBool, AtomicU64, Ordering},
        Arc,
    },
    time::{Duration, SystemTime},
};

pub(super) const DEFAULT_TERMINAL_JOB_RETENTION_SECS: u64 = 60 * 60;
const CANCELLATION_POLL_INTERVAL: u64 = 8;

#[derive(Clone, Debug, Eq, PartialEq)]
pub(super) struct JobStoreConfig {
    pub(super) terminal_job_retention: Duration,
    pub(super) runtime: JobStoreRuntimeConfig,
    pub(super) result_store: ResultStoreConfig,
}

impl Default for JobStoreConfig {
    fn default() -> Self {
        Self {
            terminal_job_retention: Duration::from_secs(DEFAULT_TERMINAL_JOB_RETENTION_SECS),
            runtime: JobStoreRuntimeConfig {
                backend: JobStoreBackendKind::InMemory,
                ..JobStoreRuntimeConfig::default()
            },
            result_store: ResultStoreConfig {
                backend: ResultStoreBackendKind::InMemory,
                ..ResultStoreConfig::default()
            },
        }
    }
}

#[derive(Clone)]
pub(super) struct JobStore {
    backend: JobStoreBackend,
    terminal_job_retention: Duration,
    result_store: ResultStore,
    runtime_storage: RuntimeStorageConfig,
}

#[derive(Clone)]
enum JobStoreBackend {
    InMemory(InMemoryJobStoreBackend),
    Filesystem(FilesystemJobStoreBackend),
    Redis(RedisJobStoreBackend),
    Postgres(PostgresJobStoreBackend),
}

#[derive(Clone, Debug, Deserialize, Serialize)]
pub(super) struct JobSnapshot {
    pub(super) id: String,
    pub(super) state: JobState,
    pub(super) created_at_unix: i64,
    pub(super) updated_at_unix: i64,
    pub(super) request: ScanArchiveRequest,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub(super) result: Option<ScanArchiveResponse>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub(super) result_ref: Option<JobResultReference>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub(super) error: Option<JobFailure>,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
struct IdempotencyRecord {
    job_id: String,
    request: ScanArchiveRequest,
}

#[derive(Clone, Debug, Default, Eq, PartialEq)]
pub(super) struct JobMetricsSnapshot {
    pub(super) retention_secs: u64,
    pub(super) visible_jobs: u64,
    pub(super) active_jobs: u64,
    pub(super) terminal_jobs: u64,
    pub(super) queued_jobs: u64,
    pub(super) running_jobs: u64,
    pub(super) succeeded_jobs: u64,
    pub(super) failed_jobs: u64,
    pub(super) cancelled_jobs: u64,
    pub(super) created_total: u64,
    pub(super) started_total: u64,
    pub(super) succeeded_total: u64,
    pub(super) failed_total: u64,
    pub(super) cancelled_total: u64,
    pub(super) expired_total: u64,
    pub(super) recovery_runs_total: u64,
    pub(super) recovered_jobs_total: u64,
    pub(super) recovered_running_jobs_total: u64,
    pub(super) recovery_deleted_result_refs_total: u64,
    pub(super) cleanup_deleted_result_refs_total: u64,
    pub(super) result_artifact_gc_runs_total: u64,
    pub(super) result_artifact_gc_deleted_total: u64,
    pub(super) result_artifact_gc_failures_total: u64,
    pub(super) storage: RuntimeStorageConfig,
}

#[derive(Clone)]
pub(super) struct JobCancellation {
    inner: JobCancellationKind,
}

#[derive(Clone)]
enum JobCancellationKind {
    InMemory(Arc<AtomicBool>),
    Store(Box<StoreCancellationState>),
}

#[derive(Clone)]
struct StoreCancellationState {
    store: JobStore,
    job_id: String,
    cached_cancelled: Arc<AtomicBool>,
    checks: Arc<AtomicU64>,
}

#[derive(Clone, Debug, Default, Deserialize, Serialize)]
#[serde(default)]
struct PersistedJobStoreState {
    next_id: u64,
    created_total: u64,
    started_total: u64,
    succeeded_total: u64,
    failed_total: u64,
    cancelled_total: u64,
    expired_total: u64,
    recovery_runs_total: u64,
    recovered_jobs_total: u64,
    recovered_running_jobs_total: u64,
    recovery_deleted_result_refs_total: u64,
    cleanup_deleted_result_refs_total: u64,
    jobs: HashMap<String, JobSnapshot>,
    idempotency_keys: HashMap<String, IdempotencyRecord>,
}

pub(super) enum CancelJobOutcome {
    Cancelled(JobSnapshot),
    AlreadyCancelled(JobSnapshot),
    NotCancellable(JobSnapshot),
}

pub(super) enum CreateJobOutcome {
    Created(JobSnapshot),
    Existing(JobSnapshot),
    Conflict(String),
}

impl JobCancellation {
    pub(super) fn is_cancelled(&self) -> bool {
        match &self.inner {
            JobCancellationKind::InMemory(flag) => flag.load(Ordering::Relaxed),
            JobCancellationKind::Store(state) => {
                if state.cached_cancelled.load(Ordering::Relaxed) {
                    return true;
                }

                let current = state.checks.fetch_add(1, Ordering::Relaxed) + 1;
                if current % CANCELLATION_POLL_INTERVAL != 0 {
                    return false;
                }

                if state.store.try_is_cancelled(&state.job_id).unwrap_or(false) {
                    state.cached_cancelled.store(true, Ordering::Relaxed);
                    true
                } else {
                    false
                }
            }
        }
    }
}

impl JobStore {
    pub(super) fn new(config: JobStoreConfig) -> Self {
        let mut result_store_config = config.result_store.clone();
        if result_store_config.artifact_retention < config.terminal_job_retention {
            result_store_config.artifact_retention = config.terminal_job_retention;
        }
        let result_store = ResultStore::new(&result_store_config);

        let backend = match config.runtime.backend {
            JobStoreBackendKind::InMemory => {
                JobStoreBackend::InMemory(InMemoryJobStoreBackend::new())
            }
            JobStoreBackendKind::Filesystem => {
                JobStoreBackend::Filesystem(FilesystemJobStoreBackend::new(
                    config.runtime.filesystem_path.clone(),
                    config.runtime.lock_timeout,
                    config.runtime.lock_retry_interval,
                ))
            }
            JobStoreBackendKind::Redis => {
                let redis_url = config
                    .runtime
                    .redis_url
                    .as_deref()
                    .expect("redis backend requires a redis_url");
                JobStoreBackend::Redis(
                    RedisJobStoreBackend::new(
                        redis_url,
                        &config.runtime.redis_key_prefix,
                        config.runtime.redis_max_connections,
                        config.terminal_job_retention,
                        result_store.clone(),
                        config.runtime.redis_cleanup_batch_size,
                    )
                    .expect("redis job store should initialize"),
                )
            }
            JobStoreBackendKind::Postgres => {
                let postgres_url = config
                    .runtime
                    .postgres_url
                    .as_deref()
                    .expect("postgres backend requires a postgres_url");
                JobStoreBackend::Postgres(
                    run_postgres_blocking(|| {
                        PostgresJobStoreBackend::new(
                            postgres_url,
                            &config.runtime.postgres_table_prefix,
                            config.runtime.postgres_max_connections,
                            config.terminal_job_retention,
                            result_store.clone(),
                        )
                    })
                    .expect("postgres job store should initialize"),
                )
            }
        };

        let mut runtime_storage = config.runtime.describe();
        result_store_config.apply_to_runtime(&mut runtime_storage);

        Self {
            backend,
            terminal_job_retention: config.terminal_job_retention,
            result_store,
            runtime_storage,
        }
    }

    #[cfg(test)]
    pub(super) fn create_job(&self, request: ScanArchiveRequest) -> JobSnapshot {
        self.try_create_job(request).expect("job store create_job should succeed")
    }

    pub(super) fn try_create_job(&self, request: ScanArchiveRequest) -> Result<JobSnapshot> {
        match &self.backend {
            JobStoreBackend::Redis(backend) => return backend.try_create_job(request),
            JobStoreBackend::Postgres(backend) => {
                return run_postgres_blocking(|| backend.try_create_job(request));
            }
            JobStoreBackend::InMemory(_) | JobStoreBackend::Filesystem(_) => {}
        }
        self.with_state(|state| {
            let snapshot = self.create_job_locked(state, request);
            Ok((snapshot, true))
        })
    }

    pub(super) fn try_resolve_idempotent_job(
        &self,
        idempotency_key: &str,
        request: &ScanArchiveRequest,
    ) -> Result<Option<CreateJobOutcome>> {
        match &self.backend {
            JobStoreBackend::Redis(backend) => {
                return backend.try_resolve_idempotent_job(idempotency_key, request);
            }
            JobStoreBackend::Postgres(backend) => {
                return run_postgres_blocking(|| {
                    backend.try_resolve_idempotent_job(idempotency_key, request)
                });
            }
            JobStoreBackend::InMemory(_) | JobStoreBackend::Filesystem(_) => {}
        }
        self.with_state(|state| {
            Ok((self.lookup_idempotency_locked(state, idempotency_key, request), false))
        })
    }

    #[cfg(test)]
    pub(super) fn create_job_with_idempotency(
        &self,
        request: ScanArchiveRequest,
        idempotency_key: String,
    ) -> CreateJobOutcome {
        self.try_create_job_with_idempotency(request, idempotency_key)
            .expect("job store idempotent create should succeed")
    }

    pub(super) fn try_create_job_with_idempotency(
        &self,
        request: ScanArchiveRequest,
        idempotency_key: String,
    ) -> Result<CreateJobOutcome> {
        match &self.backend {
            JobStoreBackend::Redis(backend) => {
                return backend.try_create_job_with_idempotency(request, idempotency_key);
            }
            JobStoreBackend::Postgres(backend) => {
                return run_postgres_blocking(|| {
                    backend.try_create_job_with_idempotency(request, idempotency_key)
                });
            }
            JobStoreBackend::InMemory(_) | JobStoreBackend::Filesystem(_) => {}
        }
        self.with_state(|state| {
            if let Some(outcome) = self.lookup_idempotency_locked(state, &idempotency_key, &request)
            {
                return Ok((outcome, false));
            }

            let snapshot = self.create_job_locked(state, request.clone());
            state.idempotency_keys.insert(
                idempotency_key,
                IdempotencyRecord { job_id: snapshot.id.clone(), request },
            );
            Ok((CreateJobOutcome::Created(snapshot), true))
        })
    }

    #[cfg(test)]
    pub(super) fn get(&self, job_id: &str) -> Option<JobSnapshot> {
        self.try_get(job_id).expect("job store get should succeed")
    }

    pub(super) fn try_get(&self, job_id: &str) -> Result<Option<JobSnapshot>> {
        match &self.backend {
            JobStoreBackend::Redis(backend) => return backend.try_get(job_id),
            JobStoreBackend::Postgres(backend) => {
                return run_postgres_blocking(|| backend.try_get(job_id));
            }
            JobStoreBackend::InMemory(_) | JobStoreBackend::Filesystem(_) => {}
        }
        self.with_state(|state| Ok((state.jobs.get(job_id).cloned(), false)))
    }

    #[cfg(test)]
    pub(super) fn mark_running(&self, job_id: &str) -> bool {
        self.try_mark_running(job_id).expect("job store mark_running should succeed")
    }

    pub(super) fn try_mark_running(&self, job_id: &str) -> Result<bool> {
        match &self.backend {
            JobStoreBackend::Redis(backend) => return backend.try_mark_running(job_id),
            JobStoreBackend::Postgres(backend) => {
                return run_postgres_blocking(|| backend.try_mark_running(job_id));
            }
            JobStoreBackend::InMemory(_) | JobStoreBackend::Filesystem(_) => {}
        }
        self.with_state(|state| {
            let Some(snapshot) = state.jobs.get_mut(job_id) else {
                return Ok((false, false));
            };
            if snapshot.state != JobState::Queued {
                return Ok((false, false));
            }

            snapshot.state = JobState::Running;
            snapshot.updated_at_unix = unix_now();
            state.started_total += 1;
            Ok((true, true))
        })
    }

    #[cfg(test)]
    pub(super) fn mark_succeeded(&self, job_id: &str, result: ScanArchiveResponse) -> bool {
        self.try_mark_succeeded(job_id, result).expect("job store mark_succeeded should succeed")
    }

    pub(super) fn try_mark_succeeded(
        &self,
        job_id: &str,
        result: ScanArchiveResponse,
    ) -> Result<bool> {
        match &self.backend {
            JobStoreBackend::Redis(backend) => return backend.try_mark_succeeded(job_id, result),
            JobStoreBackend::Postgres(backend) => {
                return run_postgres_blocking(|| backend.try_mark_succeeded(job_id, result));
            }
            JobStoreBackend::InMemory(_) | JobStoreBackend::Filesystem(_) => {}
        }
        let persisted = self.result_store.persist(job_id, &result)?;
        self.with_state(|state| {
            let Some(snapshot) = state.jobs.get_mut(job_id) else {
                if let Some(reference) = persisted.reference.as_ref() {
                    self.result_store.delete(reference)?;
                }
                return Ok((false, false));
            };
            if snapshot.state != JobState::Running {
                if let Some(reference) = persisted.reference.as_ref() {
                    self.result_store.delete(reference)?;
                }
                return Ok((false, false));
            }

            snapshot.state = JobState::Succeeded;
            snapshot.updated_at_unix = unix_now();
            snapshot.result = persisted.inline_result;
            snapshot.result_ref = persisted.reference;
            snapshot.error = None;
            state.succeeded_total += 1;
            Ok((true, true))
        })
    }

    #[cfg(test)]
    pub(super) fn mark_failed(&self, job_id: &str, error: JobFailure) -> bool {
        self.try_mark_failed(job_id, error).expect("job store mark_failed should succeed")
    }

    pub(super) fn try_mark_failed(&self, job_id: &str, error: JobFailure) -> Result<bool> {
        match &self.backend {
            JobStoreBackend::Redis(backend) => return backend.try_mark_failed(job_id, error),
            JobStoreBackend::Postgres(backend) => {
                return run_postgres_blocking(|| backend.try_mark_failed(job_id, error));
            }
            JobStoreBackend::InMemory(_) | JobStoreBackend::Filesystem(_) => {}
        }
        self.with_state(|state| {
            let Some(snapshot) = state.jobs.get_mut(job_id) else {
                return Ok((false, false));
            };
            if snapshot.state != JobState::Running {
                return Ok((false, false));
            }

            if let Some(reference) = snapshot.result_ref.take() {
                self.result_store.delete(&reference)?;
            }
            snapshot.state = JobState::Failed;
            snapshot.updated_at_unix = unix_now();
            snapshot.result = None;
            snapshot.error = Some(error);
            state.failed_total += 1;
            Ok((true, true))
        })
    }

    #[cfg(test)]
    pub(super) fn cancellation(&self, job_id: &str) -> Option<JobCancellation> {
        self.try_cancellation(job_id).expect("job store cancellation lookup should succeed")
    }

    pub(super) fn try_cancellation(&self, job_id: &str) -> Result<Option<JobCancellation>> {
        match &self.backend {
            JobStoreBackend::InMemory(backend) => Ok(backend
                .cancellation(job_id)
                .map(|flag| JobCancellation { inner: JobCancellationKind::InMemory(flag) })),
            JobStoreBackend::Filesystem(_)
            | JobStoreBackend::Redis(_)
            | JobStoreBackend::Postgres(_) => {
                Ok(self.try_get(job_id)?.map(|snapshot| JobCancellation {
                    inner: JobCancellationKind::Store(Box::new(StoreCancellationState {
                        store: self.clone(),
                        job_id: snapshot.id,
                        cached_cancelled: Arc::new(AtomicBool::new(
                            snapshot.state == JobState::Cancelled,
                        )),
                        checks: Arc::new(AtomicU64::default()),
                    })),
                }))
            }
        }
    }

    #[cfg(test)]
    pub(super) fn cancel(&self, job_id: &str) -> Option<CancelJobOutcome> {
        self.try_cancel(job_id).expect("job store cancel should succeed")
    }

    pub(super) fn try_cancel(&self, job_id: &str) -> Result<Option<CancelJobOutcome>> {
        match &self.backend {
            JobStoreBackend::Redis(backend) => {
                let outcome = backend.try_cancel(job_id)?;
                if matches!(outcome, Some(CancelJobOutcome::Cancelled(_))) {
                    self.mark_local_cancellation(job_id);
                }
                return Ok(outcome);
            }
            JobStoreBackend::Postgres(backend) => {
                return run_postgres_blocking(|| backend.try_cancel(job_id));
            }
            JobStoreBackend::InMemory(_) | JobStoreBackend::Filesystem(_) => {}
        }
        self.with_state(|state| {
            let Some(snapshot) = state.jobs.get_mut(job_id) else {
                return Ok((None, false));
            };

            let outcome = match snapshot.state {
                JobState::Queued | JobState::Running => {
                    if let Some(reference) = snapshot.result_ref.take() {
                        self.result_store.delete(&reference)?;
                    }
                    snapshot.state = JobState::Cancelled;
                    snapshot.updated_at_unix = unix_now();
                    snapshot.result = None;
                    snapshot.error = Some(JobFailure::new(
                        "job_cancelled",
                        "job was cancelled before completion",
                    ));
                    state.cancelled_total += 1;
                    self.mark_local_cancellation(job_id);
                    CancelJobOutcome::Cancelled(snapshot.clone())
                }
                JobState::Cancelled => CancelJobOutcome::AlreadyCancelled(snapshot.clone()),
                JobState::Succeeded | JobState::Failed => {
                    CancelJobOutcome::NotCancellable(snapshot.clone())
                }
            };

            Ok((Some(outcome), true))
        })
    }

    pub(super) fn try_metrics(&self) -> Result<JobMetricsSnapshot> {
        let mut snapshot = match &self.backend {
            JobStoreBackend::Redis(backend) => backend.try_metrics(&self.runtime_storage)?,
            JobStoreBackend::Postgres(backend) => {
                run_postgres_blocking(|| backend.try_metrics(&self.runtime_storage))?
            }
            JobStoreBackend::InMemory(_) | JobStoreBackend::Filesystem(_) => {
                self.with_state(|state| {
                    let mut snapshot = JobMetricsSnapshot {
                        retention_secs: self.terminal_job_retention.as_secs(),
                        visible_jobs: u64::try_from(state.jobs.len()).unwrap_or(u64::MAX),
                        created_total: state.created_total,
                        started_total: state.started_total,
                        succeeded_total: state.succeeded_total,
                        failed_total: state.failed_total,
                        cancelled_total: state.cancelled_total,
                        expired_total: state.expired_total,
                        recovery_runs_total: state.recovery_runs_total,
                        recovered_jobs_total: state.recovered_jobs_total,
                        recovered_running_jobs_total: state.recovered_running_jobs_total,
                        recovery_deleted_result_refs_total: state
                            .recovery_deleted_result_refs_total,
                        cleanup_deleted_result_refs_total: state.cleanup_deleted_result_refs_total,
                        storage: self.runtime_storage.clone(),
                        ..JobMetricsSnapshot::default()
                    };

                    for job in state.jobs.values() {
                        match job.state {
                            JobState::Queued => snapshot.queued_jobs += 1,
                            JobState::Running => snapshot.running_jobs += 1,
                            JobState::Succeeded => snapshot.succeeded_jobs += 1,
                            JobState::Failed => snapshot.failed_jobs += 1,
                            JobState::Cancelled => snapshot.cancelled_jobs += 1,
                        }
                    }

                    snapshot.active_jobs = snapshot.queued_jobs + snapshot.running_jobs;
                    snapshot.terminal_jobs =
                        snapshot.succeeded_jobs + snapshot.failed_jobs + snapshot.cancelled_jobs;

                    Ok((snapshot, false))
                })?
            }
        };
        let gc = self.result_store.maintenance_stats();
        snapshot.result_artifact_gc_runs_total = gc.gc_runs_total;
        snapshot.result_artifact_gc_deleted_total = gc.gc_deleted_artifacts_total;
        snapshot.result_artifact_gc_failures_total = gc.gc_failures_total;
        Ok(snapshot)
    }

    pub(super) fn load_result(
        &self,
        snapshot: &JobSnapshot,
    ) -> Result<Option<ScanArchiveResponse>> {
        if let Some(result) = snapshot.result.clone() {
            return Ok(Some(result));
        }
        match snapshot.result_ref.as_ref() {
            Some(reference) => self.result_store.load(reference),
            None => Ok(None),
        }
    }

    pub(super) fn try_is_cancelled(&self, job_id: &str) -> Result<bool> {
        match &self.backend {
            JobStoreBackend::Redis(backend) => return backend.try_is_cancelled(job_id),
            JobStoreBackend::Postgres(backend) => {
                return run_postgres_blocking(|| backend.try_is_cancelled(job_id));
            }
            JobStoreBackend::InMemory(_) | JobStoreBackend::Filesystem(_) => {}
        }
        Ok(self.try_get(job_id)?.is_some_and(|snapshot| snapshot.state == JobState::Cancelled))
    }

    pub(super) fn readiness_check(&self) -> Result<()> {
        self.backend.readiness_check()?;
        self.result_store.readiness_check()
    }

    pub(super) fn try_reconcile_inflight(&self) -> Result<Vec<JobSnapshot>> {
        match &self.backend {
            JobStoreBackend::Redis(backend) => return backend.try_reconcile_inflight(),
            JobStoreBackend::Postgres(backend) => {
                return run_postgres_blocking(|| backend.try_reconcile_inflight());
            }
            JobStoreBackend::InMemory(_) | JobStoreBackend::Filesystem(_) => {}
        }

        self.with_state(|state| {
            let now = unix_now();
            let mut rescheduled = Vec::new();
            let mut recovered_running_jobs = 0_u64;
            let mut deleted_result_refs = 0_u64;
            let mut job_ids: Vec<_> = state.jobs.keys().cloned().collect();
            job_ids.sort_unstable();

            for job_id in job_ids {
                let Some(snapshot) = state.jobs.get_mut(&job_id) else {
                    continue;
                };
                if !matches!(snapshot.state, JobState::Queued | JobState::Running) {
                    continue;
                }

                if let Some(reference) = snapshot.result_ref.take() {
                    self.result_store.delete(&reference)?;
                    deleted_result_refs = deleted_result_refs.saturating_add(1);
                }
                if snapshot.state == JobState::Running {
                    recovered_running_jobs = recovered_running_jobs.saturating_add(1);
                }
                snapshot.state = JobState::Queued;
                snapshot.updated_at_unix = now;
                snapshot.result = None;
                snapshot.error = None;
                rescheduled.push(snapshot.clone());
            }

            let dirty = !rescheduled.is_empty();
            if !matches!(self.backend, JobStoreBackend::InMemory(_)) {
                state.recovery_runs_total = state.recovery_runs_total.saturating_add(1);
            }
            state.recovered_jobs_total = state
                .recovered_jobs_total
                .saturating_add(u64::try_from(rescheduled.len()).unwrap_or(u64::MAX));
            state.recovered_running_jobs_total =
                state.recovered_running_jobs_total.saturating_add(recovered_running_jobs);
            state.recovery_deleted_result_refs_total =
                state.recovery_deleted_result_refs_total.saturating_add(deleted_result_refs);
            Ok((rescheduled, dirty))
        })
    }

    #[cfg(test)]
    pub(super) fn cleanup_expired_with_now(&self, now_unix: i64) {
        self.try_cleanup_expired_with_now(now_unix).expect("job store cleanup should succeed");
    }

    #[cfg(test)]
    pub(super) fn try_cleanup_expired_with_now(&self, now_unix: i64) -> Result<()> {
        match &self.backend {
            JobStoreBackend::Redis(backend) => {
                return backend.try_cleanup_expired_with_now(now_unix);
            }
            JobStoreBackend::Postgres(backend) => {
                return run_postgres_blocking(|| backend.try_cleanup_expired_with_now(now_unix));
            }
            JobStoreBackend::InMemory(_) | JobStoreBackend::Filesystem(_) => {}
        }
        self.with_state_with_now(now_unix, |_state| Ok(((), false)))
    }

    fn create_job_locked(
        &self,
        state: &mut PersistedJobStoreState,
        request: ScanArchiveRequest,
    ) -> JobSnapshot {
        state.next_id += 1;
        let job_id = format!("job-{:08}", state.next_id);
        let timestamp = unix_now();
        let snapshot = JobSnapshot {
            id: job_id.clone(),
            state: JobState::Queued,
            created_at_unix: timestamp,
            updated_at_unix: timestamp,
            request,
            result: None,
            result_ref: None,
            error: None,
        };
        if let JobStoreBackend::InMemory(backend) = &self.backend {
            backend.register_cancellation(&job_id);
        }
        state.jobs.insert(job_id, snapshot.clone());
        state.created_total += 1;
        snapshot
    }

    fn lookup_idempotency_locked(
        &self,
        state: &PersistedJobStoreState,
        idempotency_key: &str,
        request: &ScanArchiveRequest,
    ) -> Option<CreateJobOutcome> {
        let record = state.idempotency_keys.get(idempotency_key)?;
        if record.request == *request {
            let snapshot = state.jobs.get(&record.job_id)?.clone();
            Some(CreateJobOutcome::Existing(snapshot))
        } else {
            Some(CreateJobOutcome::Conflict(record.job_id.clone()))
        }
    }

    fn mark_local_cancellation(&self, job_id: &str) {
        if let JobStoreBackend::InMemory(backend) = &self.backend {
            backend.mark_cancelled(job_id);
        }
    }

    fn with_state<T>(
        &self,
        f: impl FnOnce(&mut PersistedJobStoreState) -> Result<(T, bool)>,
    ) -> Result<T> {
        self.with_state_with_now(unix_now(), f)
    }

    fn with_state_with_now<T>(
        &self,
        now_unix: i64,
        f: impl FnOnce(&mut PersistedJobStoreState) -> Result<(T, bool)>,
    ) -> Result<T> {
        self.backend.with_locked_state(|state| {
            let purge_dirty = self.purge_expired_locked(state, now_unix)?;
            let (value, dirty) = f(state)?;
            Ok((value, purge_dirty || dirty))
        })
    }

    fn purge_expired_locked(
        &self,
        state: &mut PersistedJobStoreState,
        now_unix: i64,
    ) -> Result<bool> {
        let retention = i64::try_from(self.terminal_job_retention.as_secs()).unwrap_or(i64::MAX);
        let mut removed_jobs = Vec::new();
        for (job_id, snapshot) in &state.jobs {
            let expired = snapshot.state.is_terminal()
                && now_unix.saturating_sub(snapshot.updated_at_unix) >= retention;
            if expired {
                removed_jobs.push(job_id.clone());
            }
        }

        if removed_jobs.is_empty() {
            return Ok(false);
        }

        for job_id in removed_jobs {
            if let Some(snapshot) = state.jobs.remove(&job_id) {
                if let Some(reference) = snapshot.result_ref.as_ref() {
                    self.result_store.delete(reference)?;
                    state.cleanup_deleted_result_refs_total =
                        state.cleanup_deleted_result_refs_total.saturating_add(1);
                }
                if let JobStoreBackend::InMemory(backend) = &self.backend {
                    backend.unregister_cancellation(&job_id);
                }
                state.idempotency_keys.retain(|_, record| record.job_id != job_id);
                state.expired_total = state.expired_total.saturating_add(1);
            }
        }

        Ok(true)
    }
}

impl Default for JobStore {
    fn default() -> Self {
        Self::new(JobStoreConfig::default())
    }
}

impl JobStoreBackend {
    fn with_locked_state<T>(
        &self,
        f: impl FnOnce(&mut PersistedJobStoreState) -> Result<(T, bool)>,
    ) -> Result<T> {
        match self {
            Self::InMemory(backend) => backend.with_locked_state(f),
            Self::Filesystem(backend) => backend.with_locked_state(f),
            Self::Redis(_) => unreachable!("redis backend uses dedicated key-level operations"),
            Self::Postgres(_) => {
                unreachable!("postgres backend uses dedicated row-level operations")
            }
        }
    }

    fn readiness_check(&self) -> Result<()> {
        match self {
            Self::InMemory(_) => Ok(()),
            Self::Filesystem(backend) => backend.readiness_check(),
            Self::Redis(backend) => backend.readiness_check(),
            Self::Postgres(backend) => run_postgres_blocking(|| backend.readiness_check()),
        }
    }
}

fn job_state_to_str(state: JobState) -> &'static str {
    match state {
        JobState::Queued => "queued",
        JobState::Running => "running",
        JobState::Cancelled => "cancelled",
        JobState::Succeeded => "succeeded",
        JobState::Failed => "failed",
    }
}

fn job_state_from_str(value: &str) -> Result<JobState> {
    match value {
        "queued" => Ok(JobState::Queued),
        "running" => Ok(JobState::Running),
        "cancelled" => Ok(JobState::Cancelled),
        "succeeded" => Ok(JobState::Succeeded),
        "failed" => Ok(JobState::Failed),
        _ => Err(anyhow!("unexpected job state: {value}")),
    }
}

fn unix_now() -> i64 {
    SystemTime::UNIX_EPOCH
        .elapsed()
        .map_or(0, |duration| i64::try_from(duration.as_secs()).unwrap_or(i64::MAX))
}
