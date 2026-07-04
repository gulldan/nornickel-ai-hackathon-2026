use super::super::config::{ExtractMetadataBackendKind, ExtractMetadataConfig};
use crate::extract::{ExtractArchiveResult, ExtractDestinationSummary, ExtractedEntry};
use crate::row::EntryKind;
use anyhow::{anyhow, Context, Result};
use postgres::{types::ToSql, NoTls, Transaction};
use r2d2::{Pool, PooledConnection};
use r2d2_postgres::PostgresConnectionManager;
use std::{
    fmt::Write as _,
    fs::{self, File},
    io::{BufWriter, Write},
    path::PathBuf,
    sync::Arc,
    time::SystemTime,
};

const POSTGRES_ENTRY_COLUMNS: usize = 7;
const MAX_POSTGRES_ENTRY_BATCH_SIZE: usize = 5_000;

#[derive(Clone)]
pub(in crate::service) enum ExtractMetadataStore {
    None,
    Filesystem(FilesystemExtractMetadataStore),
    Postgres(PostgresExtractMetadataStore),
}

#[derive(Clone, Debug)]
pub(in crate::service) struct FilesystemExtractMetadataStore {
    dir: PathBuf,
}

#[derive(Clone)]
pub(in crate::service) struct PostgresExtractMetadataStore {
    pool: Pool<PostgresConnectionManager<NoTls>>,
    tables: Arc<PostgresExtractTableSet>,
    batch_size: usize,
}

#[derive(Debug)]
struct PostgresExtractTableSet {
    runs: Arc<str>,
    entries: Arc<str>,
    entries_kind_index: Arc<str>,
}

pub(in crate::service) struct ExtractMetadataWriter {
    backend: ExtractMetadataWriterBackend,
}

enum ExtractMetadataWriterBackend {
    None,
    Filesystem(FilesystemExtractMetadataWriter),
    Postgres(PostgresExtractMetadataWriter),
}

struct FilesystemExtractMetadataWriter {
    summary_path: PathBuf,
    entries: BufWriter<File>,
}

struct PostgresExtractMetadataWriter {
    store: PostgresExtractMetadataStore,
    run_id: String,
    buffer: Vec<PersistedEntry>,
}

struct PersistedEntry {
    entry_index: i64,
    entry_kind: &'static str,
    sanitized_path: String,
    stored_uri: Option<String>,
    stored_size_bytes: Option<i64>,
    metadata: serde_json::Value,
}

impl ExtractMetadataStore {
    pub(in crate::service) fn new(config: &ExtractMetadataConfig) -> Self {
        match config.backend {
            ExtractMetadataBackendKind::None => Self::None,
            ExtractMetadataBackendKind::Filesystem => {
                Self::Filesystem(FilesystemExtractMetadataStore {
                    dir: config.filesystem_dir.clone(),
                })
            }
            ExtractMetadataBackendKind::Postgres => {
                let postgres_url = config
                    .postgres_url
                    .as_deref()
                    .expect("postgres extract metadata backend requires postgres_url");
                Self::Postgres(
                    PostgresExtractMetadataStore::new(
                        postgres_url,
                        &config.postgres_table_prefix,
                        config.postgres_max_connections,
                        config.batch_size,
                    )
                    .expect("postgres extract metadata store should initialize"),
                )
            }
        }
    }

    pub(in crate::service) fn readiness_check(&self) -> Result<()> {
        match self {
            Self::None => Ok(()),
            Self::Filesystem(store) => store.readiness_check(),
            Self::Postgres(store) => store.readiness_check(),
        }
    }

    pub(in crate::service) fn begin_run(
        &self,
        run_id: &str,
        archive_name: &str,
        archive_path: &str,
        destination: &ExtractDestinationSummary,
    ) -> Result<ExtractMetadataWriter> {
        let backend = match self {
            Self::None => ExtractMetadataWriterBackend::None,
            Self::Filesystem(store) => {
                ExtractMetadataWriterBackend::Filesystem(store.begin_run(run_id)?)
            }
            Self::Postgres(store) => ExtractMetadataWriterBackend::Postgres(store.begin_run(
                run_id,
                archive_name,
                archive_path,
                destination,
            )?),
        };
        Ok(ExtractMetadataWriter { backend })
    }
}

