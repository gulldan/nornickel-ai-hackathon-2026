use super::super::config::{ExtractStoreBackendKind, ExtractStoreConfig};
use crate::extract::{
    ExtractDestinationSummary, ExtractFileStore, ExtractStorageKind, FileWriteOptions,
    FileWriteOutcome, FilesystemExtractStore, StoredObject, WriteScanOutcome,
};
use anyhow::{anyhow, Context, Result};
use hmac::{Hmac, KeyInit, Mac};
use quick_xml::{events::Event, Reader};
use reqwest::{blocking::Client, Method, StatusCode};
use sha2::{Digest, Sha256};
use smallvec::SmallVec;
use std::{
    fmt::Write as _,
    fs::File,
    io::{self, Read, Seek, SeekFrom, Write},
    path::{Component, Path, PathBuf},
    time::Duration,
};
use tempfile::{Builder, NamedTempFile};
use time::{macros::format_description, OffsetDateTime};
use url::Url;

type HmacSha256 = Hmac<Sha256>;

pub(in crate::service) enum RuntimeExtractStore {
    Filesystem(FilesystemExtractStore),
    S3(S3ExtractStore),
}

#[derive(Clone, Debug)]
pub(in crate::service) struct S3ExtractStore {
    endpoint: Option<String>,
    region: String,
    bucket: Option<String>,
    key_prefix: String,
    access_key_id: Option<String>,
    secret_access_key: Option<String>,
    session_token: Option<String>,
    path_style: bool,
    temp_dir: Option<PathBuf>,
    client: Client,
}

#[derive(Clone, Copy)]
struct ValidatedS3Config<'a> {
    endpoint: &'a str,
    bucket: &'a str,
    access_key_id: &'a str,
    secret_access_key: &'a str,
    session_token: Option<&'a str>,
    region: &'a str,
}

#[derive(Debug)]
struct S3RequestTarget {
    url: String,
    host_header: String,
    canonical_uri: String,
    canonical_query: String,
}

impl RuntimeExtractStore {
    pub(in crate::service) fn new(config: &ExtractStoreConfig, extraction_id: &str) -> Self {
        match config.backend {
            ExtractStoreBackendKind::Filesystem => {
                let root = config.filesystem_dir.join(extraction_id);
                Self::Filesystem(FilesystemExtractStore::new(root))
            }
            ExtractStoreBackendKind::S3 => Self::S3(S3ExtractStore::new(config, extraction_id)),
        }
    }

    pub(in crate::service) fn readiness_check(config: &ExtractStoreConfig) -> Result<()> {
        match config.backend {
            ExtractStoreBackendKind::Filesystem => {
                FilesystemExtractStore::new(config.filesystem_dir.clone()).readiness_check()
            }
            ExtractStoreBackendKind::S3 => S3ExtractStore::new(config, ".healthcheck")
                .readiness_check()
                .context("s3 extract store readiness check failed"),
        }
    }
}

impl ExtractFileStore for RuntimeExtractStore {
    fn destination_summary(&self) -> ExtractDestinationSummary {
        match self {
            Self::Filesystem(store) => store.destination_summary(),
            Self::S3(store) => store.destination_summary(),
        }
    }

    fn create_directory(&mut self, relative_path: &Path) -> Result<Option<StoredObject>> {
        match self {
            Self::Filesystem(store) => store.create_directory(relative_path),
            Self::S3(store) => store.create_directory(relative_path),
        }
    }

    fn write_file<ShouldCancel>(
        &mut self,
        relative_path: &Path,
        reader: &mut dyn Read,
        options: FileWriteOptions<'_, ShouldCancel>,
    ) -> Result<FileWriteOutcome>
    where
        ShouldCancel: FnMut() -> bool,
    {
        match self {
            Self::Filesystem(store) => store.write_file(relative_path, reader, options),
            Self::S3(store) => store.write_file(relative_path, reader, options),
        }
    }
}

