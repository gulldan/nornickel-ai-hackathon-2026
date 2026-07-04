use archive_scan::{is_archive_ext, is_label_archive, lower_ext, ultra_fast_magic};
use std::{io::Write, path::Path};
use tempfile::NamedTempFile;

#[test]
fn test_archive_detection_comprehensive() {
    let test_cases = vec![
        ("archive.zip", true),
        ("archive.tar.gz", true),
        ("archive.tgz", true),
        ("archive.7z", true),
        ("archive.rar", true),
        ("archive.jar", true),
        ("archive.war", true),
        ("archive.deb", true),
        ("archive.rpm", true),
        ("document.pdf", false),
        ("image.png", false),
        ("data.json", false),
        ("script.sh", false),
    ];

    for (filename, expected) in test_cases {
        let path = Path::new(filename);
        assert_eq!(is_archive_ext(path), expected, "Failed for {filename}");
    }
}

#[test]
fn test_extension_extraction() {
    let test_cases = vec![
        ("file.txt", "txt"),
        ("file.TAR.GZ", "gz"),
        ("FILE.ZIP", "zip"),
        ("noext", ""),
        (".hidden", "hidden"),
        ("multiple.dots.tar", "tar"),
    ];

    for (input, expected) in test_cases {
        assert_eq!(lower_ext(input), expected, "Failed for {input}");
    }
}

#[test]
fn test_magic_number_detection_all_formats() {
    let test_cases = vec![
        (b"%PDF-1.4\x00\x00\x00\x00" as &[u8], "pdf", "application/pdf"),
        (b"PK\x03\x04", "zip", "application/zip"),
        (b"\x1f\x8b\x08\x00", "gzip", "application/gzip"),
        (b"BZh9\x00\x00\x00\x00", "bzip2", "application/x-bzip2"),
        (b"\x28\xb5\x2f\xfd", "zstd", "application/zstd"),
        (b"7z\xbc\xaf\x27\x1c", "7z", "application/x-7z-compressed"),
        (b"Rar!\x1a\x07\x00", "rar", "application/vnd.rar"),
        (b"\x89PNG\x0d\x0a\x1a\x0a", "png", "image/png"),
        (b"\xff\xd8\xff\xe0", "jpeg", "image/jpeg"),
        (b"GIF89a", "gif", "image/gif"),
        (b"RIFF\x00\x00\x00\x00WAVE", "wav", "audio/wav"),
        (b"RIFF\x00\x00\x00\x00WEBP", "webp", "image/webp"),
        (b"OggS", "ogg", "application/ogg"),
        (b"fLaC", "flac", "audio/flac"),
        (b"\x7fELF", "elf", "application/x-elf"),
        (b"MZ\x90\x00", "pebin", "application/vnd.microsoft.portable-executable"),
        (b"\xfd7zXXZ", "xz", "application/x-xz"),
        (b"ID3\x03\x00", "mp3", "audio/mpeg"),
        (b"\x00\x00\x00\x20ftypmp42", "mp4", "video/mp4"),
        (b"SQLite format 3\0", "sqlite", "application/vnd.sqlite3"),
    ];

    for (magic, expected_label, expected_mime) in test_cases {
        let (label, mime) = ultra_fast_magic(magic);
        assert_eq!(label, expected_label, "Failed label detection for {magic:?}");
        assert_eq!(mime, expected_mime, "Failed MIME detection for {magic:?}");
    }
}

#[test]
fn test_label_archive_classification() {
    let archive_labels = vec!["zip", "tar", "gzip", "bzip2", "xz", "zstd", "7z", "rar", "jar"];
    let non_archive_labels = vec!["pdf", "png", "jpeg", "mp3", "mp4", "txt", "unknown"];

    for label in archive_labels {
        assert!(is_label_archive(label), "{label} should be recognized as archive");
    }

    for label in non_archive_labels {
        assert!(!is_label_archive(label), "{label} should not be recognized as archive");
    }
}

#[test]
fn test_blake3_consistency() {
    let test_data = b"consistent test data";
    let hash1 = blake3::hash(test_data);
    let hash2 = blake3::hash(test_data);
    assert_eq!(hash1, hash2, "BLAKE3 should produce consistent hashes");

    let hash_str = hash1.to_hex();
    assert_eq!(hash_str.len(), 64, "BLAKE3 hash should be 64 hex chars");
}

#[test]
fn test_blake3_different_inputs() {
    let data1 = b"test data 1";
    let data2 = b"test data 2";
    let hash1 = blake3::hash(data1);
    let hash2 = blake3::hash(data2);
    assert_ne!(hash1, hash2, "Different inputs should produce different hashes");
}

#[test]
fn test_magic_detection_with_insufficient_data() {
    let short_data = b"AB";
    let (label, mime) = ultra_fast_magic(short_data);
    assert_eq!(label, "unknown");
    assert_eq!(mime, "");
}

#[test]
fn test_magic_detection_edge_cases() {
    let empty = b"";
    let (label, mime) = ultra_fast_magic(empty);
    assert_eq!(label, "unknown");
    assert_eq!(mime, "");

    let one_byte = b"X";
    let (label, mime) = ultra_fast_magic(one_byte);
    assert_eq!(label, "unknown");
    assert_eq!(mime, "");

    let three_bytes = b"XXX";
    let (label, mime) = ultra_fast_magic(three_bytes);
    assert_eq!(label, "unknown");
    assert_eq!(mime, "");
}