impl ExtractMetadataWriter {
    pub(in crate::service) fn record_entry(&mut self, entry: &ExtractedEntry) -> Result<()> {
        match &mut self.backend {
            ExtractMetadataWriterBackend::None => Ok(()),
            ExtractMetadataWriterBackend::Filesystem(writer) => writer.record_entry(entry),
            ExtractMetadataWriterBackend::Postgres(writer) => writer.record_entry(entry),
        }
    }

    pub(in crate::service) fn finish_success(
        mut self,
        result: &ExtractArchiveResult,
    ) -> Result<()> {
        match &mut self.backend {
            ExtractMetadataWriterBackend::None => Ok(()),
            ExtractMetadataWriterBackend::Filesystem(writer) => writer.finish_success(result),
            ExtractMetadataWriterBackend::Postgres(writer) => writer.finish_success(result),
        }
    }

    pub(in crate::service) fn finish_failed(mut self, reason: &str) -> Result<()> {
        match &mut self.backend {
            ExtractMetadataWriterBackend::None | ExtractMetadataWriterBackend::Filesystem(_) => {
                Ok(())
            }
            ExtractMetadataWriterBackend::Postgres(writer) => writer.finish_failed(reason),
        }
    }
}

impl FilesystemExtractMetadataStore {
    fn readiness_check(&self) -> Result<()> {
        fs::create_dir_all(&self.dir).with_context(|| {
            format!("failed to create extract metadata directory {}", self.dir.display())
        })?;
        let _ = tempfile::NamedTempFile::new_in(&self.dir)
            .with_context(|| format!("failed to allocate temp file in {}", self.dir.display()))?;
        Ok(())
    }

    fn begin_run(&self, run_id: &str) -> Result<FilesystemExtractMetadataWriter> {
        let run_dir = self.dir.join(run_id);
        fs::create_dir_all(&run_dir).with_context(|| {
            format!("failed to create extract metadata run directory {}", run_dir.display())
        })?;
        let entries_path = run_dir.join("entries.ndjson");
        let entries = BufWriter::with_capacity(
            128 * 1024,
            File::create(&entries_path).with_context(|| {
                format!("failed to create extract metadata file {}", entries_path.display())
            })?,
        );
        Ok(FilesystemExtractMetadataWriter { summary_path: run_dir.join("summary.json"), entries })
    }
}

impl FilesystemExtractMetadataWriter {
    fn record_entry(&mut self, entry: &ExtractedEntry) -> Result<()> {
        serde_json::to_writer(&mut self.entries, entry)
            .context("failed to serialize extract entry metadata")?;
        self.entries.write_all(b"\n").context("failed to append extract entry metadata newline")
    }

    fn finish_success(&mut self, result: &ExtractArchiveResult) -> Result<()> {
        self.entries.flush().context("failed to flush extract entry metadata")?;
        let file = File::create(&self.summary_path).with_context(|| {
            format!("failed to create extract metadata summary {}", self.summary_path.display())
        })?;
        serde_json::to_writer_pretty(BufWriter::new(file), result).with_context(|| {
            format!("failed to write extract metadata summary {}", self.summary_path.display())
        })
    }
}

impl PostgresExtractMetadataStore {
    fn new(
        postgres_url: &str,
        table_prefix: &str,
        max_connections: u32,
        batch_size: usize,
    ) -> Result<Self> {
        validate_postgres_table_prefix(table_prefix)?;
        let config = postgres_url
            .parse::<postgres::Config>()
            .context("failed to parse postgres extract metadata url")?;
        let manager = PostgresConnectionManager::new(config, NoTls);
        let pool = Pool::builder()
            .max_size(max_connections.max(1))
            .build(manager)
            .context("failed to build postgres extract metadata connection pool")?;
        let store = Self {
            pool,
            tables: Arc::new(PostgresExtractTableSet::new(table_prefix)),
            batch_size: batch_size.clamp(1, MAX_POSTGRES_ENTRY_BATCH_SIZE),
        };
        store.ensure_schema()?;
        Ok(store)
    }

