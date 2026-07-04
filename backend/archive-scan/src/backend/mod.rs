mod libarchive;

use crate::row::EntryKind;
use anyhow::Result;
use std::{io::Read, path::Path};

pub use libarchive::LibarchiveBackend;

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub struct BackendOptions {
    pub block_size: usize,
}

#[derive(Debug, Eq, PartialEq)]
pub struct EntryMetadata {
    pub name: Box<str>,
    pub kind: EntryKind,
}

pub trait ArchiveBackend: Sync {
    /// Counts entries in an archive without fully scanning them.
    ///
    /// # Errors
    ///
    /// Returns an error if the archive cannot be opened or enumerated.
    fn count_entries(&self, path: &Path, options: BackendOptions) -> Result<usize>;

    /// Iterates through each archive entry and exposes its content as a reader.
    ///
    /// # Errors
    ///
    /// Returns an error if the archive cannot be opened, read, or if the visitor fails.
    fn for_each_entry(
        &self,
        path: &Path,
        options: BackendOptions,
        visitor: &mut dyn FnMut(EntryMetadata, &mut dyn Read) -> Result<()>,
    ) -> Result<()>;
}
