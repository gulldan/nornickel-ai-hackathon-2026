#![cfg(feature = "cli")]

use crate::support::{
    assert_large_text_row, large_archive_fixture, real_archive_fixture, LARGE_ENTRY_NAME,
};
use std::process::Command;

#[test]
fn cli_scans_real_archive_fixture_and_writes_ndjson() {
    let Some(archive_path) = real_archive_fixture() else {
        eprintln!("skipping real archive CLI test because data/asr-master.zip is not present");
        return;
    };

    let out_dir = tempfile::tempdir().expect("tempdir should exist");
    let ndjson_path = out_dir.path().join("rows.ndjson");
    let binary = std::env::var_os("CARGO_BIN_EXE_archive_scan")
        .expect("cargo should expose the archive_scan test binary path");

    let output = Command::new(binary)
        .arg(&archive_path)
        .arg("--fast-only")
        .arg("--out-ndjson")
        .arg(&ndjson_path)
        .output()
        .expect("CLI process should start");

    assert!(
        output.status.success(),
        "CLI exited with status {:?}\nstdout:\n{}\nstderr:\n{}",
        output.status.code(),
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );

    let stdout = String::from_utf8_lossy(&output.stdout);
    assert!(stdout.contains("Found 1 archive files"));
    assert!(stdout.contains("Total entries scanned: 35"));
    assert!(stdout.contains("Files analyzed: 28"));
    assert!(stdout.contains("Directories: 7"));

    let content = std::fs::read_to_string(&ndjson_path).expect("ndjson output should be readable");
    let rows: Vec<serde_json::Value> = content
        .lines()
        .map(|line| serde_json::from_str(line).expect("each NDJSON line should be valid JSON"))
        .collect();

    assert_eq!(rows.len(), 35);
    assert!(rows.iter().all(|row| row["archive_name"] == "asr-master.zip"));
    let file_rows = rows.iter().filter(|row| row["entry_kind"] == "file").count();
    let directory_rows = rows.iter().filter(|row| row["entry_kind"] == "directory").count();

    let rows_with_head_hash = rows
        .iter()
        .filter(|row| row["head_b3"].as_str().is_some_and(|hash| hash.len() == 64))
        .count();
    let rows_without_head_hash = rows.iter().filter(|row| row.get("head_b3").is_none()).count();
    let empty_rows = rows.iter().filter(|row| row["bytes_scanned"].as_u64() == Some(0)).count();

    assert_eq!(file_rows, 28);
    assert_eq!(directory_rows, 7);
    assert_eq!(rows_without_head_hash, empty_rows);
    assert_eq!(rows_without_head_hash, 7);
    assert_eq!(rows_with_head_hash + rows_without_head_hash, rows.len());
    assert!(rows.iter().all(|row| row.get("full_b3").is_none()));

    let wav_count = rows.iter().filter(|row| row["label"] == "wav").count();
    let python_count = rows.iter().filter(|row| row["label"] == "python").count();
    let text_count = rows.iter().filter(|row| row["label"] == "txt").count();

    assert_eq!(wav_count, 2);
    assert_eq!(python_count, 10);
    assert_eq!(text_count, 5);
}

