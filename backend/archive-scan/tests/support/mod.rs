#[cfg(any(feature = "cli", feature = "service"))]
use std::io::Write;
#[cfg(feature = "cli")]
use std::path::{Path, PathBuf};

#[cfg(feature = "service")]
use reqwest::{
    blocking::{Client, Response},
    StatusCode,
};
#[cfg(feature = "service")]
use std::{
    collections::HashMap,
    io::Read,
    net::{TcpListener, TcpStream},
    process::{Child, Command, Stdio},
    sync::{
        atomic::{AtomicBool, Ordering},
        Arc, Barrier, Mutex,
    },
    thread::{self, JoinHandle},
    time::{Duration, Instant, SystemTime},
};
#[cfg(feature = "service")]
use tempfile::NamedTempFile;
#[cfg(any(feature = "cli", feature = "service"))]
use tempfile::TempDir;
#[cfg(any(feature = "cli", feature = "service"))]
use zip::{write::SimpleFileOptions, CompressionMethod, ZipWriter};

#[cfg(feature = "cli")]
pub(crate) const LARGE_ENTRY_NAME: &str = "large.txt";
#[cfg(feature = "cli")]
pub(crate) const LARGE_ENTRY_SIZE: usize = 64 * 1024;
#[cfg(feature = "cli")]
pub(crate) const DEFAULT_HEADER_BYTES: usize = 512;
#[cfg(feature = "cli")]
pub(crate) const TRUNCATED_ENTRY_BYTES_SCANNED: u64 = 8 * 1024;

#[cfg(feature = "service")]
const SERVICE_STARTUP_TIMEOUT: Duration = Duration::from_secs(15);
#[cfg(feature = "service")]
const SERVICE_POLL_INTERVAL: Duration = Duration::from_millis(50);
#[cfg(feature = "service")]
const HTTP_REQUEST_TIMEOUT: Duration = Duration::from_secs(2);
#[cfg(feature = "service")]
const REDIS_STARTUP_TIMEOUT: Duration = Duration::from_secs(20);
#[cfg(feature = "service")]
const REDIS_IMAGE: &str = "redis:7-alpine";
#[cfg(feature = "service")]
const POSTGRES_STARTUP_TIMEOUT: Duration = Duration::from_secs(20);
#[cfg(feature = "service")]
const POSTGRES_IMAGE: &str = "postgres:18-alpine";
#[cfg(feature = "service")]
const SEAWEEDFS_STARTUP_TIMEOUT: Duration = Duration::from_secs(30);
#[cfg(feature = "service")]
const SEAWEEDFS_IMAGE: &str = "chrislusf/seaweedfs";

#[cfg(feature = "cli")]
pub(crate) fn real_archive_fixture() -> Option<PathBuf> {
    let path = Path::new(env!("CARGO_MANIFEST_DIR")).join("data/asr-master.zip");
    path.exists().then_some(path)
}

#[cfg(feature = "cli")]
pub(crate) struct LargeArchiveFixture {
    pub(crate) root_dir: TempDir,
    pub(crate) archive_path: PathBuf,
    pub(crate) payload: Vec<u8>,
}

#[cfg(feature = "cli")]
fn large_text_payload() -> Vec<u8> {
    let line = b"large archive scan fixture line 0123456789abcdefghijklmnopqrstuvwxyz\n";
    let repeats = LARGE_ENTRY_SIZE / line.len() + 1;
    let mut payload = line.repeat(repeats);
    payload.truncate(LARGE_ENTRY_SIZE);
    payload
}

#[cfg(feature = "cli")]
pub(crate) fn large_archive_fixture() -> LargeArchiveFixture {
    let root_dir = tempfile::tempdir().expect("tempdir should exist");
    let archive_path = root_dir.path().join("large.zip");
    let payload = large_text_payload();
    let file = std::fs::File::create(&archive_path).expect("zip file should open");
    let mut writer = ZipWriter::new(file);
    let options = SimpleFileOptions::default().compression_method(CompressionMethod::Stored);

    writer.start_file(LARGE_ENTRY_NAME, options).expect("should start large entry");
    writer.write_all(&payload).expect("should write large entry payload");
    writer.finish().expect("zip archive should finish");

    LargeArchiveFixture { root_dir, archive_path, payload }
}

#[cfg(feature = "cli")]
pub(crate) fn assert_large_text_row(
    row: &serde_json::Value,
    archive_name: &str,
    payload: &[u8],
    expect_full_hash: bool,
) {
    let expected_head = blake3::hash(&payload[..DEFAULT_HEADER_BYTES]).to_hex().to_string();

    assert_eq!(row["archive_name"], archive_name);
    assert_eq!(row["entry_name"], LARGE_ENTRY_NAME);
    assert_eq!(row["entry_kind"], "file");
    assert_eq!(row["label"], "txt");
    assert_eq!(row["mime"], "text/plain");
    assert_eq!(row["detected_by"], "heuristic");
    assert_eq!(row["header_len"], DEFAULT_HEADER_BYTES as u64);
    assert_eq!(row["head_b3"], expected_head);
    assert_eq!(
        row["bytes_scanned"],
        if expect_full_hash { payload.len() as u64 } else { TRUNCATED_ENTRY_BYTES_SCANNED }
    );
    assert_eq!(row["truncated_scan"], !expect_full_hash);

    if expect_full_hash {
        let expected_full = blake3::hash(payload).to_hex().to_string();
        assert_eq!(row["full_b3"], expected_full);
    } else {
        assert!(row.get("full_b3").is_none());
    }
}

#[cfg(feature = "service")]
pub(crate) fn read_json(response: Response) -> serde_json::Value {
    let body = response.text().expect("HTTP response body should be readable");
    serde_json::from_str(&body).expect("HTTP response body should be valid JSON")
}

