#![cfg(not(feature = "jemalloc"))]

use anyhow::Result;
use archive_scan::{
    backend::{ArchiveBackend, BackendOptions, EntryMetadata},
    engine::{self, detect_type, ArchiveDescriptor, ScanConfig},
    row::{ArchiveMeta, DetectionSource, EntryKind, EntryRow},
    scan,
};
use serde::{Deserialize, Serialize};
use stats_alloc::{Region, StatsAlloc, INSTRUMENTED_SYSTEM};
use std::{
    borrow::Cow,
    collections::BTreeMap,
    io::{Cursor, Read},
    path::{Path, PathBuf},
    sync::Arc,
};

const BASELINE_ENV: &str = "ARCHIVE_SCAN_ALLOC_WRITE_BASELINE";

#[global_allocator]
static GLOBAL_ALLOCATOR: &StatsAlloc<std::alloc::System> = &INSTRUMENTED_SYSTEM;

#[derive(Clone, Copy, Debug, Default, Deserialize, Eq, PartialEq, Serialize)]
struct AllocationStats {
    allocations: u64,
    reallocations: u64,
    allocated_bytes: u64,
}

#[derive(Debug, Deserialize, Serialize)]
struct AllocationBaselineFile {
    version: u32,
    scenarios: Vec<AllocationBaselineEntry>,
}

#[derive(Debug, Deserialize, Serialize)]
struct AllocationBaselineEntry {
    name: String,
    description: String,
    max_allocations: u64,
    max_reallocations: u64,
    max_allocated_bytes: u64,
    observed_allocations: u64,
    observed_reallocations: u64,
    observed_allocated_bytes: u64,
}

struct ScenarioMeasurement {
    name: &'static str,
    description: &'static str,
    stats: AllocationStats,
}

impl ScenarioMeasurement {
    fn baseline_entry(&self) -> AllocationBaselineEntry {
        AllocationBaselineEntry {
            name: self.name.to_owned(),
            description: self.description.to_owned(),
            max_allocations: self.stats.allocations,
            max_reallocations: self.stats.reallocations,
            max_allocated_bytes: self.stats.allocated_bytes,
            observed_allocations: self.stats.allocations,
            observed_reallocations: self.stats.reallocations,
            observed_allocated_bytes: self.stats.allocated_bytes,
        }
    }
}

#[derive(Clone)]
struct SingleEntryBackend {
    entry_name: &'static str,
    entry_kind: EntryKind,
    payload: &'static [u8],
}

impl ArchiveBackend for SingleEntryBackend {
    fn count_entries(&self, _path: &Path, _options: BackendOptions) -> Result<usize> {
        Ok(1)
    }

    fn for_each_entry(
        &self,
        _path: &Path,
        _options: BackendOptions,
        visitor: &mut dyn FnMut(EntryMetadata, &mut dyn Read) -> Result<()>,
    ) -> Result<()> {
        let mut reader = Cursor::new(self.payload);
        visitor(EntryMetadata { name: self.entry_name.into(), kind: self.entry_kind }, &mut reader)
    }
}

fn measure_allocations<T>(f: impl FnOnce() -> T) -> (T, AllocationStats) {
    let region = Region::new(GLOBAL_ALLOCATOR);
    let result = f();
    let change = region.change();
    (
        result,
        AllocationStats {
            allocations: change.allocations as u64,
            reallocations: change.reallocations as u64,
            allocated_bytes: change.bytes_allocated as u64,
        },
    )
}

fn baseline_path() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("tests")
        .join("baselines")
        .join("hot_path_allocations.json")
}

fn sample_blocks(total_bytes: usize, chunk_size: usize) -> Vec<&'static [u8]> {
    let mut data = vec![0_u8; total_bytes];
    for (index, byte) in data.iter_mut().enumerate() {
        *byte = (index % 251) as u8;
    }
    data[0..8].copy_from_slice(b"\x89PNG\x0d\x0a\x1a\x0a");
    let leaked: &'static [u8] = Box::leak(data.into_boxed_slice());
    leaked.chunks(chunk_size).collect()
}

fn sample_archive_descriptor() -> ArchiveDescriptor {
    ArchiveDescriptor {
        archive_index: 7,
        archive_name: "payloads.zip".into(),
        archive_path: "/tmp/payloads.zip".into(),
        archive_ext: "zip".into(),
        archive_size: 123_456_789,
        archive_mtime_unix: 1_744_000_000,
    }
}

