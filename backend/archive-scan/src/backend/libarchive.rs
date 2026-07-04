use super::{ArchiveBackend, BackendOptions, EntryMetadata};
use crate::cancel::is_scan_cancelled_error;
use crate::row::EntryKind;
use anyhow::{Context, Result};
use std::{
    borrow::Cow,
    io::{self, Read},
    path::Path,
};

#[derive(Clone, Copy, Debug, Default)]
pub struct LibarchiveBackend;

impl ArchiveBackend for LibarchiveBackend {
    fn count_entries(&self, path: &Path, options: BackendOptions) -> Result<usize> {
        let mut archive = archive_reader::Archive::open(path);
        archive.block_size(options.block_size);
        archive
            .list_file_names()
            .map(|entries| entries.filter_map(core::result::Result::ok).count())
            .with_context(|| format!("failed to count entries in archive {}", path.display()))
    }

    fn for_each_entry(
        &self,
        path: &Path,
        options: BackendOptions,
        visitor: &mut dyn FnMut(EntryMetadata, &mut dyn Read) -> Result<()>,
    ) -> Result<()> {
        let mut archive = archive_reader::Archive::open(path);
        archive.block_size(options.block_size);
        archive
            .entries(|entry| {
                let name = entry
                    .file_name()
                    .unwrap_or(Cow::Borrowed("<invalid>"))
                    .into_owned()
                    .into_boxed_str();
                let kind = infer_entry_kind(name.as_ref());
                let mut reader = BlockStream::new(entry.read_file_by_block());
                visitor(EntryMetadata { name, kind }, &mut reader).map_err(visitor_error)
            })
            .with_context(|| format!("failed to process archive {}", path.display()))
    }
}

fn infer_entry_kind(entry_name: &str) -> EntryKind {
    if entry_name.ends_with('/') {
        EntryKind::Directory
    } else {
        EntryKind::File
    }
}

fn visitor_error(err: anyhow::Error) -> archive_reader::error::Error {
    if is_scan_cancelled_error(&err) {
        io::Error::new(io::ErrorKind::Interrupted, err.to_string()).into()
    } else {
        io::Error::other(err.to_string()).into()
    }
}

struct BlockStream<I> {
    blocks: I,
    current: Option<Box<[u8]>>,
    offset: usize,
}

impl<I> BlockStream<I> {
    fn new(blocks: I) -> Self {
        Self { blocks, current: None, offset: 0 }
    }
}

