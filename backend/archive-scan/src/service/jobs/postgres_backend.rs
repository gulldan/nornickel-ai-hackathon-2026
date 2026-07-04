use super::{
    job_state_from_str, job_state_to_str, unix_now, CancelJobOutcome, CreateJobOutcome,
    JobMetricsSnapshot, JobResultReference, JobSnapshot, JobState, ResultStore,
    RuntimeStorageConfig, ScanArchiveRequest, ScanArchiveResponse,
};
use anyhow::{anyhow, Context, Result};
use postgres::{NoTls, Row, Transaction};
use r2d2::{Pool, PooledConnection};
use r2d2_postgres::PostgresConnectionManager;
use std::{fmt, sync::Arc, time::Duration};

#[derive(Clone)]
pub(super) struct PostgresJobStoreBackend {
    pool: Pool<PostgresConnectionManager<NoTls>>,
    tables: Arc<PostgresTableSet>,
    terminal_job_retention: Duration,
    result_store: ResultStore,
}

#[derive(Debug)]
struct PostgresTableSet {
    meta: Arc<str>,
    jobs: Arc<str>,
    idempotency: Arc<str>,
    terminal_updated_index: Arc<str>,
    idempotency_job_index: Arc<str>,
}

impl PostgresJobStoreBackend {
    pub(super) fn new(
        postgres_url: &str,
        table_prefix: &str,
        max_connections: u32,
        terminal_job_retention: Duration,
        result_store: ResultStore,
    ) -> Result<Self> {
        validate_postgres_table_prefix(table_prefix)?;

        let config = postgres_url
            .parse::<postgres::Config>()
            .context("failed to parse postgres job store url")?;
        let manager = PostgresConnectionManager::new(config, NoTls);
        let pool = Pool::builder()
            .max_size(max_connections.max(1))
            .build(manager)
            .context("failed to build postgres job store connection pool")?;

        let backend = Self {
            pool,
            tables: Arc::new(PostgresTableSet::new(table_prefix)),
            terminal_job_retention,
            result_store,
        };
        backend.ensure_schema()?;
        Ok(backend)
    }

    pub(super) fn try_create_job(&self, request: ScanArchiveRequest) -> Result<JobSnapshot> {
        self.with_transaction(unix_now(), move |tx, _deferred| self.insert_job_locked(tx, request))
    }

    pub(super) fn try_resolve_idempotent_job(
        &self,
        idempotency_key: &str,
        request: &ScanArchiveRequest,
    ) -> Result<Option<CreateJobOutcome>> {
        self.with_transaction(unix_now(), move |tx, _deferred| {
            self.lookup_idempotency_locked(tx, idempotency_key, request)
        })
    }

    pub(super) fn try_create_job_with_idempotency(
        &self,
        request: ScanArchiveRequest,
        idempotency_key: String,
    ) -> Result<CreateJobOutcome> {
        for _attempt in 0..3 {
            let mut connection = self.connection()?;
            let mut tx = connection
                .transaction()
                .context("failed to open postgres job store transaction")?;
            let deferred = self.purge_expired_locked(&mut tx, unix_now())?;

            if let Some(outcome) =
                self.lookup_idempotency_locked(&mut tx, &idempotency_key, &request)?
            {
                tx.commit().context("failed to commit postgres job store transaction")?;
                self.delete_deferred_results(&deferred)?;
                return Ok(outcome);
            }

            let snapshot = self.insert_job_locked(&mut tx, request.clone())?;
            let request_value =
                serde_json::to_value(&request).context("failed to serialize job request")?;
            let inserted = tx
                .execute(
                    &format!(
                        "INSERT INTO {} (idempotency_key, job_id, request) VALUES ($1, $2, $3) \
                         ON CONFLICT (idempotency_key) DO NOTHING",
                        self.tables.idempotency
                    ),
                    &[&idempotency_key, &snapshot.id, &request_value],
                )
                .context("failed to persist postgres idempotency record")?;

            if inserted == 1 {
                tx.commit().context("failed to commit postgres job store transaction")?;
                self.delete_deferred_results(&deferred)?;
                return Ok(CreateJobOutcome::Created(snapshot));
            }

            tx.rollback().context("failed to roll back postgres idempotent create transaction")?;
        }

        self.try_resolve_idempotent_job(&idempotency_key, &request)?.ok_or_else(|| {
            anyhow!("failed to resolve postgres idempotent job after concurrent race")
        })
    }

