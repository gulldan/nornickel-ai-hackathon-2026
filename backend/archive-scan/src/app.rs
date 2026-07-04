use crate::{
    backend::{ArchiveBackend, BackendOptions, LibarchiveBackend},
    engine::{self, ArchiveDescriptor, ScanConfig, TypeAgg},
    extract::{self, ExtractArchiveResult, ExtractConfig, ExtractedEntry, FilesystemExtractStore},
    is_archive_ext, lower_ext,
    output::WriterSet,
};
use anyhow::{Context, Result};
use clap::Parser;
use indicatif::{MultiProgress, ProgressBar, ProgressStyle};
use rayon::prelude::*;
use std::{
    ffi::OsString,
    fs::File,
    io::{BufWriter, Write},
    path::Path,
    path::PathBuf,
    time::SystemTime,
};
use walkdir::WalkDir;

const PARQUET_BATCH_SIZE: usize = 8_192;

#[derive(Parser, Debug, Clone)]
#[command(
    name = "archive_scan",
    about = "High-throughput archive scanner with Magika and BLAKE3 metadata"
)]
pub struct Args {
    /// Path to directory with archives
    pub path: PathBuf,

    /// Number of threads (0 = auto)
    #[arg(long, env = "ARCHIVE_SCAN_THREADS", default_value_t = 0)]
    pub threads: usize,

    /// Header bytes for type detection
    #[arg(long, env = "ARCHIVE_SCAN_HEADER_BYTES", default_value_t = 512)]
    pub header_bytes: usize,

    /// Archive read block size (bytes)
    #[arg(long, env = "ARCHIVE_SCAN_BLOCK_SIZE", default_value_t = 2 * 1024 * 1024)]
    pub block_size: usize,

    /// Calculate full BLAKE3 for each entry (expensive)
    #[arg(
        long,
        env = "ARCHIVE_SCAN_FULL_HASH",
        default_value_t = false,
        num_args = 0..=1,
        default_missing_value = "true",
        value_parser = clap::builder::BoolishValueParser::new()
    )]
    pub full_hash: bool,

    /// Save raw per-entry records to NDJSON
    #[arg(long, env = "ARCHIVE_SCAN_OUT_NDJSON")]
    pub out_ndjson: Option<PathBuf>,

    /// Save raw per-entry records to Apache Parquet
    #[arg(long, env = "ARCHIVE_SCAN_OUT_PARQUET")]
    pub out_parquet: Option<PathBuf>,

    /// Pre-count entries (expensive, opens archives twice)
    #[arg(
        long,
        env = "ARCHIVE_SCAN_PRECOUNT",
        default_value_t = false,
        num_args = 0..=1,
        default_missing_value = "true",
        value_parser = clap::builder::BoolishValueParser::new()
    )]
    pub precount: bool,

    /// Skip Magika ML inference (use only fast magic bytes)
    #[arg(
        long,
        env = "ARCHIVE_SCAN_FAST_ONLY",
        default_value_t = false,
        num_args = 0..=1,
        default_missing_value = "true",
        value_parser = clap::builder::BoolishValueParser::new()
    )]
    pub fast_only: bool,
}

#[derive(Parser, Debug, Clone)]
#[command(name = "archive_scan extract", about = "Extract archives locally and emit metadata")]
pub struct ExtractArgs {
    /// Path to directory with archives
    pub path: PathBuf,

    /// Directory where extracted files will be written
    #[arg(long, env = "ARCHIVE_SCAN_EXTRACT_OUT_DIR")]
    pub out_dir: PathBuf,

    /// Number of threads (0 = auto)
    #[arg(long, env = "ARCHIVE_SCAN_THREADS", default_value_t = 0)]
    pub threads: usize,

    /// Header bytes for type detection
    #[arg(long, env = "ARCHIVE_SCAN_HEADER_BYTES", default_value_t = 512)]
    pub header_bytes: usize,

    /// Archive read block size (bytes)
    #[arg(long, env = "ARCHIVE_SCAN_BLOCK_SIZE", default_value_t = 2 * 1024 * 1024)]
    pub block_size: usize,

