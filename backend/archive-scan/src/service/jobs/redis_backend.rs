use super::{
    job_state_from_str, job_state_to_str, unix_now, CancelJobOutcome, CreateJobOutcome,
    JobMetricsSnapshot, JobResultReference, JobSnapshot, JobState, ResultStore,
    RuntimeStorageConfig, ScanArchiveRequest, ScanArchiveResponse,
};
use anyhow::{anyhow, Context, Result};
use r2d2::{Pool, PooledConnection};
use redis::Script;
use std::{sync::Arc, time::Duration};

type RedisMetricsTuple = (
    Option<i64>,
    Option<i64>,
    Option<i64>,
    Option<i64>,
    Option<i64>,
    Option<i64>,
    Option<i64>,
    Option<i64>,
    Option<i64>,
    Option<i64>,
    Option<i64>,
);

type RedisSnapshotTuple = (
    Option<String>,
    Option<i64>,
    Option<i64>,
    Option<String>,
    Option<String>,
    Option<String>,
    Option<String>,
);

const REDIS_CREATE_JOB_SCRIPT: &str = r#"
local next_id = redis.call('HINCRBY', KEYS[1], 'next_id', 1)
redis.call('HINCRBY', KEYS[1], 'created_total', 1)
local job_id = string.format('job-%08d', next_id)
local job_key = KEYS[2] .. job_id
redis.call('HSET', job_key,
  'state', 'queued',
  'created_at_unix', ARGV[1],
  'updated_at_unix', ARGV[1],
  'request', ARGV[2]
)
redis.call('SADD', KEYS[3], job_id)
return job_id
"#;

const REDIS_CREATE_JOB_WITH_IDEMPOTENCY_SCRIPT: &str = r#"
local existing_job_id = redis.call('HGET', KEYS[4], ARGV[3])
if existing_job_id then
  local existing_job_key = KEYS[2] .. existing_job_id
  if redis.call('EXISTS', existing_job_key) == 1 then
    local existing_request = redis.call('HGET', KEYS[5], ARGV[3]) or ''
    return {'existing', existing_job_id, existing_request}
  end

  redis.call('HDEL', KEYS[4], ARGV[3])
  redis.call('HDEL', KEYS[5], ARGV[3])
  redis.call('HDEL', KEYS[6], existing_job_id)
end

local next_id = redis.call('HINCRBY', KEYS[1], 'next_id', 1)
redis.call('HINCRBY', KEYS[1], 'created_total', 1)
local job_id = string.format('job-%08d', next_id)
local job_key = KEYS[2] .. job_id
redis.call('HSET', job_key,
  'state', 'queued',
  'created_at_unix', ARGV[1],
  'updated_at_unix', ARGV[1],
  'request', ARGV[2]
)
redis.call('SADD', KEYS[3], job_id)
redis.call('HSET', KEYS[4], ARGV[3], job_id)
redis.call('HSET', KEYS[5], ARGV[3], ARGV[2])
redis.call('HSET', KEYS[6], job_id, ARGV[3])
return {'created', job_id, ''}
"#;

const REDIS_MARK_RUNNING_SCRIPT: &str = r#"
local job_key = KEYS[2] .. ARGV[1]
local state = redis.call('HGET', job_key, 'state')
if state ~= 'queued' then
  return 0
end

redis.call('HSET', job_key,
  'state', 'running',
  'updated_at_unix', ARGV[2]
)
redis.call('SREM', KEYS[3], ARGV[1])
redis.call('SADD', KEYS[4], ARGV[1])
redis.call('HINCRBY', KEYS[1], 'started_total', 1)
return 1
"#;

const REDIS_MARK_SUCCEEDED_SCRIPT: &str = r#"
local job_key = KEYS[2] .. ARGV[1]
local state = redis.call('HGET', job_key, 'state')
if state ~= 'running' then
  return 0
end

redis.call('HSET', job_key,
  'state', 'succeeded',
  'updated_at_unix', ARGV[2]
)
if ARGV[3] == '1' then
  redis.call('HSET', job_key, 'result', ARGV[4])
else
  redis.call('HDEL', job_key, 'result')
end
if ARGV[5] == '1' then
  redis.call('HSET', job_key, 'result_ref', ARGV[6])
else
  redis.call('HDEL', job_key, 'result_ref')
