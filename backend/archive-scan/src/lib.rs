#![recursion_limit = "256"]

use std::{borrow::Cow, path::Path};

mod cancel;

#[cfg(feature = "cli")]
pub mod app;
pub mod backend;
pub mod engine;
pub mod extract;
pub mod magika_support;
pub mod output;
pub mod row;
pub mod scan;
#[cfg(feature = "service")]
pub mod service;

#[cfg(feature = "jemalloc")]
#[global_allocator]
static GLOBAL_ALLOCATOR: jemallocator::Jemalloc = jemallocator::Jemalloc;

const ARCHIVE_EXTS: &[&str] = &[
    "zip", "tar", "tgz", "gz", "bz2", "xz", "zst", "7z", "rar", "jar", "war", "ear", "apk", "ipa",
    "whl", "deb", "rpm",
];

#[must_use]
#[inline(always)]
pub fn is_archive_ext(path: &Path) -> bool {
    path.extension().and_then(|extension| extension.to_str()).is_some_and(|extension| {
        ARCHIVE_EXTS.iter().any(|candidate| extension.eq_ignore_ascii_case(candidate))
    })
}

#[must_use]
#[inline(always)]
pub fn lower_ext(name: &str) -> Cow<'_, str> {
    match raw_extension(name) {
        Some(extension) if extension.bytes().all(|byte| !byte.is_ascii_uppercase()) => {
            Cow::Borrowed(extension)
        }
        Some(extension) => Cow::Owned(extension.to_ascii_lowercase()),
        None => Cow::Borrowed(""),
    }
}

#[must_use]
#[inline(always)]
pub fn ultra_fast_magic(head: &[u8]) -> (&'static str, &'static str) {
    if head.len() < 4 {
        return ("unknown", "");
    }

    let magic = u32::from_be_bytes([head[0], head[1], head[2], head[3]]);
    match magic {
        0x2550_4446 => ("pdf", "application/pdf"),
        0x504B_0304 => ("zip", "application/zip"),
        0x1F8B_0800 | 0x1F8B_0808 => ("gzip", "application/gzip"),
        0x425A_6839 | 0x425A_6830 => ("bzip2", "application/x-bzip2"),
        0x28B5_2FFD => ("zstd", "application/zstd"),
        0x377A_BCAF => {
            if head.len() >= 6 && head[4] == 0x27 && head[5] == 0x1C {
                ("7z", "application/x-7z-compressed")
            } else {
                ("unknown", "")
            }
        }
        0x5261_7221 => {
            if head.len() >= 7 && head[4] == 0x1A && head[5] == 0x07 {
                ("rar", "application/vnd.rar")
            } else {
                ("unknown", "")
            }
        }
        0x8950_4E47 => {
            if head.len() >= 8 && head[4..8] == [0x0D, 0x0A, 0x1A, 0x0A] {
                ("png", "image/png")
            } else {
                ("unknown", "")
            }
        }
        0xFFD8_FFE0 | 0xFFD8_FFE1 | 0xFFD8_FFE2 | 0xFFD8_FFE3 | 0xFFD8_FFE8 | 0xFFD8_FFDB
        | 0xFFD8_FFC0 | 0xFFD8_FFC4 => ("jpeg", "image/jpeg"),
        0x4749_4638 => {
            if head.len() >= 6 && (head[4] == b'7' || head[4] == b'9') && head[5] == b'a' {
                ("gif", "image/gif")
            } else {
                ("unknown", "")
            }
        }
        0x5249_4646 => {
            if head.len() >= 12 {
                match &head[8..12] {
                    b"WAVE" => ("wav", "audio/wav"),
                    b"WEBP" => ("webp", "image/webp"),
                    _ => ("unknown", ""),
                }
            } else {
                ("unknown", "")
            }
        }
        0x4F67_6753 => ("ogg", "application/ogg"),
        0x664C_6143 => ("flac", "audio/flac"),
        0x7F45_4C46 => ("elf", "application/x-elf"),
        0x4D5A_9000 | 0x4D5A_0000 => ("pebin", "application/vnd.microsoft.portable-executable"),
        0xFD37_7A58 => {
            if head.len() >= 6 && head[4] == b'X' && head[5] == b'Z' {
                ("xz", "application/x-xz")
            } else {
                ("unknown", "")
            }
        }
        _ => {
            if head.len() >= 3 && &head[0..3] == b"ID3" {
                ("mp3", "audio/mpeg")
            } else if head.len() >= 12 && &head[4..8] == b"ftyp" {
                ("mp4", "video/mp4")
            } else if head.len() >= 16 && head.starts_with(b"SQLite format 3\0") {
                ("sqlite", "application/vnd.sqlite3")
            } else {
                ("unknown", "")
            }
        }
    }
}