impl S3ExtractStore {
    fn new(config: &ExtractStoreConfig, extraction_id: &str) -> Self {
        Self {
            endpoint: config.s3_endpoint.clone(),
            region: config.s3_region.clone(),
            bucket: config.s3_bucket.clone(),
            key_prefix: normalize_key_prefix(&format!(
                "{}/{}",
                normalize_key_prefix(&config.s3_key_prefix),
                normalize_key_prefix(extraction_id)
            )),
            access_key_id: config.s3_access_key_id.clone(),
            secret_access_key: config.s3_secret_access_key.clone(),
            session_token: config.s3_session_token.clone(),
            path_style: config.s3_path_style,
            temp_dir: config.temp_dir.clone(),
            client: Client::builder()
                .connect_timeout(Duration::from_secs(10))
                .timeout(Duration::from_secs(300))
                .build()
                .expect("s3 extract store client should build"),
        }
    }

    fn readiness_check(&self) -> Result<()> {
        self.validate_config()?;
        if let Some(temp_dir) = self.temp_dir.as_deref() {
            std::fs::create_dir_all(temp_dir).with_context(|| {
                format!("failed to create s3 extract temp directory {}", temp_dir.display())
            })?;
            let _ = NamedTempFile::new_in(temp_dir).with_context(|| {
                format!("failed to allocate temp file in {}", temp_dir.display())
            })?;
        }
        let key = self.key_for_relative_path(Path::new(".healthcheck"));
        self.put_empty(&key)?;
        self.delete_key(&key)
    }

    fn destination_summary(&self) -> ExtractDestinationSummary {
        let root = self.bucket.as_ref().map_or_else(
            || "s3://<unconfigured>".to_owned(),
            |bucket| {
                if self.key_prefix.is_empty() {
                    format!("s3://{bucket}")
                } else {
                    format!("s3://{bucket}/{}", self.key_prefix)
                }
            },
        );
        ExtractDestinationSummary { backend: ExtractStorageKind::S3, root }
    }

    #[allow(clippy::needless_pass_by_ref_mut, clippy::unnecessary_wraps)]
    fn create_directory(&mut self, _relative_path: &Path) -> Result<Option<StoredObject>> {
        Ok(None)
    }

    #[allow(clippy::needless_pass_by_ref_mut)]
    fn write_file<ShouldCancel>(
        &mut self,
        relative_path: &Path,
        reader: &mut dyn Read,
        options: FileWriteOptions<'_, ShouldCancel>,
    ) -> Result<FileWriteOutcome>
    where
        ShouldCancel: FnMut() -> bool,
    {
        let mut staging_file = self.temp_file_for(relative_path)?;
        let (scan, payload_sha256) =
            copy_reader_to_temp(reader, staging_file.as_file_mut(), options)?;
        staging_file.as_file_mut().flush().context("failed to flush s3 extract temp file")?;
        staging_file
            .as_file_mut()
            .seek(SeekFrom::Start(0))
            .context("failed to rewind s3 extract temp file")?;

        let key = self.key_for_relative_path(relative_path);
        let size_bytes = scan.bytes_scanned;
        let file =
            staging_file.reopen().context("failed to reopen s3 extract temp file for upload")?;
        self.put_file(&key, file, size_bytes, &payload_sha256, "application/octet-stream")?;

        Ok(FileWriteOutcome {
            stored_object: StoredObject {
                kind: ExtractStorageKind::S3,
                uri: self.object_uri(&key),
                size_bytes,
                b3: Some(scan.b3.clone()),
            },
            scan,
        })
    }

