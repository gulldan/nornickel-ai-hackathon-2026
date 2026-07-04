use crate::{
    backend::{ArchiveBackend, BackendOptions},
    cancel::scan_cancelled_io_error,
    engine::{self, ArchiveDescriptor, ProcessControl, ScanConfig, TypeAgg},
    is_label_archive, lower_ext,
    row::{ArchiveMeta, DetectionSource, EntryKind, EntryRow},
};
use anyhow::{anyhow, Context, Result};
use serde::{Deserialize, Serialize};
use smallvec::SmallVec;
use std::{
    borrow::Cow,
    collections::HashSet,
    fs,
    io::{self, BufWriter, Read, Write},
    path::{Component, Path, PathBuf},
    sync::Arc,
    time::{Instant, SystemTime},
};
use tempfile::NamedTempFile;

#[cfg_attr(feature = "service", derive(utoipa::ToSchema))]
#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum ExtractStorageKind {
    Filesystem,
    S3,
}

#[cfg_attr(feature = "service", derive(utoipa::ToSchema))]
#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub struct StoredObject {
    pub kind: ExtractStorageKind,
    pub uri: String,
    pub size_bytes: u64,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub b3: Option<String>,
}

#[cfg_attr(feature = "service", derive(utoipa::ToSchema))]
#[derive(Clone, Debug, Deserialize, Serialize)]
pub struct ExtractedEntry {
    pub row: EntryRow,
    pub sanitized_path: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub stored_object: Option<StoredObject>,
}

#[cfg_attr(feature = "service", derive(utoipa::ToSchema))]
#[derive(Clone, Debug, Deserialize, Serialize)]
pub struct ExtractArchiveSummary {
    pub path: String,
    pub name: String,
    pub size_bytes: u64,
    pub mtime_unix: i64,
}

#[cfg_attr(feature = "service", derive(utoipa::ToSchema))]
#[derive(Clone, Debug, Deserialize, Serialize)]
pub struct ExtractTypeCount {
    #[cfg_attr(feature = "service", schema(value_type = String))]
    pub label: Box<str>,
    pub count: u64,
}

#[cfg_attr(feature = "service", derive(utoipa::ToSchema))]
#[derive(Clone, Debug, Deserialize, Serialize)]
pub struct ExtractMimeCount {
    #[cfg_attr(feature = "service", schema(value_type = String))]
    pub mime: Box<str>,
    pub count: u64,
}

#[cfg_attr(feature = "service", derive(utoipa::ToSchema))]
#[derive(Clone, Debug, Deserialize, Serialize)]
pub struct ExtractEntryKindCount {
    pub kind: EntryKind,
    pub count: u64,
}

#[cfg_attr(feature = "service", derive(utoipa::ToSchema))]
#[derive(Clone, Debug, Deserialize, Serialize)]
pub struct ExtractDestinationSummary {
    pub backend: ExtractStorageKind,
    pub root: String,
}

#[cfg_attr(feature = "service", derive(utoipa::ToSchema))]
#[derive(Clone, Debug, Deserialize, Serialize)]
pub struct ExtractArchiveResult {
    pub extraction_id: String,
    pub archive: ExtractArchiveSummary,
    pub destination: ExtractDestinationSummary,
    pub total_entries: u64,
    pub total_files: u64,
    pub total_directories: u64,
    pub total_other_entries: u64,
    pub stored_files: u64,
    pub stored_bytes: u64,
    /// Entries skipped because their uncompressed size exceeded the per-file cap.
    #[serde(default)]
    pub skipped_oversize_files: u64,
    pub entry_kinds: Vec<ExtractEntryKindCount>,
    pub types: Vec<ExtractTypeCount>,
    pub mimes: Vec<ExtractMimeCount>,
    pub entries: Option<Vec<ExtractedEntry>>,
}

#[derive(Clone, Debug)]
pub struct ExtractConfig {
    pub extraction_id: String,
    pub header_bytes: usize,
    pub full_hash: bool,
    pub emit_hashes: bool,
    pub fast_only: bool,
    pub collect_entries: bool,
    pub limits: ExtractLimits,
}

/// Resource-exhaustion guards applied while extracted bytes stream.
///
/// All caps are optional (None disables a cap); `compressed_size` and `deadline`
/// feed the zip-bomb ratio and timeout checks. Values come from the worker's
/// ARCHIVE_MAX_FILE_MB / ARCHIVE_MAX_TOTAL_MB / ARCHIVE_MAX_RATIO /
/// ARCHIVE_EXTRACT_TIMEOUT environment knobs.
#[derive(Clone, Copy, Debug, Default)]
pub struct ExtractLimits {
    pub max_file_bytes: Option<u64>,
    pub max_total_bytes: Option<u64>,
    pub max_ratio: Option<f64>,
    pub compressed_size: u64,
    pub deadline: Option<Instant>,
}

/// Why streaming was aborted. Per-file breaches skip a single entry; the rest
/// abort the whole extraction.
#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub enum LimitBreach {
    FileTooLarge,
    TotalTooLarge,
    RatioExceeded,
    Timeout,
}