    pub(super) fn try_get(&self, job_id: &str) -> Result<Option<JobSnapshot>> {
        self.with_transaction(unix_now(), move |tx, _deferred| {
            self.query_snapshot(tx, job_id, false)
        })
    }

    pub(super) fn try_mark_running(&self, job_id: &str) -> Result<bool> {
        self.with_transaction(unix_now(), move |tx, _deferred| {
            let now = unix_now();
            let updated = tx
                .execute(
                    &format!(
                        "UPDATE {} SET state = $2, updated_at_unix = $3 \
                         WHERE job_id = $1 AND state = $4",
                        self.tables.jobs
                    ),
                    &[
                        &job_id,
                        &job_state_to_str(JobState::Running),
                        &now,
                        &job_state_to_str(JobState::Queued),
                    ],
                )
                .context("failed to update postgres job state to running")?;
            if updated == 0 {
                return Ok(false);
            }

            tx.execute(
                &format!(
                    "UPDATE {} SET started_total = started_total + 1 WHERE singleton = TRUE",
                    self.tables.meta
                ),
                &[],
            )
            .context("failed to update postgres job metrics")?;
            Ok(true)
        })
    }

    pub(super) fn try_mark_succeeded(
        &self,
        job_id: &str,
        result: ScanArchiveResponse,
    ) -> Result<bool> {
        let persisted = self.result_store.persist(job_id, &result)?;
        let serialized_inline = persisted
            .inline_result
            .as_ref()
            .map(serde_json::to_value)
            .transpose()
            .context("failed to serialize inline postgres job result")?;
        let serialized_ref = persisted
            .reference
            .as_ref()
            .map(serde_json::to_value)
            .transpose()
            .context("failed to serialize postgres job result reference")?;

        let outcome = self.with_transaction(unix_now(), move |tx, _deferred| {
            let now = unix_now();
            let updated = tx
                .execute(
                    &format!(
                        "UPDATE {} SET state = $2, updated_at_unix = $3, result = $4, result_ref = $5, error = NULL \
                         WHERE job_id = $1 AND state = $6",
                        self.tables.jobs
                    ),
                    &[
                        &job_id,
                        &job_state_to_str(JobState::Succeeded),
                        &now,
                        &serialized_inline,
                        &serialized_ref,
                        &job_state_to_str(JobState::Running),
                    ],
                )
                .context("failed to persist postgres completed job result")?;
            if updated == 0 {
                return Ok(false);
            }

            tx.execute(
                &format!(
                    "UPDATE {} SET succeeded_total = succeeded_total + 1 WHERE singleton = TRUE",
                    self.tables.meta
                ),
                &[],
            )
            .context("failed to update postgres job metrics")?;
            Ok(true)
        });

        match outcome {
            Ok(true) => Ok(true),
            Ok(false) => {
                if let Some(reference) = persisted.reference.as_ref() {
                    self.result_store.delete(reference)?;
                }
                Ok(false)
            }
            Err(err) => {
                if let Some(reference) = persisted.reference.as_ref() {
                    self.result_store.delete(reference)?;
                }
                Err(err)
            }
        }
    }