    /// Calculate full BLAKE3 for each extracted file
    #[arg(
        long,
        env = "ARCHIVE_SCAN_FULL_HASH",
        default_value_t = false,
        num_args = 0..=1,
        default_missing_value = "true",
        value_parser = clap::builder::BoolishValueParser::new()
    )]
    pub full_hash: bool,

    /// Skip Magika ML inference (use only fast magic bytes)
    #[arg(
        long,
        env = "ARCHIVE_SCAN_FAST_ONLY",
        default_value_t = false,
        num_args = 0..=1,
        default_missing_value = "true",
        value_parser = clap::builder::BoolishValueParser::new()
    )]
    pub fast_only: bool,

    /// Save per-entry extraction metadata to NDJSON
    #[arg(long, env = "ARCHIVE_SCAN_EXTRACT_METADATA_NDJSON")]
    pub metadata_ndjson: Option<PathBuf>,

    /// Print per-entry extraction metadata as NDJSON to stdout
    #[arg(
        long,
        env = "ARCHIVE_SCAN_EXTRACT_PRINT_ENTRIES",
        default_value_t = false,
        num_args = 0..=1,
        default_missing_value = "true",
        value_parser = clap::builder::BoolishValueParser::new()
    )]
    pub print_entries: bool,
}

#[derive(Clone)]
struct ProgressBars {
    archives: Option<ProgressBar>,
    entries: Option<ProgressBar>,
    manager: MultiProgress,
}

impl ProgressBars {
    fn new(archive_count: usize, entry_count: Option<usize>) -> Self {
        let manager = MultiProgress::new();
        let archives = (archive_count > 1).then(|| {
            let progress = manager.add(ProgressBar::new(archive_count as u64));
            progress.set_style(progress_bar_style());
            progress.set_message("Archives");
            progress
        });

        let entries = entry_count.map_or_else(
            || {
                let progress = manager.add(ProgressBar::new_spinner());
                progress.set_style(progress_spinner_style());
                progress.set_message("Processing entries");
                Some(progress)
            },
            |total| {
                let progress = manager.add(ProgressBar::new(total as u64));
                progress.set_style(progress_bar_style());
                progress.set_message("Entries");
                Some(progress)
            },
        );

        Self { archives, entries, manager }
    }

    fn inc_archive(&self) {
        if let Some(progress) = &self.archives {
            progress.inc(1);
        }
    }

    fn inc_entries(&self, delta: u64) {
        if delta == 0 {
            return;
        }
        if let Some(progress) = &self.entries {
            progress.inc(delta);
        }
    }

    fn finish(self) {
        if let Some(progress) = self.archives {
            progress.finish_and_clear();
        }
        if let Some(progress) = self.entries {
            progress.finish_and_clear();
        }
        let _ = self.manager.clear();
    }
}

#[inline(always)]
fn file_size(path: &Path) -> u64 {
    path.metadata().map_or(0, |metadata| metadata.len())
}

#[inline(always)]
fn file_mtime_unix(path: &Path) -> i64 {
    path.metadata()
        .and_then(|metadata| metadata.modified())
        .unwrap_or(SystemTime::UNIX_EPOCH)
        .duration_since(SystemTime::UNIX_EPOCH)
        .map_or(0, |duration| i64::try_from(duration.as_secs()).unwrap_or(i64::MAX))
}

fn saturating_u32_from_usize(value: usize) -> u32 {
    u32::try_from(value).unwrap_or(u32::MAX)
}

fn progress_bar_style() -> ProgressStyle {
    ProgressStyle::default_bar()
        .template(
            "{spinner:.green} {msg} [{bar:40.cyan/blue}] {pos}/{len} ({percent}%) [{elapsed_precise}] ETA: {eta}",
        )
        .expect("progress template is valid")
        .progress_chars("█▉▊▋▌▍▎▏  ")
}

fn progress_spinner_style() -> ProgressStyle {
    ProgressStyle::default_spinner()
        .template("{spinner:.green} {msg} {pos} entries [{elapsed_precise}]")
        .expect("spinner template is valid")
}