impl LimitBreach {
    const fn fatal(self) -> bool {
        !matches!(self, Self::FileTooLarge)
    }

    const fn message(self) -> &'static str {
        match self {
            Self::FileTooLarge => "archive entry exceeds the per-file size limit",
            Self::TotalTooLarge => "archive extraction exceeds the total size limit",
            Self::RatioExceeded => {
                "archive decompression ratio exceeds the limit (possible zip bomb)"
            }
            Self::Timeout => "archive extraction exceeded the time limit",
        }
    }
}

/// Tracks cumulative extracted bytes for one archive and enforces the caps.
///
/// The size, ratio and timeout caps are checked as bytes accumulate. Created
/// once per archive and borrowed by each entry's writer; `file_bytes` resets
/// per entry.
#[derive(Debug)]
pub struct LimitTracker {
    limits: ExtractLimits,
    total_bytes: u64,
    file_bytes: u64,
}

impl LimitTracker {
    #[must_use]
    pub fn new(limits: ExtractLimits) -> Self {
        Self { limits, total_bytes: 0, file_bytes: 0 }
    }

    pub(crate) fn begin_file(&mut self) {
        self.file_bytes = 0;
    }

    /// Accounts a freshly written chunk and returns the breach that aborts
    /// streaming, if any. Checked mid-stream so a bomb never fully materializes.
    pub(crate) fn account(&mut self, len: u64) -> Result<(), LimitBreach> {
        self.file_bytes = self.file_bytes.saturating_add(len);
        self.total_bytes = self.total_bytes.saturating_add(len);
        if let Some(deadline) = self.limits.deadline {
            if Instant::now() >= deadline {
                return Err(LimitBreach::Timeout);
            }
        }
        if self.limits.max_file_bytes.is_some_and(|cap| self.file_bytes > cap) {
            // The entry is about to be skipped and its partial output discarded,
            // so roll its bytes back out of the cumulative total.
            self.total_bytes = self.total_bytes.saturating_sub(self.file_bytes);
            return Err(LimitBreach::FileTooLarge);
        }
        if self.limits.max_total_bytes.is_some_and(|cap| self.total_bytes > cap) {
            return Err(LimitBreach::TotalTooLarge);
        }
        if let Some(ratio) = self.limits.max_ratio {
            // Guard against tiny archives inflating the ratio: only enforce once
            // the cumulative output is meaningfully larger than the input.
            let compressed = self.limits.compressed_size.max(1);
            #[allow(clippy::cast_precision_loss)]
            if self.total_bytes > compressed
                && (self.total_bytes as f64) / (compressed as f64) > ratio
            {
                return Err(LimitBreach::RatioExceeded);
            }
        }
        Ok(())
    }
}

/// io::Error carrying a [`LimitBreach`], so the per-entry writer can surface a
/// breach through the `Read`/`Write` plumbing and the archive loop can classify
/// it as skip-entry vs abort-archive. Mirrors the scan-cancelled sentinel.
pub(crate) fn limit_breach_io_error(breach: LimitBreach) -> io::Error {
    io::Error::other(LimitBreachError(breach))
}

#[derive(Debug)]
struct LimitBreachError(LimitBreach);

impl std::fmt::Display for LimitBreachError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(self.0.message())
    }
}

impl std::error::Error for LimitBreachError {}

/// Recovers the [`LimitBreach`] from an error chain, if streaming aborted on a
/// limit.
fn limit_breach_from_error(err: &anyhow::Error) -> Option<LimitBreach> {
    err.chain().find_map(|cause| {
        cause
            .downcast_ref::<io::Error>()
            .and_then(io::Error::get_ref)
            .and_then(|inner| inner.downcast_ref::<LimitBreachError>())
            .map(|breach| breach.0)
    })
}

#[derive(Debug)]
pub struct FileWriteOptions<'a, ShouldCancel>
where
    ShouldCancel: FnMut() -> bool,
{
    pub header_bytes: usize,
    pub full_hash: bool,
    pub emit_hashes: bool,
    pub should_cancel: &'a mut ShouldCancel,
    /// Per-archive byte tracker enforcing the size/ratio/timeout caps; None
    /// disables streaming limits (e.g. unit tests, scan-only paths).
    pub limits: Option<&'a mut LimitTracker>,
}

#[derive(Debug)]
pub struct FileWriteOutcome {
    pub stored_object: StoredObject,
    pub scan: WriteScanOutcome,
}

#[derive(Debug)]
pub struct WriteScanOutcome {
    pub header: SmallVec<[u8; 512]>,
    pub head_b3: Option<Box<str>>,
    pub full_b3: Option<Box<str>>,
    pub b3: String,
    pub bytes_scanned: u64,
    pub truncated_scan: bool,
}

pub trait ExtractFileStore {
    fn destination_summary(&self) -> ExtractDestinationSummary;

    fn create_directory(&mut self, relative_path: &Path) -> Result<Option<StoredObject>>;