    pub(super) fn try_mark_failed(&self, job_id: &str, error: super::JobFailure) -> Result<bool> {
        let serialized_error =
            serde_json::to_value(&error).context("failed to serialize postgres job error")?;

        self.with_transaction(unix_now(), move |tx, _deferred| {
            let now = unix_now();
            let updated = tx
                .execute(
                    &format!(
                        "UPDATE {} SET state = $2, updated_at_unix = $3, result = NULL, result_ref = NULL, error = $4 \
                         WHERE job_id = $1 AND state = $5",
                        self.tables.jobs
                    ),
                    &[
                        &job_id,
                        &job_state_to_str(JobState::Failed),
                        &now,
                        &serialized_error,
                        &job_state_to_str(JobState::Running),
                    ],
                )
                .context("failed to update postgres job state to failed")?;
            if updated == 0 {
                return Ok(false);
            }

            tx.execute(
                &format!(
                    "UPDATE {} SET failed_total = failed_total + 1 WHERE singleton = TRUE",
                    self.tables.meta
                ),
                &[],
            )
            .context("failed to update postgres job metrics")?;
            Ok(true)
        })
    }

    pub(super) fn try_cancel(&self, job_id: &str) -> Result<Option<CancelJobOutcome>> {
        self.with_transaction(unix_now(), move |tx, deferred| {
            let Some(mut snapshot) = self.query_snapshot(tx, job_id, true)? else {
                return Ok(None);
            };

            let outcome = match snapshot.state {
                JobState::Queued | JobState::Running => {
                    if let Some(reference) = snapshot.result_ref.take() {
                        deferred.push(reference);
                    }

                    snapshot.state = JobState::Cancelled;
                    snapshot.updated_at_unix = unix_now();
                    snapshot.result = None;
                    snapshot.error = Some(super::JobFailure::new(
                        "job_cancelled",
                        "job was cancelled before completion",
                    ));
                    let error_value = snapshot
                        .error
                        .as_ref()
                        .map(serde_json::to_value)
                        .transpose()
                        .context("failed to serialize postgres cancellation error")?;

                    tx.execute(
                        &format!(
                            "UPDATE {} SET state = $2, updated_at_unix = $3, result = NULL, result_ref = NULL, error = $4 \
                             WHERE job_id = $1",
                            self.tables.jobs
                        ),
                        &[
                            &job_id,
                            &job_state_to_str(JobState::Cancelled),
                            &snapshot.updated_at_unix,
                            &error_value,
                        ],
                    )
                    .context("failed to update postgres job state to cancelled")?;
                    tx.execute(
                        &format!(
                            "UPDATE {} SET cancelled_total = cancelled_total + 1 WHERE singleton = TRUE",
                            self.tables.meta
                        ),
                        &[],
                    )
                    .context("failed to update postgres job metrics")?;
                    CancelJobOutcome::Cancelled(snapshot.clone())
                }
                JobState::Cancelled => CancelJobOutcome::AlreadyCancelled(snapshot),
                JobState::Succeeded | JobState::Failed => CancelJobOutcome::NotCancellable(snapshot),
            };

            Ok(Some(outcome))
        })
    }

