use crate::row::{EntryRow, SharedEntryRow};
use anyhow::{anyhow, Context, Result};
use crossbeam_channel::{bounded, Sender};
use parquet::{
    arrow::arrow_writer::ArrowWriter, basic::Compression, file::properties::WriterProperties,
};
use std::{
    fs::File,
    io::{BufWriter, Write},
    path::Path,
    path::PathBuf,
    sync::Arc,
    thread::JoinHandle,
};

const CHANNEL_CAPACITY: usize = 131_072;

struct WriterThread<T> {
    tx: Sender<T>,
    join: JoinHandle<Result<()>>,
    label: &'static str,
}

#[derive(Clone, Default)]
pub struct WriterFanout {
    ndjson: Option<Sender<SharedEntryRow>>,
    parquet: Option<Sender<SharedEntryRow>>,
}

pub struct WriterSet {
    ndjson: Option<WriterThread<SharedEntryRow>>,
    parquet: Option<WriterThread<SharedEntryRow>>,
}

impl WriterFanout {
    #[must_use]
    pub const fn enabled(&self) -> bool {
        self.ndjson.is_some() || self.parquet.is_some()
    }

    /// Sends a scanned row to every configured output sink.
    ///
    /// # Errors
    ///
    /// Returns an error if any configured writer channel is disconnected.
    pub fn send(&self, row: EntryRow) -> Result<()> {
        match (&self.ndjson, &self.parquet) {
            (None, None) => Ok(()),
            (Some(ndjson), None) => {
                ndjson.send(Arc::new(row)).context("failed to send row to NDJSON writer")
            }
            (None, Some(parquet)) => {
                parquet.send(Arc::new(row)).context("failed to send row to Parquet writer")
            }
            (Some(ndjson), Some(parquet)) => {
                let shared = Arc::new(row);
                ndjson.send(Arc::clone(&shared)).context("failed to send row to NDJSON writer")?;
                parquet.send(shared).context("failed to send row to Parquet writer")
            }
        }
    }
}

impl WriterSet {
    /// Creates file-backed NDJSON and/or Parquet writers.
    ///
    /// # Errors
    ///
    /// Returns an error if any target file cannot be created or a writer thread cannot start.
    pub fn new(
        ndjson_path: Option<&Path>,
        parquet_path: Option<&Path>,
        parquet_batch_size: usize,
    ) -> Result<(WriterFanout, Self)> {
        let ndjson = ndjson_path.map(spawn_ndjson_writer).transpose()?;
        let parquet =
            parquet_path.map(|path| spawn_parquet_writer(path, parquet_batch_size)).transpose()?;

        let fanout = WriterFanout {
            ndjson: ndjson.as_ref().map(|writer| writer.tx.clone()),
            parquet: parquet.as_ref().map(|writer| writer.tx.clone()),
        };

        Ok((fanout, Self { ndjson, parquet }))
    }

    /// Flushes and joins every configured writer thread.
    ///
    /// # Errors
    ///
    /// Returns an error if a writer thread fails, panics, or cannot finalize its output.
    pub fn finish(self) -> Result<()> {
        if let Some(writer) = self.ndjson {
            join_writer(writer)?;
        }
        if let Some(writer) = self.parquet {
            join_writer(writer)?;
        }
        Ok(())
    }
}

fn join_writer<T>(writer: WriterThread<T>) -> Result<()> {
    let WriterThread { tx, join, label } = writer;
    drop(tx);
    join.join().map_err(|_| anyhow!("{label} writer thread panicked"))?
}

fn spawn_ndjson_writer(path: &Path) -> Result<WriterThread<SharedEntryRow>> {
    let file = File::create(path)
        .with_context(|| format!("failed to create NDJSON file {}", path.display()))?;
    let path = path.to_path_buf();
    let (tx, rx) = bounded::<SharedEntryRow>(CHANNEL_CAPACITY);
    let join = std::thread::spawn(move || ndjson_writer_loop(path, file, rx));

    Ok(WriterThread { tx, join, label: "NDJSON" })
}