    fn begin_run(
        &self,
        run_id: &str,
        archive_name: &str,
        archive_path: &str,
        destination: &ExtractDestinationSummary,
    ) -> Result<PostgresExtractMetadataWriter> {
        let mut connection = self.connection()?;
        let destination_value =
            serde_json::to_value(destination).context("failed to serialize extract destination")?;
        connection
            .execute(
                &format!(
                    "INSERT INTO {} \
                     (run_id, status, created_at_unix, updated_at_unix, archive_name, archive_path, destination, result, error) \
                     VALUES ($1, 'running', $2, $2, $3, $4, $5, NULL, NULL) \
                     ON CONFLICT (run_id) DO UPDATE SET \
                       status = EXCLUDED.status, updated_at_unix = EXCLUDED.updated_at_unix, \
                       archive_name = EXCLUDED.archive_name, archive_path = EXCLUDED.archive_path, \
                       destination = EXCLUDED.destination, result = NULL, error = NULL",
                    self.tables.runs
                ),
                &[
                    &run_id,
                    &unix_now(),
                    &archive_name,
                    &archive_path,
                    &destination_value,
                ],
            )
            .context("failed to insert postgres extract metadata run")?;
        connection
            .execute(&format!("DELETE FROM {} WHERE run_id = $1", self.tables.entries), &[&run_id])
            .context("failed to clear stale postgres extract entries for run")?;

        Ok(PostgresExtractMetadataWriter {
            store: self.clone(),
            run_id: run_id.to_owned(),
            buffer: Vec::with_capacity(self.batch_size),
        })
    }

    fn readiness_check(&self) -> Result<()> {
        self.ensure_schema()?;
        let mut connection = self.connection()?;
        connection
            .query_one("SELECT 1", &[])
            .context("failed to query postgres extract metadata readiness")?;
        Ok(())
    }

    fn ensure_schema(&self) -> Result<()> {
        self.connection()?
            .batch_execute(&self.tables.bootstrap_sql())
            .context("failed to bootstrap postgres extract metadata schema")
    }

    fn connection(&self) -> Result<PooledConnection<PostgresConnectionManager<NoTls>>> {
        self.pool.get().context("failed to acquire postgres extract metadata connection")
    }

    fn with_transaction<T>(&self, f: impl FnOnce(&mut Transaction<'_>) -> Result<T>) -> Result<T> {
        let mut connection = self.connection()?;
        let mut tx = connection
            .transaction()
            .context("failed to open postgres extract metadata transaction")?;
        let value = f(&mut tx)?;
        tx.commit().context("failed to commit postgres extract metadata transaction")?;
        Ok(value)
    }
}

impl PostgresExtractMetadataWriter {
    fn record_entry(&mut self, entry: &ExtractedEntry) -> Result<()> {
        self.buffer.push(PersistedEntry::from_entry(entry)?);
        if self.buffer.len() >= self.store.batch_size {
            self.flush()?;
        }
        Ok(())
    }

    fn finish_success(&mut self, result: &ExtractArchiveResult) -> Result<()> {
        self.flush()?;
        let result_value =
            serde_json::to_value(result).context("failed to serialize extract result")?;
        self.store.with_transaction(|tx| {
            tx.execute(
                &format!(
                    "UPDATE {} SET status = 'succeeded', updated_at_unix = $2, result = $3, error = NULL \
                     WHERE run_id = $1",
                    self.store.tables.runs
                ),
                &[&self.run_id, &unix_now(), &result_value],
            )
            .context("failed to mark postgres extract metadata run succeeded")?;
            Ok(())
        })
    }

    fn finish_failed(&mut self, reason: &str) -> Result<()> {
        let _ = self.flush();
        self.store.with_transaction(|tx| {
            tx.execute(
                &format!(
                    "UPDATE {} SET status = 'failed', updated_at_unix = $2, error = $3 \
                     WHERE run_id = $1",
                    self.store.tables.runs
                ),
                &[&self.run_id, &unix_now(), &reason],
            )
            .context("failed to mark postgres extract metadata run failed")?;
            Ok(())
        })
    }