end
redis.call('HDEL', job_key, 'error')
redis.call('SREM', KEYS[3], ARGV[1])
redis.call('SADD', KEYS[4], ARGV[1])
redis.call('ZADD', KEYS[5], ARGV[2], ARGV[1])
redis.call('HINCRBY', KEYS[1], 'succeeded_total', 1)
return 1
"#;

const REDIS_MARK_FAILED_SCRIPT: &str = r#"
local job_key = KEYS[2] .. ARGV[1]
local state = redis.call('HGET', job_key, 'state')
if state ~= 'running' then
  return {'0', ''}
end

local previous_result_ref = redis.call('HGET', job_key, 'result_ref') or ''
redis.call('HSET', job_key,
  'state', 'failed',
  'updated_at_unix', ARGV[2],
  'error', ARGV[3]
)
redis.call('HDEL', job_key, 'result')
redis.call('HDEL', job_key, 'result_ref')
redis.call('SREM', KEYS[3], ARGV[1])
redis.call('SADD', KEYS[4], ARGV[1])
redis.call('ZADD', KEYS[5], ARGV[2], ARGV[1])
redis.call('HINCRBY', KEYS[1], 'failed_total', 1)
return {'1', previous_result_ref}
"#;

const REDIS_CANCEL_SCRIPT: &str = r#"
local job_key = KEYS[2] .. ARGV[1]
local state = redis.call('HGET', job_key, 'state')
if not state then
  return {'missing', ''}
end
if state ~= 'queued' and state ~= 'running' then
  if state == 'cancelled' then
    return {'already_cancelled', ''}
  end
  return {'not_cancellable', ''}
end

local previous_result_ref = redis.call('HGET', job_key, 'result_ref') or ''
redis.call('HSET', job_key,
  'state', 'cancelled',
  'updated_at_unix', ARGV[2],
  'error', ARGV[3]
)
redis.call('HDEL', job_key, 'result')
redis.call('HDEL', job_key, 'result_ref')
if state == 'queued' then
  redis.call('SREM', KEYS[3], ARGV[1])
else
  redis.call('SREM', KEYS[4], ARGV[1])
end
redis.call('SADD', KEYS[5], ARGV[1])
redis.call('ZADD', KEYS[6], ARGV[2], ARGV[1])
redis.call('HINCRBY', KEYS[1], 'cancelled_total', 1)
return {'cancelled', previous_result_ref}
"#;

const REDIS_RECONCILE_INFLIGHT_SCRIPT: &str = r#"
local job_key = KEYS[1] .. ARGV[1]
local state = redis.call('HGET', job_key, 'state')
if not state then
  return {'missing', ''}
end
if state ~= 'queued' and state ~= 'running' then
  return {state, ''}
end

local previous_result_ref = redis.call('HGET', job_key, 'result_ref') or ''
redis.call('HSET', job_key,
  'state', 'queued',
  'updated_at_unix', ARGV[2]
)
redis.call('HDEL', job_key, 'result')
redis.call('HDEL', job_key, 'result_ref')
redis.call('HDEL', job_key, 'error')
redis.call('SREM', KEYS[3], ARGV[1])
redis.call('SADD', KEYS[2], ARGV[1])
redis.call('HINCRBY', KEYS[4], 'recovered_jobs_total', 1)
if state == 'running' then
  redis.call('HINCRBY', KEYS[4], 'recovered_running_jobs_total', 1)
end
if previous_result_ref ~= '' then
  redis.call('HINCRBY', KEYS[4], 'recovery_deleted_result_refs_total', 1)
end
return {'requeued', previous_result_ref}
"#;