#[test]
fn test_archive_ext_case_insensitivity() {
    let mixed_case_archives = vec!["test.ZIP", "test.TaR", "test.GZ", "test.7Z", "test.RaR"];

    for archive in mixed_case_archives {
        assert!(
            is_archive_ext(Path::new(archive)),
            "{archive} should be recognized regardless of case"
        );
    }
}

#[test]
fn test_lower_ext_special_cases() {
    assert_eq!(lower_ext(""), "");
    assert_eq!(lower_ext("."), "");
    assert_eq!(lower_ext(".."), "");
    assert_eq!(lower_ext("/path/to/file.txt"), "txt");
    assert_eq!(lower_ext("file."), "");
}

#[test]
fn test_path_without_extension() {
    let paths = vec!["README", "Makefile", "LICENSE", "noext"];

    for path in paths {
        assert_eq!(lower_ext(path), "", "Path without extension should return empty string");
        assert!(!is_archive_ext(Path::new(path)), "Path without extension should not be archive");
    }
}

#[test]
fn test_create_temp_file_with_content() {
    let mut temp_file = NamedTempFile::new().expect("Failed to create temp file");
    let content = b"test content for file";
    temp_file.write_all(content).expect("Failed to write to temp file");

    assert!(temp_file.path().exists());
}

#[test]
fn test_jpeg_variants() {
    let jpeg_variants = vec![
        b"\xff\xd8\xff\xe0" as &[u8],
        b"\xff\xd8\xff\xe1",
        b"\xff\xd8\xff\xe2",
        b"\xff\xd8\xff\xe3",
        b"\xff\xd8\xff\xe8",
        b"\xff\xd8\xff\xdb",
        b"\xff\xd8\xff\xc0",
        b"\xff\xd8\xff\xc4",
    ];

    for variant in jpeg_variants {
        let (label, mime) = ultra_fast_magic(variant);
        assert_eq!(label, "jpeg", "Failed for JPEG variant: {variant:?}");
        assert_eq!(mime, "image/jpeg");
    }
}

#[test]
fn test_gif_variants() {
    let gif87 = b"GIF87a";
    let gif89 = b"GIF89a";

    let (label, mime) = ultra_fast_magic(gif87);
    assert_eq!(label, "gif");
    assert_eq!(mime, "image/gif");

    let (label, mime) = ultra_fast_magic(gif89);
    assert_eq!(label, "gif");
    assert_eq!(mime, "image/gif");
}

#[test]
fn test_pe_executable_variants() {
    let pe_variants = vec![b"MZ\x90\x00" as &[u8], b"MZ\x00\x00"];

    for variant in pe_variants {
        let (label, _) = ultra_fast_magic(variant);
        assert_eq!(label, "pebin", "Failed for PE variant: {variant:?}");
    }
}

#[test]
fn test_riff_format_disambiguation() {
    let wav_magic = b"RIFF\x00\x00\x00\x00WAVE";
    let (label, mime) = ultra_fast_magic(wav_magic);
    assert_eq!(label, "wav");
    assert_eq!(mime, "audio/wav");

    let webp_magic = b"RIFF\x00\x00\x00\x00WEBP";
    let (label, mime) = ultra_fast_magic(webp_magic);
    assert_eq!(label, "webp");
    assert_eq!(mime, "image/webp");

    let unknown_riff = b"RIFF\x00\x00\x00\x00UNKN";
    let (label, mime) = ultra_fast_magic(unknown_riff);
    assert_eq!(label, "unknown");
    assert_eq!(mime, "");
}

#[test]
fn test_7z_magic_incomplete() {
    let incomplete_7z = b"7z\xbc\xaf";
    let (label, mime) = ultra_fast_magic(incomplete_7z);
    assert_eq!(label, "unknown");
    assert_eq!(mime, "");

    let complete_7z = b"7z\xbc\xaf\x27\x1c";
    let (label, mime) = ultra_fast_magic(complete_7z);
    assert_eq!(label, "7z");
    assert_eq!(mime, "application/x-7z-compressed");
}

#[test]
fn test_rar_magic_incomplete() {
    let incomplete_rar = b"Rar!\x1a";
    let (label, mime) = ultra_fast_magic(incomplete_rar);
    assert_eq!(label, "unknown");
    assert_eq!(mime, "");

    let complete_rar = b"Rar!\x1a\x07\x00";
    let (label, mime) = ultra_fast_magic(complete_rar);
    assert_eq!(label, "rar");
    assert_eq!(mime, "application/vnd.rar");
}

#[test]
fn test_png_magic_incomplete() {
    let incomplete_png = b"\x89PNG\x0d\x0a";
    let (label, mime) = ultra_fast_magic(incomplete_png);
    assert_eq!(label, "unknown");
    assert_eq!(mime, "");

    let complete_png = b"\x89PNG\x0d\x0a\x1a\x0a";
    let (label, mime) = ultra_fast_magic(complete_png);
    assert_eq!(label, "png");
    assert_eq!(mime, "image/png");
}

#[test]
fn test_xz_magic_incomplete() {
    let incomplete_xz = b"\xfd7zX";
    let (label, mime) = ultra_fast_magic(incomplete_xz);
    assert_eq!(label, "unknown");
    assert_eq!(mime, "");

    let complete_xz = b"\xfd7zXXZ";
    let (label, mime) = ultra_fast_magic(complete_xz);
    assert_eq!(label, "xz");
    assert_eq!(mime, "application/x-xz");
}