    pub(super) fn try_metrics(
        &self,
        runtime_storage: &RuntimeStorageConfig,
    ) -> Result<JobMetricsSnapshot> {
        self.with_transaction(unix_now(), move |tx, _deferred| {
            let meta = tx
                .query_one(
                    &format!(
                        "SELECT created_total, started_total, succeeded_total, failed_total, cancelled_total, expired_total, \
                                recovery_runs_total, recovered_jobs_total, recovered_running_jobs_total, \
                                recovery_deleted_result_refs_total, cleanup_deleted_result_refs_total \
                         FROM {} WHERE singleton = TRUE",
                        self.tables.meta
                    ),
                    &[],
                )
                .context("failed to read postgres job store metrics")?;

            let mut snapshot = JobMetricsSnapshot {
                retention_secs: self.terminal_job_retention.as_secs(),
                created_total: i64_counter_to_u64(meta.get::<_, i64>("created_total"))?,
                started_total: i64_counter_to_u64(meta.get::<_, i64>("started_total"))?,
                succeeded_total: i64_counter_to_u64(meta.get::<_, i64>("succeeded_total"))?,
                failed_total: i64_counter_to_u64(meta.get::<_, i64>("failed_total"))?,
                cancelled_total: i64_counter_to_u64(meta.get::<_, i64>("cancelled_total"))?,
                expired_total: i64_counter_to_u64(meta.get::<_, i64>("expired_total"))?,
                recovery_runs_total: i64_counter_to_u64(
                    meta.get::<_, i64>("recovery_runs_total"),
                )?,
                recovered_jobs_total: i64_counter_to_u64(
                    meta.get::<_, i64>("recovered_jobs_total"),
                )?,
                recovered_running_jobs_total: i64_counter_to_u64(
                    meta.get::<_, i64>("recovered_running_jobs_total"),
                )?,
                recovery_deleted_result_refs_total: i64_counter_to_u64(
                    meta.get::<_, i64>("recovery_deleted_result_refs_total"),
                )?,
                cleanup_deleted_result_refs_total: i64_counter_to_u64(
                    meta.get::<_, i64>("cleanup_deleted_result_refs_total"),
                )?,
                storage: runtime_storage.clone(),
                ..JobMetricsSnapshot::default()
            };

            for row in tx
                .query(
                    &format!(
                        "SELECT state, COUNT(*) AS count FROM {} GROUP BY state",
                        self.tables.jobs
                    ),
                    &[],
                )
                .context("failed to aggregate postgres job states")?
            {
                let count = i64_counter_to_u64(row.get::<_, i64>("count"))?;
                match job_state_from_str(row.get::<_, &str>("state"))? {
                    JobState::Queued => snapshot.queued_jobs = count,
                    JobState::Running => snapshot.running_jobs = count,
                    JobState::Succeeded => snapshot.succeeded_jobs = count,
                    JobState::Failed => snapshot.failed_jobs = count,
                    JobState::Cancelled => snapshot.cancelled_jobs = count,
                }
            }

            snapshot.visible_jobs = snapshot.queued_jobs
                + snapshot.running_jobs
                + snapshot.succeeded_jobs
                + snapshot.failed_jobs
                + snapshot.cancelled_jobs;
            snapshot.active_jobs = snapshot.queued_jobs + snapshot.running_jobs;
            snapshot.terminal_jobs =
                snapshot.succeeded_jobs + snapshot.failed_jobs + snapshot.cancelled_jobs;

            Ok(snapshot)
        })
    }

    pub(super) fn try_is_cancelled(&self, job_id: &str) -> Result<bool> {
        self.with_transaction(unix_now(), move |tx, _deferred| {
            let state = tx
                .query_opt(
                    &format!("SELECT state FROM {} WHERE job_id = $1", self.tables.jobs),
                    &[&job_id],
                )
                .context("failed to read postgres cancellation state")?
                .map(|row| row.get::<_, String>("state"));
            Ok(state.as_deref() == Some(job_state_to_str(JobState::Cancelled)))
        })
    }

