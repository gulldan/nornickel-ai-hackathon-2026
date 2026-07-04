use crate::cancel::scan_cancelled_io_error;
use smallvec::SmallVec;
use std::io::{self, Read};

#[derive(Debug)]
pub struct ScanOutcome {
    pub header: SmallVec<[u8; 512]>,
    pub head_b3: Option<Box<str>>,
    pub full_b3: Option<Box<str>>,
    pub bytes_scanned: u64,
    pub truncated_scan: bool,
}

/// Reads enough bytes from an entry to classify it and optionally compute hashes.
///
/// # Errors
///
/// Returns any I/O error produced by the underlying reader.
pub fn analyze_reader<R>(
    reader: &mut R,
    header_bytes: usize,
    want_full_hash: bool,
    emit_hashes: bool,
) -> io::Result<ScanOutcome>
where
    R: Read + ?Sized,
{
    analyze_reader_with_interrupt(reader, header_bytes, want_full_hash, emit_hashes, || false)
}

pub fn analyze_reader_with_interrupt<R, ShouldCancel>(
    reader: &mut R,
    header_bytes: usize,
    want_full_hash: bool,
    emit_hashes: bool,
    mut should_cancel: ShouldCancel,
) -> io::Result<ScanOutcome>
where
    R: Read + ?Sized,
    ShouldCancel: FnMut() -> bool,
{
    let mut header = SmallVec::<[u8; 512]>::with_capacity(header_bytes.min(512));
    let mut bytes_scanned = 0_u64;
    let mut truncated_scan = false;
    let mut full_hasher = want_full_hash.then(blake3::Hasher::new);
    let mut buffer = [0_u8; 8 * 1024];

    loop {
        if should_cancel() {
            return Err(scan_cancelled_io_error());
        }

        let bytes_read = reader.read(&mut buffer)?;
        if bytes_read == 0 {
            break;
        }

        let chunk = &buffer[..bytes_read];
        bytes_scanned += chunk.len() as u64;

        if header.len() < header_bytes {
            let required = (header_bytes - header.len()).min(chunk.len());
            header.extend_from_slice(&chunk[..required]);
        }

        if let Some(hasher) = full_hasher.as_mut() {
            hasher.update(chunk);
        } else if header.len() >= header_bytes {
            truncated_scan = true;
            break;
        }
    }

    let head_b3 = emit_hashes
        .then(|| {
            (!header.is_empty())
                .then(|| blake3::hash(&header).to_hex().to_string().into_boxed_str())
        })
        .flatten();
    let full_b3 = full_hasher.map(|hasher| hasher.finalize().to_hex().to_string().into_boxed_str());

    Ok(ScanOutcome { header, head_b3, full_b3, bytes_scanned, truncated_scan })
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::{Cursor, ErrorKind};

    #[test]
    fn analyze_reader_stops_after_header_when_full_hash_disabled() {
        let payload = vec![b'a'; 16 * 1024];
        let mut reader = Cursor::new(payload);

        let outcome = analyze_reader(&mut reader, 512, false, false).expect("scan should succeed");

        assert_eq!(outcome.header.len(), 512);
        assert_eq!(outcome.bytes_scanned, 8 * 1024);
        assert!(outcome.truncated_scan);
        assert!(outcome.head_b3.is_none());
        assert!(outcome.full_b3.is_none());
    }

    #[test]
    fn analyze_reader_computes_hashes_when_requested() {
        let payload = b"hello world".repeat(256);
        let expected_head = blake3::hash(&payload[..32]).to_hex().to_string();
        let expected_full = blake3::hash(&payload).to_hex().to_string();
        let mut reader = Cursor::new(payload);

        let outcome = analyze_reader(&mut reader, 32, true, true).expect("scan should succeed");

        assert_eq!(outcome.header.len(), 32);
        assert_eq!(outcome.bytes_scanned, 11 * 256);
        assert!(!outcome.truncated_scan);
        assert_eq!(outcome.head_b3.as_deref(), Some(expected_head.as_str()));
        assert_eq!(outcome.full_b3.as_deref(), Some(expected_full.as_str()));
    }

    #[test]
    fn analyze_reader_handles_empty_inputs() {
        let mut reader = Cursor::new(Vec::<u8>::new());

        let outcome = analyze_reader(&mut reader, 512, false, true).expect("scan should succeed");

        assert!(outcome.header.is_empty());
        assert_eq!(outcome.bytes_scanned, 0);
        assert!(!outcome.truncated_scan);
        assert!(outcome.head_b3.is_none());
        assert!(outcome.full_b3.is_none());
    }

    #[test]
    fn analyze_reader_propagates_io_errors() {
        struct FaultyReader;

        impl Read for FaultyReader {
            fn read(&mut self, _buf: &mut [u8]) -> io::Result<usize> {
                Err(io::Error::new(ErrorKind::InvalidData, "boom"))
            }
        }

        let err = analyze_reader(&mut FaultyReader, 512, false, false)
            .expect_err("scan should propagate read errors");

        assert_eq!(err.kind(), ErrorKind::InvalidData);
    }

    #[test]
    fn analyze_reader_with_interrupt_returns_interrupted_when_cancelled() {
        let payload = vec![b'a'; 16 * 1024];
        let mut reader = Cursor::new(payload);
        let mut checks = 0_u8;

        let err = analyze_reader_with_interrupt(&mut reader, 512, true, false, || {
            checks += 1;
            checks >= 2
        })
        .expect_err("scan should stop once cancellation is requested");

        assert_eq!(err.kind(), ErrorKind::Interrupted);
        assert_eq!(err.to_string(), "scan cancelled");
    }
}