    fn temp_file_for(&self, relative_path: &Path) -> Result<NamedTempFile> {
        let suffix = relative_path
            .extension()
            .and_then(|ext| ext.to_str())
            .map(|ext| format!(".{ext}"))
            .unwrap_or_default();
        let mut builder = Builder::new();
        builder.prefix("archive-scan-extract-").suffix(&suffix);
        match self.temp_dir.as_deref() {
            Some(temp_dir) => {
                std::fs::create_dir_all(temp_dir).with_context(|| {
                    format!("failed to create s3 extract temp directory {}", temp_dir.display())
                })?;
                builder.tempfile_in(temp_dir).with_context(|| {
                    format!("failed to allocate s3 extract temp file in {}", temp_dir.display())
                })
            }
            None => builder.tempfile().context("failed to allocate s3 extract temp file"),
        }
    }

    fn key_for_relative_path(&self, relative_path: &Path) -> String {
        let relative = path_to_s3_key(relative_path);
        if self.key_prefix.is_empty() {
            relative
        } else if relative.is_empty() {
            self.key_prefix.clone()
        } else {
            format!("{}/{}", self.key_prefix, relative)
        }
    }

    fn object_uri(&self, key: &str) -> String {
        self.bucket.as_ref().map_or_else(
            || format!("s3://<unconfigured>/{key}"),
            |bucket| format!("s3://{bucket}/{key}"),
        )
    }

    fn put_empty(&self, key: &str) -> Result<()> {
        self.send_request(Method::PUT, key, &sha256_hex(&[]), 0, None, None)?;
        Ok(())
    }

    fn put_file(
        &self,
        key: &str,
        file: File,
        size_bytes: u64,
        payload_sha256: &str,
        content_type: &str,
    ) -> Result<()> {
        self.send_request(
            Method::PUT,
            key,
            payload_sha256,
            size_bytes,
            Some(file),
            Some(content_type),
        )?;
        Ok(())
    }

    fn delete_key(&self, key: &str) -> Result<()> {
        self.send_request(Method::DELETE, key, &sha256_hex(&[]), 0, None, None)?;
        Ok(())
    }

    fn send_request(
        &self,
        method: Method,
        key: &str,
        payload_hash: &str,
        content_length: u64,
        body: Option<File>,
        content_type: Option<&str>,
    ) -> Result<()> {
        let config = self.validate_config()?;
        let target = self.build_target(config, Some(key), &[])?;
        let timestamp = OffsetDateTime::now_utc();
        let amz_date = timestamp
            .format(&format_description!("[year][month][day]T[hour][minute][second]Z"))
            .context("failed to format s3 request timestamp")?;
        let date_stamp = timestamp
            .format(&format_description!("[year][month][day]"))
            .context("failed to format s3 request date stamp")?;

        let mut signed_headers = vec![
            ("host".to_owned(), target.host_header.clone()),
            ("x-amz-content-sha256".to_owned(), payload_hash.to_owned()),
            ("x-amz-date".to_owned(), amz_date.clone()),
        ];
        if let Some(token) = config.session_token {
            signed_headers.push(("x-amz-security-token".to_owned(), token.to_owned()));
        }
        signed_headers.sort_by(|left, right| left.0.cmp(&right.0));

        let canonical_headers = signed_headers
            .iter()
            .map(|(name, value)| format!("{name}:{}\n", value.trim()))
            .collect::<String>();
        let signed_header_names =
            signed_headers.iter().map(|(name, _)| name.as_str()).collect::<Vec<_>>().join(";");
        let canonical_request = format!(
            "{}\n{}\n{}\n{}\n{}\n{}",
            method.as_str(),
            target.canonical_uri,
            target.canonical_query,
            canonical_headers,
            signed_header_names,
            payload_hash,
        );
        let credential_scope = format!("{date_stamp}/{}/s3/aws4_request", config.region);
        let string_to_sign = format!(
            "AWS4-HMAC-SHA256\n{amz_date}\n{credential_scope}\n{}",
            sha256_hex(canonical_request.as_bytes())
        );
        let signing_key =
            signing_key(config.secret_access_key.as_bytes(), &date_stamp, config.region);
        let signature = hmac_sha256(&signing_key, string_to_sign.as_bytes());
        let authorization = format!(
            "AWS4-HMAC-SHA256 Credential={}/{credential_scope}, SignedHeaders={}, Signature={}",
            config.access_key_id,
            signed_header_names,
            to_hex(&signature),
        );

        let mut request = self
            .client
            .request(method, &target.url)
            .header("host", target.host_header)
            .header("x-amz-content-sha256", payload_hash)
            .header("x-amz-date", amz_date)
            .header("authorization", authorization)
            .header("content-length", content_length);
        if let Some(token) = config.session_token {
            request = request.header("x-amz-security-token", token);
        }
        if let Some(value) = content_type {
            request = request.header("content-type", value);
        }
        if let Some(file) = body {
            request = request.body(reqwest::blocking::Body::new(file));
        }

        let response = request
            .send()
            .with_context(|| format!("failed to execute s3 request {}", target.url))?;
        expect_status(
            response,
            &[StatusCode::OK, StatusCode::CREATED, StatusCode::NO_CONTENT, StatusCode::NOT_FOUND],
            "write/delete s3 extracted object",
        )
    }

