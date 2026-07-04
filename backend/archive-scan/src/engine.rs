use crate::{
    backend::{ArchiveBackend, BackendOptions},
    cancel::scan_cancelled_io_error,
    is_label_archive, lower_ext, magika_support,
    row::{ArchiveMeta, DetectionSource, EntryKind, EntryRow},
    scan, ultra_fast_magic,
};
use ahash::AHashMap as FastMap;
use anyhow::Result;
use std::{borrow::Cow, path::Path, sync::Arc};

pub const ENTRY_PROGRESS_CHUNK: u64 = 100;

pub struct ProcessControl<OnProgress, ShouldCancel> {
    pub(crate) on_progress: OnProgress,
    pub(crate) should_cancel: ShouldCancel,
}

impl<OnProgress, ShouldCancel> ProcessControl<OnProgress, ShouldCancel> {
    pub fn new(on_progress: OnProgress, should_cancel: ShouldCancel) -> Self {
        Self { on_progress, should_cancel }
    }
}

#[derive(Clone, Debug)]
pub struct ArchiveDescriptor {
    pub archive_index: u32,
    pub archive_name: Box<str>,
    pub archive_path: Box<str>,
    pub archive_ext: Box<str>,
    pub archive_size: u64,
    pub archive_mtime_unix: i64,
}

impl ArchiveDescriptor {
    fn shared_meta(&self) -> Arc<ArchiveMeta> {
        Arc::new(ArchiveMeta {
            archive_index: self.archive_index,
            archive_name: self.archive_name.clone(),
            archive_path: self.archive_path.clone(),
            archive_ext: self.archive_ext.clone(),
            archive_size: self.archive_size,
            archive_mtime_unix: self.archive_mtime_unix,
        })
    }
}

#[derive(Clone, Copy, Debug)]
pub struct ScanConfig {
    pub header_bytes: usize,
    pub full_hash: bool,
    pub emit_hashes: bool,
    pub emit_rows: bool,
    pub fast_only: bool,
}

#[derive(Default, Debug, Clone)]
pub struct TypeAgg {
    by_label: FastMap<Box<str>, u64>,
    by_mime: FastMap<Box<str>, u64>,
    total_entries: u64,
    total_files: u64,
    total_directories: u64,
    total_other_entries: u64,
}

impl TypeAgg {
    fn increment(map: &mut FastMap<Box<str>, u64>, key: &str) {
        if let Some(count) = map.get_mut(key) {
            *count += 1;
        } else {
            map.insert(key.into(), 1);
        }
    }

    pub fn record_file(&mut self, label: &str, mime: &str) {
        self.total_entries += 1;
        self.total_files += 1;
        if let Some(count) = self.by_label.get_mut(label) {
            *count += 1;
        } else {
            self.by_label.insert(label.into(), 1);
        }
        Self::increment(&mut self.by_mime, mime);
    }

    pub fn record_directory(&mut self) {
        self.total_entries += 1;
        self.total_directories += 1;
    }

    pub fn record_other_entry(&mut self) {
        self.total_entries += 1;
        self.total_other_entries += 1;
    }

    pub fn merge_from(&mut self, other: Self) {
        for (label, count) in other.by_label {
            *self.by_label.entry(label).or_insert(0) += count;
        }
        for (mime, count) in other.by_mime {
            *self.by_mime.entry(mime).or_insert(0) += count;
        }
        self.total_entries += other.total_entries;
        self.total_files += other.total_files;
        self.total_directories += other.total_directories;
        self.total_other_entries += other.total_other_entries;
    }

    #[must_use]
    pub const fn total_entries(&self) -> u64 {
        self.total_entries
    }

    #[must_use]
    pub const fn total_files(&self) -> u64 {
        self.total_files
    }

    #[must_use]
    pub const fn total_directories(&self) -> u64 {
        self.total_directories
    }

    #[must_use]
    pub const fn total_other_entries(&self) -> u64 {
        self.total_other_entries
    }