    fn flush(&mut self) -> Result<()> {
        if self.buffer.is_empty() {
            return Ok(());
        }
        let entries = std::mem::take(&mut self.buffer);
        self.store.with_transaction(|tx| {
            let mut sql = format!(
                "INSERT INTO {} \
                 (run_id, entry_index, entry_kind, sanitized_path, stored_uri, stored_size_bytes, metadata) \
                 VALUES ",
                self.store.tables.entries
            );
            let mut params =
                Vec::<&(dyn ToSql + Sync)>::with_capacity(entries.len() * POSTGRES_ENTRY_COLUMNS);
            for (index, entry) in entries.iter().enumerate() {
                if index > 0 {
                    sql.push_str(", ");
                }
                let base = index * POSTGRES_ENTRY_COLUMNS;
                write!(
                    sql,
                    "(${}, ${}, ${}, ${}, ${}, ${}, ${})",
                    base + 1,
                    base + 2,
                    base + 3,
                    base + 4,
                    base + 5,
                    base + 6,
                    base + 7
                )
                .expect("writing to string should not fail");
                params.extend([
                    &self.run_id as &(dyn ToSql + Sync),
                    &entry.entry_index,
                    &entry.entry_kind,
                    &entry.sanitized_path,
                    &entry.stored_uri,
                    &entry.stored_size_bytes,
                    &entry.metadata,
                ]);
            }
            sql.push_str(
                " ON CONFLICT (run_id, entry_index) DO UPDATE SET \
                   entry_kind = EXCLUDED.entry_kind, sanitized_path = EXCLUDED.sanitized_path, \
                   stored_uri = EXCLUDED.stored_uri, stored_size_bytes = EXCLUDED.stored_size_bytes, \
                   metadata = EXCLUDED.metadata",
            );
            tx.execute(&sql, &params)
                .context("failed to insert postgres extract metadata entry batch")?;
            Ok(())
        })
    }
}

impl PersistedEntry {
    fn from_entry(entry: &ExtractedEntry) -> Result<Self> {
        let stored_uri = entry.stored_object.as_ref().map(|object| object.uri.clone());
        let stored_size_bytes = entry
            .stored_object
            .as_ref()
            .map(|object| i64::try_from(object.size_bytes).unwrap_or(i64::MAX));
        Ok(Self {
            entry_index: i64::try_from(entry.row.entry_index).unwrap_or(i64::MAX),
            entry_kind: entry_kind_to_str(entry.row.entry_kind),
            sanitized_path: entry.sanitized_path.clone(),
            stored_uri,
            stored_size_bytes,
            metadata: serde_json::to_value(entry)
                .context("failed to serialize extract entry metadata")?,
        })
    }
}

impl PostgresExtractTableSet {
    fn new(prefix: &str) -> Self {
        Self {
            runs: format!("{prefix}_extract_runs").into(),
            entries: format!("{prefix}_extract_entries").into(),
            entries_kind_index: format!("{prefix}_extract_entries_kind_idx").into(),
        }
    }

    fn bootstrap_sql(&self) -> String {
        format!(
            "CREATE TABLE IF NOT EXISTS {} (
                run_id TEXT PRIMARY KEY,
                status TEXT NOT NULL,
                created_at_unix BIGINT NOT NULL,
                updated_at_unix BIGINT NOT NULL,
                archive_name TEXT NOT NULL,
                archive_path TEXT NOT NULL,
                destination JSONB NOT NULL,
                result JSONB,
                error TEXT
            );
            CREATE TABLE IF NOT EXISTS {} (
                run_id TEXT NOT NULL REFERENCES {} (run_id) ON DELETE CASCADE,
                entry_index BIGINT NOT NULL,
                entry_kind TEXT NOT NULL,
                sanitized_path TEXT NOT NULL,
                stored_uri TEXT,
                stored_size_bytes BIGINT,
                metadata JSONB NOT NULL,
                PRIMARY KEY (run_id, entry_index)
            );
            CREATE INDEX IF NOT EXISTS {} ON {} (run_id, entry_kind);",
            self.runs, self.entries, self.runs, self.entries_kind_index, self.entries
        )
    }
}

fn validate_postgres_table_prefix(prefix: &str) -> Result<()> {
    let valid = !prefix.is_empty()
        && prefix.bytes().all(|byte| byte.is_ascii_alphanumeric() || byte == b'_')
        && prefix.bytes().next().is_some_and(|byte| byte.is_ascii_alphabetic() || byte == b'_');
    if valid {
        Ok(())
    } else {
        Err(anyhow!(
            "postgres table prefix must be a non-empty identifier using letters, digits, and underscores"
        ))
    }
}

fn entry_kind_to_str(kind: EntryKind) -> &'static str {
    match kind {
        EntryKind::File => "file",
        EntryKind::Directory => "directory",
        EntryKind::Other => "other",
    }
}

fn unix_now() -> i64 {
    SystemTime::now()
        .duration_since(SystemTime::UNIX_EPOCH)
        .map_or(0, |duration| i64::try_from(duration.as_secs()).unwrap_or(i64::MAX))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn postgres_table_prefix_validation_rejects_invalid_identifiers() {
        assert!(validate_postgres_table_prefix("archive_scan").is_ok());
        assert!(validate_postgres_table_prefix("1bad").is_err());
        assert!(validate_postgres_table_prefix("bad-name").is_err());
    }
}