fn collect_archives(root: &Path) -> Vec<PathBuf> {
    WalkDir::new(root)
        .into_iter()
        .filter_map(Result::ok)
        .filter(|entry| entry.file_type().is_file())
        .map(walkdir::DirEntry::into_path)
        .filter(|path| is_archive_ext(path))
        .filter(|path| path.metadata().map_or(true, |metadata| metadata.len() >= 100))
        .collect()
}

fn archive_descriptor_from_path(archive_index: usize, path: &Path) -> ArchiveDescriptor {
    let archive_name = path
        .file_name()
        .and_then(|name| name.to_str())
        .unwrap_or("<unknown>")
        .to_owned()
        .into_boxed_str();

    ArchiveDescriptor {
        archive_index: saturating_u32_from_usize(archive_index),
        archive_ext: lower_ext(archive_name.as_ref()).into_owned().into_boxed_str(),
        archive_name,
        archive_path: path.display().to_string().into_boxed_str(),
        archive_size: file_size(path),
        archive_mtime_unix: file_mtime_unix(path),
    }
}

fn print_summary(totals: TypeAgg) {
    println!("\n=== Archive Entry Summary ===");
    println!("Total entries scanned: {}", totals.total_entries());
    println!("Files analyzed: {}", totals.total_files());
    println!("Directories: {}", totals.total_directories());
    if totals.total_other_entries() > 0 {
        println!("Other entries: {}", totals.total_other_entries());
    }
    println!();
    println!("=== File Types inside archives (sorted by frequency) ===");
    let total_files = totals.total_files();
    for (label, count) in totals.into_sorted_counts() {
        let percentage =
            if total_files == 0 { 0.0 } else { (count as f64 / total_files as f64) * 100.0 };
        println!("{count:>8}  {percentage:5.1}%  {label}");
    }
}

/// Runs the default CLI pipeline with the built-in libarchive backend.
///
/// # Errors
///
/// Returns an error if the scan pipeline, output writers, or backend processing fails.
pub fn run(args: Args) -> Result<()> {
    let backend = LibarchiveBackend;
    run_with_backend(&args, &backend)
}

/// Parses and runs the command-line interface.
///
/// Existing invocations like `archive_scan /path/to/archives` keep the scan behavior. New production
/// extraction uses `archive_scan extract /path/to/archives --out-dir /data/out`.
///
/// # Errors
///
/// Returns an error if CLI parsing, archive scanning, extraction, or output persistence fails.
pub fn run_cli() -> Result<()> {
    let argv = std::env::args_os().collect::<Vec<_>>();
    let Some(command) = argv.get(1).and_then(|value| value.to_str()) else {
        return run(Args::parse());
    };

    match command {
        "scan" => run(Args::parse_from(command_argv_without_subcommand(&argv))),
        "extract" => run_extract(ExtractArgs::parse_from(command_argv_without_subcommand(&argv))),
        _ => run(Args::parse_from(argv)),
    }
}

/// Runs the CLI pipeline against the provided backend implementation.
///
/// # Errors
///
/// Returns an error if thread-pool setup, archive processing, or result persistence fails.
pub fn run_with_backend(args: &Args, backend: &dyn ArchiveBackend) -> Result<()> {
    if args.threads > 0 {
        rayon::ThreadPoolBuilder::new()
            .num_threads(args.threads)
            .build_global()
            .context("failed to configure Rayon global thread pool")?;
    }

    let archives = collect_archives(&args.path);
    if archives.is_empty() {
        println!("No archive files found");
        return Ok(());
    }

    println!("Found {} archive files", archives.len());
    let backend_options = BackendOptions { block_size: args.block_size };

    let entry_count = if args.precount {
        println!("Counting entries...");
        let total: usize = archives
            .par_iter()
            .map(|path| backend.count_entries(path, backend_options).unwrap_or(0))
            .sum();
        println!("Processing {} entries in {} archives", total, archives.len());
        Some(total)
    } else {
        None
    };

    let progress = ProgressBars::new(archives.len(), entry_count);
    let (fanout, writers) = WriterSet::new(
        args.out_ndjson.as_deref(),
        args.out_parquet.as_deref(),
        PARQUET_BATCH_SIZE,
    )?;
    let config = ScanConfig {
        header_bytes: args.header_bytes,
        full_hash: args.full_hash,
        emit_hashes: fanout.enabled(),
        emit_rows: fanout.enabled(),
        fast_only: args.fast_only,
    };

    let totals_result = archives
        .par_iter()
        .enumerate()
        .map(|(archive_index, path)| {
            let descriptor = archive_descriptor_from_path(archive_index, path);
            let totals = engine::process_archive(
                &descriptor,
                path,
                backend,
                backend_options,
                config,
                |row| fanout.send(row),
                |delta| progress.inc_entries(delta),
            )?;
            progress.inc_archive();
            Ok::<TypeAgg, anyhow::Error>(totals)
        })
        .try_reduce(TypeAgg::default, |mut left, right| {
            left.merge_from(right);
            Ok(left)
        });

    drop(fanout);
    let writers_result = writers.finish();
    progress.finish();

    let totals = totals_result?;
    writers_result?;
    print_summary(totals);
    Ok(())
}