    pub fn iter(&self) -> impl Iterator<Item = (&str, u64)> + '_ {
        self.by_label.iter().map(|(label, count)| (label.as_ref(), *count))
    }

    pub fn mime_iter(&self) -> impl Iterator<Item = (&str, u64)> + '_ {
        self.by_mime.iter().map(|(mime, count)| (mime.as_ref(), *count))
    }

    #[must_use]
    pub fn into_sorted_counts(self) -> Vec<(Box<str>, u64)> {
        let mut pairs: Vec<_> = self.by_label.into_iter().collect();
        pairs.sort_by(|left, right| right.1.cmp(&left.1).then_with(|| left.0.cmp(&right.0)));
        pairs
    }

    #[must_use]
    pub fn into_sorted_mime_counts(self) -> Vec<(Box<str>, u64)> {
        let mut pairs: Vec<_> = self.by_mime.into_iter().collect();
        pairs.sort_by(|left, right| right.1.cmp(&left.1).then_with(|| left.0.cmp(&right.0)));
        pairs
    }

    #[must_use]
    pub fn entry_kind_counts(&self) -> Vec<(EntryKind, u64)> {
        let mut counts = Vec::new();
        if self.total_files > 0 {
            counts.push((EntryKind::File, self.total_files));
        }
        if self.total_directories > 0 {
            counts.push((EntryKind::Directory, self.total_directories));
        }
        if self.total_other_entries > 0 {
            counts.push((EntryKind::Other, self.total_other_entries));
        }
        counts
    }
}

struct DetectionMatch {
    label: Cow<'static, str>,
    mime: Cow<'static, str>,
    source: DetectionSource,
    confidence: f32,
}

impl DetectionMatch {
    fn heuristic(label: &'static str, mime: &'static str, confidence: f32) -> Self {
        Self {
            label: Cow::Borrowed(label),
            mime: Cow::Borrowed(mime),
            source: DetectionSource::Heuristic,
            confidence,
        }
    }

    fn unknown() -> Self {
        Self {
            label: Cow::Borrowed("unknown"),
            mime: Cow::Borrowed(""),
            source: DetectionSource::Unknown,
            confidence: 0.0,
        }
    }

    fn directory() -> Self {
        Self::heuristic("directory", "inode/directory", 1.0)
    }

    fn into_parts(self) -> (Cow<'static, str>, Cow<'static, str>, DetectionSource, f32) {
        (self.label, self.mime, self.source, self.confidence)
    }
}

#[must_use]
pub fn detect_type(
    entry_name: &str,
    entry_ext: &str,
    header: &[u8],
    fast_only: bool,
) -> (Cow<'static, str>, Cow<'static, str>, DetectionSource, f32) {
    let (fast_label, fast_mime) = ultra_fast_magic(header);
    if fast_label != "unknown" {
        return (Cow::Borrowed(fast_label), Cow::Borrowed(fast_mime), DetectionSource::Magic, 1.0);
    }

    let heuristic = heuristic_detection(entry_name, entry_ext, header);
    if !fast_only && header.len() >= magika_support::min_magika_bytes() {
        if let Some((magika_label, magika_mime, score)) = magika_support::identify(header) {
            let magika = DetectionMatch {
                label: Cow::Owned(magika_label),
                mime: Cow::Owned(magika_mime),
                source: DetectionSource::Magika,
                confidence: score,
            };
            return if heuristic.as_ref().is_some_and(|candidate| candidate.confidence > score) {
                heuristic.unwrap_or_else(DetectionMatch::unknown).into_parts()
            } else {
                magika.into_parts()
            };
        }
    }

    heuristic.unwrap_or_else(DetectionMatch::unknown).into_parts()
}