    fn write_file<ShouldCancel>(
        &mut self,
        relative_path: &Path,
        reader: &mut dyn Read,
        options: FileWriteOptions<'_, ShouldCancel>,
    ) -> Result<FileWriteOutcome>
    where
        ShouldCancel: FnMut() -> bool;
}

#[derive(Clone, Debug)]
pub struct FilesystemExtractStore {
    root: PathBuf,
}

impl FilesystemExtractStore {
    #[must_use]
    pub fn new(root: PathBuf) -> Self {
        Self { root }
    }

    #[must_use]
    pub fn root(&self) -> &Path {
        &self.root
    }

    pub fn readiness_check(&self) -> Result<()> {
        fs::create_dir_all(&self.root).with_context(|| {
            format!("failed to create extract directory {}", self.root.display())
        })?;
        let _ = NamedTempFile::new_in(&self.root)
            .with_context(|| format!("failed to allocate temp file in {}", self.root.display()))?;
        Ok(())
    }

    fn absolute_path(&self, relative_path: &Path) -> PathBuf {
        self.root.join(relative_path)
    }
}

impl ExtractFileStore for FilesystemExtractStore {
    fn destination_summary(&self) -> ExtractDestinationSummary {
        ExtractDestinationSummary {
            backend: ExtractStorageKind::Filesystem,
            root: self.root.display().to_string(),
        }
    }

    fn create_directory(&mut self, relative_path: &Path) -> Result<Option<StoredObject>> {
        let path = self.absolute_path(relative_path);
        fs::create_dir_all(&path)
            .with_context(|| format!("failed to create extracted directory {}", path.display()))?;
        Ok(Some(StoredObject {
            kind: ExtractStorageKind::Filesystem,
            uri: path.display().to_string(),
            size_bytes: 0,
            b3: None,
        }))
    }

    fn write_file<ShouldCancel>(
        &mut self,
        relative_path: &Path,
        reader: &mut dyn Read,
        options: FileWriteOptions<'_, ShouldCancel>,
    ) -> Result<FileWriteOutcome>
    where
        ShouldCancel: FnMut() -> bool,
    {
        let path = self.absolute_path(relative_path);
        if let Some(parent) = path.parent() {
            fs::create_dir_all(parent).with_context(|| {
                format!("failed to create extracted file parent directory {}", parent.display())
            })?;
        }

        let parent = path
            .parent()
            .ok_or_else(|| anyhow!("extracted file target has no parent directory"))?;
        let mut staging_file = NamedTempFile::new_in(parent).with_context(|| {
            format!("failed to allocate temp extracted file in {}", parent.display())
        })?;
        let scan = {
            let mut writer = BufWriter::with_capacity(128 * 1024, staging_file.as_file_mut());
            copy_reader_to_writer_with_scan(reader, &mut writer, options)?
        };
        staging_file
            .as_file_mut()
            .sync_all()
            .with_context(|| format!("failed to fsync extracted file {}", path.display()))?;
        staging_file.persist(&path).map_err(|err| {
            anyhow!("failed to persist extracted file {}: {}", path.display(), err)
        })?;

        Ok(FileWriteOutcome {
            stored_object: StoredObject {
                kind: ExtractStorageKind::Filesystem,
                uri: path.display().to_string(),
                size_bytes: scan.bytes_scanned,
                b3: Some(scan.b3.clone()),
            },
            scan,
        })
    }
}

pub fn archive_descriptor_from_path(path: &Path, archive_index: u32) -> Result<ArchiveDescriptor> {
    let metadata = path
        .metadata()
        .with_context(|| format!("failed to read archive metadata {}", path.display()))?;
    let archive_name =
        path.file_name().and_then(|name| name.to_str()).unwrap_or("<unknown>").to_owned();

    Ok(ArchiveDescriptor {
        archive_index,
        archive_ext: lower_ext(&archive_name).into_owned().into_boxed_str(),
        archive_name: archive_name.into_boxed_str(),
        archive_path: path.display().to_string().into_boxed_str(),
        archive_size: metadata.len(),
        archive_mtime_unix: metadata
            .modified()
            .unwrap_or(SystemTime::UNIX_EPOCH)
            .duration_since(SystemTime::UNIX_EPOCH)
            .map_or(0, |duration| i64::try_from(duration.as_secs()).unwrap_or(i64::MAX)),
    })
}

pub fn archive_descriptor_for_uploaded_file(
    display_path: &str,
    archive_name: &str,
    path: &Path,
) -> Result<ArchiveDescriptor> {
    let metadata = path
        .metadata()
        .with_context(|| format!("failed to read uploaded archive metadata {}", path.display()))?;

    Ok(ArchiveDescriptor {
        archive_index: 0,
        archive_name: archive_name.to_owned().into_boxed_str(),
        archive_path: display_path.to_owned().into_boxed_str(),
        archive_ext: lower_ext(archive_name).into_owned().into_boxed_str(),
        archive_size: metadata.len(),
        archive_mtime_unix: metadata
            .modified()
            .unwrap_or(SystemTime::UNIX_EPOCH)
            .duration_since(SystemTime::UNIX_EPOCH)
            .map_or(0, |duration| i64::try_from(duration.as_secs()).unwrap_or(i64::MAX)),
    })
}