    fn build_target(
        &self,
        config: ValidatedS3Config<'_>,
        key: Option<&str>,
        query: &[(String, String)],
    ) -> Result<S3RequestTarget> {
        let endpoint = Url::parse(config.endpoint).context("failed to parse s3 endpoint")?;
        if endpoint.query().is_some() {
            return Err(anyhow!("s3 endpoint must not contain a query string"));
        }
        if endpoint.fragment().is_some() {
            return Err(anyhow!("s3 endpoint must not contain a fragment"));
        }
        let scheme = endpoint.scheme();
        let host = endpoint.host_str().ok_or_else(|| anyhow!("s3 endpoint must include a host"))?;
        let port = endpoint.port();
        let base_path = endpoint.path().trim_end_matches('/');
        let encoded_key = key.map(encode_uri_path).unwrap_or_default();

        let host_without_port =
            if self.path_style { host.to_owned() } else { format!("{}.{}", config.bucket, host) };
        let host_header = format_host_header(&host_without_port, port, scheme);
        let canonical_uri = if self.path_style {
            let mut path = format!("{base_path}/{}", encode_query_component(config.bucket));
            if !encoded_key.is_empty() {
                path.push('/');
                path.push_str(&encoded_key);
            }
            normalize_canonical_uri(&path)
        } else {
            let mut path = base_path.to_owned();
            if !encoded_key.is_empty() {
                path.push('/');
                path.push_str(&encoded_key);
            }
            normalize_canonical_uri(&path)
        };
        let canonical_query = canonicalize_query(query);
        let mut url = format!("{scheme}://{host_header}{canonical_uri}");
        if !canonical_query.is_empty() {
            url.push('?');
            url.push_str(&canonical_query);
        }

        Ok(S3RequestTarget { url, host_header, canonical_uri, canonical_query })
    }

    fn validate_config(&self) -> Result<ValidatedS3Config<'_>> {
        let endpoint = non_empty_string(self.endpoint.as_deref(), "s3 extract endpoint")?;
        let bucket = non_empty_string(self.bucket.as_deref(), "s3 extract bucket")?;
        let access_key_id =
            non_empty_string(self.access_key_id.as_deref(), "s3 extract access key id")?;
        let secret_access_key =
            non_empty_string(self.secret_access_key.as_deref(), "s3 extract secret access key")?;
        let region = non_empty_string(Some(self.region.as_str()), "s3 extract region")?;

        Ok(ValidatedS3Config {
            endpoint,
            bucket,
            access_key_id,
            secret_access_key,
            session_token: self.session_token.as_deref(),
            region,
        })
    }
}