/// Extracts every archive under `args.path` into `args.out_dir` and writes metadata.
///
/// # Errors
///
/// Returns an error if thread-pool setup, archive processing, extraction, or metadata output fails.
pub fn run_extract(args: ExtractArgs) -> Result<()> {
    let backend = LibarchiveBackend;
    run_extract_with_backend(&args, &backend)
}

pub fn run_extract_with_backend(args: &ExtractArgs, backend: &dyn ArchiveBackend) -> Result<()> {
    if args.threads > 0 {
        let _ = rayon::ThreadPoolBuilder::new().num_threads(args.threads).build_global();
    }

    let archives = collect_archives(&args.path);
    if archives.is_empty() {
        println!("No archive files found");
        return Ok(());
    }

    std::fs::create_dir_all(&args.out_dir).with_context(|| {
        format!("failed to create extraction output directory {}", args.out_dir.display())
    })?;

    println!("Found {} archive files", archives.len());
    println!("Extracting into {}", args.out_dir.display());

    let metadata_writer = args
        .metadata_ndjson
        .as_deref()
        .map(File::create)
        .transpose()
        .with_context(|| {
            format!(
                "failed to create metadata NDJSON file {}",
                args.metadata_ndjson
                    .as_deref()
                    .map_or_else(|| "<none>".to_owned(), |path| path.display().to_string())
            )
        })?
        .map(|file| std::sync::Mutex::new(BufWriter::with_capacity(128 * 1024, file)));
    let print_lock = std::sync::Mutex::new(());
    let backend_options = BackendOptions { block_size: args.block_size };
    let progress = ProgressBars::new(archives.len(), None);

    let results = archives
        .par_iter()
        .enumerate()
        .map(|(archive_index, path)| {
            let descriptor = archive_descriptor_from_path(archive_index, path);
            let extract_root =
                args.out_dir.join(archive_output_dir_name(archive_index, path, &descriptor));
            let mut store = FilesystemExtractStore::new(extract_root);
            let extraction_id = format!("local-{archive_index:08}");
            let result = extract::extract_archive_with_interrupt(
                &descriptor,
                path,
                backend,
                backend_options,
                ExtractConfig {
                    extraction_id,
                    header_bytes: args.header_bytes,
                    full_hash: args.full_hash,
                    emit_hashes: args.full_hash,
                    fast_only: args.fast_only,
                    collect_entries: false,
                    // The local CLI extractor is operator-driven; resource caps
                    // are enforced on the service path, not here.
                    limits: extract::ExtractLimits::default(),
                },
                &mut store,
                |entry| {
                    if let Some(writer) = &metadata_writer {
                        let mut writer = writer
                            .lock()
                            .map_err(|_| anyhow::anyhow!("metadata writer lock is poisoned"))?;
                        write_extracted_entry_ndjson(&mut *writer, entry)?;
                    }
                    if args.print_entries {
                        let _guard = print_lock
                            .lock()
                            .map_err(|_| anyhow::anyhow!("stdout metadata lock is poisoned"))?;
                        serde_json::to_writer(std::io::stdout(), entry)
                            .context("failed to write extraction metadata to stdout")?;
                        println!();
                    }
                    Ok(())
                },
                engine::ProcessControl::new(|delta| progress.inc_entries(delta), || false),
            )?;
            progress.inc_archive();
            Ok::<ExtractArchiveResult, anyhow::Error>(result)
        })
        .collect::<Result<Vec<_>>>()?;

    if let Some(writer) = metadata_writer {
        writer
            .into_inner()
            .map_err(|_| anyhow::anyhow!("metadata writer lock is poisoned"))?
            .flush()
            .context("failed to flush extraction metadata NDJSON")?;
    }
    progress.finish();
    print_extract_summary(&results);
    Ok(())
}