#[cfg(feature = "service")]
pub(crate) fn concurrent_create_job_requests(
    service: &ServiceProcess,
    request: &serde_json::Value,
    idempotency_key: &str,
    workers: usize,
) -> Vec<(StatusCode, serde_json::Value)> {
    let url = service.url("/v1/jobs");
    let body = serde_json::to_vec(request).expect("request JSON should serialize");
    let barrier = Arc::new(Barrier::new(workers));
    let mut handles = Vec::with_capacity(workers);

    for _ in 0..workers {
        let client = service.client().clone();
        let barrier = Arc::clone(&barrier);
        let url = url.clone();
        let body = body.clone();
        let idempotency_key = idempotency_key.to_owned();
        handles.push(thread::spawn(move || {
            barrier.wait();
            let response = client
                .post(url)
                .header("content-type", "application/json")
                .header("Idempotency-Key", idempotency_key)
                .body(body)
                .send()
                .expect("concurrent idempotent create should succeed");
            let status = response.status();
            let value = read_json(response);
            (status, value)
        }));
    }

    handles
        .into_iter()
        .map(|handle| handle.join().expect("request worker should not panic"))
        .collect()
}

#[cfg(feature = "service")]
pub(crate) fn assert_single_idempotent_job(
    responses: Vec<(StatusCode, serde_json::Value)>,
    service: &ServiceProcess,
) {
    let created_count =
        responses.iter().filter(|(status, _)| *status == StatusCode::CREATED).count();
    assert_eq!(created_count, 1, "exactly one create must win the race");
    assert!(responses
        .iter()
        .all(|(status, _)| { *status == StatusCode::CREATED || *status == StatusCode::OK }));

    let job_ids: std::collections::BTreeSet<_> = responses
        .iter()
        .map(|(_, value)| value["job_id"].as_str().expect("job_id should be present").to_owned())
        .collect();
    assert_eq!(job_ids.len(), 1, "all replays must resolve to one job");

    let job_id = job_ids.into_iter().next().expect("a single job id should be present");
    let result = wait_for_job_result(service, &job_id);
    assert_eq!(result["total_entries"], 2);
}

#[cfg(feature = "service")]
pub(crate) fn wait_for_job_result(service: &ServiceProcess, job_id: &str) -> serde_json::Value {
    let deadline = Instant::now() + SERVICE_STARTUP_TIMEOUT;

    loop {
        let response = service
            .client()
            .get(service.url(&format!("/v1/jobs/{job_id}/result")))
            .send()
            .expect("service should respond to job result polling");

        match response.status() {
            StatusCode::OK => return read_json(response),
            StatusCode::ACCEPTED => {
                assert!(
                    Instant::now() < deadline,
                    "timed out waiting for async job {job_id} to finish"
                );
                thread::sleep(SERVICE_POLL_INTERVAL);
            }
            status => {
                let body = response.text().unwrap_or_else(|_| "<failed to read body>".to_owned());
                panic!("unexpected job result status {status}: {body}");
            }
        }
    }
}

#[cfg(feature = "service")]
pub(crate) fn assert_inflight_running_job_is_recovered_across_restart(env: &[(String, String)]) {
    let delayed = delayed_object_storage_fixture_server();
    let mut service = ServiceProcess::spawn_with_env(env);
    let create = service
        .client()
        .post(service.url("/v1/jobs"))
        .header("content-type", "application/json")
        .body(
            serde_json::to_vec(&serde_json::json!({
                "source": {
                    "kind": "object_storage_url",
                    "url": delayed.url.clone()
                },
                "fast_only": true
            }))
            .expect("request JSON should serialize"),
        )
        .send()
        .expect("service should create delayed async job");

    assert_eq!(create.status(), StatusCode::CREATED);
    let create_value = read_json(create);
    let job_id = create_value["job_id"].as_str().expect("job_id should be present").to_owned();

    let running = wait_for_job_state(&service, &job_id, "running");
    assert_eq!(running["request"]["source"]["kind"], "object_storage_url");
    assert_eq!(running["request"]["source"]["url"], delayed.url);

    delayed.wait_for_request();
    service.kill();
    delayed.release();

    let restarted = ServiceProcess::spawn_with_env(env);
    let result = wait_for_job_result(&restarted, &job_id);
    assert_eq!(result["archive"]["path"], delayed.url);
    assert_eq!(result["archive"]["name"], "remote-sample.zip");
    assert_eq!(result["total_entries"], 2);
    assert_eq!(result["total_files"], 2);

    let status = restarted
        .client()
        .get(restarted.url(&format!("/v1/jobs/{job_id}")))
        .send()
        .expect("restarted service should expose recovered job status");
    assert_eq!(status.status(), StatusCode::OK);
    assert_eq!(read_json(status)["state"], "succeeded");

    let metrics = restarted
        .client()
        .get(restarted.url("/v1/jobs/metrics"))
        .send()
        .expect("restarted service should expose recovered job metrics");
    assert_eq!(metrics.status(), StatusCode::OK);
    let metrics_value = read_json(metrics);
    assert_eq!(metrics_value["lifecycle"]["created_total"], 1);
    assert_eq!(metrics_value["lifecycle"]["started_total"], 2);
    assert_eq!(metrics_value["current"]["queued_jobs"], 0);
    assert_eq!(metrics_value["current"]["running_jobs"], 0);
    assert_eq!(metrics_value["current"]["succeeded_jobs"], 1);
}

#[cfg(feature = "service")]
fn wait_for_job_state(
    service: &ServiceProcess,
    job_id: &str,
    expected_state: &str,
) -> serde_json::Value {
    let deadline = Instant::now() + SERVICE_STARTUP_TIMEOUT;

    loop {
        let response = service
            .client()
            .get(service.url(&format!("/v1/jobs/{job_id}")))
            .send()
            .expect("service should respond to job status polling");
        assert_eq!(response.status(), StatusCode::OK);
        let value = read_json(response);

        if value["state"] == expected_state {
            return value;
        }
        assert!(
            Instant::now() < deadline,
            "timed out waiting for async job {job_id} to reach {expected_state}"
        );
        thread::sleep(SERVICE_POLL_INTERVAL);
    }
}