pub fn extract_archive<Store, OnEntry>(
    descriptor: &ArchiveDescriptor,
    source_path: &Path,
    backend: &dyn ArchiveBackend,
    backend_options: BackendOptions,
    config: ExtractConfig,
    store: &mut Store,
    on_entry: OnEntry,
) -> Result<ExtractArchiveResult>
where
    Store: ExtractFileStore,
    OnEntry: FnMut(&ExtractedEntry) -> Result<()>,
{
    extract_archive_with_interrupt(
        descriptor,
        source_path,
        backend,
        backend_options,
        config,
        store,
        on_entry,
        ProcessControl::new(|_| {}, || false),
    )
}

pub fn extract_archive_with_interrupt<Store, OnEntry, OnProgress, ShouldCancel>(
    descriptor: &ArchiveDescriptor,
    source_path: &Path,
    backend: &dyn ArchiveBackend,
    backend_options: BackendOptions,
    config: ExtractConfig,
    store: &mut Store,
    mut on_entry: OnEntry,
    mut control: ProcessControl<OnProgress, ShouldCancel>,
) -> Result<ExtractArchiveResult>
where
    Store: ExtractFileStore,
    OnEntry: FnMut(&ExtractedEntry) -> Result<()>,
    OnProgress: FnMut(u64),
    ShouldCancel: FnMut() -> bool,
{
    let destination = store.destination_summary();
    let archive = summary_from_descriptor(descriptor);
    let archive_meta = Arc::new(ArchiveMeta {
        archive_index: descriptor.archive_index,
        archive_name: descriptor.archive_name.clone(),
        archive_path: descriptor.archive_path.clone(),
        archive_ext: descriptor.archive_ext.clone(),
        archive_size: descriptor.archive_size,
        archive_mtime_unix: descriptor.archive_mtime_unix,
    });
    let mut totals = TypeAgg::default();
    let mut entries = config.collect_entries.then(Vec::new);
    let mut path_registry = PathRegistry::default();
    let mut stored_files = 0_u64;
    let mut stored_bytes = 0_u64;
    let mut skipped_oversize = 0_u64;
    let mut pending_progress = 0_u64;
    let mut entry_index = 0_u64;
    let mut tracker = LimitTracker::new(config.limits);
    let scan_config = ScanConfig {
        header_bytes: config.header_bytes,
        full_hash: config.full_hash,
        emit_hashes: config.emit_hashes,
        emit_rows: true,
        fast_only: config.fast_only,
    };

    backend.for_each_entry(source_path, backend_options, &mut |entry, reader| {
        if (control.should_cancel)() {
            return Err(scan_cancelled_io_error().into());
        }

        let entry_name = entry.name;
        let entry_kind = entry.kind;
        let safe_path = path_registry.safe_unique_path(entry_name.as_ref(), entry_index)?;
        let safe_path_display = path_to_portable_string(&safe_path);
        let entry_ext = lower_ext(entry_name.as_ref()).into_owned().into_boxed_str();

        let (scan, stored_object) = match entry_kind {
            EntryKind::Directory => {
                let stored = store.create_directory(&safe_path)?;
                (None, stored)
            }
            EntryKind::File => {
                let outcome = store.write_file(
                    &safe_path,
                    reader,
                    FileWriteOptions {
                        header_bytes: scan_config.header_bytes,
                        full_hash: scan_config.full_hash,
                        emit_hashes: scan_config.emit_hashes,
                        should_cancel: &mut control.should_cancel,
                        limits: Some(&mut tracker),
                    },
                );
                let outcome = match outcome {
                    Ok(outcome) => outcome,
                    Err(err) => match limit_breach_from_error(&err) {
                        // A single oversize entry is skipped (its partial temp
                        // file is discarded), like a too-deep or duplicate entry.
                        Some(breach) if !breach.fatal() => {
                            skipped_oversize = skipped_oversize.saturating_add(1);
                            entry_index = entry_index.saturating_add(1);
                            return Ok(());
                        }
                        // Total-size / ratio / timeout abort the whole archive.
                        _ => return Err(err),
                    },
                };
                stored_files = stored_files.saturating_add(1);
                stored_bytes = stored_bytes.saturating_add(outcome.scan.bytes_scanned);
                (Some(outcome.scan), Some(outcome.stored_object))
            }
            EntryKind::Other => (None, None),
        };

        if (control.should_cancel)() {
            return Err(scan_cancelled_io_error().into());
        }

        let (label, mime, detected_by, confidence) = match (entry_kind, scan.as_ref()) {
            (EntryKind::Directory, _) => (
                Cow::Borrowed("directory"),
                Cow::Borrowed("inode/directory"),
                DetectionSource::Heuristic,
                1.0,
            ),
            (_, Some(scan)) => engine::detect_type(
                entry_name.as_ref(),
                entry_ext.as_ref(),
                &scan.header,
                scan_config.fast_only,
            ),
            (_, None) => {
                (Cow::Borrowed("unknown"), Cow::Borrowed(""), DetectionSource::Unknown, 0.0)
            }
        };

        match entry_kind {
            EntryKind::File => totals.record_file(label.as_ref(), mime.as_ref()),
            EntryKind::Directory => totals.record_directory(),
            EntryKind::Other => totals.record_other_entry(),
        }

        let is_nested_archive = entry_kind == EntryKind::File && is_label_archive(label.as_ref());
        let row = EntryRow {
            archive: Arc::clone(&archive_meta),
            entry_index,
            entry_ext,
            entry_name,
            entry_kind,
            label,
            mime,
            detected_by,
            confidence,
            is_nested_archive,
            header_len: scan
                .as_ref()
                .map_or(0, |scan| saturating_u32_from_usize(scan.header.len())),
            bytes_scanned: scan.as_ref().map_or(0, |scan| scan.bytes_scanned),
            truncated_scan: scan.as_ref().is_some_and(|scan| scan.truncated_scan),
            head_b3: scan.as_ref().and_then(|scan| scan.head_b3.clone()),
            full_b3: scan.and_then(|scan| scan.full_b3),
        };
        let extracted = ExtractedEntry { row, sanitized_path: safe_path_display, stored_object };
        on_entry(&extracted)?;
        if let Some(entries) = entries.as_mut() {
            entries.push(extracted);
        }

        entry_index = entry_index.saturating_add(1);
        pending_progress = pending_progress.saturating_add(1);
        if pending_progress >= engine::ENTRY_PROGRESS_CHUNK {
            (control.on_progress)(pending_progress);
            pending_progress = 0;
        }
        Ok(())
    })?;

    (control.on_progress)(pending_progress);
    Ok(result_from_totals(
        config.extraction_id,
        archive,
        destination,
        totals,
        stored_files,
        stored_bytes,
        skipped_oversize,
        entries,
    ))
}