fn sample_entry_row() -> EntryRow {
    EntryRow {
        archive: Arc::new(ArchiveMeta {
            archive_index: 7,
            archive_name: "payloads.zip".into(),
            archive_path: "/tmp/payloads.zip".into(),
            archive_ext: "zip".into(),
            archive_size: 123_456_789,
            archive_mtime_unix: 1_744_000_000,
        }),
        entry_index: 42,
        entry_name: "nested/archive/document.PDF".into(),
        entry_ext: "pdf".into(),
        entry_kind: EntryKind::File,
        label: Cow::Borrowed("pdf"),
        mime: Cow::Borrowed("application/pdf"),
        detected_by: DetectionSource::Magic,
        confidence: 1.0,
        is_nested_archive: false,
        header_len: 512,
        bytes_scanned: 8_192,
        truncated_scan: true,
        head_b3: Some("82f64e6be809763df98195dfa5de656c6a58c1239fdc866f88c4a8c9cfd263d1".into()),
        full_b3: Some("22d266cdab1db4ea6fcec0f81612a82bcfc17f25f5a784f2270f4c5fdc2f0c6b".into()),
    }
}

fn collect_measurements() -> Vec<ScenarioMeasurement> {
    let mut scenarios = Vec::new();

    let (extension, stats) =
        measure_allocations(|| archive_scan::lower_ext("nested/archive/file.txt"));
    assert_eq!(extension.as_ref(), "txt");
    scenarios.push(ScenarioMeasurement {
        name: "lower_ext.lowercase_extension",
        description: "Already lowercase extensions in the archive hot path stay allocation-free.",
        stats,
    });

    let ((label, mime, source, confidence), stats) = measure_allocations(|| {
        detect_type("nested/archive/README.TXT", "TXT", b"plain text\n", true)
    });
    assert_eq!(label.as_ref(), "txt");
    assert_eq!(mime.as_ref(), "text/plain");
    assert_eq!(source, DetectionSource::Heuristic);
    assert_eq!(confidence, 0.7);
    scenarios.push(ScenarioMeasurement {
        name: "detect_type.heuristic_uppercase_extension",
        description:
            "Filename/extension heuristics must not allocate when fast_only short-circuits Magika.",
        stats,
    });

    let scan_blocks = sample_blocks(128 * 1024, 4 * 1024);
    let scan_payload = scan_blocks.concat();

    let (scan_outcome, stats) = measure_allocations(|| {
        let mut reader = Cursor::new(scan_payload.as_slice());
        scan::analyze_reader(&mut reader, 512, false, false)
    });
    let scan_outcome = scan_outcome.expect("header-only scan should succeed");
    assert_eq!(scan_outcome.header.len(), 512);
    assert_eq!(scan_outcome.bytes_scanned, 8 * 1024);
    assert!(scan_outcome.truncated_scan);
    scenarios.push(ScenarioMeasurement {
        name: "scan.analyze_reader_header_only",
        description: "Header-only scan on the hot path should avoid heap traffic entirely.",
        stats,
    });

    let (scan_outcome, stats) = measure_allocations(|| {
        let mut reader = Cursor::new(scan_payload.as_slice());
        scan::analyze_reader(&mut reader, 512, false, true)
    });
    let scan_outcome = scan_outcome.expect("header-hash scan should succeed");
    assert_eq!(scan_outcome.header.len(), 512);
    assert!(scan_outcome.head_b3.is_some());
    assert!(scan_outcome.full_b3.is_none());
    scenarios.push(ScenarioMeasurement {
        name: "scan.analyze_reader_header_hash",
        description: "Header hashing should stay bounded to the single hash-string allocation.",
        stats,
    });

    let (scan_outcome, stats) = measure_allocations(|| {
        let mut reader = Cursor::new(scan_payload.as_slice());
        scan::analyze_reader(&mut reader, 512, true, true)
    });
    let scan_outcome = scan_outcome.expect("full-hash scan should succeed");
    assert_eq!(scan_outcome.header.len(), 512);
    assert!(scan_outcome.head_b3.is_some());
    assert!(scan_outcome.full_b3.is_some());
    scenarios.push(ScenarioMeasurement {
        name: "scan.analyze_reader_full_hash",
        description: "Full hashing should stay bounded to the two hash-string allocations.",
        stats,
    });

    let backend = SingleEntryBackend {
        entry_name: "nested/archive/README.TXT",
        entry_kind: EntryKind::File,
        payload: b"plain text file\n",
    };
    let descriptor = sample_archive_descriptor();
    let (totals, stats) = measure_allocations(|| {
        engine::process_archive(
            &descriptor,
            Path::new("/tmp/payloads.zip"),
            &backend,
            BackendOptions { block_size: 4 * 1024 },
            ScanConfig {
                header_bytes: 512,
                full_hash: false,
                emit_hashes: false,
                emit_rows: false,
                fast_only: true,
            },
            |_| Ok(()),
            |_| {},
        )
    });
    let totals = totals.expect("engine scan without rows should succeed");
    assert_eq!(totals.total_entries(), 1);
    assert_eq!(totals.total_files(), 1);
    scenarios.push(ScenarioMeasurement {
        name: "engine.process_archive_without_rows",
        description: "Core scan aggregation without row sinks should not regress in heap traffic.",
        stats,
    });

    let mut seen_row = None;
    let (totals, stats) = measure_allocations(|| {
        engine::process_archive(
            &descriptor,
            Path::new("/tmp/payloads.zip"),
            &backend,
            BackendOptions { block_size: 4 * 1024 },
            ScanConfig {
                header_bytes: 512,
                full_hash: false,
                emit_hashes: false,
                emit_rows: true,
                fast_only: true,
            },
            |row| {
                seen_row = Some(row);
                Ok(())
            },
            |_| {},
        )
    });
    let totals = totals.expect("engine scan with row emission should succeed");
    assert_eq!(totals.total_entries(), 1);
    let seen_row = seen_row.expect("row sink should receive the scanned entry");
    assert_eq!(seen_row.entry_ext.as_ref(), "txt");
    assert_eq!(seen_row.label.as_ref(), "txt");
    scenarios.push(ScenarioMeasurement {
        name: "engine.process_archive_with_rows",
        description: "Row emission stays within a fixed allocation budget per scanned entry.",
        stats,
    });

    let row = sample_entry_row();
    let mut buffer = Vec::with_capacity(1024);
    let ((), stats) = measure_allocations(|| {
        buffer.clear();
        serde_json::to_writer(&mut buffer, &row).expect("row serialization should succeed");
    });
    assert!(!buffer.is_empty());
    scenarios.push(ScenarioMeasurement {
        name: "serialization.entry_row_ndjson_preallocated",
        description: "Serializing an entry row into a preallocated buffer should avoid heap churn.",
        stats,
    });

    scenarios
}

