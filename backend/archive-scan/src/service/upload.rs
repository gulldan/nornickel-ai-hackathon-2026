use super::{
    config::{ExtractStoreConfig, SourceDownloadConfig},
    error::{ServiceError, ServiceResult},
    extract_runtime::{metadata::ExtractMetadataStore, store::RuntimeExtractStore},
    model,
};
use crate::{
    backend::{BackendOptions, LibarchiveBackend},
    engine,
    extract::{
        self as archive_extract, ExtractArchiveResult, ExtractConfig, ExtractFileStore,
        ExtractLimits,
    },
};
use anyhow::Result;
use axum::extract::{multipart::Field, Multipart};
use std::{
    io::Write,
    path::Path,
    sync::atomic::{AtomicU64, Ordering},
    time::{Duration, Instant, SystemTime},
};
use tempfile::{Builder, NamedTempFile};

#[derive(Debug)]
struct UploadExtractOptions {
    extraction_id: String,
    archive_name: String,
    header_bytes: usize,
    block_size: usize,
    full_hash: bool,
    fast_only: bool,
    include_entries: bool,
    // Resource-exhaustion guards (0 / unset disables a cap). Bytes are derived
    // from the worker's *_MB knobs; timeout from ARCHIVE_EXTRACT_TIMEOUT.
    max_file_bytes: Option<u64>,
    max_total_bytes: Option<u64>,
    max_ratio: Option<f64>,
    extract_timeout: Option<Duration>,
}

pub(super) struct UploadedArchive {
    temp_file: NamedTempFile,
    options: UploadExtractOptions,
}

impl Default for UploadExtractOptions {
    fn default() -> Self {
        Self {
            extraction_id: next_extraction_id(),
            archive_name: "uploaded-archive".to_owned(),
            header_bytes: model::DEFAULT_HEADER_BYTES,
            block_size: model::DEFAULT_BLOCK_SIZE,
            full_hash: false,
            fast_only: false,
            include_entries: false,
            max_file_bytes: None,
            max_total_bytes: None,
            max_ratio: None,
            extract_timeout: None,
        }
    }
}

pub(super) async fn read_extract_upload(
    multipart: &mut Multipart,
    config: &SourceDownloadConfig,
) -> ServiceResult<UploadedArchive> {
    let mut options = UploadExtractOptions::default();
    let mut archive: Option<NamedTempFile> = None;

    while let Some(field) = multipart
        .next_field()
        .await
        .map_err(|err| ServiceError::invalid_multipart(err.to_string()))?
    {
        let name = field.name().unwrap_or_default().to_owned();
        if name == "archive" {
            if archive.is_some() {
                return Err(ServiceError::invalid_request_body(
                    "multipart request must contain exactly one `archive` file field",
                ));
            }
            if let Some(file_name) = field.file_name().map(str::to_owned) {
                options.archive_name = sanitize_upload_file_name(&file_name);
            }
            archive =
                Some(write_uploaded_archive_field(field, config, &options.archive_name).await?);
            continue;
        }

        let text =
            field.text().await.map_err(|err| ServiceError::invalid_multipart(err.to_string()))?;
        apply_upload_option(&mut options, &name, &text)?;
    }

    let temp_file = archive.ok_or_else(|| {
        ServiceError::invalid_request_body("multipart request requires an `archive` file field")
    })?;
    Ok(UploadedArchive { temp_file, options })
}