#[must_use]
#[inline(always)]
pub fn is_label_archive(label: &str) -> bool {
    matches!(label, "zip" | "tar" | "gzip" | "bzip2" | "xz" | "zstd" | "7z" | "rar" | "jar")
}

#[inline(always)]
fn raw_extension(name: &str) -> Option<&str> {
    let path = Path::new(name);
    if let Some(extension) = path.extension().and_then(|extension| extension.to_str()) {
        return Some(extension);
    }

    path.file_name()
        .and_then(|file_name| file_name.to_str())
        .and_then(|file_name| file_name.strip_prefix('.'))
        .filter(|file_name| !file_name.is_empty() && !file_name.contains('.'))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_is_archive_ext() {
        assert!(is_archive_ext(Path::new("test.zip")));
        assert!(is_archive_ext(Path::new("test.tar")));
        assert!(is_archive_ext(Path::new("test.tgz")));
        assert!(is_archive_ext(Path::new("test.gz")));
        assert!(is_archive_ext(Path::new("test.bz2")));
        assert!(is_archive_ext(Path::new("test.xz")));
        assert!(is_archive_ext(Path::new("test.7z")));
        assert!(is_archive_ext(Path::new("test.rar")));
        assert!(is_archive_ext(Path::new("test.jar")));
        assert!(is_archive_ext(Path::new("test.war")));
        assert!(is_archive_ext(Path::new("test.ear")));
        assert!(is_archive_ext(Path::new("test.apk")));
        assert!(is_archive_ext(Path::new("test.whl")));
        assert!(is_archive_ext(Path::new("test.deb")));
        assert!(is_archive_ext(Path::new("test.rpm")));
        assert!(is_archive_ext(Path::new("test.ZIP")));
        assert!(is_archive_ext(Path::new("test.TaR")));
        assert!(!is_archive_ext(Path::new("test.txt")));
        assert!(!is_archive_ext(Path::new("test.pdf")));
        assert!(!is_archive_ext(Path::new("test")));
    }

    #[test]
    fn test_lower_ext() {
        assert_eq!(lower_ext("test.zip"), "zip");
        assert_eq!(lower_ext("test.ZIP"), "zip");
        assert_eq!(lower_ext("test.TaR"), "tar");
        assert_eq!(lower_ext("test"), "");
        assert_eq!(lower_ext("test."), "");
    }

    #[test]
    fn test_ultra_fast_magic_pdf() {
        let pdf_magic = b"%PDF-1.4\n";
        let (label, mime) = ultra_fast_magic(pdf_magic);
        assert_eq!(label, "pdf");
        assert_eq!(mime, "application/pdf");
    }

    #[test]
    fn test_ultra_fast_magic_zip() {
        let zip_magic = b"PK\x03\x04";
        let (label, mime) = ultra_fast_magic(zip_magic);
        assert_eq!(label, "zip");
        assert_eq!(mime, "application/zip");
    }

    #[test]
    fn test_ultra_fast_magic_gzip() {
        let gzip_magic = b"\x1f\x8b\x08\x00";
        let (label, mime) = ultra_fast_magic(gzip_magic);
        assert_eq!(label, "gzip");
        assert_eq!(mime, "application/gzip");
    }

    #[test]
    fn test_ultra_fast_magic_bzip2() {
        let bzip2_magic = b"BZh9";
        let (label, mime) = ultra_fast_magic(bzip2_magic);
        assert_eq!(label, "bzip2");
        assert_eq!(mime, "application/x-bzip2");
    }

    #[test]
    fn test_ultra_fast_magic_zstd() {
        let zstd_magic = b"\x28\xb5\x2f\xfd";
        let (label, mime) = ultra_fast_magic(zstd_magic);
        assert_eq!(label, "zstd");
        assert_eq!(mime, "application/zstd");
    }

    #[test]
    fn test_ultra_fast_magic_7z() {
        let seven_z_magic = b"7z\xbc\xaf\x27\x1c";
        let (label, mime) = ultra_fast_magic(seven_z_magic);
        assert_eq!(label, "7z");
        assert_eq!(mime, "application/x-7z-compressed");
    }

    #[test]
    fn test_ultra_fast_magic_rar() {
        let rar_magic = b"Rar!\x1a\x07\x00";
        let (label, mime) = ultra_fast_magic(rar_magic);
        assert_eq!(label, "rar");
        assert_eq!(mime, "application/vnd.rar");
    }

    #[test]
    fn test_ultra_fast_magic_png() {
        let png_magic = b"\x89PNG\x0d\x0a\x1a\x0a";
        let (label, mime) = ultra_fast_magic(png_magic);
        assert_eq!(label, "png");
        assert_eq!(mime, "image/png");
    }

    #[test]
    fn test_ultra_fast_magic_jpeg() {
        let jpeg_magic = b"\xff\xd8\xff\xe0";
        let (label, mime) = ultra_fast_magic(jpeg_magic);
        assert_eq!(label, "jpeg");
        assert_eq!(mime, "image/jpeg");
    }

    #[test]
    fn test_ultra_fast_magic_gif() {
        let gif_magic = b"GIF89a";
        let (label, mime) = ultra_fast_magic(gif_magic);
        assert_eq!(label, "gif");
        assert_eq!(mime, "image/gif");
    }

    #[test]
    fn test_ultra_fast_magic_wav() {
        let wav_magic = b"RIFF\x00\x00\x00\x00WAVE";
        let (label, mime) = ultra_fast_magic(wav_magic);
        assert_eq!(label, "wav");
        assert_eq!(mime, "audio/wav");
    }

    #[test]
    fn test_ultra_fast_magic_webp() {
        let webp_magic = b"RIFF\x00\x00\x00\x00WEBP";
        let (label, mime) = ultra_fast_magic(webp_magic);
        assert_eq!(label, "webp");
        assert_eq!(mime, "image/webp");
    }

    #[test]
    fn test_ultra_fast_magic_ogg() {
        let ogg_magic = b"OggS";
        let (label, mime) = ultra_fast_magic(ogg_magic);
        assert_eq!(label, "ogg");
        assert_eq!(mime, "application/ogg");
    }

    #[test]
    fn test_ultra_fast_magic_flac() {
        let flac_magic = b"fLaC";
        let (label, mime) = ultra_fast_magic(flac_magic);
        assert_eq!(label, "flac");
        assert_eq!(mime, "audio/flac");
    }

    #[test]
    fn test_ultra_fast_magic_elf() {
        let elf_magic = b"\x7fELF";
        let (label, mime) = ultra_fast_magic(elf_magic);
        assert_eq!(label, "elf");
        assert_eq!(mime, "application/x-elf");
    }

    #[test]
    fn test_ultra_fast_magic_pe() {
        let pe_magic = b"MZ\x90\x00";
        let (label, mime) = ultra_fast_magic(pe_magic);
        assert_eq!(label, "pebin");
        assert_eq!(mime, "application/vnd.microsoft.portable-executable");
    }

    #[test]
    fn test_ultra_fast_magic_xz() {
        let xz_magic = b"\xfd7zXXZ";
        let (label, mime) = ultra_fast_magic(xz_magic);
        assert_eq!(label, "xz");
        assert_eq!(mime, "application/x-xz");
    }

    #[test]
    fn test_ultra_fast_magic_mp3() {
        let mp3_magic = b"ID3\x03\x00";
        let (label, mime) = ultra_fast_magic(mp3_magic);
        assert_eq!(label, "mp3");
        assert_eq!(mime, "audio/mpeg");
    }

    #[test]
    fn test_ultra_fast_magic_mp4() {
        let mp4_magic = b"\x00\x00\x00\x20ftypmp42";
        let (label, mime) = ultra_fast_magic(mp4_magic);
        assert_eq!(label, "mp4");
        assert_eq!(mime, "video/mp4");
    }

    #[test]
    fn test_ultra_fast_magic_sqlite() {
        let sqlite_magic = b"SQLite format 3\0";
        let (label, mime) = ultra_fast_magic(sqlite_magic);
        assert_eq!(label, "sqlite");
        assert_eq!(mime, "application/vnd.sqlite3");
    }

    #[test]
    fn test_ultra_fast_magic_unknown() {
        let unknown_magic = b"XXXX";
        let (label, mime) = ultra_fast_magic(unknown_magic);
        assert_eq!(label, "unknown");
        assert_eq!(mime, "");
    }

    #[test]
    fn test_ultra_fast_magic_short_data() {
        let short_data = b"XX";
        let (label, mime) = ultra_fast_magic(short_data);
        assert_eq!(label, "unknown");
        assert_eq!(mime, "");
    }

    #[test]
    fn test_is_label_archive() {
        assert!(is_label_archive("zip"));
        assert!(is_label_archive("tar"));
        assert!(is_label_archive("gzip"));
        assert!(is_label_archive("bzip2"));
        assert!(is_label_archive("xz"));
        assert!(is_label_archive("zstd"));
        assert!(is_label_archive("7z"));
        assert!(is_label_archive("rar"));
        assert!(is_label_archive("jar"));
        assert!(!is_label_archive("pdf"));
        assert!(!is_label_archive("png"));
        assert!(!is_label_archive("jpeg"));
    }

    #[test]
    fn test_blake3_hash() {
        let data = b"test data";
        let hash = blake3::hash(data);
        assert_eq!(hash.to_hex().len(), 64);
    }
}
