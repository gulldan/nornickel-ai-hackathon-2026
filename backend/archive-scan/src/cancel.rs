use anyhow::Error;
use std::io::{self, ErrorKind};

pub(crate) const SCAN_CANCELLED_MESSAGE: &str = "scan cancelled";

pub(crate) fn scan_cancelled_io_error() -> io::Error {
    io::Error::new(ErrorKind::Interrupted, SCAN_CANCELLED_MESSAGE)
}

pub(crate) fn is_scan_cancelled_io_error(err: &io::Error) -> bool {
    err.kind() == ErrorKind::Interrupted && err.to_string() == SCAN_CANCELLED_MESSAGE
}

pub(crate) fn is_scan_cancelled_error(err: &Error) -> bool {
    err.chain()
        .any(|cause| cause.downcast_ref::<io::Error>().is_some_and(is_scan_cancelled_io_error))
}