fn load_baseline(path: &Path) -> AllocationBaselineFile {
    let content = std::fs::read_to_string(path)
        .unwrap_or_else(|err| panic!("failed to read {path:?}: {err}"));
    serde_json::from_str(&content)
        .unwrap_or_else(|err| panic!("failed to parse allocation baseline {path:?}: {err}"))
}

fn write_baseline(path: &Path, scenarios: &[ScenarioMeasurement]) {
    let file = AllocationBaselineFile {
        version: 1,
        scenarios: scenarios.iter().map(ScenarioMeasurement::baseline_entry).collect(),
    };
    let parent = path.parent().expect("baseline file should always have a parent directory");
    std::fs::create_dir_all(parent)
        .unwrap_or_else(|err| panic!("failed to create baseline directory {parent:?}: {err}"));
    let json =
        serde_json::to_string_pretty(&file).expect("allocation baseline should serialize to JSON");
    std::fs::write(path, format!("{json}\n"))
        .unwrap_or_else(|err| panic!("failed to write allocation baseline {path:?}: {err}"));
}

#[test]
fn hot_path_allocation_regression_matches_baseline() {
    let scenarios = collect_measurements();
    let baseline_path = baseline_path();

    if std::env::var_os(BASELINE_ENV).is_some() {
        write_baseline(&baseline_path, &scenarios);
        return;
    }

    let baseline = load_baseline(&baseline_path);
    assert_eq!(baseline.version, 1, "unexpected allocation baseline schema version");

    let mut remaining = baseline
        .scenarios
        .into_iter()
        .map(|entry| (entry.name.clone(), entry))
        .collect::<BTreeMap<_, _>>();

    let mut failures = Vec::new();
    for scenario in &scenarios {
        let Some(expected) = remaining.remove(scenario.name) else {
            failures.push(format!("missing baseline entry for scenario {}", scenario.name));
            continue;
        };

        let actual = scenario.stats;
        if actual.allocations > expected.max_allocations
            || actual.reallocations > expected.max_reallocations
            || actual.allocated_bytes > expected.max_allocated_bytes
        {
            failures.push(format!(
                concat!(
                    "{} exceeded allocation budget\n",
                    "  description: {}\n",
                    "  actual: allocations={}, reallocations={}, allocated_bytes={}\n",
                    "  budget: allocations<={}, reallocations<={}, allocated_bytes<={}"
                ),
                scenario.name,
                scenario.description,
                actual.allocations,
                actual.reallocations,
                actual.allocated_bytes,
                expected.max_allocations,
                expected.max_reallocations,
                expected.max_allocated_bytes,
            ));
        }
    }

    if !remaining.is_empty() {
        let extras = remaining.into_keys().collect::<Vec<_>>().join(", ");
        failures.push(format!("baseline contains stale scenarios: {extras}"));
    }

    if !failures.is_empty() {
        panic!("allocation regression detected:\n{}", failures.join("\n\n"));
    }
}