#[cfg(feature = "service")]
pub(crate) fn sample_zip_archive() -> NamedTempFile {
    let archive = tempfile::Builder::new()
        .prefix("sample-")
        .suffix(".zip")
        .tempfile()
        .expect("temp zip should be created");
    let file = std::fs::File::create(archive.path()).expect("zip file should open");
    let mut writer = ZipWriter::new(file);
    let options = SimpleFileOptions::default().compression_method(CompressionMethod::Stored);

    writer.start_file("alpha.zip", options).expect("should start alpha entry");
    writer.write_all(b"PK\x03\x04payload").expect("should write alpha entry");
    writer.start_file("nested/bravo.pdf", options).expect("should start bravo entry");
    writer.write_all(b"%PDF-1.7 payload").expect("should write bravo entry");
    writer.finish().expect("zip archive should finish");

    archive
}

#[cfg(feature = "service")]
pub(crate) struct ServiceProcess {
    base_url: String,
    child: Child,
    client: Client,
    _runtime_dir: TempDir,
}

#[cfg(feature = "service")]
impl ServiceProcess {
    pub(crate) fn spawn() -> Self {
        Self::spawn_with_env(&[])
    }

    pub(crate) fn spawn_with_env(envs: &[(String, String)]) -> Self {
        let client = Client::builder()
            .timeout(HTTP_REQUEST_TIMEOUT)
            .build()
            .expect("HTTP client should build");
        let binary = std::env::var_os("CARGO_BIN_EXE_archive_scan_server")
            .expect("cargo should expose the archive_scan_server test binary path");
        let runtime_dir = tempfile::tempdir().expect("service runtime tempdir should exist");
        let job_store_path = runtime_dir.path().join("jobs/state.json");
        let result_store_dir = runtime_dir.path().join("results");
        let extract_store_dir = runtime_dir.path().join("extracted");
        let extract_metadata_dir = runtime_dir.path().join("extract-metadata");
        let object_source_temp_dir = runtime_dir.path().join("object-source-temp");
        let extract_temp_dir = runtime_dir.path().join("extract-temp");

        for attempt in 0..8 {
            let addr = reserve_local_addr();
            let mut command = Command::new(&binary);
            command
                .env("ARCHIVE_SCAN_SERVICE_ADDR", &addr)
                .env("ARCHIVE_SCAN_JOB_STORE_PATH", &job_store_path)
                .env("ARCHIVE_SCAN_RESULT_STORE_DIR", &result_store_dir)
                .env("ARCHIVE_SCAN_EXTRACT_STORE_DIR", &extract_store_dir)
                .env("ARCHIVE_SCAN_EXTRACT_METADATA_DIR", &extract_metadata_dir)
                .env("ARCHIVE_SCAN_OBJECT_SOURCE_TEMP_DIR", &object_source_temp_dir)
                .env("ARCHIVE_SCAN_EXTRACT_STORE_TEMP_DIR", &extract_temp_dir)
                .stdout(Stdio::piped())
                .stderr(Stdio::piped());
            for (key, value) in envs {
                command.env(key, value);
            }
            let mut child = command.spawn().expect("service process should start");

            match wait_for_service_ready(&client, &mut child, &addr) {
                Ok(()) => {
                    return Self {
                        base_url: format!("http://{addr}"),
                        child,
                        client,
                        _runtime_dir: runtime_dir,
                    };
                }
                Err(output) if output.contains("Address already in use") && attempt < 7 => {
                    continue;
                }
                Err(output) => {
                    panic!("service process did not become ready on {addr}: {output}");
                }
            }
        }

        panic!("service process did not bind to an available local address after retries")
    }

    pub(crate) fn client(&self) -> &Client {
        &self.client
    }

    pub(crate) fn url(&self, path: &str) -> String {
        format!("{}{}", self.base_url, path)
    }

    pub(crate) fn kill(&mut self) {
        match self.child.try_wait() {
            Ok(Some(_)) => {}
            Ok(None) => {
                self.child.kill().expect("service process should accept kill signal");
                self.child.wait().expect("service process should exit after kill");
            }
            Err(err) => panic!("service process should remain inspectable: {err}"),
        }
    }
}

#[cfg(feature = "service")]
impl Drop for ServiceProcess {
    fn drop(&mut self) {
        match self.child.try_wait() {
            Ok(Some(_)) => {}
            Ok(None) => {
                let _ = self.child.kill();
                let _ = self.child.wait();
            }
            Err(_) => {}
        }
    }
}

#[cfg(feature = "service")]
pub(crate) struct RedisFixture {
    container_id: String,
    pub(crate) url: String,
    pub(crate) key_prefix: String,
}

#[cfg(feature = "service")]
impl Drop for RedisFixture {
    fn drop(&mut self) {
        let _ = Command::new("docker").args(["rm", "-f", &self.container_id]).output();
    }
}

#[cfg(feature = "service")]
pub(crate) fn redis_fixture() -> Option<RedisFixture> {
    if !docker_command_succeeds(["info", "--format", "{{.ServerVersion}}"]) {
        eprintln!("skipping redis integration test because docker is unavailable");
        return None;
    }

    if !docker_command_succeeds(["image", "inspect", REDIS_IMAGE, "--format", "{{.Id}}"]) {
        let pulled = Command::new("docker").args(["pull", REDIS_IMAGE]).output().ok()?;
        if !pulled.status.success() {
            eprintln!(
                "skipping redis integration test because docker pull failed: {}",
                String::from_utf8_lossy(&pulled.stderr)
            );
            return None;
        }
    }

    let container_id = docker_stdout([
        "run",
        "-d",
        "--rm",
        "-p",
        "127.0.0.1::6379",
        REDIS_IMAGE,
        "redis-server",
        "--save",
        "",
        "--appendonly",
        "yes",
    ])?;
    let mut fixture =
        RedisFixture { container_id, url: String::new(), key_prefix: unique_redis_key_prefix() };

    let port_output = docker_stdout(["port", &fixture.container_id, "6379/tcp"])?;
    let host_port = port_output.rsplit(':').next()?.trim();
    fixture.url = format!("redis://127.0.0.1:{host_port}/0");

    if !wait_for_redis_ready(&fixture.container_id) {
        eprintln!("skipping redis integration test because redis did not become ready");
        return None;
    }

    Some(fixture)
}