    pub(super) fn try_reconcile_inflight(&self) -> Result<Vec<JobSnapshot>> {
        self.with_transaction(unix_now(), move |tx, deferred| {
            let now = unix_now();
            let rows = tx
                .query(
                    &format!(
                        "SELECT job_id, state, created_at_unix, updated_at_unix, request, result, result_ref, error \
                         FROM {} \
                         WHERE state IN ('queued', 'running') \
                         ORDER BY job_id \
                         FOR UPDATE SKIP LOCKED",
                        self.tables.jobs
                    ),
                    &[],
                )
                .context("failed to query postgres in-flight jobs during startup reconciliation")?;

            let recovered_jobs = i64::try_from(rows.len()).unwrap_or(i64::MAX);
            let recovered_running_jobs = i64::try_from(
                rows.iter()
                    .filter(|row| row.get::<_, &str>("state") == job_state_to_str(JobState::Running))
                    .count(),
            )
            .unwrap_or(i64::MAX);
            let recovery_deleted_result_refs = i64::try_from(
                rows.iter()
                    .filter(|row| row.get::<_, Option<serde_json::Value>>("result_ref").is_some())
                    .count(),
            )
            .unwrap_or(i64::MAX);
            tx.execute(
                &format!(
                    "UPDATE {} SET recovery_runs_total = recovery_runs_total + 1, \
                     recovered_jobs_total = recovered_jobs_total + $1, \
                     recovered_running_jobs_total = recovered_running_jobs_total + $2, \
                     recovery_deleted_result_refs_total = recovery_deleted_result_refs_total + $3 \
                     WHERE singleton = TRUE",
                    self.tables.meta
                ),
                &[
                    &recovered_jobs,
                    &recovered_running_jobs,
                    &recovery_deleted_result_refs,
                ],
            )
            .context("failed to update postgres recovery metrics")?;

            let mut rescheduled = Vec::with_capacity(rows.len());
            for row in rows {
                let mut snapshot = snapshot_from_row(&row)?;
                if let Some(reference) = snapshot.result_ref.take() {
                    deferred.push(reference);
                }
                snapshot.state = JobState::Queued;
                snapshot.updated_at_unix = now;
                snapshot.result = None;
                snapshot.error = None;

                tx.execute(
                    &format!(
                        "UPDATE {} SET state = $2, updated_at_unix = $3, result = NULL, result_ref = NULL, error = NULL \
                         WHERE job_id = $1",
                        self.tables.jobs
                    ),
                    &[
                        &snapshot.id,
                        &job_state_to_str(JobState::Queued),
                        &snapshot.updated_at_unix,
                    ],
                )
                .context("failed to requeue postgres in-flight job during startup reconciliation")?;
                rescheduled.push(snapshot);
            }

            Ok(rescheduled)
        })
    }

    #[cfg(test)]
    pub(super) fn try_cleanup_expired_with_now(&self, now_unix: i64) -> Result<()> {
        self.with_transaction(now_unix, |_tx, _deferred| Ok(()))
    }

    pub(super) fn readiness_check(&self) -> Result<()> {
        let mut connection = self.connection()?;
        self.ensure_schema_on(&mut connection)?;
        connection
            .query_one(
                &format!("SELECT next_id FROM {} WHERE singleton = TRUE", self.tables.meta),
                &[],
            )
            .context("failed to query postgres job store readiness")?;
        Ok(())
    }

