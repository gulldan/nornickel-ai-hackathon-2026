use super::{
    config::SourceDownloadConfig,
    error::{ServiceError, ServiceResult},
    model::{
        ArchiveSummary, EntryKindCount, MimeCount, ScanArchiveRequest, ScanArchiveResponse,
        ScanSourceRef, TypeCount,
    },
    source,
};
use crate::{
    backend::{BackendOptions, LibarchiveBackend},
    engine::{self, ArchiveDescriptor, ScanConfig},
    lower_ext,
};
use anyhow::{Context, Result};
use std::{path::Path, time::SystemTime};

#[cfg(test)]
pub(super) fn scan_archive_at_path(
    path: &Path,
    request: ScanArchiveRequest,
) -> Result<ScanArchiveResponse> {
    scan_archive_at_path_with_interrupt(path, request, || false)
}

pub(super) fn scan_archive_request(
    request: ScanArchiveRequest,
    source_download: &SourceDownloadConfig,
) -> Result<ScanArchiveResponse> {
    scan_archive_request_with_interrupt(request, source_download, || false)
}

pub(super) fn scan_archive_request_with_interrupt<ShouldCancel>(
    request: ScanArchiveRequest,
    source_download: &SourceDownloadConfig,
    should_cancel: ShouldCancel,
) -> Result<ScanArchiveResponse>
where
    ShouldCancel: FnMut() -> bool,
{
    let prepared = source::prepare_archive_for_scan(request.source_ref(), source_download)?;
    scan_prepared_archive(prepared, request, should_cancel)
}

#[cfg(test)]
pub(super) fn scan_archive_at_path_with_interrupt<ShouldCancel>(
    path: &Path,
    request: ScanArchiveRequest,
    should_cancel: ShouldCancel,
) -> Result<ScanArchiveResponse>
where
    ShouldCancel: FnMut() -> bool,
{
    scan_prepared_archive(
        source::PreparedArchive::from_local_path(path.to_path_buf())?,
        request,
        should_cancel,
    )
}

fn scan_prepared_archive<ShouldCancel>(
    prepared: source::PreparedArchive,
    request: ScanArchiveRequest,
    should_cancel: ShouldCancel,
) -> Result<ScanArchiveResponse>
where
    ShouldCancel: FnMut() -> bool,
{
    let backend = LibarchiveBackend;
    let mut entries = Vec::new();
    let descriptor = prepared.descriptor();

    let totals = engine::process_archive_with_interrupt(
        descriptor,
        prepared.scan_path(),
        &backend,
        BackendOptions { block_size: request.block_size },
        ScanConfig {
            header_bytes: request.header_bytes,
            full_hash: request.full_hash,
            emit_hashes: request.include_entries,
            emit_rows: request.include_entries,
            fast_only: request.fast_only,
        },
        |row| {
            entries.push(row);
            Ok(())
        },
        engine::ProcessControl::new(|_delta| {}, should_cancel),
    )?;

    Ok(ScanArchiveResponse {
        archive: ArchiveSummary {
            path: descriptor.archive_path.to_string(),
            name: descriptor.archive_name.to_string(),
            size_bytes: descriptor.archive_size,
            mtime_unix: descriptor.archive_mtime_unix,
        },
        total_entries: totals.total_entries(),
        total_files: totals.total_files(),
        total_directories: totals.total_directories(),
        total_other_entries: totals.total_other_entries(),
        entry_kinds: totals
            .entry_kind_counts()
            .into_iter()
            .map(|(kind, count)| EntryKindCount { kind, count })
            .collect(),
        types: totals
            .clone()
            .into_sorted_counts()
            .into_iter()
            .map(|(label, count)| TypeCount { label, count })
            .collect(),
        mimes: totals
            .into_sorted_mime_counts()
            .into_iter()
            .map(|(mime, count)| MimeCount { mime, count })
            .collect(),
        entries: request.include_entries.then_some(entries),
    })
}

pub(super) fn validate_scan_path(source: ScanSourceRef<'_>, path: &Path) -> ServiceResult<()> {
    if !path.exists() {
        return Err(ServiceError::archive_path_not_found(source.kind, path));
    }
    if !path.is_file() {
        return Err(ServiceError::archive_path_not_file(source.kind, path));
    }
    Ok(())
}

pub(super) fn archive_descriptor_from_path(path: &Path) -> Result<ArchiveDescriptor> {
    let metadata = path
        .metadata()
        .with_context(|| format!("failed to read metadata for {}", path.display()))?;
    let archive_name = path
        .file_name()
        .and_then(|name| name.to_str())
        .unwrap_or("<unknown>")
        .to_owned()
        .into_boxed_str();

    Ok(ArchiveDescriptor {
        archive_index: 0,
        archive_ext: lower_ext(archive_name.as_ref()).into_owned().into_boxed_str(),
        archive_name,
        archive_path: path.display().to_string().into_boxed_str(),
        archive_size: metadata.len(),
        archive_mtime_unix: metadata
            .modified()
            .unwrap_or(SystemTime::UNIX_EPOCH)
            .duration_since(SystemTime::UNIX_EPOCH)
            .map_or(0, |duration| i64::try_from(duration.as_secs()).unwrap_or(i64::MAX)),
    })
}