fn copy_reader_to_temp<R, W, ShouldCancel>(
    reader: &mut R,
    writer: &mut W,
    options: FileWriteOptions<'_, ShouldCancel>,
) -> io::Result<(WriteScanOutcome, String)>
where
    R: Read + ?Sized,
    W: Write + ?Sized,
    ShouldCancel: FnMut() -> bool,
{
    let mut header = SmallVec::<[u8; 512]>::with_capacity(options.header_bytes.min(512));
    let mut bytes_scanned = 0_u64;
    let mut b3_hasher = blake3::Hasher::new();
    let mut sha256 = Sha256::new();
    let mut buffer = vec![0_u8; 128 * 1024].into_boxed_slice();
    let should_cancel = options.should_cancel;
    let mut limits = options.limits;
    if let Some(limits) = limits.as_deref_mut() {
        limits.begin_file();
    }

    loop {
        if should_cancel() {
            return Err(crate::cancel::scan_cancelled_io_error());
        }
        let bytes_read = reader.read(&mut buffer)?;
        if bytes_read == 0 {
            break;
        }
        let chunk = &buffer[..bytes_read];
        writer.write_all(chunk)?;
        bytes_scanned =
            bytes_scanned.saturating_add(u64::try_from(chunk.len()).unwrap_or(u64::MAX));
        b3_hasher.update(chunk);
        sha256.update(chunk);
        if let Some(limits) = limits.as_deref_mut() {
            limits
                .account(u64::try_from(chunk.len()).unwrap_or(u64::MAX))
                .map_err(crate::extract::limit_breach_io_error)?;
        }
        if header.len() < options.header_bytes {
            let required = (options.header_bytes - header.len()).min(chunk.len());
            header.extend_from_slice(&chunk[..required]);
        }
    }
    writer.flush()?;

    let head_b3 = options
        .emit_hashes
        .then(|| {
            (!header.is_empty())
                .then(|| blake3::hash(&header).to_hex().to_string().into_boxed_str())
        })
        .flatten();
    let b3 = b3_hasher.finalize().to_hex().to_string();
    let full_b3 = options.full_hash.then(|| b3.clone().into_boxed_str());
    let payload_sha256 = to_hex(&sha256.finalize());

    Ok((
        WriteScanOutcome { header, head_b3, full_b3, b3, bytes_scanned, truncated_scan: false },
        payload_sha256,
    ))
}

fn path_to_s3_key(path: &Path) -> String {
    path.components()
        .filter_map(|component| match component {
            Component::Normal(value) => Some(value.to_string_lossy()),
            _ => None,
        })
        .collect::<Vec<_>>()
        .join("/")
}

fn normalize_key_prefix(prefix: &str) -> String {
    prefix.trim_matches('/').to_owned()
}

fn normalize_canonical_uri(path: &str) -> String {
    if path.is_empty() {
        "/".to_owned()
    } else if path.starts_with('/') {
        path.to_owned()
    } else {
        format!("/{path}")
    }
}

fn non_empty_string<'a>(value: Option<&'a str>, description: &str) -> Result<&'a str> {
    value
        .map(str::trim)
        .filter(|value| !value.is_empty())
        .ok_or_else(|| anyhow!("{description} must be configured"))
}

fn format_host_header(host: &str, port: Option<u16>, scheme: &str) -> String {
    match (port, scheme) {
        (Some(80), "http") | (Some(443), "https") | (None, _) => host.to_owned(),
        (Some(port), _) => format!("{host}:{port}"),
    }
}

fn canonicalize_query(query: &[(String, String)]) -> String {
    let mut pairs = query
        .iter()
        .map(|(name, value)| (encode_query_component(name), encode_query_component(value)))
        .collect::<Vec<_>>();
    pairs.sort();
    pairs.into_iter().map(|(name, value)| format!("{name}={value}")).collect::<Vec<_>>().join("&")
}