#[cfg(feature = "service")]
pub(crate) struct PostgresFixture {
    container_id: String,
    pub(crate) url: String,
    pub(crate) table_prefix: String,
}

#[cfg(feature = "service")]
impl Drop for PostgresFixture {
    fn drop(&mut self) {
        let _ = Command::new("docker").args(["rm", "-f", &self.container_id]).output();
    }
}

#[cfg(feature = "service")]
pub(crate) fn postgres_fixture() -> Option<PostgresFixture> {
    if !docker_command_succeeds(["info", "--format", "{{.ServerVersion}}"]) {
        eprintln!("skipping postgres integration test because docker is unavailable");
        return None;
    }

    if !docker_command_succeeds(["image", "inspect", POSTGRES_IMAGE, "--format", "{{.Id}}"]) {
        let pulled = Command::new("docker").args(["pull", POSTGRES_IMAGE]).output().ok()?;
        if !pulled.status.success() {
            eprintln!(
                "skipping postgres integration test because docker pull failed: {}",
                String::from_utf8_lossy(&pulled.stderr)
            );
            return None;
        }
    }

    let container_id = docker_stdout([
        "run",
        "-d",
        "--rm",
        "-e",
        "POSTGRES_PASSWORD=postgres",
        "-e",
        "POSTGRES_DB=archive_scan_test",
        "-p",
        "127.0.0.1::5432",
        POSTGRES_IMAGE,
    ])?;
    let fixture = PostgresFixture {
        container_id,
        url: String::new(),
        table_prefix: unique_postgres_table_prefix(),
    };

    let port_output = docker_stdout(["port", &fixture.container_id, "5432/tcp"])?;
    let host_port = port_output.rsplit(':').next()?.trim();
    let mut fixture = fixture;
    fixture.url = format!("postgresql://postgres:postgres@127.0.0.1:{host_port}/archive_scan_test");

    if !wait_for_postgres_ready(&fixture.container_id) {
        eprintln!("skipping postgres integration test because postgres did not become ready");
        return None;
    }

    Some(fixture)
}

#[cfg(feature = "service")]
pub(crate) struct SeaweedFsFixture {
    container_id: String,
    pub(crate) endpoint: String,
    pub(crate) bucket: String,
}

#[cfg(feature = "service")]
impl Drop for SeaweedFsFixture {
    fn drop(&mut self) {
        let _ = Command::new("docker").args(["rm", "-f", &self.container_id]).output();
    }
}

#[cfg(feature = "service")]
impl SeaweedFsFixture {
    pub(crate) fn list_keys(&self, prefix: &str) -> Result<Vec<String>, String> {
        let url = format!(
            "{}/{}?list-type=2&prefix={}",
            self.endpoint,
            self.bucket,
            percent_encode_query(prefix)
        );
        let response = Client::builder()
            .timeout(HTTP_REQUEST_TIMEOUT)
            .build()
            .map_err(|err| err.to_string())?
            .get(url)
            .send()
            .map_err(|err| err.to_string())?;
        let status = response.status();
        let body = response.text().map_err(|err| err.to_string())?;
        if !status.is_success() {
            return Err(format!("seaweedfs list returned {status}: {body}"));
        }
        Ok(parse_s3_keys_from_list_xml(&body))
    }
}

#[cfg(feature = "service")]
pub(crate) fn seaweedfs_fixture(bucket: &str) -> Option<SeaweedFsFixture> {
    if !docker_command_succeeds(["info", "--format", "{{.ServerVersion}}"]) {
        eprintln!("skipping seaweedfs integration test because docker is unavailable");
        return None;
    }

    if !docker_command_succeeds(["image", "inspect", SEAWEEDFS_IMAGE, "--format", "{{.Id}}"]) {
        let pulled = Command::new("docker").args(["pull", SEAWEEDFS_IMAGE]).output().ok()?;
        if !pulled.status.success() {
            eprintln!(
                "skipping seaweedfs integration test because docker pull failed: {}",
                String::from_utf8_lossy(&pulled.stderr)
            );
            return None;
        }
    }

    let bucket_env = format!("S3_BUCKET={bucket}");
    let container_id = docker_stdout([
        "run",
        "-d",
        "--rm",
        "-e",
        &bucket_env,
        "-p",
        "127.0.0.1::8333",
        SEAWEEDFS_IMAGE,
    ])?;
    let port_output = docker_stdout(["port", &container_id, "8333/tcp"])?;
    let host_port = port_output.rsplit(':').next()?.trim();
    let fixture = SeaweedFsFixture {
        container_id,
        endpoint: format!("http://127.0.0.1:{host_port}"),
        bucket: bucket.to_owned(),
    };

    if !wait_for_seaweedfs_ready(&fixture.endpoint, bucket) {
        eprintln!("skipping seaweedfs integration test because seaweedfs did not become ready");
        return None;
    }

    Some(fixture)
}

#[cfg(feature = "service")]
fn docker_command_succeeds<const N: usize>(args: [&str; N]) -> bool {
    Command::new("docker").args(args).output().is_ok_and(|output| output.status.success())
}