fn ndjson_writer_loop(
    path: PathBuf,
    file: File,
    rx: crossbeam_channel::Receiver<SharedEntryRow>,
) -> Result<()> {
    let mut writer = BufWriter::with_capacity(64 * 1024, file);
    while let Ok(row) = rx.recv() {
        serde_json::to_writer(&mut writer, row.as_ref())
            .with_context(|| format!("failed to serialize NDJSON row into {}", path.display()))?;
        writer
            .write_all(b"\n")
            .with_context(|| format!("failed to append newline to {}", path.display()))?;
    }
    writer.flush().with_context(|| format!("failed to flush NDJSON file {}", path.display()))
}

fn spawn_parquet_writer(path: &Path, batch_size: usize) -> Result<WriterThread<SharedEntryRow>> {
    let file = File::create(path)
        .with_context(|| format!("failed to create Parquet file {}", path.display()))?;
    let path = path.to_path_buf();
    let (tx, rx) = bounded::<SharedEntryRow>(CHANNEL_CAPACITY);
    let join = std::thread::spawn(move || parquet_writer_loop(path, file, rx, batch_size));

    Ok(WriterThread { tx, join, label: "Parquet" })
}

fn parquet_writer_loop(
    path: PathBuf,
    file: File,
    rx: crossbeam_channel::Receiver<SharedEntryRow>,
    batch_size: usize,
) -> Result<()> {
    use arrow_array::{
        ArrayRef, BooleanArray, Float32Array, Int64Array, RecordBatch, StringArray, UInt32Array,
        UInt64Array,
    };
    use arrow_schema::{DataType, Field, Schema};

    fn flush_batch<W: Write + Send>(
        writer: &mut ArrowWriter<W>,
        schema: &Arc<Schema>,
        buffer: &mut Vec<SharedEntryRow>,
    ) -> Result<()> {
        if buffer.is_empty() {
            return Ok(());
        }

        let archive_index =
            UInt32Array::from_iter_values(buffer.iter().map(|entry| entry.archive.archive_index));
        let archive_name = StringArray::from_iter_values(
            buffer.iter().map(|entry| entry.archive.archive_name.as_ref()),
        );
        let archive_path = StringArray::from_iter_values(
            buffer.iter().map(|entry| entry.archive.archive_path.as_ref()),
        );
        let archive_ext = StringArray::from_iter_values(
            buffer.iter().map(|entry| entry.archive.archive_ext.as_ref()),
        );
        let archive_size =
            UInt64Array::from_iter_values(buffer.iter().map(|entry| entry.archive.archive_size));
        let archive_mtime = Int64Array::from_iter_values(
            buffer.iter().map(|entry| entry.archive.archive_mtime_unix),
        );
        let entry_index =
            UInt64Array::from_iter_values(buffer.iter().map(|entry| entry.entry_index));
        let entry_name =
            StringArray::from_iter_values(buffer.iter().map(|entry| entry.entry_name.as_ref()));
        let entry_ext =
            StringArray::from_iter_values(buffer.iter().map(|entry| entry.entry_ext.as_ref()));
        let entry_kind =
            StringArray::from_iter_values(buffer.iter().map(|entry| match entry.entry_kind {
                crate::row::EntryKind::File => "file",
                crate::row::EntryKind::Directory => "directory",
                crate::row::EntryKind::Other => "other",
            }));
        let label = StringArray::from_iter_values(buffer.iter().map(|entry| entry.label.as_ref()));
        let mime = StringArray::from_iter_values(buffer.iter().map(|entry| entry.mime.as_ref()));
        let detected_by =
            StringArray::from_iter_values(buffer.iter().map(|entry| match entry.detected_by {
                crate::row::DetectionSource::Magic => "magic",
                crate::row::DetectionSource::Magika => "magika",
                crate::row::DetectionSource::Heuristic => "heuristic",
                crate::row::DetectionSource::Unknown => "unknown",
            }));
        let confidence =
            Float32Array::from_iter_values(buffer.iter().map(|entry| entry.confidence));
        let is_nested: BooleanArray =
            buffer.iter().map(|entry| Some(entry.is_nested_archive)).collect();
        let header_len = UInt32Array::from_iter_values(buffer.iter().map(|entry| entry.header_len));
        let bytes_scanned =
            UInt64Array::from_iter_values(buffer.iter().map(|entry| entry.bytes_scanned));
        let truncated_scan: BooleanArray =
            buffer.iter().map(|entry| Some(entry.truncated_scan)).collect();
        let head_b3: StringArray = buffer.iter().map(|entry| entry.head_b3.as_deref()).collect();
        let full_b3: StringArray = buffer.iter().map(|entry| entry.full_b3.as_deref()).collect();

        let arrays: Vec<ArrayRef> = vec![
            Arc::new(archive_index),
            Arc::new(archive_name),
            Arc::new(archive_path),
            Arc::new(archive_ext),
            Arc::new(archive_size),
            Arc::new(archive_mtime),
            Arc::new(entry_index),
            Arc::new(entry_name),
            Arc::new(entry_ext),
            Arc::new(entry_kind),
            Arc::new(label),
            Arc::new(mime),
            Arc::new(detected_by),
            Arc::new(confidence),
            Arc::new(is_nested),
            Arc::new(header_len),
            Arc::new(bytes_scanned),
            Arc::new(truncated_scan),
            Arc::new(head_b3),
            Arc::new(full_b3),
        ];

        let batch = RecordBatch::try_new(Arc::clone(schema), arrays)?;
        writer.write(&batch)?;
        buffer.clear();
        Ok(())
    }

    let schema = Arc::new(Schema::new(vec![
        Field::new("archive_index", DataType::UInt32, false),
        Field::new("archive_name", DataType::Utf8, false),
        Field::new("archive_path", DataType::Utf8, false),
        Field::new("archive_ext", DataType::Utf8, false),
        Field::new("archive_size", DataType::UInt64, false),
        Field::new("archive_mtime_unix", DataType::Int64, false),
        Field::new("entry_index", DataType::UInt64, false),
        Field::new("entry_name", DataType::Utf8, false),
        Field::new("entry_ext", DataType::Utf8, false),
        Field::new("entry_kind", DataType::Utf8, false),
        Field::new("label", DataType::Utf8, false),
        Field::new("mime", DataType::Utf8, false),
        Field::new("detected_by", DataType::Utf8, false),
        Field::new("confidence", DataType::Float32, false),
        Field::new("is_nested_archive", DataType::Boolean, false),
        Field::new("header_len", DataType::UInt32, false),
        Field::new("bytes_scanned", DataType::UInt64, false),
        Field::new("truncated_scan", DataType::Boolean, false),
        Field::new("head_b3", DataType::Utf8, true),
        Field::new("full_b3", DataType::Utf8, true),
    ]));

    let properties = WriterProperties::builder().set_compression(Compression::UNCOMPRESSED).build();
    let mut writer = ArrowWriter::try_new(
        BufWriter::with_capacity(128 * 1024, file),
        Arc::clone(&schema),
        Some(properties),
    )
    .with_context(|| format!("failed to initialize Parquet writer {}", path.display()))?;

    let mut buffer = Vec::with_capacity(batch_size);
    while let Ok(entry) = rx.recv() {
        buffer.push(entry);
        if buffer.len() >= batch_size {
            flush_batch(&mut writer, &schema, &mut buffer)
                .with_context(|| format!("failed to write Parquet batch {}", path.display()))?;
        }
    }

    if !buffer.is_empty() {
        flush_batch(&mut writer, &schema, &mut buffer)
            .with_context(|| format!("failed to finalize Parquet batch {}", path.display()))?;
    }

    writer
        .close()
        .with_context(|| format!("failed to finalize Parquet file {}", path.display()))?;
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::row::{ArchiveMeta, DetectionSource, EntryKind};
    use parquet::file::reader::{FileReader, SerializedFileReader};
    use std::{borrow::Cow, fs, panic, sync::Arc};
    use tempfile::tempdir;

    fn sample_row(label: &'static str) -> EntryRow {
        EntryRow {
            archive: Arc::new(ArchiveMeta {
                archive_index: 3,
                archive_name: "fixture.zip".into(),
                archive_path: "/tmp/fixture.zip".into(),
                archive_ext: "zip".into(),
                archive_size: 4_096,
                archive_mtime_unix: 1_700_000_000,
            }),
            entry_index: 11,
            entry_name: "nested/file.bin".into(),
            entry_ext: "bin".into(),
            entry_kind: EntryKind::File,
            label: Cow::Borrowed(label),
            mime: Cow::Borrowed("application/octet-stream"),
            detected_by: DetectionSource::Magic,
            confidence: 1.0,
            is_nested_archive: false,
            header_len: 128,
            bytes_scanned: 128,
            truncated_scan: true,
            head_b3: Some("head-hash".into()),
            full_b3: Some("full-hash".into()),
        }
    }

    #[test]
    fn writer_fanout_without_sinks_is_noop() {
        let fanout = WriterFanout::default();
        fanout.send(sample_row("binary")).expect("send without sinks should succeed");
    }

    #[test]
    fn ndjson_writer_persists_rows() {
        let dir = tempdir().expect("tempdir should exist");
        let ndjson_path = dir.path().join("out.ndjson");
        let (fanout, writers) =
            WriterSet::new(Some(&ndjson_path), None, 8).expect("writer set should initialize");

        fanout.send(sample_row("pdf")).expect("ndjson send should succeed");
        drop(fanout);
        writers.finish().expect("writers should finish");

        let content = fs::read_to_string(&ndjson_path).expect("ndjson file should be readable");
        assert!(content.contains("\"label\":\"pdf\""));
        assert!(content.contains("\"archive_name\":\"fixture.zip\""));
    }

    #[test]
    fn parquet_writer_persists_rows() {
        let dir = tempdir().expect("tempdir should exist");
        let parquet_path = dir.path().join("out.parquet");
        let (fanout, writers) =
            WriterSet::new(None, Some(&parquet_path), 1).expect("writer set should initialize");

        fanout.send(sample_row("png")).expect("parquet send should succeed");
        drop(fanout);
        writers.finish().expect("writers should finish");

        let reader =
            SerializedFileReader::new(File::open(&parquet_path).expect("parquet file should open"))
                .expect("parquet reader should initialize");
        assert_eq!(reader.metadata().file_metadata().num_rows(), 1);
    }

    #[test]
    fn writer_fanout_broadcasts_to_both_outputs() {
        let dir = tempdir().expect("tempdir should exist");
        let ndjson_path = dir.path().join("out.ndjson");
        let parquet_path = dir.path().join("out.parquet");
        let (fanout, writers) = WriterSet::new(Some(&ndjson_path), Some(&parquet_path), 1)
            .expect("writer set should initialize");

        fanout.send(sample_row("zip")).expect("broadcast send should succeed");
        drop(fanout);
        writers.finish().expect("writers should finish");

        let ndjson = fs::read_to_string(&ndjson_path).expect("ndjson file should be readable");
        let parquet =
            SerializedFileReader::new(File::open(&parquet_path).expect("parquet file should open"))
                .expect("parquet reader should initialize");

        assert!(ndjson.contains("\"label\":\"zip\""));
        assert_eq!(parquet.metadata().file_metadata().num_rows(), 1);
    }

    #[test]
    fn ndjson_writer_loop_flushes_rows_directly() {
        let dir = tempdir().expect("tempdir should exist");
        let path = dir.path().join("direct.ndjson");
        let file = File::create(&path).expect("ndjson file should open");
        let (tx, rx) = bounded(8);
        tx.send(Arc::new(sample_row("json"))).expect("row should send to direct loop");
        drop(tx);

        ndjson_writer_loop(path.clone(), file, rx).expect("direct ndjson loop should succeed");

        let content = fs::read_to_string(path).expect("ndjson output should be readable");
        assert!(content.contains("\"label\":\"json\""));
    }

    #[test]
    fn parquet_writer_loop_flushes_tail_batch_directly() {
        let dir = tempdir().expect("tempdir should exist");
        let path = dir.path().join("direct.parquet");
        let file = File::create(&path).expect("parquet file should open");
        let (tx, rx) = bounded(8);
        tx.send(Arc::new(sample_row("tail"))).expect("row should send to direct loop");
        drop(tx);

        parquet_writer_loop(path.clone(), file, rx, 8).expect("direct parquet loop should work");

        let reader =
            SerializedFileReader::new(File::open(&path).expect("parquet file should reopen"))
                .expect("parquet reader should initialize");
        assert_eq!(reader.metadata().file_metadata().num_rows(), 1);
    }

    #[test]
    fn join_writer_reports_panics() {
        let (tx, _rx) = bounded::<SharedEntryRow>(1);
        let join = std::thread::spawn(|| -> Result<()> {
            panic!("boom");
        });
        let writer = WriterThread { tx, join, label: "panic-test" };

        let err = join_writer(writer).expect_err("join_writer should surface panics");
        assert!(err.to_string().contains("panic-test writer thread panicked"));
    }
}