fn heuristic_detection(entry_name: &str, entry_ext: &str, header: &[u8]) -> Option<DetectionMatch> {
    let file_name =
        Path::new(entry_name).file_name().and_then(|name| name.to_str()).unwrap_or(entry_name);

    if file_name.eq_ignore_ascii_case("dockerfile") {
        return Some(DetectionMatch::heuristic("dockerfile", "text/x-dockerfile", 0.95));
    }

    if file_name.eq_ignore_ascii_case(".gitlab-ci.yml")
        || file_name.eq_ignore_ascii_case(".gitlab-ci.yaml")
    {
        return Some(DetectionMatch::heuristic("yaml", "application/yaml", 0.95));
    }

    if file_name.eq_ignore_ascii_case(".dockerignore")
        || file_name.eq_ignore_ascii_case(".gitignore")
        || file_name.eq_ignore_ascii_case(".python-version")
    {
        return Some(DetectionMatch::heuristic("txt", "text/plain", 0.8));
    }

    if entry_ext.eq_ignore_ascii_case("py") {
        Some(DetectionMatch::heuristic("python", "text/x-python", 0.95))
    } else if entry_ext.eq_ignore_ascii_case("html") || entry_ext.eq_ignore_ascii_case("htm") {
        Some(DetectionMatch::heuristic("html", "text/html", 0.9))
    } else if entry_ext.eq_ignore_ascii_case("yml") || entry_ext.eq_ignore_ascii_case("yaml") {
        Some(DetectionMatch::heuristic("yaml", "application/yaml", 0.9))
    } else if entry_ext.eq_ignore_ascii_case("sh")
        || entry_ext.eq_ignore_ascii_case("bash")
        || entry_ext.eq_ignore_ascii_case("zsh")
    {
        Some(DetectionMatch::heuristic("shell", "text/x-shellscript", 0.9))
    } else if entry_ext.eq_ignore_ascii_case("md") || entry_ext.eq_ignore_ascii_case("markdown") {
        Some(DetectionMatch::heuristic("markdown", "text/markdown", 0.9))
    } else if entry_ext.eq_ignore_ascii_case("toml") {
        Some(DetectionMatch::heuristic("toml", "application/toml", 0.9))
    } else if entry_ext.eq_ignore_ascii_case("txt") {
        Some(DetectionMatch::heuristic("txt", "text/plain", 0.7))
    } else if entry_ext.eq_ignore_ascii_case("asm") || entry_ext.eq_ignore_ascii_case("s") {
        Some(DetectionMatch::heuristic("asm", "text/x-asm", 0.85))
    } else if entry_ext.eq_ignore_ascii_case("onnx") {
        Some(DetectionMatch::heuristic("onnx", "application/octet-stream", 0.85))
    } else if looks_like_text(header) {
        Some(DetectionMatch::heuristic("txt", "text/plain", 0.55))
    } else {
        None
    }
}

fn looks_like_text(header: &[u8]) -> bool {
    !header.is_empty()
        && header.iter().all(|byte| {
            byte.is_ascii_graphic() || byte.is_ascii_whitespace() || matches!(byte, 0x08 | 0x0C)
        })
}

fn saturating_u32_from_usize(value: usize) -> u32 {
    u32::try_from(value).unwrap_or(u32::MAX)
}

/// Scans a single archive and reports per-entry types through a callback.
///
/// `on_row` is invoked only when `config.emit_rows` is enabled.
///
/// # Errors
///
/// Returns an error if the backend cannot iterate the archive, entry analysis fails, or the
/// `on_row` callback reports a sink failure.
pub fn process_archive<OnRow, OnProgress>(
    descriptor: &ArchiveDescriptor,
    source_path: &Path,
    backend: &dyn ArchiveBackend,
    backend_options: BackendOptions,
    config: ScanConfig,
    on_row: OnRow,
    on_progress: OnProgress,
) -> Result<TypeAgg>
where
    OnRow: FnMut(EntryRow) -> Result<()>,
    OnProgress: FnMut(u64),
{
    process_archive_with_interrupt(
        descriptor,
        source_path,
        backend,
        backend_options,
        config,
        on_row,
        ProcessControl::new(on_progress, || false),
    )
}