#[cfg(feature = "service")]
fn docker_stdout<const N: usize>(args: [&str; N]) -> Option<String> {
    let output = Command::new("docker").args(args).output().ok()?;
    if !output.status.success() {
        return None;
    }
    let value = String::from_utf8(output.stdout).ok()?;
    let trimmed = value.trim();
    (!trimmed.is_empty()).then(|| trimmed.to_owned())
}

#[cfg(feature = "service")]
fn wait_for_redis_ready(container_id: &str) -> bool {
    let deadline = Instant::now() + REDIS_STARTUP_TIMEOUT;
    while Instant::now() < deadline {
        if docker_command_succeeds(["exec", container_id, "redis-cli", "PING"]) {
            return true;
        }
        thread::sleep(SERVICE_POLL_INTERVAL);
    }
    false
}

#[cfg(feature = "service")]
fn wait_for_postgres_ready(container_id: &str) -> bool {
    let deadline = Instant::now() + POSTGRES_STARTUP_TIMEOUT;
    while Instant::now() < deadline {
        if docker_command_succeeds([
            "exec",
            container_id,
            "pg_isready",
            "-U",
            "postgres",
            "-d",
            "archive_scan_test",
        ]) {
            return true;
        }
        thread::sleep(SERVICE_POLL_INTERVAL);
    }
    false
}

#[cfg(feature = "service")]
fn wait_for_seaweedfs_ready(endpoint: &str, bucket: &str) -> bool {
    let deadline = Instant::now() + SEAWEEDFS_STARTUP_TIMEOUT;
    let client =
        Client::builder().timeout(HTTP_REQUEST_TIMEOUT).build().expect("HTTP client should build");
    let url = format!("{endpoint}/{bucket}?list-type=2");
    while Instant::now() < deadline {
        if client.get(&url).send().is_ok_and(|response| response.status().is_success()) {
            return true;
        }
        thread::sleep(SERVICE_POLL_INTERVAL);
    }
    false
}

#[cfg(feature = "service")]
fn unique_redis_key_prefix() -> String {
    let stamp =
        SystemTime::UNIX_EPOCH.elapsed().map_or(0, |elapsed| elapsed.as_nanos() % 1_000_000_000);
    format!("archive_scan_r_{}_{}", std::process::id(), stamp)
}

#[cfg(feature = "service")]
fn unique_postgres_table_prefix() -> String {
    let stamp =
        SystemTime::UNIX_EPOCH.elapsed().map_or(0, |elapsed| elapsed.as_nanos() % 1_000_000_000);
    format!("archive_scan_t_{}_{}", std::process::id(), stamp)
}

#[cfg(feature = "service")]
fn wait_for_service_ready(client: &Client, child: &mut Child, addr: &str) -> Result<(), String> {
    let deadline = Instant::now() + SERVICE_STARTUP_TIMEOUT;
    let health_url = format!("http://{addr}/healthz");

    loop {
        if let Some(status) = child.try_wait().expect("service process should remain inspectable") {
            let output = read_child_output(child);
            return Err(format!("service process exited early with {status}: {output}"));
        }

        if let Ok(response) = client.get(&health_url).send() {
            if response.status() == StatusCode::OK {
                return Ok(());
            }
        }

        if Instant::now() >= deadline {
            let output = read_child_output(child);
            return Err(output);
        }

        thread::sleep(SERVICE_POLL_INTERVAL);
    }
}

#[cfg(feature = "service")]
fn read_child_output(child: &mut Child) -> String {
    let mut output = String::new();

    if let Some(mut stdout) = child.stdout.take() {
        let mut buffer = String::new();
        let _ = stdout.read_to_string(&mut buffer);
        if !buffer.is_empty() {
            output.push_str("stdout:\n");
            output.push_str(&buffer);
        }
    }

    if let Some(mut stderr) = child.stderr.take() {
        let mut buffer = String::new();
        let _ = stderr.read_to_string(&mut buffer);
        if !buffer.is_empty() {
            if !output.is_empty() {
                output.push('\n');
            }
            output.push_str("stderr:\n");
            output.push_str(&buffer);
        }
    }

    if output.is_empty() {
        output.push_str("<no process output>");
    }

    output
}

#[cfg(feature = "service")]
pub(crate) struct ObjectStorageFixtureServer {
    pub(crate) url: String,
    stop: Arc<AtomicBool>,
    handle: Option<JoinHandle<()>>,
}

#[cfg(feature = "service")]
impl Drop for ObjectStorageFixtureServer {
    fn drop(&mut self) {
        self.stop.store(true, Ordering::Relaxed);
        if let Some(handle) = self.handle.take() {
            let _ = handle.join();
        }
    }
}

#[cfg(feature = "service")]
struct DelayedObjectStorageFixtureServer {
    url: String,
    release: Arc<AtomicBool>,
    saw_request: Arc<AtomicBool>,
    stop: Arc<AtomicBool>,
    handle: Option<JoinHandle<()>>,
}

#[cfg(feature = "service")]
impl DelayedObjectStorageFixtureServer {
    fn release(&self) {
        self.release.store(true, Ordering::Relaxed);
    }

    fn wait_for_request(&self) {
        let deadline = Instant::now() + SERVICE_STARTUP_TIMEOUT;
        while Instant::now() < deadline {
            if self.saw_request.load(Ordering::Relaxed) {
                return;
            }
            thread::sleep(SERVICE_POLL_INTERVAL);
        }

        panic!("timed out waiting for delayed object source request");
    }
}

#[cfg(feature = "service")]
impl Drop for DelayedObjectStorageFixtureServer {
    fn drop(&mut self) {
        self.stop.store(true, Ordering::Relaxed);
        self.release.store(true, Ordering::Relaxed);
        if let Some(handle) = self.handle.take() {
            let _ = handle.join();
        }
    }
}

#[cfg(feature = "service")]
pub(crate) struct S3ResultStoreFixtureServer {
    pub(crate) endpoint: String,
    state: Arc<Mutex<HashMap<String, Vec<u8>>>>,
    stop: Arc<AtomicBool>,
    handle: Option<JoinHandle<()>>,
}

