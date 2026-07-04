//! Criterion benchmarks for the clustering hot path: chunk pooling and the
//! kNN-graph build.

use std::hint::black_box;

use criterion::{criterion_group, criterion_main, BenchmarkId, Criterion, Throughput};
use graph_compute::domain::config::DomainConfig;
use graph_compute::domain::graph::build_graph;
use graph_compute::domain::knn::build_knn_maps;
use graph_compute::domain::vector::{aggregate_docs, l2_normalize, ChunkPoint};

fn synthetic_points(docs: usize, chunks: usize, dim: usize) -> Vec<ChunkPoint> {
    let mut points = Vec::with_capacity(docs * chunks);
    for doc in 0..docs {
        let group = doc % 4;
        for chunk in 0..chunks {
            let mut vector = vec![0.0_f32; dim];
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
                    "Chunk {chunk} of document {doc}. We present results and findings with \
                     enough prose to exercise the section heuristics across sentences."
                )
                .into(),
                filename: Some(format!("doc-{doc}.pdf").into()),
            });
        }
    }
    points
}

fn normalized_docs(docs: usize, chunks: usize, dim: usize) -> Vec<Vec<f32>> {
    let config = DomainConfig::default();
    let points = synthetic_points(docs, chunks, dim);
    aggregate_docs(&points, &config).iter().map(|d| l2_normalize(&d.vector)).collect()
}

fn bench_aggregate(c: &mut Criterion) {
    let config = DomainConfig::default();
    let mut group = c.benchmark_group("aggregate_docs");
    for &docs in &[50_usize, 200] {
        let points = synthetic_points(docs, 4, 64);
        group.throughput(Throughput::Elements((docs * 4) as u64));
        group.bench_with_input(BenchmarkId::from_parameter(docs), &points, |b, points| {
            b.iter(|| aggregate_docs(black_box(points), &config));
        });
    }
    group.finish();
}

fn bench_knn_graph(c: &mut Criterion) {
    let config = DomainConfig::default();
    let mut group = c.benchmark_group("knn_graph");
    for &docs in &[100_usize, 400] {
        let vectors = normalized_docs(docs, 4, 64);
        let k = config.knn_k.min(vectors.len() - 1).max(1);
        group.throughput(Throughput::Elements(docs as u64));
        group.bench_with_input(BenchmarkId::from_parameter(docs), &vectors, |b, vectors| {
            b.iter(|| {
                let knn = build_knn_maps(black_box(vectors), k, config.knn_block_size);
                build_graph(&knn, vectors.len(), &config)
            });
        });
    }
    group.finish();
}

criterion_group!(hot_path, bench_aggregate, bench_knn_graph);
criterion_main!(hot_path);