fn encode_uri_path(value: &str) -> String {
    let mut encoded = String::with_capacity(value.len());
    for byte in value.bytes() {
        match byte {
            b'A'..=b'Z' | b'a'..=b'z' | b'0'..=b'9' | b'-' | b'_' | b'.' | b'~' | b'/' => {
                encoded.push(char::from(byte));
            }
            _ => {
                let _ = write!(&mut encoded, "%{byte:02X}");
            }
        }
    }
    encoded
}

fn encode_query_component(value: &str) -> String {
    let mut encoded = String::with_capacity(value.len());
    for byte in value.bytes() {
        match byte {
            b'A'..=b'Z' | b'a'..=b'z' | b'0'..=b'9' | b'-' | b'_' | b'.' | b'~' => {
                encoded.push(char::from(byte));
            }
            _ => {
                let _ = write!(&mut encoded, "%{byte:02X}");
            }
        }
    }
    encoded
}

fn expect_status(
    response: reqwest::blocking::Response,
    expected: &[StatusCode],
    action: &str,
) -> Result<()> {
    let status = response.status();
    if expected.contains(&status) {
        return Ok(());
    }
    let body = response.text().unwrap_or_else(|_| "<failed to read response body>".to_owned());
    Err(anyhow!("{action} returned {status}: {body}"))
}

fn sha256_hex(payload: &[u8]) -> String {
    to_hex(&Sha256::digest(payload))
}

fn signing_key(secret_key: &[u8], date_stamp: &str, region: &str) -> [u8; 32] {
    let date_key = hmac_sha256(
        format!("AWS4{}", String::from_utf8_lossy(secret_key)).as_bytes(),
        date_stamp.as_bytes(),
    );
    let region_key = hmac_sha256(&date_key, region.as_bytes());
    let service_key = hmac_sha256(&region_key, b"s3");
    hmac_sha256(&service_key, b"aws4_request")
}

fn hmac_sha256(key: &[u8], payload: &[u8]) -> [u8; 32] {
    let mut mac = HmacSha256::new_from_slice(key).expect("hmac accepts arbitrary key sizes");
    mac.update(payload);
    let bytes = mac.finalize().into_bytes();
    let mut output = [0_u8; 32];
    output.copy_from_slice(&bytes);
    output
}

fn to_hex(bytes: &[u8]) -> String {
    let mut value = String::with_capacity(bytes.len() * 2);
    for byte in bytes {
        let _ = write!(&mut value, "{byte:02x}");
    }
    value
}

#[allow(dead_code)]
fn parse_s3_list_page(body: &str) -> Result<Vec<String>> {
    let mut reader = Reader::from_str(body);
    reader.config_mut().trim_text(true);
    let mut buf = Vec::new();
    let mut current_tag = Vec::new();
    let mut inside_contents = false;
    let mut keys = Vec::new();

    loop {
        match reader
            .read_event_into(&mut buf)
            .context("failed to parse s3 list objects response")?
        {
            Event::Start(event) => {
                current_tag = event.name().as_ref().to_vec();
                if current_tag.as_slice() == b"Contents" {
                    inside_contents = true;
                }
            }
            Event::End(event) => {
                if event.name().as_ref() == b"Contents" {
                    inside_contents = false;
                }
                current_tag.clear();
            }
            Event::Text(event) if current_tag.as_slice() == b"Key" && inside_contents => {
                keys.push(
                    event
                        .decode()
                        .context("failed to decode s3 list objects XML text")?
                        .into_owned(),
                );
            }
            Event::Eof => break,
            _ => {}
        }
        buf.clear();
    }
    Ok(keys)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn path_to_s3_key_uses_forward_slashes() {
        assert_eq!(path_to_s3_key(Path::new("nested/file.txt")), "nested/file.txt");
    }

    #[test]
    fn canonical_query_is_sorted_and_encoded() {
        assert_eq!(
            canonicalize_query(&[
                ("prefix".to_owned(), "a b".to_owned()),
                ("list-type".to_owned(), "2".to_owned())
            ]),
            "list-type=2&prefix=a%20b"
        );
    }
}