async fn write_uploaded_archive_field(
    mut field: Field<'_>,
    config: &SourceDownloadConfig,
    archive_name: &str,
) -> ServiceResult<NamedTempFile> {
    let suffix =
        archive_name.rsplit_once('.').map(|(_, ext)| format!(".{ext}")).unwrap_or_default();
    let mut builder = Builder::new();
    builder.prefix("archive-scan-upload-").suffix(&suffix);
    let mut temp_file = match config.temp_dir.as_deref() {
        Some(temp_dir) => {
            std::fs::create_dir_all(temp_dir).map_err(|err| {
                ServiceError::request_body_read_failed(format!(
                    "failed to create upload temp directory {}: {err}",
                    temp_dir.display()
                ))
            })?;
            builder.tempfile_in(temp_dir)
        }
        None => builder.tempfile(),
    }
    .map_err(|err| ServiceError::request_body_read_failed(err.to_string()))?;

    let mut bytes_written = 0_u64;
    while let Some(chunk) =
        field.chunk().await.map_err(|err| ServiceError::invalid_multipart(err.to_string()))?
    {
        bytes_written =
            bytes_written.saturating_add(u64::try_from(chunk.len()).unwrap_or(u64::MAX));
        if let Some(limit) = config.max_bytes {
            if bytes_written > limit {
                return Err(ServiceError::request_too_large(limit));
            }
        }
        temp_file
            .as_file_mut()
            .write_all(&chunk)
            .map_err(|err| ServiceError::request_body_read_failed(err.to_string()))?;
    }
    temp_file
        .as_file_mut()
        .flush()
        .map_err(|err| ServiceError::request_body_read_failed(err.to_string()))?;
    Ok(temp_file)
}

fn apply_upload_option(
    options: &mut UploadExtractOptions,
    name: &str,
    value: &str,
) -> ServiceResult<()> {
    let value = value.trim();
    match name {
        "extraction_id" => {
            if value.is_empty() {
                return Err(ServiceError::invalid_request_body(
                    "`extraction_id` must not be blank",
                ));
            }
            options.extraction_id = sanitize_extraction_id(value)?;
        }
        "header_bytes" => options.header_bytes = parse_usize_field(name, value)?,
        "block_size" => options.block_size = parse_usize_field(name, value)?,
        "full_hash" => options.full_hash = parse_bool_field(name, value)?,
        "fast_only" => options.fast_only = parse_bool_field(name, value)?,
        "include_entries" => options.include_entries = parse_bool_field(name, value)?,
        // 0 disables the corresponding cap.
        "max_file_bytes" => options.max_file_bytes = parse_optional_u64_field(name, value)?,
        "max_total_bytes" => options.max_total_bytes = parse_optional_u64_field(name, value)?,
        "max_ratio" => options.max_ratio = parse_optional_ratio_field(name, value)?,
        "extract_timeout_secs" => {
            options.extract_timeout =
                parse_optional_u64_field(name, value)?.map(Duration::from_secs);
        }
        "" => {}
        _ => {
            return Err(ServiceError::invalid_request_body(format!(
                "unknown multipart field `{name}`"
            )));
        }
    }
    Ok(())
}

fn parse_usize_field(name: &str, value: &str) -> ServiceResult<usize> {
    value.parse::<usize>().map_err(|err| {
        ServiceError::invalid_request_body(format!("`{name}` must be a positive integer: {err}"))
    })
}

// A 0 value means "no cap" and maps to None.
fn parse_optional_u64_field(name: &str, value: &str) -> ServiceResult<Option<u64>> {
    let parsed = value.parse::<u64>().map_err(|err| {
        ServiceError::invalid_request_body(format!(
            "`{name}` must be a non-negative integer: {err}"
        ))
    })?;
    Ok((parsed > 0).then_some(parsed))
}

fn parse_optional_ratio_field(name: &str, value: &str) -> ServiceResult<Option<f64>> {
    let parsed = value.parse::<f64>().map_err(|err| {
        ServiceError::invalid_request_body(format!("`{name}` must be a number: {err}"))
    })?;
    if !parsed.is_finite() || parsed < 0.0 {
        return Err(ServiceError::invalid_request_body(format!(
            "`{name}` must be a finite, non-negative number"
        )));
    }
    Ok((parsed > 0.0).then_some(parsed))
}

fn parse_bool_field(name: &str, value: &str) -> ServiceResult<bool> {
    match value.to_ascii_lowercase().as_str() {
        "1" | "true" | "yes" | "on" => Ok(true),
        "0" | "false" | "no" | "off" => Ok(false),
        _ => Err(ServiceError::invalid_request_body(format!("`{name}` must be a boolean"))),
    }
}