fn command_argv_without_subcommand(argv: &[OsString]) -> Vec<OsString> {
    argv.iter()
        .enumerate()
        .filter(|(index, _)| *index != 1)
        .map(|(_, value)| value.clone())
        .collect()
}

fn write_extracted_entry_ndjson(writer: &mut dyn Write, entry: &ExtractedEntry) -> Result<()> {
    serde_json::to_writer(&mut *writer, entry)
        .context("failed to serialize extraction metadata")?;
    writer.write_all(b"\n").context("failed to append extraction metadata newline")
}

fn archive_output_dir_name(index: usize, path: &Path, descriptor: &ArchiveDescriptor) -> String {
    let stem = path
        .file_stem()
        .and_then(|stem| stem.to_str())
        .unwrap_or_else(|| descriptor.archive_name.as_ref());
    let safe = sanitize_output_dir_segment(stem);
    format!("{index:08}-{safe}")
}

fn sanitize_output_dir_segment(value: &str) -> String {
    let mut output = String::with_capacity(value.len());
    for character in value.chars() {
        if character.is_ascii_alphanumeric() || matches!(character, '-' | '_' | '.') {
            output.push(character);
        } else {
            output.push('_');
        }
    }
    if output.is_empty() {
        "archive".to_owned()
    } else {
        output
    }
}