    fn with_transaction<T>(
        &self,
        now_unix: i64,
        f: impl FnOnce(&mut Transaction<'_>, &mut Vec<JobResultReference>) -> Result<T>,
    ) -> Result<T> {
        let mut connection = self.connection()?;
        let mut tx =
            connection.transaction().context("failed to open postgres job store transaction")?;
        let mut deferred = self.purge_expired_locked(&mut tx, now_unix)?;
        let value = f(&mut tx, &mut deferred)?;
        tx.commit().context("failed to commit postgres job store transaction")?;
        self.delete_deferred_results(&deferred)?;
        Ok(value)
    }

    fn connection(&self) -> Result<PooledConnection<PostgresConnectionManager<NoTls>>> {
        self.pool.get().context("failed to acquire postgres job store connection")
    }

    fn ensure_schema(&self) -> Result<()> {
        let mut connection = self.connection()?;
        self.ensure_schema_on(&mut connection)
    }

    fn ensure_schema_on(
        &self,
        connection: &mut PooledConnection<PostgresConnectionManager<NoTls>>,
    ) -> Result<()> {
        connection
            .batch_execute(&self.tables.bootstrap_sql())
            .context("failed to bootstrap postgres job store schema")
    }

    fn insert_job_locked(
        &self,
        tx: &mut Transaction<'_>,
        request: ScanArchiveRequest,
    ) -> Result<JobSnapshot> {
        let next_id = tx
            .query_one(
                &format!(
                    "UPDATE {} SET next_id = next_id + 1, created_total = created_total + 1 \
                     WHERE singleton = TRUE RETURNING next_id",
                    self.tables.meta
                ),
                &[],
            )
            .context("failed to allocate postgres job id")?
            .get::<_, i64>("next_id");
        let job_id = format!("job-{next_id:08}");
        let timestamp = unix_now();
        let request_value =
            serde_json::to_value(&request).context("failed to serialize postgres job request")?;

        tx.execute(
            &format!(
                "INSERT INTO {} (job_id, state, created_at_unix, updated_at_unix, request, result, result_ref, error) \
                 VALUES ($1, $2, $3, $4, $5, NULL, NULL, NULL)",
                self.tables.jobs
            ),
            &[
                &job_id,
                &job_state_to_str(JobState::Queued),
                &timestamp,
                &timestamp,
                &request_value,
            ],
        )
        .context("failed to insert postgres job snapshot")?;

        Ok(JobSnapshot {
            id: job_id,
            state: JobState::Queued,
            created_at_unix: timestamp,
            updated_at_unix: timestamp,
            request,
            result: None,
            result_ref: None,
            error: None,
        })
    }

    fn lookup_idempotency_locked(
        &self,
        tx: &mut Transaction<'_>,
        idempotency_key: &str,
        request: &ScanArchiveRequest,
    ) -> Result<Option<CreateJobOutcome>> {
        let row = tx
            .query_opt(
                &format!(
                    "SELECT jobs.job_id, jobs.state, jobs.created_at_unix, jobs.updated_at_unix, jobs.request, jobs.result, jobs.result_ref, jobs.error, idem.request AS idempotency_request \
                     FROM {} AS idem \
                     JOIN {} AS jobs ON jobs.job_id = idem.job_id \
                     WHERE idem.idempotency_key = $1",
                    self.tables.idempotency, self.tables.jobs
                ),
                &[&idempotency_key],
            )
            .context("failed to query postgres idempotency record")?;
        let Some(row) = row else {
            return Ok(None);
        };

        let persisted_request: ScanArchiveRequest =
            serde_json::from_value(row.get::<_, serde_json::Value>("idempotency_request"))
                .context("failed to deserialize postgres idempotency request")?;
        if persisted_request != *request {
            return Ok(Some(CreateJobOutcome::Conflict(row.get::<_, String>("job_id"))));
        }

        Ok(Some(CreateJobOutcome::Existing(snapshot_from_row(&row)?)))
    }

    fn query_snapshot(
        &self,
        tx: &mut Transaction<'_>,
        job_id: &str,
        for_update: bool,
    ) -> Result<Option<JobSnapshot>> {
        let mut query = format!(
            "SELECT job_id, state, created_at_unix, updated_at_unix, request, result, result_ref, error \
             FROM {} WHERE job_id = $1",
            self.tables.jobs
        );
        if for_update {
            query.push_str(" FOR UPDATE");
        }

        tx.query_opt(&query, &[&job_id])
            .context("failed to query postgres job snapshot")?
            .map(|row| snapshot_from_row(&row))
            .transpose()
    }

    fn purge_expired_locked(
        &self,
        tx: &mut Transaction<'_>,
        now_unix: i64,
    ) -> Result<Vec<JobResultReference>> {
        let retention = i64::try_from(self.terminal_job_retention.as_secs()).unwrap_or(i64::MAX);
        let cutoff = now_unix.saturating_sub(retention);
        let rows = tx
            .query(
                &format!(
                    "WITH expired AS ( \
                        SELECT job_id \
                        FROM {} \
                        WHERE state IN ('succeeded', 'failed', 'cancelled') AND updated_at_unix <= $1 \
                        FOR UPDATE SKIP LOCKED \
                     ) \
                     DELETE FROM {} AS jobs \
                     USING expired \
                     WHERE jobs.job_id = expired.job_id \
                     RETURNING jobs.result_ref",
                    self.tables.jobs, self.tables.jobs
                ),
                &[&cutoff],
            )
            .context("failed to purge expired postgres jobs")?;

        if rows.is_empty() {
            return Ok(Vec::new());
        }

        let result_ref_count = i64::try_from(
            rows.iter()
                .filter(|row| row.get::<_, Option<serde_json::Value>>("result_ref").is_some())
                .count(),
        )
        .unwrap_or(i64::MAX);
        tx.execute(
            &format!(
                "UPDATE {} SET expired_total = expired_total + $1, \
                 cleanup_deleted_result_refs_total = cleanup_deleted_result_refs_total + $2 \
                 WHERE singleton = TRUE",
                self.tables.meta
            ),
            &[&(rows.len() as i64), &result_ref_count],
        )
        .context("failed to update postgres expired job metrics")?;

        let mut deferred = Vec::with_capacity(rows.len());
        for row in rows {
            if let Some(value) = row.get::<_, Option<serde_json::Value>>("result_ref") {
                deferred.push(
                    serde_json::from_value(value)
                        .context("failed to deserialize expired postgres result reference")?,
                );
            }
        }

        Ok(deferred)
    }

    fn delete_deferred_results(&self, references: &[JobResultReference]) -> Result<()> {
        for reference in references {
            self.result_store.delete(reference)?;
        }
        Ok(())
    }
}

impl PostgresTableSet {
    fn new(table_prefix: &str) -> Self {
        let meta = format!("{table_prefix}_job_store_meta");
        let jobs = format!("{table_prefix}_job_store_jobs");
        let idempotency = format!("{table_prefix}_job_store_idempotency");
        let terminal_updated_index = format!("{table_prefix}_job_store_terminal_updated_idx");
        let idempotency_job_index = format!("{table_prefix}_job_store_idempotency_job_idx");

        Self {
            meta: quote_postgres_identifier(&meta).into_boxed_str().into(),
            jobs: quote_postgres_identifier(&jobs).into_boxed_str().into(),
            idempotency: quote_postgres_identifier(&idempotency).into_boxed_str().into(),
            terminal_updated_index: quote_postgres_identifier(&terminal_updated_index)
                .into_boxed_str()
                .into(),
            idempotency_job_index: quote_postgres_identifier(&idempotency_job_index)
                .into_boxed_str()
                .into(),
        }
    }