pub fn process_archive_with_interrupt<OnRow, OnProgress, ShouldCancel>(
    descriptor: &ArchiveDescriptor,
    source_path: &Path,
    backend: &dyn ArchiveBackend,
    backend_options: BackendOptions,
    config: ScanConfig,
    mut on_row: OnRow,
    mut control: ProcessControl<OnProgress, ShouldCancel>,
) -> Result<TypeAgg>
where
    OnRow: FnMut(EntryRow) -> Result<()>,
    OnProgress: FnMut(u64),
    ShouldCancel: FnMut() -> bool,
{
    let mut local = TypeAgg::default();
    let mut pending_progress = 0_u64;
    let mut entry_index = 0_u64;
    let archive_meta = config.emit_rows.then(|| descriptor.shared_meta());

    backend.for_each_entry(source_path, backend_options, &mut |entry, reader| {
        if (control.should_cancel)() {
            return Err(scan_cancelled_io_error().into());
        }

        let entry_name = entry.name;
        let entry_kind = entry.kind;
        let entry_ext = Path::new(entry_name.as_ref())
            .extension()
            .and_then(|extension| extension.to_str())
            .unwrap_or_default();
        let scan = (entry_kind != EntryKind::Directory)
            .then(|| {
                scan::analyze_reader_with_interrupt(
                    reader,
                    config.header_bytes,
                    config.full_hash,
                    config.emit_hashes,
                    &mut control.should_cancel,
                )
            })
            .transpose()?;

        if (control.should_cancel)() {
            return Err(scan_cancelled_io_error().into());
        }

        let (label, mime, detected_by, confidence) = match (entry_kind, scan.as_ref()) {
            (EntryKind::Directory, _) => DetectionMatch::directory().into_parts(),
            (_, Some(scan)) => {
                detect_type(entry_name.as_ref(), entry_ext, &scan.header, config.fast_only)
            }
            (_, None) => DetectionMatch::unknown().into_parts(),
        };

        match entry_kind {
            EntryKind::File => local.record_file(label.as_ref(), mime.as_ref()),
            EntryKind::Directory => local.record_directory(),
            EntryKind::Other => local.record_other_entry(),
        }

        if let Some(archive_meta) = &archive_meta {
            let is_nested_archive =
                entry_kind == EntryKind::File && is_label_archive(label.as_ref());
            let row = EntryRow {
                archive: Arc::clone(archive_meta),
                entry_index,
                entry_ext: lower_ext(entry_name.as_ref()).into_owned().into_boxed_str(),
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
            on_row(row)?;
        }

        entry_index += 1;
        pending_progress += 1;
        if pending_progress >= ENTRY_PROGRESS_CHUNK {
            (control.on_progress)(pending_progress);
            pending_progress = 0;
        }

        Ok(())
    })?;

    (control.on_progress)(pending_progress);
    Ok(local)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::{backend::EntryMetadata, cancel::is_scan_cancelled_error};
    use std::{
        collections::HashMap,
        io::{Cursor, Read},
    };

    type MockFile = (Box<str>, Vec<u8>);
    type MockArchive = Vec<MockFile>;
    type MockFixture<'a> = (&'a str, Vec<(&'a str, &'a [u8])>);

    struct MockBackend {
        entries: HashMap<String, MockArchive>,
        fail_on_visit: bool,
    }

    impl MockBackend {
        fn new(entries: &[MockFixture<'_>]) -> Self {
            let entries = entries
                .iter()
                .map(|(archive, files)| {
                    (
                        (*archive).to_owned(),
                        files
                            .iter()
                            .map(|(name, content)| ((*name).into(), content.to_vec()))
                            .collect(),
                    )
                })
                .collect();
            Self { entries, fail_on_visit: false }
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
            if self.fail_on_visit {
                anyhow::bail!("visit failed");
            }

            if let Some(entries) = self.entries.get(path.to_string_lossy().as_ref()) {
                for (name, content) in entries {
                    let mut reader = Cursor::new(content.clone());
                    visitor(
                        EntryMetadata {
                            name: name.clone(),
                            kind: if name.ends_with('/') {
                                EntryKind::Directory
                            } else {
                                EntryKind::File
                            },
                        },
                        &mut reader,
                    )?;
                }
            }

            Ok(())
        }
    }

    fn sample_descriptor() -> ArchiveDescriptor {
        ArchiveDescriptor {
            archive_index: 7,
            archive_name: "sample.zip".into(),
            archive_path: "/tmp/sample.zip".into(),
            archive_ext: "zip".into(),
            archive_size: 4_096,
            archive_mtime_unix: 1_700_000_000,
        }
    }

    #[test]
    fn detect_type_uses_magic_for_known_headers() {
        let (label, mime, source, confidence) =
            detect_type("payload.zip", "zip", b"PK\x03\x04payload", true);

        assert_eq!(label, "zip");
        assert_eq!(mime, "application/zip");
        assert_eq!(source, DetectionSource::Magic);
        assert_eq!(confidence, 1.0);
    }

    #[test]
    fn detect_type_keeps_unknown_when_magika_is_unavailable() {
        magika_support::set_disabled_for_tests(true);

        let (label, mime, source, confidence) =
            detect_type("mystery.bin", "bin", &vec![0_u8; 512], false);

        assert_eq!(label, "unknown");
        assert_eq!(mime, "");
        assert_eq!(source, DetectionSource::Unknown);
        assert_eq!(confidence, 0.0);

        magika_support::set_disabled_for_tests(false);
    }

    #[test]
    fn process_archive_aggregates_types_without_rows() {
        let backend = MockBackend::new(&[(
            "/tmp/sample.zip",
            vec![("alpha.pdf", b"%PDF-1.7 payload"), ("beta.zip", b"PK\x03\x04payload")],
        )]);
        let mut progress_updates = Vec::new();

        let totals = process_archive(
            &sample_descriptor(),
            Path::new("/tmp/sample.zip"),
            &backend,
            BackendOptions { block_size: 128 },
            ScanConfig {
                header_bytes: 512,
                full_hash: false,
                emit_hashes: false,
                emit_rows: false,
                fast_only: true,
            },
            |_row| Ok(()),
            |delta| progress_updates.push(delta),
        )
        .expect("archive scan should succeed");

        let mut counts: Vec<_> = totals.iter().collect();
        counts.sort_by(|left, right| left.0.cmp(right.0));

        assert_eq!(counts, vec![("pdf", 1), ("zip", 1)]);
        assert_eq!(totals.total_entries(), 2);
        assert_eq!(totals.total_files(), 2);
        assert_eq!(totals.total_directories(), 0);
        assert_eq!(progress_updates, vec![2]);
    }

    #[test]
    fn type_agg_sorts_ties_deterministically() {
        let mut totals = TypeAgg::default();
        totals.record_file("shell", "text/x-shellscript");
        totals.record_file("onnx", "application/octet-stream");
        totals.record_file("shell", "text/x-shellscript");
        totals.record_file("onnx", "application/octet-stream");
        totals.record_file("txt", "text/plain");

        assert_eq!(
            totals.clone().into_sorted_counts(),
            vec![("onnx".into(), 2), ("shell".into(), 2), ("txt".into(), 1)]
        );
        assert_eq!(
            totals.into_sorted_mime_counts(),
            vec![
                ("application/octet-stream".into(), 2),
                ("text/x-shellscript".into(), 2),
                ("text/plain".into(), 1)
            ]
        );
    }

    #[test]
    fn process_archive_emits_rows_with_hashes_when_requested() {
        let backend = MockBackend::new(&[(
            "/tmp/sample.zip",
            vec![("nested/archive.zip", b"PK\x03\x04payload")],
        )]);
        let mut rows = Vec::new();

        let totals = process_archive(
            &sample_descriptor(),
            Path::new("/tmp/sample.zip"),
            &backend,
            BackendOptions { block_size: 128 },
            ScanConfig {
                header_bytes: 4,
                full_hash: true,
                emit_hashes: true,
                emit_rows: true,
                fast_only: true,
            },
            |row| {
                rows.push(row);
                Ok(())
            },
            |_delta| {},
        )
        .expect("archive scan should succeed");

        assert_eq!(totals.total_files(), 1);
        assert_eq!(rows.len(), 1);
        assert_eq!(rows[0].archive.archive_name.as_ref(), "sample.zip");
        assert_eq!(rows[0].entry_name.as_ref(), "nested/archive.zip");
        assert_eq!(rows[0].entry_kind, EntryKind::File);
        assert_eq!(rows[0].label.as_ref(), "zip");
        assert!(rows[0].is_nested_archive);
        assert!(rows[0].head_b3.is_some());
        assert!(rows[0].full_b3.is_some());
    }

    #[test]
    fn process_archive_tracks_directory_entries_separately_from_file_types() {
        let backend = MockBackend::new(&[(
            "/tmp/sample.zip",
            vec![("folder/", b""), ("folder/main.py", b"print('ok')")],
        )]);
        let mut rows = Vec::new();

        let totals = process_archive(
            &sample_descriptor(),
            Path::new("/tmp/sample.zip"),
            &backend,
            BackendOptions { block_size: 128 },
            ScanConfig {
                header_bytes: 512,
                full_hash: false,
                emit_hashes: false,
                emit_rows: true,
                fast_only: true,
            },
            |row| {
                rows.push(row);
                Ok(())
            },
            |_delta| {},
        )
        .expect("archive scan should succeed");

        let counts: Vec<_> = totals.iter().collect();
        assert_eq!(counts, vec![("python", 1)]);
        assert_eq!(totals.total_entries(), 2);
        assert_eq!(totals.total_files(), 1);
        assert_eq!(totals.total_directories(), 1);
        assert_eq!(rows[0].entry_kind, EntryKind::Directory);
        assert_eq!(rows[0].label.as_ref(), "directory");
        assert_eq!(rows[1].entry_kind, EntryKind::File);
        assert_eq!(rows[1].label.as_ref(), "python");
    }

    #[test]
    fn detect_type_uses_filename_heuristics_for_short_text_configs() {
        let (label, mime, source, confidence) =
            detect_type(".gitlab-ci.yml", "yml", b"stages:\n  - test\n", true);

        assert_eq!(label, "yaml");
        assert_eq!(mime, "application/yaml");
        assert_eq!(source, DetectionSource::Heuristic);
        assert!(confidence >= 0.9);
    }

    #[test]
    fn process_archive_propagates_backend_errors() {
        let mut backend = MockBackend::new(&[]);
        backend.fail_on_visit = true;

        let err = process_archive(
            &sample_descriptor(),
            Path::new("/tmp/sample.zip"),
            &backend,
            BackendOptions { block_size: 128 },
            ScanConfig {
                header_bytes: 512,
                full_hash: false,
                emit_hashes: false,
                emit_rows: false,
                fast_only: true,
            },
            |_row| Ok(()),
            |_delta| {},
        )
        .expect_err("backend failure should bubble up");

        assert!(err.to_string().contains("visit failed"));
    }

    #[test]
    fn type_agg_sorts_by_frequency() {
        let mut totals = TypeAgg::default();
        totals.record_file("zip", "application/zip");
        totals.record_file("pdf", "application/pdf");
        totals.record_file("zip", "application/zip");

        let sorted = totals.clone().into_sorted_counts();

        assert_eq!(sorted[0].0.as_ref(), "zip");
        assert_eq!(sorted[0].1, 2);
        assert_eq!(sorted[1].0.as_ref(), "pdf");
        assert_eq!(sorted[1].1, 1);
        assert_eq!(totals.total_entries(), 3);
        assert_eq!(totals.total_files(), 3);
    }

    #[test]
    fn process_archive_with_interrupt_returns_cancelled_error() {
        let backend = MockBackend::new(&[("/tmp/sample.zip", vec![("alpha.txt", b"payload")])]);

        let err = process_archive_with_interrupt(
            &sample_descriptor(),
            Path::new("/tmp/sample.zip"),
            &backend,
            BackendOptions { block_size: 128 },
            ScanConfig {
                header_bytes: 512,
                full_hash: false,
                emit_hashes: false,
                emit_rows: false,
                fast_only: true,
            },
            |_row| Ok(()),
            ProcessControl::new(|_delta| {}, || true),
        )
        .expect_err("cancelled scans should bubble up as an error");

        assert!(is_scan_cancelled_error(&err));
    }
}