#[cfg(feature = "service")]
impl Drop for S3ResultStoreFixtureServer {
    fn drop(&mut self) {
        self.stop.store(true, Ordering::Relaxed);
        if let Some(handle) = self.handle.take() {
            let _ = handle.join();
        }
    }
}

#[cfg(feature = "service")]
impl S3ResultStoreFixtureServer {
    pub(crate) fn objects(&self) -> Vec<String> {
        self.state.lock().expect("fixture state should not be poisoned").keys().cloned().collect()
    }
}

#[cfg(feature = "service")]
pub(crate) fn object_storage_fixture_server() -> ObjectStorageFixtureServer {
    let archive = sample_zip_archive();
    let payload = Arc::new(
        std::fs::read(archive.path()).expect("object storage fixture archive should read"),
    );
    let listener = TcpListener::bind("127.0.0.1:0").expect("fixture listener should bind");
    listener.set_nonblocking(true).expect("fixture listener should be non-blocking");
    let addr = listener.local_addr().expect("fixture listener should expose local address");
    let stop = Arc::new(AtomicBool::new(false));
    let handle = thread::spawn({
        let stop = Arc::clone(&stop);
        let payload = Arc::clone(&payload);
        move || {
            while !stop.load(Ordering::Relaxed) {
                match listener.accept() {
                    Ok((stream, _)) => serve_fixture_request(stream, payload.as_ref()),
                    Err(err) if err.kind() == std::io::ErrorKind::WouldBlock => {
                        thread::sleep(SERVICE_POLL_INTERVAL);
                    }
                    Err(err) => panic!("fixture listener accept failed: {err}"),
                }
            }
        }
    });

    ObjectStorageFixtureServer {
        url: format!("http://{addr}/fixtures/remote-sample.zip"),
        stop,
        handle: Some(handle),
    }
}

#[cfg(feature = "service")]
fn delayed_object_storage_fixture_server() -> DelayedObjectStorageFixtureServer {
    let archive = sample_zip_archive();
    let payload = Arc::new(
        std::fs::read(archive.path()).expect("delayed object source fixture archive should read"),
    );
    let listener = TcpListener::bind("127.0.0.1:0").expect("fixture listener should bind");
    listener.set_nonblocking(true).expect("fixture listener should be non-blocking");
    let addr = listener.local_addr().expect("fixture listener should expose local address");
    let release = Arc::new(AtomicBool::new(false));
    let saw_request = Arc::new(AtomicBool::new(false));
    let stop = Arc::new(AtomicBool::new(false));
    let handle = thread::spawn({
        let stop = Arc::clone(&stop);
        let release = Arc::clone(&release);
        let saw_request = Arc::clone(&saw_request);
        let payload = Arc::clone(&payload);
        move || {
            while !stop.load(Ordering::Relaxed) {
                match listener.accept() {
                    Ok((stream, _)) => {
                        let release = Arc::clone(&release);
                        let saw_request = Arc::clone(&saw_request);
                        let payload = Arc::clone(&payload);
                        thread::spawn(move || {
                            serve_delayed_fixture_request(
                                stream,
                                payload.as_ref(),
                                &release,
                                &saw_request,
                            );
                        });
                    }
                    Err(err) if err.kind() == std::io::ErrorKind::WouldBlock => {
                        thread::sleep(SERVICE_POLL_INTERVAL);
                    }
                    Err(err) => panic!("fixture listener accept failed: {err}"),
                }
            }
        }
    });

    DelayedObjectStorageFixtureServer {
        url: format!("http://{addr}/fixtures/remote-sample.zip"),
        release,
        saw_request,
        stop,
        handle: Some(handle),
    }
}

#[cfg(feature = "service")]
pub(crate) fn s3_result_store_fixture_server() -> S3ResultStoreFixtureServer {
    let state = Arc::new(Mutex::new(HashMap::new()));
    let listener = TcpListener::bind("127.0.0.1:0").expect("fixture listener should bind");
    listener.set_nonblocking(true).expect("fixture listener should be non-blocking");
    let addr = listener.local_addr().expect("fixture listener should expose local address");
    let stop = Arc::new(AtomicBool::new(false));
    let handle = thread::spawn({
        let stop = Arc::clone(&stop);
        let state = Arc::clone(&state);
        move || {
            while !stop.load(Ordering::Relaxed) {
                match listener.accept() {
                    Ok((stream, _)) => serve_s3_fixture_request(stream, &state),
                    Err(err) if err.kind() == std::io::ErrorKind::WouldBlock => {
                        thread::sleep(SERVICE_POLL_INTERVAL);
                    }
                    Err(err) => panic!("fixture listener accept failed: {err}"),
                }
            }
        }
    });

    S3ResultStoreFixtureServer {
        endpoint: format!("http://{addr}"),
        state,
        stop,
        handle: Some(handle),
    }
}

#[cfg(feature = "service")]
fn serve_fixture_request(mut stream: TcpStream, payload: &[u8]) {
    let _ = stream.set_read_timeout(Some(HTTP_REQUEST_TIMEOUT));
    let _ = stream.set_write_timeout(Some(HTTP_REQUEST_TIMEOUT));

    let request = read_http_request(&mut stream, 1024);

    let request_line = String::from_utf8_lossy(&request);
    let response = if request_line
        .lines()
        .next()
        .is_some_and(|line| line.starts_with("GET /fixtures/remote-sample.zip "))
    {
        let mut response = format!(
            "HTTP/1.1 200 OK\r\ncontent-type: application/zip\r\ncontent-length: {}\r\nconnection: close\r\n\r\n",
            payload.len()
        )
        .into_bytes();
        response.extend_from_slice(payload);
        response
    } else {
        b"HTTP/1.1 404 Not Found\r\ncontent-length: 0\r\nconnection: close\r\n\r\n".to_vec()
    };

    stream.write_all(&response).expect("fixture response should be written");
    stream.flush().expect("fixture response should be flushed");
}