fn print_extract_summary(results: &[ExtractArchiveResult]) {
    let archives = results.len();
    let total_entries = results.iter().map(|result| result.total_entries).sum::<u64>();
    let total_files = results.iter().map(|result| result.total_files).sum::<u64>();
    let stored_files = results.iter().map(|result| result.stored_files).sum::<u64>();
    let stored_bytes = results.iter().map(|result| result.stored_bytes).sum::<u64>();

    println!("\n=== Archive Extraction Summary ===");
    println!("Archives processed: {archives}");
    println!("Total entries: {total_entries}");
    println!("Files analyzed: {total_files}");
    println!("Files stored: {stored_files}");
    println!("Bytes stored: {stored_bytes}");
    println!();

    for result in results {
        println!(
            "{} -> {} (entries: {}, files: {}, stored bytes: {})",
            result.archive.name,
            result.destination.root,
            result.total_entries,
            result.stored_files,
            result.stored_bytes
        );
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::backend::EntryMetadata;
    use crate::engine::{detect_type, process_archive};
    use crate::row::{DetectionSource, EntryKind};
    use std::{
        collections::HashMap,
        io::{Cursor, Read},
    };
    use tempfile::tempdir;

    type MockFile = (Box<str>, Vec<u8>);
    type MockArchive = Vec<MockFile>;
    type MockFixture<'a> = (&'a str, Vec<(&'a str, &'a [u8])>);

    struct MockBackend {
        entries: HashMap<String, MockArchive>,
        fail_on_visit: bool,
        count_error: bool,
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
            Self { entries, fail_on_visit: false, count_error: false }
        }
    }

    impl ArchiveBackend for MockBackend {
        fn count_entries(&self, path: &Path, _options: BackendOptions) -> Result<usize> {
            if self.count_error {
                anyhow::bail!("count failed");
            }
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

    #[test]
    fn collect_archives_filters_non_archives_and_small_files() {
        let dir = tempdir().expect("tempdir should exist");
        let archive = dir.path().join("good.zip");
        let tiny_archive = dir.path().join("tiny.zip");
        let plain = dir.path().join("plain.txt");

        std::fs::write(&archive, vec![0_u8; 128]).expect("archive fixture should be written");
        std::fs::write(&tiny_archive, vec![0_u8; 12]).expect("tiny archive should be written");
        std::fs::write(&plain, vec![0_u8; 256]).expect("plain file should be written");

        let archives = collect_archives(dir.path());

        assert_eq!(archives, vec![archive]);
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
        crate::magika_support::set_disabled_for_tests(true);
        let header = vec![0_u8; crate::magika_support::min_magika_bytes()];

        let (label, mime, source, confidence) = detect_type("mystery.bin", "bin", &header, false);

        assert_eq!(label, "unknown");
        assert_eq!(mime, "");
        assert_eq!(source, DetectionSource::Unknown);
        assert_eq!(confidence, 0.0);

        crate::magika_support::set_disabled_for_tests(false);
    }

    #[test]
    fn process_archive_aggregates_entry_types() {
        let archive_path = Path::new("/virtual/archive.zip");
        let backend = MockBackend::new(&[(
            "/virtual/archive.zip",
            vec![
                ("nested.pdf", b"%PDF-1.7payload" as &[u8]),
                ("image.png", b"\x89PNG\x0d\x0a\x1a\x0aextra"),
                ("inner.zip", b"PK\x03\x04payload"),
            ],
        )]);
        let progress = ProgressBars::new(1, Some(3));
        let (fanout, writers) =
            WriterSet::new(None, None, PARQUET_BATCH_SIZE).expect("writer set should initialize");

        let totals = process_archive(
            &archive_descriptor_from_path(0, archive_path),
            archive_path,
            &backend,
            BackendOptions { block_size: 64 * 1024 },
            ScanConfig {
                header_bytes: 512,
                full_hash: false,
                emit_hashes: false,
                emit_rows: false,
                fast_only: true,
            },
            |row| fanout.send(row),
            |delta| progress.inc_entries(delta),
        )
        .expect("archive should be processed");

        progress.inc_archive();
        drop(fanout);
        writers.finish().expect("writers should finish");
        progress.finish();

        let counts: HashMap<_, _> = totals.iter().collect();
        assert_eq!(counts.get("pdf"), Some(&1));
        assert_eq!(counts.get("png"), Some(&1));
        assert_eq!(counts.get("zip"), Some(&1));
        assert_eq!(totals.total_entries(), 3);
        assert_eq!(totals.total_files(), 3);
        assert_eq!(totals.total_directories(), 0);
    }

    #[test]
    fn run_with_backend_tolerates_precount_errors() {
        let dir = tempdir().expect("tempdir should exist");
        let archive = dir.path().join("sample.zip");
        std::fs::write(&archive, vec![0_u8; 128]).expect("archive fixture should be written");

        let backend =
            MockBackend { entries: HashMap::new(), fail_on_visit: false, count_error: true };

        run_with_backend(
            &Args {
                path: dir.path().to_path_buf(),
                threads: 0,
                header_bytes: 512,
                block_size: 64 * 1024,
                full_hash: false,
                out_ndjson: None,
                out_parquet: None,
                precount: true,
                fast_only: true,
            },
            &backend,
        )
        .expect("run should tolerate precount failures");
    }

    #[test]
    fn progress_bars_cover_bar_and_spinner_modes() {
        let bars = ProgressBars::new(2, Some(5));
        bars.inc_archive();
        bars.inc_entries(2);
        bars.inc_entries(0);
        bars.finish();

        let spinner = ProgressBars::new(1, None);
        spinner.inc_entries(3);
        spinner.finish();
    }

    #[test]
    fn file_metadata_helpers_handle_existing_and_missing_files() {
        let dir = tempdir().expect("tempdir should exist");
        let file = dir.path().join("sample.bin");
        std::fs::write(&file, vec![0_u8; 32]).expect("fixture file should be written");

        assert_eq!(file_size(&file), 32);
        assert!(file_mtime_unix(&file) > 0);

        let missing = dir.path().join("missing.bin");
        assert_eq!(file_size(&missing), 0);
        assert_eq!(file_mtime_unix(&missing), 0);
    }

    #[test]
    fn process_archive_writes_rows_when_outputs_are_enabled() {
        let dir = tempdir().expect("tempdir should exist");
        let archive_path = dir.path().join("fixture.zip");
        std::fs::write(&archive_path, vec![0_u8; 128]).expect("archive fixture should exist");
        let ndjson_path = dir.path().join("rows.ndjson");
        let backend = MockBackend::new(&[(
            archive_path.to_string_lossy().as_ref(),
            vec![("inner.zip", b"PK\x03\x04payload" as &[u8])],
        )]);
        let progress = ProgressBars::new(1, Some(1));
        let (fanout, writers) = WriterSet::new(Some(&ndjson_path), None, PARQUET_BATCH_SIZE)
            .expect("writer set should initialize");

        let totals = process_archive(
            &archive_descriptor_from_path(3, &archive_path),
            &archive_path,
            &backend,
            BackendOptions { block_size: 64 * 1024 },
            ScanConfig {
                header_bytes: 512,
                full_hash: false,
                emit_hashes: true,
                emit_rows: true,
                fast_only: true,
            },
            |row| fanout.send(row),
            |delta| progress.inc_entries(delta),
        )
        .expect("archive should be processed");

        progress.inc_archive();
        drop(fanout);
        writers.finish().expect("writers should finish");
        progress.finish();

        let content = std::fs::read_to_string(&ndjson_path).expect("ndjson should exist");
        let counts: HashMap<_, _> = totals.iter().collect();
        assert_eq!(counts.get("zip"), Some(&1));
        assert!(content.contains("\"archive_index\":3"));
        assert!(content.contains("\"archive_name\":\"fixture.zip\""));
        assert!(content.contains("\"entry_kind\":\"file\""));
        assert!(content.contains("\"is_nested_archive\":true"));
    }

    #[test]
    fn print_summary_executes_for_empty_and_non_empty_totals() {
        print_summary(TypeAgg::default());

        let mut totals = TypeAgg::default();
        totals.record_file("zip", "application/zip");
        totals.record_file("zip", "application/zip");
        totals.record_file("pdf", "application/pdf");
        print_summary(totals);
    }

    #[test]
    fn run_with_backend_returns_ok_when_no_archives_are_found() {
        let dir = tempdir().expect("tempdir should exist");

        run_with_backend(
            &Args {
                path: dir.path().to_path_buf(),
                threads: 0,
                header_bytes: 512,
                block_size: 64 * 1024,
                full_hash: false,
                out_ndjson: None,
                out_parquet: None,
                precount: false,
                fast_only: true,
            },
            &MockBackend::new(&[]),
        )
        .expect("empty directory should be handled");
    }

    #[test]
    fn run_uses_default_backend_and_handles_empty_directories() {
        let dir = tempdir().expect("tempdir should exist");

        run(Args {
            path: dir.path().to_path_buf(),
            threads: 0,
            header_bytes: 512,
            block_size: 64 * 1024,
            full_hash: false,
            out_ndjson: None,
            out_parquet: None,
            precount: false,
            fast_only: true,
        })
        .expect("default backend run should handle empty directories");
    }

    #[test]
    fn run_with_backend_processes_archives_without_precount() {
        let dir = tempdir().expect("tempdir should exist");
        let archive_path = dir.path().join("fixture.zip");
        std::fs::write(&archive_path, vec![0_u8; 128]).expect("archive fixture should exist");
        let backend = MockBackend::new(&[(
            archive_path.to_string_lossy().as_ref(),
            vec![("doc.pdf", b"%PDF-1.7payload" as &[u8])],
        )]);

        run_with_backend(
            &Args {
                path: dir.path().to_path_buf(),
                threads: 0,
                header_bytes: 512,
                block_size: 64 * 1024,
                full_hash: false,
                out_ndjson: None,
                out_parquet: None,
                precount: false,
                fast_only: true,
            },
            &backend,
        )
        .expect("backend run should process archives without precount");
    }
}