impl<I> Read for BlockStream<I>
where
    I: Iterator<Item = archive_reader::error::Result<Box<[u8]>>>,
{
    fn read(&mut self, buf: &mut [u8]) -> io::Result<usize> {
        if buf.is_empty() {
            return Ok(0);
        }

        loop {
            if let Some(block) = &self.current {
                let remaining = &block[self.offset..];
                if remaining.is_empty() {
                    self.current = None;
                    self.offset = 0;
                    continue;
                }

                let bytes_to_copy = remaining.len().min(buf.len());
                buf[..bytes_to_copy].copy_from_slice(&remaining[..bytes_to_copy]);
                self.offset += bytes_to_copy;
                if self.offset >= block.len() {
                    self.current = None;
                    self.offset = 0;
                }
                return Ok(bytes_to_copy);
            }

            match self.blocks.next() {
                Some(Ok(block)) if !block.is_empty() => {
                    self.current = Some(block);
                    self.offset = 0;
                }
                Some(Ok(_)) => {}
                Some(Err(err)) => return Err(io::Error::other(err.to_string())),
                None => return Ok(0),
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::{
        fs::File,
        io::{Read, Write},
    };
    use tempfile::NamedTempFile;
    use zip::{write::SimpleFileOptions, CompressionMethod, ZipWriter};

    #[test]
    fn block_stream_reads_across_boundaries() {
        let blocks = vec![Ok(Box::<[u8]>::from([1_u8, 2, 3])), Ok(Box::<[u8]>::from([4_u8, 5]))];
        let mut reader = BlockStream::new(blocks.into_iter());
        let mut out = Vec::new();
        reader.read_to_end(&mut out).expect("block stream should read all data");

        assert_eq!(out, vec![1, 2, 3, 4, 5]);
    }

    #[test]
    fn block_stream_handles_empty_buffers_and_empty_blocks() {
        let blocks = vec![
            Ok(Box::<[u8]>::from([])),
            Ok(Box::<[u8]>::from([9_u8, 8])),
            Ok(Box::<[u8]>::from([])),
        ];
        let mut reader = BlockStream::new(blocks.into_iter());
        let mut empty = [];
        let mut buf = [0_u8; 1];

        assert_eq!(reader.read(&mut empty).expect("empty buf read should work"), 0);
        assert_eq!(reader.read(&mut buf).expect("first byte should read"), 1);
        assert_eq!(buf[0], 9);
        assert_eq!(reader.read(&mut buf).expect("second byte should read"), 1);
        assert_eq!(buf[0], 8);
        assert_eq!(reader.read(&mut buf).expect("reader should hit eof"), 0);
    }

    #[test]
    fn block_stream_propagates_errors() {
        let blocks =
            vec![Ok(Box::<[u8]>::from([1_u8, 2, 3])), Err(io::Error::other("boom").into())];
        let mut reader = BlockStream::new(blocks.into_iter());
        let mut buf = [0_u8; 4];

        assert_eq!(reader.read(&mut buf).expect("first read should work"), 3);
        let err = reader.read(&mut buf).expect_err("second read should fail");
        assert_eq!(err.kind(), io::ErrorKind::Other);
    }

    #[test]
    fn libarchive_backend_propagates_visitor_errors() {
        let archive = sample_zip_archive();
        let backend = LibarchiveBackend;

        let err = backend
            .for_each_entry(
                archive.path(),
                BackendOptions { block_size: 64 * 1024 },
                &mut |_entry, _reader| anyhow::bail!("visitor failed"),
            )
            .expect_err("visitor error should propagate");

        assert!(err.to_string().contains("failed to process archive"));
    }

    #[test]
    fn libarchive_backend_roundtrips_zip_entries() {
        let archive = sample_zip_archive();
        let backend = LibarchiveBackend;
        let mut seen = Vec::new();

        backend
            .for_each_entry(
                archive.path(),
                BackendOptions { block_size: 64 * 1024 },
                &mut |entry, reader| {
                    let mut content = String::new();
                    reader.read_to_string(&mut content)?;
                    seen.push((entry.name, entry.kind, content));
                    Ok(())
                },
            )
            .expect("backend should iterate over zip entries");

        assert_eq!(seen.len(), 2);
        assert_eq!(seen[0].0.as_ref(), "alpha.txt");
        assert_eq!(seen[0].1, EntryKind::File);
        assert_eq!(seen[0].2, "alpha");
        assert_eq!(seen[1].0.as_ref(), "nested/beta.txt");
        assert_eq!(seen[1].1, EntryKind::File);
        assert_eq!(seen[1].2, "beta");
    }

    #[test]
    fn libarchive_backend_counts_entries() {
        let archive = sample_zip_archive();
        let backend = LibarchiveBackend;

        let count = backend
            .count_entries(archive.path(), BackendOptions { block_size: 64 * 1024 })
            .expect("backend should count zip entries");

        assert_eq!(count, 2);
    }

    #[test]
    fn infer_entry_kind_treats_trailing_slash_as_directory() {
        assert_eq!(infer_entry_kind("folder/"), EntryKind::Directory);
        assert_eq!(infer_entry_kind("folder/file.txt"), EntryKind::File);
    }

    fn sample_zip_archive() -> NamedTempFile {
        let archive = NamedTempFile::new().expect("temp zip should be created");
        let file = File::create(archive.path()).expect("zip file should open");
        let mut writer = ZipWriter::new(file);
        let options = SimpleFileOptions::default().compression_method(CompressionMethod::Stored);

        writer.start_file("alpha.txt", options).expect("should start alpha entry");
        writer.write_all(b"alpha").expect("should write alpha entry");
        writer.start_file("nested/beta.txt", options).expect("should start beta entry");
        writer.write_all(b"beta").expect("should write beta entry");
        writer.finish().expect("zip archive should finish");

        archive
    }
}