#[cfg(feature = "service")]
fn serve_delayed_fixture_request(
    mut stream: TcpStream,
    payload: &[u8],
    release: &Arc<AtomicBool>,
    saw_request: &Arc<AtomicBool>,
) {
    let _ = stream.set_read_timeout(Some(HTTP_REQUEST_TIMEOUT));
    let _ = stream.set_write_timeout(Some(HTTP_REQUEST_TIMEOUT));

    let request = read_http_request(&mut stream, 1024);
    let request_line = String::from_utf8_lossy(&request);
    if request_line
        .lines()
        .next()
        .is_some_and(|line| line.starts_with("GET /fixtures/remote-sample.zip "))
    {
        saw_request.store(true, Ordering::Relaxed);

        while !release.load(Ordering::Relaxed) {
            thread::sleep(SERVICE_POLL_INTERVAL);
        }

        let mut response = format!(
            "HTTP/1.1 200 OK\r\ncontent-type: application/zip\r\ncontent-length: {}\r\nconnection: close\r\n\r\n",
            payload.len()
        )
        .into_bytes();
        response.extend_from_slice(payload);
        let _ = stream.write_all(&response);
        let _ = stream.flush();
        return;
    }

    let _ = stream
        .write_all(b"HTTP/1.1 404 Not Found\r\ncontent-length: 0\r\nconnection: close\r\n\r\n");
    let _ = stream.flush();
}

#[cfg(feature = "service")]
fn serve_s3_fixture_request(mut stream: TcpStream, state: &Arc<Mutex<HashMap<String, Vec<u8>>>>) {
    let _ = stream.set_read_timeout(Some(HTTP_REQUEST_TIMEOUT));
    let _ = stream.set_write_timeout(Some(HTTP_REQUEST_TIMEOUT));

    let request = read_http_request(&mut stream, 64 * 1024);

    let request_text = String::from_utf8_lossy(&request);
    let mut sections = request_text.split("\r\n\r\n");
    let head = sections.next().unwrap_or_default();
    let body = sections.next().unwrap_or_default().as_bytes().to_vec();
    let mut lines = head.lines();
    let request_line = lines.next().unwrap_or_default();
    let headers = lines
        .filter_map(|line| line.split_once(':'))
        .map(|(name, value)| (name.trim().to_ascii_lowercase(), value.trim().to_owned()))
        .collect::<HashMap<_, _>>();

    assert!(
        headers
            .get("authorization")
            .is_some_and(|value| value.starts_with("AWS4-HMAC-SHA256 Credential=")),
        "s3 fixture expects sigv4 Authorization header"
    );
    assert!(headers.contains_key("x-amz-date"));
    assert!(headers.contains_key("x-amz-content-sha256"));

    let mut parts = request_line.split_whitespace();
    let method = parts.next().unwrap_or_default();
    let target = parts.next().unwrap_or_default();
    let (path, query) = target.split_once('?').unwrap_or((target, ""));
    let key =
        path.trim_start_matches('/').strip_prefix("result-bucket/").unwrap_or_default().to_owned();

    let (status_line, content_type, response_body) = match method {
        "PUT" => {
            state.lock().expect("fixture state should not be poisoned").insert(key, body);
            ("HTTP/1.1 200 OK", None, Vec::new())
        }
        "GET" if query.contains("list-type=2") => {
            let prefix = query
                .split('&')
                .filter_map(|pair| pair.split_once('='))
                .find_map(|(name, value)| (name == "prefix").then_some(value))
                .map_or_else(String::new, percent_decode_s3_query);
            let keys = state
                .lock()
                .expect("fixture state should not be poisoned")
                .keys()
                .filter(|key| key.starts_with(&prefix))
                .cloned()
                .collect::<Vec<_>>();
            let xml = format!(
                "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\
<ListBucketResult><IsTruncated>false</IsTruncated>{}</ListBucketResult>",
                keys.iter()
                    .map(|key| format!("<Contents><Key>{key}</Key></Contents>"))
                    .collect::<String>()
            );
            ("HTTP/1.1 200 OK", Some("application/xml"), xml.into_bytes())
        }
        "GET" => {
            let payload =
                state.lock().expect("fixture state should not be poisoned").get(&key).cloned();
            if let Some(payload) = payload {
                ("HTTP/1.1 200 OK", Some("application/json"), payload)
            } else {
                ("HTTP/1.1 404 Not Found", None, Vec::new())
            }
        }
        "DELETE" => {
            state.lock().expect("fixture state should not be poisoned").remove(&key);
            ("HTTP/1.1 204 No Content", None, Vec::new())
        }
        _ => ("HTTP/1.1 405 Method Not Allowed", None, Vec::new()),
    };

    let mut response = format!(
        "{status_line}\r\ncontent-length: {}\r\nconnection: close\r\n",
        response_body.len()
    )
    .into_bytes();
    if let Some(content_type) = content_type {
        response.extend_from_slice(format!("content-type: {content_type}\r\n").as_bytes());
    }
    response.extend_from_slice(b"\r\n");
    response.extend_from_slice(&response_body);

    stream.write_all(&response).expect("fixture response should be written");
    stream.flush().expect("fixture response should be flushed");
}

#[cfg(feature = "service")]
fn percent_decode_s3_query(value: &str) -> String {
    let mut decoded = Vec::with_capacity(value.len());
    let bytes = value.as_bytes();
    let mut index = 0;
    while index < bytes.len() {
        if bytes[index] == b'%' && index + 2 < bytes.len() {
            let hex = &value[index + 1..index + 3];
            if let Ok(byte) = u8::from_str_radix(hex, 16) {
                decoded.push(byte);
                index += 3;
                continue;
            }
        }
        decoded.push(bytes[index]);
        index += 1;
    }
    String::from_utf8(decoded).expect("decoded query should stay utf-8")
}