fn sanitize_upload_file_name(file_name: &str) -> String {
    let name = Path::new(file_name)
        .file_name()
        .and_then(|name| name.to_str())
        .unwrap_or("uploaded-archive");
    let sanitized = name
        .chars()
        .map(|character| {
            if character.is_ascii_alphanumeric() || matches!(character, '-' | '_' | '.') {
                character
            } else {
                '_'
            }
        })
        .collect::<String>();
    if sanitized.is_empty() {
        "uploaded-archive".to_owned()
    } else {
        sanitized
    }
}

fn sanitize_extraction_id(value: &str) -> ServiceResult<String> {
    let sanitized = value
        .chars()
        .map(|character| {
            if character.is_ascii_alphanumeric() || matches!(character, '-' | '_' | '.') {
                character
            } else {
                '_'
            }
        })
        .collect::<String>();
    if sanitized.is_empty() {
        Err(ServiceError::invalid_request_body(
            "`extraction_id` must contain at least one safe character",
        ))
    } else {
        Ok(sanitized)
    }
}

fn next_extraction_id() -> String {
    static NEXT_ID: AtomicU64 = AtomicU64::new(1);
    let seq = NEXT_ID.fetch_add(1, Ordering::Relaxed);
    let now = SystemTime::now()
        .duration_since(SystemTime::UNIX_EPOCH)
        .map_or(0, |duration| duration.as_secs());
    format!("extract-{now}-{seq:08}")
}

pub(super) fn extract_uploaded_archive(
    upload: UploadedArchive,
    extract_store: ExtractStoreConfig,
    metadata_store: ExtractMetadataStore,
) -> Result<ExtractArchiveResult> {
    let UploadedArchive { temp_file, options } = upload;
    let extraction_id = options.extraction_id.clone();
    let descriptor = archive_extract::archive_descriptor_for_uploaded_file(
        &format!("upload://{}", options.archive_name),
        &options.archive_name,
        temp_file.path(),
    )?;
    let backend = LibarchiveBackend;
    let mut store = RuntimeExtractStore::new(&extract_store, &options.extraction_id);
    let destination = store.destination_summary();
    let mut metadata = metadata_store.begin_run(
        &options.extraction_id,
        descriptor.archive_name.as_ref(),
        descriptor.archive_path.as_ref(),
        &destination,
    )?;
    let limits = ExtractLimits {
        max_file_bytes: options.max_file_bytes,
        max_total_bytes: options.max_total_bytes,
        max_ratio: options.max_ratio,
        compressed_size: descriptor.archive_size,
        deadline: options.extract_timeout.map(|timeout| Instant::now() + timeout),
    };
    tracing::info!(
        extraction_id = %extraction_id,
        archive_name = %descriptor.archive_name,
        destination_backend = ?destination.backend,
        destination_root = %destination.root,
        max_file_bytes = ?limits.max_file_bytes,
        max_total_bytes = ?limits.max_total_bytes,
        max_ratio = ?limits.max_ratio,
        extract_timeout_secs = ?options.extract_timeout.map(|timeout| timeout.as_secs()),
        "extract upload started"
    );

    let result = archive_extract::extract_archive_with_interrupt(
        &descriptor,
        temp_file.path(),
        &backend,
        BackendOptions { block_size: options.block_size },
        ExtractConfig {
            extraction_id: options.extraction_id,
            header_bytes: options.header_bytes,
            full_hash: options.full_hash,
            emit_hashes: options.full_hash || options.include_entries,
            fast_only: options.fast_only,
            collect_entries: options.include_entries,
            limits,
        },
        &mut store,
        |entry| metadata.record_entry(entry),
        engine::ProcessControl::new(|_delta| {}, || false),
    );

    match result {
        Ok(result) => {
            metadata.finish_success(&result)?;
            tracing::info!(
                extraction_id = %result.extraction_id,
                total_entries = result.total_entries,
                stored_files = result.stored_files,
                stored_bytes = result.stored_bytes,
                "extract upload succeeded"
            );
            Ok(result)
        }
        Err(err) => {
            let _ = metadata.finish_failed(&err.to_string());
            tracing::error!(extraction_id = %extraction_id, error = %err, "extract upload failed");
            Err(err)
        }
    }
}