pub fn copy_reader_to_writer_with_scan<R, W, ShouldCancel>(
    reader: &mut R,
    writer: &mut W,
    options: FileWriteOptions<'_, ShouldCancel>,
) -> io::Result<WriteScanOutcome>
where
    R: Read + ?Sized,
    W: Write + ?Sized,
    ShouldCancel: FnMut() -> bool,
{
    let mut header = SmallVec::<[u8; 512]>::with_capacity(options.header_bytes.min(512));
    let mut bytes_scanned = 0_u64;
    let mut b3_hasher = blake3::Hasher::new();
    let mut buffer = vec![0_u8; 128 * 1024].into_boxed_slice();
    let should_cancel = options.should_cancel;
    let mut limits = options.limits;
    if let Some(limits) = limits.as_deref_mut() {
        limits.begin_file();
    }

    loop {
        if should_cancel() {
            return Err(scan_cancelled_io_error());
        }

        let bytes_read = reader.read(&mut buffer)?;
        if bytes_read == 0 {
            break;
        }

        let chunk = &buffer[..bytes_read];
        writer.write_all(chunk)?;
        b3_hasher.update(chunk);
        bytes_scanned =
            bytes_scanned.saturating_add(u64::try_from(chunk.len()).unwrap_or(u64::MAX));
        if let Some(limits) = limits.as_deref_mut() {
            limits
                .account(u64::try_from(chunk.len()).unwrap_or(u64::MAX))
                .map_err(limit_breach_io_error)?;
        }

        if header.len() < options.header_bytes {
            let required = (options.header_bytes - header.len()).min(chunk.len());
            header.extend_from_slice(&chunk[..required]);
        }
    }
    writer.flush()?;

    let head_b3 = options
        .emit_hashes
        .then(|| {
            (!header.is_empty())
                .then(|| blake3::hash(&header).to_hex().to_string().into_boxed_str())
        })
        .flatten();
    let b3 = b3_hasher.finalize().to_hex().to_string();
    let full_b3 = options.full_hash.then(|| b3.clone().into_boxed_str());

    Ok(WriteScanOutcome { header, head_b3, full_b3, b3, bytes_scanned, truncated_scan: false })
}

fn summary_from_descriptor(descriptor: &ArchiveDescriptor) -> ExtractArchiveSummary {
    ExtractArchiveSummary {
        path: descriptor.archive_path.to_string(),
        name: descriptor.archive_name.to_string(),
        size_bytes: descriptor.archive_size,
        mtime_unix: descriptor.archive_mtime_unix,
    }
}