const REDIS_PURGE_EXPIRED_SCRIPT: &str = r#"
local job_ids = redis.call('ZRANGEBYSCORE', KEYS[1], '-inf', ARGV[1], 'LIMIT', 0, ARGV[2])
local response = {tostring(#job_ids)}
if #job_ids == 0 then
  return response
end

local result_ref_count = 0
for _, job_id in ipairs(job_ids) do
  local job_key = KEYS[2] .. job_id
  local state = redis.call('HGET', job_key, 'state')
  local result_ref = redis.call('HGET', job_key, 'result_ref')
  if state == 'succeeded' then
    redis.call('SREM', KEYS[3], job_id)
  elseif state == 'failed' then
    redis.call('SREM', KEYS[4], job_id)
  elseif state == 'cancelled' then
    redis.call('SREM', KEYS[5], job_id)
  end

  redis.call('DEL', job_key)
  redis.call('ZREM', KEYS[1], job_id)

  local idempotency_key = redis.call('HGET', KEYS[8], job_id)
  if idempotency_key then
    redis.call('HDEL', KEYS[6], idempotency_key)
    redis.call('HDEL', KEYS[7], idempotency_key)
    redis.call('HDEL', KEYS[8], job_id)
  end

  if result_ref then
    result_ref_count = result_ref_count + 1
    table.insert(response, result_ref)
  end
end

redis.call('HINCRBY', KEYS[9], 'expired_total', #job_ids)
redis.call('HINCRBY', KEYS[9], 'cleanup_deleted_result_refs_total', result_ref_count)
return response
"#;

#[derive(Clone)]
pub(super) struct RedisJobStoreBackend {
    pool: Pool<redis::Client>,
    keys: Arc<RedisKeySet>,
    scripts: Arc<RedisScriptSet>,
    terminal_job_retention: Duration,
    result_store: ResultStore,
    cleanup_batch_size: usize,
}

#[derive(Debug)]
struct RedisKeySet {
    meta: Arc<str>,
    jobs_prefix: Arc<str>,
    idempotency_jobs: Arc<str>,
    idempotency_requests: Arc<str>,
    idempotency_job_index: Arc<str>,
    queued: Arc<str>,
    running: Arc<str>,
    succeeded: Arc<str>,
    failed: Arc<str>,
    cancelled: Arc<str>,
    terminal_updated: Arc<str>,
}

#[derive(Debug)]
struct RedisScriptSet {
    create_job: Script,
    create_job_with_idempotency: Script,
    mark_running: Script,
    mark_succeeded: Script,
    mark_failed: Script,
    cancel: Script,
    reconcile_inflight: Script,
    purge_expired: Script,
}

impl RedisJobStoreBackend {
    pub(super) fn new(
        redis_url: &str,
        key_prefix: &str,
        max_connections: u32,
        terminal_job_retention: Duration,
        result_store: ResultStore,
        cleanup_batch_size: usize,
    ) -> Result<Self> {
        let client =
            redis::Client::open(redis_url).context("failed to parse redis job store url")?;
        let pool = Pool::builder()
            .max_size(max_connections.max(1))
            .build(client)
            .context("failed to build redis job store connection pool")?;

        let backend = Self {
            pool,
            keys: Arc::new(RedisKeySet::new(key_prefix)),
            scripts: Arc::new(RedisScriptSet::new()),
            terminal_job_retention,
            result_store,
            cleanup_batch_size: cleanup_batch_size.max(1),
        };
        backend.readiness_check()?;
        Ok(backend)
    }

    pub(super) fn try_create_job(&self, request: ScanArchiveRequest) -> Result<JobSnapshot> {
        self.cleanup_expired(unix_now())?;

        let request_json =
            serde_json::to_string(&request).context("failed to serialize redis job request")?;
        let now = unix_now();
        let mut connection = self.connection()?;
        let job_id: String = self
            .scripts
            .create_job
            .prepare_invoke()
            .key(self.keys.meta.as_ref())
            .key(self.keys.jobs_prefix.as_ref())
            .key(self.keys.queued.as_ref())
            .arg(now)
            .arg(request_json)
            .invoke(&mut *connection)
            .context("failed to create redis job snapshot")?;
        self.fetch_snapshot(&mut connection, &job_id)?
            .ok_or_else(|| anyhow!("redis job {job_id} disappeared after creation"))
    }

    pub(super) fn try_resolve_idempotent_job(
        &self,
        idempotency_key: &str,
        request: &ScanArchiveRequest,
    ) -> Result<Option<CreateJobOutcome>> {
        self.cleanup_expired(unix_now())?;

        let mut connection = self.connection()?;
        let (job_id, request_json): (Option<String>, Option<String>) = redis::pipe()
            .cmd("HGET")
            .arg(self.keys.idempotency_jobs.as_ref())
            .arg(idempotency_key)
            .cmd("HGET")
            .arg(self.keys.idempotency_requests.as_ref())
            .arg(idempotency_key)
            .query(&mut *connection)
            .context("failed to query redis idempotency record")?;
        let Some(job_id) = job_id else {
            return Ok(None);
        };
        let Some(request_json) = request_json else {
            return Err(anyhow!(
                "redis idempotency record {idempotency_key} is missing its request payload"
            ));
        };

        let Some(snapshot) = self.fetch_snapshot(&mut connection, &job_id)? else {
            self.delete_stale_idempotency(&mut connection, idempotency_key, &job_id)?;
            return Ok(None);
        };

        let persisted_request: ScanArchiveRequest = serde_json::from_str(&request_json)
            .context("failed to deserialize redis idempotency request")?;
        if persisted_request == *request {
            Ok(Some(CreateJobOutcome::Existing(snapshot)))
        } else {
            Ok(Some(CreateJobOutcome::Conflict(job_id)))
        }
    }

    pub(super) fn try_create_job_with_idempotency(
        &self,
        request: ScanArchiveRequest,
        idempotency_key: String,
    ) -> Result<CreateJobOutcome> {
        self.cleanup_expired(unix_now())?;

        let request_json =
            serde_json::to_string(&request).context("failed to serialize redis job request")?;
        let now = unix_now();
        let mut connection = self.connection()?;
        let response: Vec<String> = self
            .scripts
            .create_job_with_idempotency
            .prepare_invoke()
            .key(self.keys.meta.as_ref())
            .key(self.keys.jobs_prefix.as_ref())
            .key(self.keys.queued.as_ref())
            .key(self.keys.idempotency_jobs.as_ref())
            .key(self.keys.idempotency_requests.as_ref())
            .key(self.keys.idempotency_job_index.as_ref())
            .arg(now)
            .arg(&request_json)
            .arg(&idempotency_key)
            .invoke(&mut *connection)
            .context("failed to create redis idempotent job")?;

        let status = redis_response_field(&response, 0, "redis idempotent create status")?;
        let job_id = redis_response_field(&response, 1, "redis idempotent create job id")?;
        let snapshot = self
            .fetch_snapshot(&mut connection, job_id)?
            .ok_or_else(|| anyhow!("redis job {job_id} disappeared after idempotent create"))?;

        match status {
            "created" => Ok(CreateJobOutcome::Created(snapshot)),
            "existing" => {
                let existing_request_json =
                    redis_response_field(&response, 2, "redis idempotent request payload")?;
                let existing_request: ScanArchiveRequest =
                    serde_json::from_str(existing_request_json)
                        .context("failed to deserialize redis idempotency request")?;
                if existing_request == request {
                    Ok(CreateJobOutcome::Existing(snapshot))
                } else {
                    Ok(CreateJobOutcome::Conflict(job_id.to_owned()))
                }
            }
            other => Err(anyhow!("unexpected redis idempotent create status: {other}")),
        }
    }

    pub(super) fn try_get(&self, job_id: &str) -> Result<Option<JobSnapshot>> {
        self.cleanup_expired(unix_now())?;
        let mut connection = self.connection()?;
        self.fetch_snapshot(&mut connection, job_id)
    }

    pub(super) fn try_mark_running(&self, job_id: &str) -> Result<bool> {
        self.cleanup_expired(unix_now())?;

        let mut connection = self.connection()?;
        let updated: i32 = self
            .scripts
            .mark_running
            .prepare_invoke()
            .key(self.keys.meta.as_ref())
            .key(self.keys.jobs_prefix.as_ref())
            .key(self.keys.queued.as_ref())
            .key(self.keys.running.as_ref())
            .arg(job_id)
            .arg(unix_now())
            .invoke(&mut *connection)
            .context("failed to update redis job state to running")?;
        Ok(updated == 1)
    }

    pub(super) fn try_mark_succeeded(
        &self,
        job_id: &str,
        result: ScanArchiveResponse,
    ) -> Result<bool> {
        self.cleanup_expired(unix_now())?;

        let persisted = self.result_store.persist(job_id, &result)?;
        let result_json = persisted
            .inline_result
            .as_ref()
            .map(serde_json::to_string)
            .transpose()
            .context("failed to serialize redis inline job result")?;
        let result_ref_json = persisted
            .reference
            .as_ref()
            .map(serde_json::to_string)
            .transpose()
            .context("failed to serialize redis job result reference")?;

        let outcome = (|| {
            let mut connection = self.connection()?;
            let updated: i32 = self
                .scripts
                .mark_succeeded
                .prepare_invoke()
                .key(self.keys.meta.as_ref())
                .key(self.keys.jobs_prefix.as_ref())
                .key(self.keys.running.as_ref())
                .key(self.keys.succeeded.as_ref())
                .key(self.keys.terminal_updated.as_ref())
                .arg(job_id)
                .arg(unix_now())
                .arg(if result_json.is_some() { "1" } else { "0" })
                .arg(result_json.as_deref().unwrap_or(""))
                .arg(if result_ref_json.is_some() { "1" } else { "0" })
                .arg(result_ref_json.as_deref().unwrap_or(""))
                .invoke(&mut *connection)
                .context("failed to persist redis completed job result")?;
            Ok(updated == 1)
        })();

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
        self.cleanup_expired(unix_now())?;

        let error_json =
            serde_json::to_string(&error).context("failed to serialize redis job error")?;
        let mut connection = self.connection()?;
        let response: Vec<String> = self
            .scripts
            .mark_failed
            .prepare_invoke()
            .key(self.keys.meta.as_ref())
            .key(self.keys.jobs_prefix.as_ref())
            .key(self.keys.running.as_ref())
            .key(self.keys.failed.as_ref())
            .key(self.keys.terminal_updated.as_ref())
            .arg(job_id)
            .arg(unix_now())
            .arg(error_json)
            .invoke(&mut *connection)
            .context("failed to update redis job state to failed")?;

        let updated = redis_response_field(&response, 0, "redis failed transition marker")? == "1";
        if updated {
            if let Some(reference) = redis_optional_reference(&response, 1)? {
                self.result_store.delete(&reference)?;
            }
        }
        Ok(updated)
    }

    pub(super) fn try_cancel(&self, job_id: &str) -> Result<Option<CancelJobOutcome>> {
        self.cleanup_expired(unix_now())?;

        let error_json = serde_json::to_string(&super::JobFailure::new(
            "job_cancelled",
            "job was cancelled before completion",
        ))
        .context("failed to serialize redis cancellation error")?;
        let mut connection = self.connection()?;
        let response: Vec<String> = self
            .scripts
            .cancel
            .prepare_invoke()
            .key(self.keys.meta.as_ref())
            .key(self.keys.jobs_prefix.as_ref())
            .key(self.keys.queued.as_ref())
            .key(self.keys.running.as_ref())
            .key(self.keys.cancelled.as_ref())
            .key(self.keys.terminal_updated.as_ref())
            .arg(job_id)
            .arg(unix_now())
            .arg(error_json)
            .invoke(&mut *connection)
            .context("failed to update redis job state to cancelled")?;

        let status = redis_response_field(&response, 0, "redis cancellation status")?;
        if status == "missing" {
            return Ok(None);
        }
        if let Some(reference) = redis_optional_reference(&response, 1)? {
            self.result_store.delete(&reference)?;
        }

        let snapshot = self
            .fetch_snapshot(&mut connection, job_id)?
            .ok_or_else(|| anyhow!("redis job {job_id} disappeared after cancellation"))?;
        let outcome = match status {
            "cancelled" => CancelJobOutcome::Cancelled(snapshot),
            "already_cancelled" => CancelJobOutcome::AlreadyCancelled(snapshot),
            "not_cancellable" => CancelJobOutcome::NotCancellable(snapshot),
            other => return Err(anyhow!("unexpected redis cancellation status: {other}")),
        };
        Ok(Some(outcome))
    }

    pub(super) fn try_metrics(
        &self,
        runtime_storage: &RuntimeStorageConfig,
    ) -> Result<JobMetricsSnapshot> {
        self.cleanup_expired(unix_now())?;

        let mut connection = self.connection()?;
        let (
            created_total,
            started_total,
            succeeded_total,
            failed_total,
            cancelled_total,
            expired_total,
            recovery_runs_total,
            recovered_jobs_total,
            recovered_running_jobs_total,
            recovery_deleted_result_refs_total,
            cleanup_deleted_result_refs_total,
        ): RedisMetricsTuple = redis::cmd("HMGET")
            .arg(self.keys.meta.as_ref())
            .arg(&[
                "created_total",
                "started_total",
                "succeeded_total",
                "failed_total",
                "cancelled_total",
                "expired_total",
                "recovery_runs_total",
                "recovered_jobs_total",
                "recovered_running_jobs_total",
                "recovery_deleted_result_refs_total",
                "cleanup_deleted_result_refs_total",
            ])
            .query(&mut *connection)
            .context("failed to read redis job store metrics")?;
        let queued_jobs: u64 = redis::cmd("SCARD")
            .arg(self.keys.queued.as_ref())
            .query(&mut *connection)
            .context("failed to count queued redis jobs")?;
        let running_jobs: u64 = redis::cmd("SCARD")
            .arg(self.keys.running.as_ref())
            .query(&mut *connection)
            .context("failed to count running redis jobs")?;
        let succeeded_jobs: u64 = redis::cmd("SCARD")
            .arg(self.keys.succeeded.as_ref())
            .query(&mut *connection)
            .context("failed to count succeeded redis jobs")?;
        let failed_jobs: u64 = redis::cmd("SCARD")
            .arg(self.keys.failed.as_ref())
            .query(&mut *connection)
            .context("failed to count failed redis jobs")?;
        let cancelled_jobs: u64 = redis::cmd("SCARD")
            .arg(self.keys.cancelled.as_ref())
            .query(&mut *connection)
            .context("failed to count cancelled redis jobs")?;

        let active_jobs = queued_jobs.saturating_add(running_jobs);
        let terminal_jobs =
            succeeded_jobs.saturating_add(failed_jobs).saturating_add(cancelled_jobs);

        Ok(JobMetricsSnapshot {
            retention_secs: self.terminal_job_retention.as_secs(),
            visible_jobs: active_jobs.saturating_add(terminal_jobs),
            active_jobs,
            terminal_jobs,
            queued_jobs,
            running_jobs,
            succeeded_jobs,
            failed_jobs,
            cancelled_jobs,
            created_total: redis_counter(created_total),
            started_total: redis_counter(started_total),
            succeeded_total: redis_counter(succeeded_total),
            failed_total: redis_counter(failed_total),
            cancelled_total: redis_counter(cancelled_total),
            expired_total: redis_counter(expired_total),
            recovery_runs_total: redis_counter(recovery_runs_total),
            recovered_jobs_total: redis_counter(recovered_jobs_total),
            recovered_running_jobs_total: redis_counter(recovered_running_jobs_total),
            recovery_deleted_result_refs_total: redis_counter(recovery_deleted_result_refs_total),
            cleanup_deleted_result_refs_total: redis_counter(cleanup_deleted_result_refs_total),
            storage: runtime_storage.clone(),
            ..JobMetricsSnapshot::default()
        })
    }

    pub(super) fn try_is_cancelled(&self, job_id: &str) -> Result<bool> {
        self.cleanup_expired(unix_now())?;
        let mut connection = self.connection()?;
        let state: Option<String> = redis::cmd("HGET")
            .arg(self.keys.job_key(job_id))
            .arg("state")
            .query(&mut *connection)
            .context("failed to read redis cancellation state")?;
        Ok(state.as_deref() == Some(job_state_to_str(JobState::Cancelled)))
    }

    pub(super) fn try_reconcile_inflight(&self) -> Result<Vec<JobSnapshot>> {
        self.cleanup_expired(unix_now())?;

        let mut connection = self.connection()?;
        let _: i64 = redis::cmd("HINCRBY")
            .arg(self.keys.meta.as_ref())
            .arg("recovery_runs_total")
            .arg(1)
            .query(&mut *connection)
            .context("failed to update redis recovery metrics")?;
        let queued_ids: Vec<String> = redis::cmd("SMEMBERS")
            .arg(self.keys.queued.as_ref())
            .query(&mut *connection)
            .context("failed to read queued redis jobs during startup reconciliation")?;
        let running_ids: Vec<String> = redis::cmd("SMEMBERS")
            .arg(self.keys.running.as_ref())
            .query(&mut *connection)
            .context("failed to read running redis jobs during startup reconciliation")?;

        let mut job_ids = std::collections::BTreeSet::new();
        job_ids.extend(queued_ids);
        job_ids.extend(running_ids);

        let now = unix_now();
        let mut rescheduled = Vec::with_capacity(job_ids.len());
        for job_id in job_ids {
            let response: Vec<String> = self
                .scripts
                .reconcile_inflight
                .prepare_invoke()
                .key(self.keys.jobs_prefix.as_ref())
                .key(self.keys.queued.as_ref())
                .key(self.keys.running.as_ref())
                .key(self.keys.meta.as_ref())
                .arg(&job_id)
                .arg(now)
                .invoke(&mut *connection)
                .context("failed to reconcile redis in-flight job")?;

            let status =
                redis_response_field(&response, 0, "redis inflight reconciliation status")?;
            if status != "requeued" {
                continue;
            }
            if let Some(reference) = redis_optional_reference(&response, 1)? {
                self.result_store.delete(&reference)?;
            }

            let Some(snapshot) = self.fetch_snapshot(&mut connection, &job_id)? else {
                return Err(anyhow!("redis job {job_id} disappeared after startup reconciliation"));
            };
            rescheduled.push(snapshot);
        }

        Ok(rescheduled)
    }

    #[cfg(test)]
    pub(super) fn try_cleanup_expired_with_now(&self, now_unix: i64) -> Result<()> {
        self.cleanup_expired(now_unix)
    }

    pub(super) fn readiness_check(&self) -> Result<()> {
        let mut connection = self.connection()?;
        let pong: String =
            redis::cmd("PING").query(&mut *connection).context("failed to ping redis job store")?;
        if pong != "PONG" {
            return Err(anyhow!("unexpected redis ping response: {pong}"));
        }
        Ok(())
    }

    fn cleanup_expired(&self, now_unix: i64) -> Result<()> {
        let retention = i64::try_from(self.terminal_job_retention.as_secs()).unwrap_or(i64::MAX);
        let cutoff = now_unix.saturating_sub(retention);

        loop {
            let mut connection = self.connection()?;
            let response: Vec<String> = self
                .scripts
                .purge_expired
                .prepare_invoke()
                .key(self.keys.terminal_updated.as_ref())
                .key(self.keys.jobs_prefix.as_ref())
                .key(self.keys.succeeded.as_ref())
                .key(self.keys.failed.as_ref())
                .key(self.keys.cancelled.as_ref())
                .key(self.keys.idempotency_jobs.as_ref())
                .key(self.keys.idempotency_requests.as_ref())
                .key(self.keys.idempotency_job_index.as_ref())
                .key(self.keys.meta.as_ref())
                .arg(cutoff)
                .arg(self.cleanup_batch_size)
                .invoke(&mut *connection)
                .context("failed to purge expired redis jobs")?;

            let deleted = response
                .first()
                .ok_or_else(|| anyhow!("redis purge response is missing the deleted count"))?
                .parse::<usize>()
                .context("failed to parse redis purge deleted count")?;
            for value in response.iter().skip(1) {
                let reference: JobResultReference = serde_json::from_str(value)
                    .context("failed to deserialize expired redis result reference")?;
                self.result_store.delete(&reference)?;
            }
            if deleted == 0 {
                return Ok(());
            }
        }
    }

    fn connection(&self) -> Result<PooledConnection<redis::Client>> {
        self.pool.get().context("failed to acquire redis job store connection")
    }

    fn fetch_snapshot(
        &self,
        connection: &mut PooledConnection<redis::Client>,
        job_id: &str,
    ) -> Result<Option<JobSnapshot>> {
        let job_key = self.keys.job_key(job_id);
        let (
            state,
            created_at_unix,
            updated_at_unix,
            request_json,
            result_json,
            result_ref_json,
            error_json,
        ): RedisSnapshotTuple = redis::cmd("HMGET")
            .arg(job_key.as_str())
            .arg(&[
                "state",
                "created_at_unix",
                "updated_at_unix",
                "request",
                "result",
                "result_ref",
                "error",
            ])
            .query(&mut **connection)
            .context("failed to query redis job snapshot")?;
        let Some(state) = state else {
            return Ok(None);
        };

        let request_json = request_json
            .ok_or_else(|| anyhow!("redis job {job_id} is missing its request payload"))?;
        let created_at_unix = created_at_unix
            .ok_or_else(|| anyhow!("redis job {job_id} is missing created_at_unix"))?;
        let updated_at_unix = updated_at_unix
            .ok_or_else(|| anyhow!("redis job {job_id} is missing updated_at_unix"))?;

        Ok(Some(JobSnapshot {
            id: job_id.to_owned(),
            state: job_state_from_str(&state)?,
            created_at_unix,
            updated_at_unix,
            request: serde_json::from_str(&request_json)
                .context("failed to deserialize redis job request")?,
            result: result_json
                .as_deref()
                .map(|value| {
                    serde_json::from_str(value)
                        .context("failed to deserialize redis inline job result")
                })
                .transpose()?,
            result_ref: result_ref_json
                .as_deref()
                .map(|value| {
                    serde_json::from_str(value)
                        .context("failed to deserialize redis job result reference")
                })
                .transpose()?,
            error: error_json
                .as_deref()
                .map(|value| {
                    serde_json::from_str(value).context("failed to deserialize redis job error")
                })
                .transpose()?,
        }))
    }

    fn delete_stale_idempotency(
        &self,
        connection: &mut PooledConnection<redis::Client>,
        idempotency_key: &str,
        job_id: &str,
    ) -> Result<()> {
        let _: () = redis::pipe()
            .atomic()
            .cmd("HDEL")
            .arg(self.keys.idempotency_jobs.as_ref())
            .arg(idempotency_key)
            .cmd("HDEL")
            .arg(self.keys.idempotency_requests.as_ref())
            .arg(idempotency_key)
            .cmd("HDEL")
            .arg(self.keys.idempotency_job_index.as_ref())
            .arg(job_id)
            .query(&mut **connection)
            .context("failed to delete stale redis idempotency record")?;
        Ok(())
    }
}

impl RedisKeySet {
    fn new(key_prefix: &str) -> Self {
        let prefix = format!("{key_prefix}:job_store");
        Self {
            meta: format!("{prefix}:meta").into_boxed_str().into(),
            jobs_prefix: format!("{prefix}:jobs:").into_boxed_str().into(),
            idempotency_jobs: format!("{prefix}:idempotency:jobs").into_boxed_str().into(),
            idempotency_requests: format!("{prefix}:idempotency:requests").into_boxed_str().into(),
            idempotency_job_index: format!("{prefix}:idempotency:job_index")
                .into_boxed_str()
                .into(),
            queued: format!("{prefix}:state:queued").into_boxed_str().into(),
            running: format!("{prefix}:state:running").into_boxed_str().into(),
            succeeded: format!("{prefix}:state:succeeded").into_boxed_str().into(),
            failed: format!("{prefix}:state:failed").into_boxed_str().into(),
            cancelled: format!("{prefix}:state:cancelled").into_boxed_str().into(),
            terminal_updated: format!("{prefix}:state:terminal_updated").into_boxed_str().into(),
        }
    }

    fn job_key(&self, job_id: &str) -> String {
        let mut key = String::with_capacity(self.jobs_prefix.len() + job_id.len());
        key.push_str(self.jobs_prefix.as_ref());
        key.push_str(job_id);
        key
    }
}

impl RedisScriptSet {
    fn new() -> Self {
        Self {
            create_job: Script::new(REDIS_CREATE_JOB_SCRIPT),
            create_job_with_idempotency: Script::new(REDIS_CREATE_JOB_WITH_IDEMPOTENCY_SCRIPT),
            mark_running: Script::new(REDIS_MARK_RUNNING_SCRIPT),
            mark_succeeded: Script::new(REDIS_MARK_SUCCEEDED_SCRIPT),
            mark_failed: Script::new(REDIS_MARK_FAILED_SCRIPT),
            cancel: Script::new(REDIS_CANCEL_SCRIPT),
            reconcile_inflight: Script::new(REDIS_RECONCILE_INFLIGHT_SCRIPT),
            purge_expired: Script::new(REDIS_PURGE_EXPIRED_SCRIPT),
        }
    }
}

fn redis_counter(value: Option<i64>) -> u64 {
    value.and_then(|count| u64::try_from(count).ok()).unwrap_or(0)
}

fn redis_response_field<'a>(
    response: &'a [String],
    index: usize,
    description: &str,
) -> Result<&'a str> {
    response
        .get(index)
        .map(String::as_str)
        .ok_or_else(|| anyhow!("{description} is missing from redis script response"))
}

fn redis_optional_reference(
    response: &[String],
    index: usize,
) -> Result<Option<JobResultReference>> {
    let Some(value) = response.get(index) else {
        return Ok(None);
    };
    if value.is_empty() {
        return Ok(None);
    }

    serde_json::from_str(value)
        .map(Some)
        .context("failed to deserialize redis job result reference")
}
