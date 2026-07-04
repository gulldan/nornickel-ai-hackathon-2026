use archive_scan::{
    is_archive_ext, lower_ext,
    row::{ArchiveMeta, DetectionSource, EntryKind, EntryRow},
    scan, ultra_fast_magic,
};
use criterion::{criterion_group, criterion_main, BatchSize, BenchmarkId, Criterion, Throughput};
use std::{borrow::Cow, hint::black_box, io::Cursor, path::Path, sync::Arc};

fn sample_archive_meta() -> Arc<ArchiveMeta> {
    Arc::new(ArchiveMeta {
        archive_index: 7,
        archive_name: "payloads.zip".into(),
        archive_path: "/tmp/payloads.zip".into(),
        archive_ext: "zip".into(),
        archive_size: 123_456_789,
        archive_mtime_unix: 1_744_000_000,
    })
}

fn sample_entry_row() -> EntryRow {
    EntryRow {
        archive: sample_archive_meta(),
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

fn sample_blocks(total_bytes: usize, chunk_size: usize) -> Vec<&'static [u8]> {
    let mut payload = vec![0_u8; total_bytes];
    for (index, byte) in payload.iter_mut().enumerate() {
        *byte = (index % 251) as u8;
    }
    payload[0..8].copy_from_slice(b"\x89PNG\x0d\x0a\x1a\x0a");
    let leaked: &'static [u8] = Box::leak(payload.into_boxed_slice());
    leaked.chunks(chunk_size).collect()
}

fn bench_extensions(c: &mut Criterion) {
    let mut group = c.benchmark_group("extensions");
    let cases = [
        "folder/payload.zip",
        "folder/PAYLOAD.ZIP",
        ".hidden",
        "folder/without_ext",
        "folder/deep/archive.tar.gz",
    ];

    for input in cases {
        group.bench_with_input(BenchmarkId::new("lower_ext", input), &input, |b, input| {
            b.iter(|| lower_ext(black_box(input)));
        });
        group.bench_with_input(BenchmarkId::new("is_archive_ext", input), &input, |b, input| {
            let path = Path::new(input);
            b.iter(|| is_archive_ext(black_box(path)));
        });
    }
    group.finish();
}

fn bench_magic_detection(c: &mut Criterion) {
    let mut group = c.benchmark_group("magic_detection");
    let fixtures: [(&str, &[u8]); 4] = [
        ("zip", b"PK\x03\x04metadata"),
        ("png", b"\x89PNG\x0d\x0a\x1a\x0aextra"),
        ("sqlite", b"SQLite format 3\0with payload"),
        ("unknown", b"????????????????"),
    ];

    for (name, bytes) in fixtures {
        group.throughput(Throughput::Bytes(bytes.len() as u64));
        group.bench_with_input(BenchmarkId::from_parameter(name), &bytes, |b, bytes| {
            b.iter(|| ultra_fast_magic(black_box(bytes)));
        });
    }
    group.finish();
}

fn bench_scan(c: &mut Criterion) {
    let blocks = sample_blocks(128 * 1024, 4 * 1024);

    let mut group = c.benchmark_group("scan");
    group.throughput(Throughput::Bytes((128 * 1024) as u64));

    group.bench_function("header_only", |b| {
        b.iter_batched(
            || Cursor::new(blocks.concat()),
            |mut reader| {
                scan::analyze_reader(black_box(&mut reader), 512, false, false)
                    .expect("scan header_only should succeed")
            },
            BatchSize::SmallInput,
        );
    });

    group.bench_function("header_hash", |b| {
        b.iter_batched(
            || Cursor::new(blocks.concat()),
            |mut reader| {
                scan::analyze_reader(black_box(&mut reader), 512, false, true)
                    .expect("scan header_hash should succeed")
            },
            BatchSize::SmallInput,
        );
    });

    group.bench_function("full_hash", |b| {
        b.iter_batched(
            || Cursor::new(blocks.concat()),
            |mut reader| {
                scan::analyze_reader(black_box(&mut reader), 512, true, true)
                    .expect("scan full_hash should succeed")
            },
            BatchSize::SmallInput,
        );
    });

    group.finish();
}

fn bench_ndjson_serialization(c: &mut Criterion) {
    let row = sample_entry_row();
    let mut group = c.benchmark_group("serialization");
    group.bench_function("entry_row_ndjson", |b| {
        b.iter_batched(
            Vec::new,
            |mut buffer| {
                serde_json::to_writer(&mut buffer, black_box(&row))
                    .expect("serialization should succeed");
                buffer
            },
            BatchSize::SmallInput,
        );
    });
    group.finish();
}

criterion_group!(
    hot_path,
    bench_extensions,
    bench_magic_detection,
    bench_scan,
    bench_ndjson_serialization
);
criterion_main!(hot_path);