#[allow(clippy::too_many_arguments)]
fn result_from_totals(
    extraction_id: String,
    archive: ExtractArchiveSummary,
    destination: ExtractDestinationSummary,
    totals: TypeAgg,
    stored_files: u64,
    stored_bytes: u64,
    skipped_oversize_files: u64,
    entries: Option<Vec<ExtractedEntry>>,
) -> ExtractArchiveResult {
    let entry_kinds = totals
        .entry_kind_counts()
        .into_iter()
        .map(|(kind, count)| ExtractEntryKindCount { kind, count })
        .collect();
    let mimes = totals
        .clone()
        .into_sorted_mime_counts()
        .into_iter()
        .map(|(mime, count)| ExtractMimeCount { mime, count })
        .collect();
    let types = totals
        .clone()
        .into_sorted_counts()
        .into_iter()
        .map(|(label, count)| ExtractTypeCount { label, count })
        .collect();

    ExtractArchiveResult {
        extraction_id,
        archive,
        destination,
        total_entries: totals.total_entries(),
        total_files: totals.total_files(),
        total_directories: totals.total_directories(),
        total_other_entries: totals.total_other_entries(),
        stored_files,
        stored_bytes,
        skipped_oversize_files,
        entry_kinds,
        types,
        mimes,
        entries,
    }
}

#[derive(Default)]
struct PathRegistry {
    used: HashSet<PathBuf>,
}

impl PathRegistry {
    fn safe_unique_path(&mut self, entry_name: &str, entry_index: u64) -> Result<PathBuf> {
        let safe = sanitize_entry_path(entry_name, entry_index)?;
        if self.used.insert(safe.clone()) {
            return Ok(safe);
        }

        let mut candidate = safe;
        for attempt in 0_u32..1000 {
            candidate = suffixed_path(&candidate, entry_index, attempt);
            if self.used.insert(candidate.clone()) {
                return Ok(candidate);
            }
        }
        Err(anyhow!("failed to allocate unique extracted path for archive entry `{entry_name}`"))
    }
}

fn sanitize_entry_path(entry_name: &str, entry_index: u64) -> Result<PathBuf> {
    let normalized = entry_name.replace('\\', "/");
    if normalized.trim().is_empty() {
        return Ok(PathBuf::from(format!("entry-{entry_index}")));
    }

    let path = Path::new(&normalized);
    let mut output = PathBuf::new();
    for (index, component) in path.components().enumerate() {
        match component {
            Component::Normal(value) => {
                let value = value.to_string_lossy();
                if index == 0 && value.ends_with(':') {
                    return Err(anyhow!(
                        "archive entry `{entry_name}` uses a Windows drive prefix"
                    ));
                }
                if value.is_empty() {
                    continue;
                }
                output.push(value.as_ref());
            }
            Component::CurDir => {}
            Component::ParentDir => {
                return Err(anyhow!(
                    "archive entry `{entry_name}` contains parent-directory traversal"
                ));
            }
            Component::RootDir | Component::Prefix(_) => {
                return Err(anyhow!("archive entry `{entry_name}` uses an absolute path"));
            }
        }
    }

    if output.as_os_str().is_empty() {
        Ok(PathBuf::from(format!("entry-{entry_index}")))
    } else {
        Ok(output)
    }
}

fn suffixed_path(path: &Path, entry_index: u64, attempt: u32) -> PathBuf {
    let suffix = if attempt == 0 {
        format!("__{entry_index}")
    } else {
        format!("__{entry_index}_{attempt}")
    };
    let parent = path.parent().filter(|parent| !parent.as_os_str().is_empty());
    let file_name = path.file_name().and_then(|name| name.to_str()).unwrap_or("entry");
    let candidate = match file_name.rsplit_once('.') {
        Some((stem, ext)) if !stem.is_empty() && !ext.is_empty() => {
            format!("{stem}{suffix}.{ext}")
        }
        _ => format!("{file_name}{suffix}"),
    };
    parent.map_or_else(|| PathBuf::from(&candidate), |parent| parent.join(&candidate))
}

fn path_to_portable_string(path: &Path) -> String {
    path.components()
        .filter_map(|component| match component {
            Component::Normal(value) => Some(value.to_string_lossy()),
            _ => None,
        })
        .collect::<Vec<_>>()
        .join("/")
}