    fn bootstrap_sql(&self) -> String {
        format!(
            "CREATE TABLE IF NOT EXISTS {} (\
                singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton), \
                next_id BIGINT NOT NULL DEFAULT 0, \
                created_total BIGINT NOT NULL DEFAULT 0, \
                started_total BIGINT NOT NULL DEFAULT 0, \
                succeeded_total BIGINT NOT NULL DEFAULT 0, \
                failed_total BIGINT NOT NULL DEFAULT 0, \
                cancelled_total BIGINT NOT NULL DEFAULT 0, \
                expired_total BIGINT NOT NULL DEFAULT 0, \
                recovery_runs_total BIGINT NOT NULL DEFAULT 0, \
                recovered_jobs_total BIGINT NOT NULL DEFAULT 0, \
                recovered_running_jobs_total BIGINT NOT NULL DEFAULT 0, \
                recovery_deleted_result_refs_total BIGINT NOT NULL DEFAULT 0, \
                cleanup_deleted_result_refs_total BIGINT NOT NULL DEFAULT 0\
            );\
            ALTER TABLE {} ADD COLUMN IF NOT EXISTS recovery_runs_total BIGINT NOT NULL DEFAULT 0;\
            ALTER TABLE {} ADD COLUMN IF NOT EXISTS recovered_jobs_total BIGINT NOT NULL DEFAULT 0;\
            ALTER TABLE {} ADD COLUMN IF NOT EXISTS recovered_running_jobs_total BIGINT NOT NULL DEFAULT 0;\
            ALTER TABLE {} ADD COLUMN IF NOT EXISTS recovery_deleted_result_refs_total BIGINT NOT NULL DEFAULT 0;\
            ALTER TABLE {} ADD COLUMN IF NOT EXISTS cleanup_deleted_result_refs_total BIGINT NOT NULL DEFAULT 0;\
            INSERT INTO {} (singleton) VALUES (TRUE) ON CONFLICT (singleton) DO NOTHING;\
            CREATE TABLE IF NOT EXISTS {} (\
                job_id TEXT PRIMARY KEY, \
                state TEXT NOT NULL CHECK (state IN ('queued', 'running', 'cancelled', 'succeeded', 'failed')), \
                created_at_unix BIGINT NOT NULL, \
                updated_at_unix BIGINT NOT NULL, \
                request JSONB NOT NULL, \
                result JSONB, \
                result_ref JSONB, \
                error JSONB\
            );\
            CREATE TABLE IF NOT EXISTS {} (\
                idempotency_key TEXT PRIMARY KEY, \
                job_id TEXT NOT NULL REFERENCES {}(job_id) ON DELETE CASCADE, \
                request JSONB NOT NULL\
            );\
            CREATE INDEX IF NOT EXISTS {} ON {} (updated_at_unix) WHERE state IN ('succeeded', 'failed', 'cancelled');\
            CREATE INDEX IF NOT EXISTS {} ON {} (job_id);",
            self.meta,
            self.meta,
            self.meta,
            self.meta,
            self.meta,
            self.meta,
            self.meta,
            self.jobs,
            self.idempotency,
            self.jobs,
            self.terminal_updated_index,
            self.jobs,
            self.idempotency_job_index,
            self.idempotency,
        )
    }
}

impl fmt::Debug for PostgresJobStoreBackend {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("PostgresJobStoreBackend")
            .field("tables", &self.tables)
            .field("terminal_job_retention", &self.terminal_job_retention)
            .finish_non_exhaustive()
    }
}