#[cfg(feature = "service")]
fn percent_encode_query(value: &str) -> String {
    let mut encoded = String::with_capacity(value.len());
    for byte in value.bytes() {
        match byte {
            b'A'..=b'Z' | b'a'..=b'z' | b'0'..=b'9' | b'-' | b'_' | b'.' | b'~' => {
                encoded.push(char::from(byte));
            }
            _ => encoded.push_str(&format!("%{byte:02X}")),
        }
    }
    encoded
}

#[cfg(feature = "service")]
fn parse_s3_keys_from_list_xml(body: &str) -> Vec<String> {
    let mut keys = Vec::new();
    let mut rest = body;
    while let Some(start) = rest.find("<Key>") {
        let after_start = &rest[start + "<Key>".len()..];
        let Some(end) = after_start.find("</Key>") else {
            break;
        };
        keys.push(after_start[..end].to_owned());
        rest = &after_start[end + "</Key>".len()..];
    }
    keys
}

#[cfg(feature = "service")]
pub(crate) fn assert_postgres_extract_metadata(
    postgres_fixture: &PostgresFixture,
    extraction_id: &str,
    expected_entries: i64,
) {
    let mut client = postgres::Client::connect(&postgres_fixture.url, postgres::NoTls)
        .expect("postgres metadata assertion should connect");
    let run_row = client
        .query_one(
            &format!(
                "SELECT status, result FROM {}_extract_runs WHERE run_id = $1",
                postgres_fixture.table_prefix
            ),
            &[&extraction_id],
        )
        .expect("extract metadata run should exist");
    let status: String = run_row.get("status");
    let result: serde_json::Value = run_row.get("result");
    assert_eq!(status, "succeeded");
    assert_eq!(result["stored_files"], expected_entries);
    assert_eq!(result["total_files"], expected_entries);
    assert_eq!(result["entries"].as_array().map(Vec::len), Some(expected_entries as usize));

    let entry_row = client
        .query_one(
            &format!(
                "SELECT COUNT(*)::BIGINT AS count FROM {}_extract_entries WHERE run_id = $1",
                postgres_fixture.table_prefix
            ),
            &[&extraction_id],
        )
        .expect("extract metadata entries should be queryable");
    let count: i64 = entry_row.get("count");
    assert_eq!(count, expected_entries);

    let rows = client
        .query(
            &format!(
                "SELECT sanitized_path, stored_uri, stored_size_bytes, metadata \
                 FROM {}_extract_entries \
                 WHERE run_id = $1 \
                 ORDER BY entry_index",
                postgres_fixture.table_prefix
            ),
            &[&extraction_id],
        )
        .expect("extract metadata entry payloads should be queryable");
    assert_eq!(rows.len(), expected_entries as usize);

    for row in rows {
        let sanitized_path: String = row.get("sanitized_path");
        let stored_uri: Option<String> = row.get("stored_uri");
        let stored_size_bytes: Option<i64> = row.get("stored_size_bytes");
        let metadata: serde_json::Value = row.get("metadata");

        assert_eq!(metadata["sanitized_path"], sanitized_path);
        assert_eq!(metadata["row"]["entry_kind"], "file");
        assert_eq!(metadata["stored_object"]["uri"].as_str(), stored_uri.as_deref());
        assert_eq!(metadata["stored_object"]["size_bytes"].as_i64(), stored_size_bytes);
        assert!(metadata["stored_object"]["b3"].as_str().is_some_and(|hash| hash.len() == 64));
        assert!(metadata["row"]["head_b3"].as_str().is_some_and(|hash| hash.len() == 64));
        assert!(metadata["row"]["full_b3"].as_str().is_some_and(|hash| hash.len() == 64));
        assert!(metadata["stored_object"]["uri"]
            .as_str()
            .is_some_and(|uri| uri.starts_with("s3://")));
    }
}

#[cfg(feature = "service")]
fn read_http_request(stream: &mut TcpStream, buffer_size: usize) -> Vec<u8> {
    let mut request = Vec::new();
    let mut buffer = vec![0_u8; buffer_size];
    let mut header_end = None;
    let mut content_length = 0_usize;

    loop {
        match stream.read(&mut buffer) {
            Ok(0) => break,
            Ok(read) => {
                request.extend_from_slice(&buffer[..read]);
                if header_end.is_none() {
                    header_end = find_header_end(&request);
                    if let Some(end) = header_end {
                        content_length = parse_content_length(&request[..end]).unwrap_or(0);
                    }
                }

                if let Some(end) = header_end {
                    if request.len() >= end.saturating_add(content_length) {
                        break;
                    }
                }
            }
            Err(err)
                if matches!(
                    err.kind(),
                    std::io::ErrorKind::WouldBlock | std::io::ErrorKind::TimedOut
                ) =>
            {
                if header_end.is_some() {
                    break;
                }
            }
            Err(err) => panic!("fixture request read failed: {err}"),
        }
    }

    request
}

#[cfg(feature = "service")]
fn find_header_end(request: &[u8]) -> Option<usize> {
    request.windows(4).position(|window| window == b"\r\n\r\n").map(|index| index + 4)
}

#[cfg(feature = "service")]
fn parse_content_length(head: &[u8]) -> Option<usize> {
    let text = String::from_utf8_lossy(head);
    text.lines().find_map(|line| {
        let (name, value) = line.split_once(':')?;
        name.trim()
            .eq_ignore_ascii_case("content-length")
            .then(|| value.trim().parse::<usize>().ok())
            .flatten()
    })
}

#[cfg(feature = "service")]
fn reserve_local_addr() -> String {
    let listener = TcpListener::bind("127.0.0.1:0").expect("ephemeral listener should bind");
    let addr = listener.local_addr().expect("ephemeral listener should expose local address");
    addr.to_string()
}