fn saturating_u32_from_usize(value: usize) -> u32 {
    u32::try_from(value).unwrap_or(u32::MAX)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::backend::EntryMetadata;
    use std::{collections::HashMap, io::Cursor};
    use tempfile::tempdir;

    type MockEntry = (Box<str>, Vec<u8>, EntryKind);

    struct MockBackend {
        entries: HashMap<String, Vec<MockEntry>>,
    }

    impl MockBackend {
        fn new(path: &str, entries: Vec<(&str, &[u8], EntryKind)>) -> Self {
            Self {
                entries: HashMap::from([(
                    path.to_owned(),
                    entries
                        .into_iter()
                        .map(|(name, content, kind)| (name.into(), content.to_vec(), kind))
                        .collect(),
                )]),
            }
        }
    }

    impl ArchiveBackend for MockBackend {
        fn count_entries(&self, path: &Path, _options: BackendOptions) -> Result<usize> {
            Ok(self.entries.get(path.to_string_lossy().as_ref()).map_or(0, Vec::len))
        }

        fn for_each_entry(
            &self,
            path: &Path,
            _options: BackendOptions,
            visitor: &mut dyn FnMut(EntryMetadata, &mut dyn Read) -> Result<()>,
        ) -> Result<()> {
            if let Some(entries) = self.entries.get(path.to_string_lossy().as_ref()) {
                for (name, content, kind) in entries {
                    let mut reader = Cursor::new(content.clone());
                    visitor(EntryMetadata { name: name.clone(), kind: *kind }, &mut reader)?;
                }
            }
            Ok(())
        }
    }

    fn descriptor(path: &str) -> ArchiveDescriptor {
        ArchiveDescriptor {
            archive_index: 0,
            archive_name: "sample.zip".into(),
            archive_path: path.into(),
            archive_ext: "zip".into(),
            archive_size: 1024,
            archive_mtime_unix: 1_700_000_000,
        }
    }

    #[test]
    fn sanitize_entry_path_rejects_traversal_and_absolute_paths() {
        assert!(sanitize_entry_path("../evil.txt", 0).is_err());
        assert!(sanitize_entry_path("/evil.txt", 0).is_err());
        assert!(sanitize_entry_path("C:/evil.txt", 0).is_err());
        assert_eq!(
            sanitize_entry_path("nested\\file.txt", 0).expect("path should sanitize"),
            PathBuf::from("nested/file.txt")
        );
    }

    #[test]
    fn path_registry_allocates_unique_paths() {
        let mut registry = PathRegistry::default();

        assert_eq!(
            registry.safe_unique_path("same.txt", 1).expect("first path should work"),
            PathBuf::from("same.txt")
        );
        assert_eq!(
            registry.safe_unique_path("same.txt", 2).expect("second path should be suffixed"),
            PathBuf::from("same__2.txt")
        );
    }

    #[test]
    fn filesystem_extract_store_writes_files_and_metadata() {
        let dir = tempdir().expect("tempdir should exist");
        let archive_path = dir.path().join("archive.zip");
        fs::write(&archive_path, b"not-used").expect("archive placeholder should be written");
        let backend = MockBackend::new(
            archive_path.to_string_lossy().as_ref(),
            vec![
                ("nested/", b"", EntryKind::Directory),
                ("nested/file.txt", b"hello world", EntryKind::File),
                ("nested/file.txt", b"again", EntryKind::File),
            ],
        );
        let mut store = FilesystemExtractStore::new(dir.path().join("out"));

        let result = extract_archive(
            &descriptor(archive_path.to_string_lossy().as_ref()),
            &archive_path,
            &backend,
            BackendOptions { block_size: 1024 },
            ExtractConfig {
                extraction_id: "extract-test".to_owned(),
                header_bytes: 512,
                full_hash: true,
                emit_hashes: true,
                fast_only: true,
                collect_entries: true,
                limits: ExtractLimits::default(),
            },
            &mut store,
            |_| Ok(()),
        )
        .expect("archive should extract");

        assert_eq!(result.total_entries, 3);
        assert_eq!(result.total_files, 2);
        assert_eq!(result.total_directories, 1);
        assert_eq!(result.stored_files, 2);
        assert_eq!(result.stored_bytes, 16);
        assert!(dir.path().join("out/nested/file.txt").exists());
        assert!(dir.path().join("out/nested/file__2.txt").exists());
        let entries = result.entries.expect("entries should be collected");
        assert_eq!(entries[1].row.label.as_ref(), "txt");
        assert_eq!(entries[2].sanitized_path, "nested/file__2.txt");
        assert!(entries[1].row.full_b3.is_some());
    }

    #[test]
    fn copy_reader_to_writer_with_scan_writes_and_hashes() {
        let mut reader = Cursor::new(b"abcdef".to_vec());
        let mut writer = Vec::new();
        let mut cancelled = false;

        let outcome = copy_reader_to_writer_with_scan(
            &mut reader,
            &mut writer,
            FileWriteOptions {
                header_bytes: 3,
                full_hash: true,
                emit_hashes: true,
                should_cancel: &mut || cancelled,
                limits: None,
            },
        )
        .expect("copy should succeed");

        assert_eq!(writer, b"abcdef");
        assert_eq!(outcome.header.as_slice(), b"abc");
        assert_eq!(outcome.bytes_scanned, 6);
        assert_eq!(outcome.b3.len(), 64);
        assert!(outcome.head_b3.is_some());
        assert!(outcome.full_b3.is_some());

        cancelled = true;
        let mut reader = Cursor::new(b"abcdef".to_vec());
        let err = copy_reader_to_writer_with_scan(
            &mut reader,
            &mut Vec::new(),
            FileWriteOptions {
                header_bytes: 3,
                full_hash: false,
                emit_hashes: false,
                should_cancel: &mut || cancelled,
                limits: None,
            },
        )
        .expect_err("cancelled copy should fail");
        assert_eq!(err.kind(), io::ErrorKind::Interrupted);
    }

    // A tiny "compressed" size against a much larger uncompressed stream is the
    // signature of a zip bomb: the ratio guard must fire mid-stream.
    #[test]
    fn limit_tracker_flags_high_ratio() {
        let mut tracker = LimitTracker::new(ExtractLimits {
            max_file_bytes: None,
            max_total_bytes: None,
            max_ratio: Some(10.0),
            compressed_size: 100,
            deadline: None,
        });
        tracker.begin_file();
        // 1000 bytes against 100 compressed = ratio 10, still at the boundary.
        assert_eq!(tracker.account(1000), Ok(()));
        // One more byte tips it over 10x -> zip-bomb breach.
        assert_eq!(tracker.account(1), Err(LimitBreach::RatioExceeded));
    }

    #[test]
    fn limit_tracker_enforces_per_file_and_total_caps() {
        let mut tracker = LimitTracker::new(ExtractLimits {
            max_file_bytes: Some(64),
            max_total_bytes: Some(150),
            max_ratio: None,
            compressed_size: 0,
            deadline: None,
        });
        // First entry exceeds the per-file cap (skippable, non-fatal); its bytes
        // are rolled back out of the running total so they don't count.
        tracker.begin_file();
        assert_eq!(tracker.account(65), Err(LimitBreach::FileTooLarge));
        assert!(!LimitBreach::FileTooLarge.fatal());
        // Three within-cap entries: total climbs 50 -> 100 -> 150 (ok), then the
        // fourth tips it over the total cap (fatal).
        for _ in 0..3 {
            tracker.begin_file();
            assert_eq!(tracker.account(50), Ok(()));
        }
        tracker.begin_file();
        assert_eq!(tracker.account(1), Err(LimitBreach::TotalTooLarge));
        assert!(LimitBreach::TotalTooLarge.fatal());
    }

    // End to end: an oversize entry is skipped (and counted), while smaller
    // entries are still extracted — the archive is not aborted.
    #[test]
    fn extract_archive_skips_oversize_entry() {
        let dir = tempdir().expect("tempdir should exist");
        let archive_path = dir.path().join("archive.zip");
        fs::write(&archive_path, b"not-used").expect("archive placeholder should be written");
        let backend = MockBackend::new(
            archive_path.to_string_lossy().as_ref(),
            vec![
                ("small.txt", b"hello", EntryKind::File),
                ("huge.bin", &[0_u8; 4096], EntryKind::File),
                ("tail.txt", b"bye", EntryKind::File),
            ],
        );
        let mut store = FilesystemExtractStore::new(dir.path().join("out"));

        let result = extract_archive_with_interrupt(
            &descriptor(archive_path.to_string_lossy().as_ref()),
            &archive_path,
            &backend,
            BackendOptions { block_size: 1024 },
            ExtractConfig {
                extraction_id: "extract-limit".to_owned(),
                header_bytes: 512,
                full_hash: false,
                emit_hashes: false,
                fast_only: true,
                collect_entries: true,
                limits: ExtractLimits {
                    max_file_bytes: Some(1024),
                    max_total_bytes: None,
                    max_ratio: None,
                    compressed_size: 8,
                    deadline: None,
                },
            },
            &mut store,
            |_| Ok(()),
            ProcessControl::new(|_| {}, || false),
        )
        .expect("oversize entry must be skipped, not fail the archive");

        assert_eq!(result.skipped_oversize_files, 1);
        assert_eq!(result.stored_files, 2);
        assert!(dir.path().join("out/small.txt").exists());
        assert!(dir.path().join("out/tail.txt").exists());
        assert!(!dir.path().join("out/huge.bin").exists());
    }

    // A breach of the total-size cap aborts the whole archive.
    #[test]
    fn extract_archive_aborts_on_total_cap() {
        let dir = tempdir().expect("tempdir should exist");
        let archive_path = dir.path().join("archive.zip");
        fs::write(&archive_path, b"not-used").expect("archive placeholder should be written");
        let backend = MockBackend::new(
            archive_path.to_string_lossy().as_ref(),
            vec![
                ("a.bin", &[0_u8; 600], EntryKind::File),
                ("b.bin", &[0_u8; 600], EntryKind::File),
            ],
        );
        let mut store = FilesystemExtractStore::new(dir.path().join("out"));

        let err = extract_archive_with_interrupt(
            &descriptor(archive_path.to_string_lossy().as_ref()),
            &archive_path,
            &backend,
            BackendOptions { block_size: 1024 },
            ExtractConfig {
                extraction_id: "extract-total".to_owned(),
                header_bytes: 512,
                full_hash: false,
                emit_hashes: false,
                fast_only: true,
                collect_entries: false,
                limits: ExtractLimits {
                    max_file_bytes: None,
                    max_total_bytes: Some(1000),
                    max_ratio: None,
                    compressed_size: 8,
                    deadline: None,
                },
            },
            &mut store,
            |_| Ok(()),
            ProcessControl::new(|_| {}, || false),
        )
        .expect_err("total-size breach must abort the archive");

        assert_eq!(limit_breach_from_error(&err), Some(LimitBreach::TotalTooLarge));
    }
}