fn snapshot_from_row(row: &Row) -> Result<JobSnapshot> {
    Ok(JobSnapshot {
        id: row.get("job_id"),
        state: job_state_from_str(row.get::<_, &str>("state"))?,
        created_at_unix: row.get("created_at_unix"),
        updated_at_unix: row.get("updated_at_unix"),
        request: serde_json::from_value(row.get::<_, serde_json::Value>("request"))
            .context("failed to deserialize postgres job request")?,
        result: row
            .get::<_, Option<serde_json::Value>>("result")
            .map(|value| {
                serde_json::from_value(value)
                    .context("failed to deserialize postgres inline job result")
            })
            .transpose()?,
        result_ref: row
            .get::<_, Option<serde_json::Value>>("result_ref")
            .map(|value| {
                serde_json::from_value(value)
                    .context("failed to deserialize postgres job result reference")
            })
            .transpose()?,
        error: row
            .get::<_, Option<serde_json::Value>>("error")
            .map(|value| {
                serde_json::from_value(value).context("failed to deserialize postgres job error")
            })
            .transpose()?,
    })
}

fn validate_postgres_table_prefix(prefix: &str) -> Result<()> {
    let mut chars = prefix.chars();
    let Some(first) = chars.next() else {
        return Err(anyhow!("postgres table prefix must not be empty"));
    };
    if !(first == '_' || first.is_ascii_alphabetic()) {
        return Err(anyhow!("postgres table prefix must start with an ASCII letter or underscore"));
    }
    if !chars.all(|ch| ch == '_' || ch.is_ascii_alphanumeric()) {
        return Err(anyhow!(
            "postgres table prefix may contain only ASCII letters, digits, and underscores"
        ));
    }
    Ok(())
}

fn quote_postgres_identifier(identifier: &str) -> String {
    format!("\"{identifier}\"")
}

fn i64_counter_to_u64(value: i64) -> Result<u64> {
    u64::try_from(value).map_err(|_| anyhow!("postgres counter must be non-negative, got {value}"))
}

pub(super) fn run_postgres_blocking<T>(f: impl FnOnce() -> Result<T>) -> Result<T> {
    if tokio::runtime::Handle::try_current().is_ok() {
        tokio::task::block_in_place(f)
    } else {
        f()
    }
}