#[cfg(feature = "cli")]
#[test]
fn cli_scans_large_entry_with_and_without_full_hash() {
    let fixture = large_archive_fixture();
    let binary = std::env::var_os("CARGO_BIN_EXE_archive_scan")
        .expect("cargo should expose the archive_scan test binary path");
    let out_dir = tempfile::tempdir().expect("tempdir should exist");

    for (full_hash, file_name) in [(false, "rows-truncated.ndjson"), (true, "rows-full.ndjson")] {
        let ndjson_path = out_dir.path().join(file_name);
        let mut command = Command::new(&binary);
        command
            .arg(fixture.root_dir.path())
            .arg("--fast-only")
            .arg("--out-ndjson")
            .arg(&ndjson_path);
        if full_hash {
            command.arg("--full-hash");
        }

        let output = command.output().expect("CLI process should start");
        assert!(
            output.status.success(),
            "CLI exited with status {:?}\nstdout:\n{}\nstderr:\n{}",
            output.status.code(),
            String::from_utf8_lossy(&output.stdout),
            String::from_utf8_lossy(&output.stderr)
        );

        let stdout = String::from_utf8_lossy(&output.stdout);
        assert!(stdout.contains("Found 1 archive files"));
        assert!(stdout.contains("Total entries scanned: 1"));
        assert!(stdout.contains("Files analyzed: 1"));
        assert!(stdout.contains("Directories: 0"));

        let content =
            std::fs::read_to_string(&ndjson_path).expect("ndjson output should be readable");
        let rows: Vec<serde_json::Value> = content
            .lines()
            .map(|line| serde_json::from_str(line).expect("each NDJSON line should be valid JSON"))
            .collect();

        assert_eq!(rows.len(), 1);
        assert_large_text_row(&rows[0], "large.zip", &fixture.payload, full_hash);
        assert_eq!(rows[0]["archive_path"], fixture.archive_path.display().to_string());
    }
}

#[cfg(feature = "cli")]
#[test]
fn cli_extracts_archives_locally_and_writes_metadata_ndjson() {
    let fixture = large_archive_fixture();
    let binary = std::env::var_os("CARGO_BIN_EXE_archive_scan")
        .expect("cargo should expose the archive_scan test binary path");
    let out_dir = tempfile::tempdir().expect("extract tempdir should exist");
    let metadata_path = out_dir.path().join("metadata.ndjson");
    let extracted_root = out_dir.path().join("files");

    let output = Command::new(binary)
        .arg("extract")
        .arg(fixture.root_dir.path())
        .arg("--out-dir")
        .arg(&extracted_root)
        .arg("--fast-only")
        .arg("--full-hash")
        .arg("--metadata-ndjson")
        .arg(&metadata_path)
        .output()
        .expect("CLI extract process should start");

    assert!(
        output.status.success(),
        "CLI extract exited with status {:?}\nstdout:\n{}\nstderr:\n{}",
        output.status.code(),
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );
    let stdout = String::from_utf8_lossy(&output.stdout);
    assert!(stdout.contains("Archive Extraction Summary"));
    assert!(stdout.contains("Files stored: 1"));

    let extracted_file = extracted_root.join("00000000-large").join(LARGE_ENTRY_NAME);
    assert_eq!(
        std::fs::read(&extracted_file).expect("extracted file should be readable"),
        fixture.payload
    );

    let metadata = std::fs::read_to_string(&metadata_path)
        .expect("extract metadata NDJSON should be readable");
    let rows = metadata.lines().collect::<Vec<_>>();
    assert_eq!(rows.len(), 1);
    let row: serde_json::Value =
        serde_json::from_str(rows[0]).expect("metadata row should be valid JSON");
    assert_eq!(row["sanitized_path"], LARGE_ENTRY_NAME);
    assert_eq!(row["row"]["entry_name"], LARGE_ENTRY_NAME);
    assert_eq!(row["row"]["full_b3"].as_str().map(str::len), Some(64));
    assert_eq!(row["stored_object"]["b3"].as_str().map(str::len), Some(64));
    assert!(row["stored_object"]["uri"]
        .as_str()
        .is_some_and(|uri| uri.ends_with(LARGE_ENTRY_NAME)));
}

#[test]
fn cli_help_lists_env_backed_flags() {
    let binary = std::env::var_os("CARGO_BIN_EXE_archive_scan")
        .expect("cargo should expose cli binary path");
    let output = Command::new(binary).arg("--help").output().expect("cli help should start");

    assert!(output.status.success());
    let stdout = String::from_utf8_lossy(&output.stdout);
    assert!(stdout.contains("--threads"));
    assert!(stdout.contains("--header-bytes"));
    assert!(stdout.contains("--block-size"));
    assert!(stdout.contains("--out-ndjson"));
    assert!(stdout.contains("--out-parquet"));
    assert!(stdout.contains("--fast-only"));
}
