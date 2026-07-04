//! Allocation regression for the clustering hot path (aggregate_docs →
//! build_knn_maps → build_graph). Mirrors archive-scan's harness: an instrumented
//! global allocator measures each scenario against a committed baseline; set
//! `GRAPH_COMPUTE_ALLOC_WRITE_BASELINE` to (re)write the baseline.

use graph_compute::domain::config::DomainConfig;
use graph_compute::domain::graph::build_graph;
use graph_compute::domain::knn::build_knn_maps;
use graph_compute::domain::vector::{
    aggregate_docs, l2_normalize, looks_like_prose, looks_like_references, ChunkPoint,
};
use serde::{Deserialize, Serialize};
use stats_alloc::{Region, StatsAlloc, INSTRUMENTED_SYSTEM};
use std::collections::BTreeMap;
use std::path::{Path, PathBuf};

const BASELINE_ENV: &str = "GRAPH_COMPUTE_ALLOC_WRITE_BASELINE";

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

fn synthetic_points() -> Vec<ChunkPoint> {
    const DOCS: usize = 40;
    const CHUNKS: usize = 4;
    const DIM: usize = 48;
    let mut points = Vec::with_capacity(DOCS * CHUNKS);
    for doc in 0..DOCS {
        let group = doc % 4;
        for chunk in 0..CHUNKS {
            let mut vector = vec![0.0_f32; DIM];
            for (k, slot) in vector.iter_mut().enumerate() {
                *slot = ((doc * 31 + chunk * 7 + k * 13) % 97) as f32 / 97.0 - 0.5;
            }
            vector[group] += 2.0;
            points.push(ChunkPoint {
                id: format!("doc-{doc}-{chunk}").into(),
                document_id: format!("doc-{doc}").into(),
                vector,
                chunk_index: chunk as i64,
                text: format!(
                    "Chunk {chunk} of document {doc}. We present a method and report results \
                     and findings across several experiments with enough running prose to make \
                     the section heuristics do real work over multiple sentences."
                )
                .into(),
                filename: Some(format!("doc-{doc}.pdf").into()),
            });
        }
    }
    points
}

/// Trigger every `LazyLock` regex and any one-time allocator state *before*
/// measuring, so their one-off cost is not attributed to a scenario.
fn warmup() {
    let _ = looks_like_prose("warm up prose with several words and sentences. Another sentence.");
    let _ = looks_like_references("References [1] x [2] y [3] z [4] w [5] v doi.org/10.1/a");
    let config = DomainConfig::default();
    let points = synthetic_points();
    let docs = aggregate_docs(&points, &config);
    let normalized: Vec<Vec<f32>> = docs.iter().map(|d| l2_normalize(&d.vector)).collect();
    let knn = build_knn_maps(&normalized, 6, config.knn_block_size);
    let _ = build_graph(&knn, normalized.len(), &config);
}

fn collect_measurements() -> Vec<ScenarioMeasurement> {
    warmup();
    let config = DomainConfig::default();
    let points = synthetic_points();
    let mut scenarios = Vec::new();

    let (docs, stats) = measure_allocations(|| aggregate_docs(&points, &config));
    assert!(!docs.is_empty(), "aggregation should yield documents");
    scenarios.push(ScenarioMeasurement {
        name: "aggregate_docs.synthetic",
        description: "Per-chunk normalize + weighted pooling of 40 docs × 4 chunks.",
        stats,
    });

    let normalized: Vec<Vec<f32>> = docs.iter().map(|d| l2_normalize(&d.vector)).collect();
    let k = config.knn_k.min(normalized.len().saturating_sub(1)).max(1);
    let (knn, stats) =
        measure_allocations(|| build_knn_maps(&normalized, k, config.knn_block_size));
    assert_eq!(knn.len(), normalized.len());
    scenarios.push(ScenarioMeasurement {
        name: "build_knn_maps.synthetic",
        description: "Blocked exact top-k cosine neighbours over the pooled document vectors.",
        stats,
    });

    let node_count = normalized.len();
    let (graph, stats) = measure_allocations(|| build_graph(&knn, node_count, &config));
    assert!(graph.edge_count() > 0, "the kNN graph should have edges");
    scenarios.push(ScenarioMeasurement {
        name: "build_graph.synthetic",
        description: "Mutual-kNN weighted graph build with isolated-node reattachment.",
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

    assert!(failures.is_empty(), "allocation regression detected:\n{}", failures.join("\n\n"));
}
