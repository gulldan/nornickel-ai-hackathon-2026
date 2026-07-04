use super::{
    artifact_file_name, artifact_is_expired, duration_to_i64_secs, ensure_reference_kind,
    should_run_gc, unix_now, JobResultReference, JobResultReferenceKind, PersistedResult,
    ResultArtifactGcPolicy, ResultStoreGcMetrics, ResultStoreMaintenanceStats, ScanArchiveResponse,
};
use anyhow::{anyhow, Context, Result};
use hmac::{Hmac, KeyInit, Mac};
use quick_xml::{events::Event, Reader};
use reqwest::{Client, Method, StatusCode};
use sha2::{Digest, Sha256};
use std::{
    fmt::Write as _,
    future::Future,
    sync::{atomic::AtomicU64, Arc},
    time::Duration,
};
use time::{macros::format_description, OffsetDateTime};
use url::Url;

type HmacSha256 = Hmac<Sha256>;

#[derive(Clone, Debug)]
pub(in crate::service) struct S3ResultStore {
    endpoint: Option<String>,
    region: String,
    bucket: Option<String>,
    key_prefix: String,
    access_key_id: Option<String>,
    secret_access_key: Option<String>,
    session_token: Option<String>,
    path_style: bool,
    inline_max_bytes: usize,
    gc: ResultArtifactGcPolicy,
    last_gc_unix: Arc<AtomicU64>,
    gc_metrics: Arc<ResultStoreGcMetrics>,
    client: Client,
}

#[derive(Debug)]
struct S3RequestTarget {
    url: String,
    host_header: String,
    canonical_uri: String,
    canonical_query: String,
}

#[derive(Debug, Default)]
struct S3ListPage {
    keys: Vec<String>,
    next_token: Option<String>,
    is_truncated: bool,
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

impl S3ResultStore {
    pub(super) fn new(
        config: &super::super::config::ResultStoreConfig,
        gc: ResultArtifactGcPolicy,
    ) -> Self {
        Self {
            endpoint: config.s3_endpoint.clone(),
            region: config.s3_region.clone(),
            bucket: config.s3_bucket.clone(),
            key_prefix: normalize_key_prefix(&config.s3_key_prefix),
            access_key_id: config.s3_access_key_id.clone(),
            secret_access_key: config.s3_secret_access_key.clone(),
            session_token: config.s3_session_token.clone(),
            path_style: config.s3_path_style,
            inline_max_bytes: config.inline_max_bytes,
            gc,
            last_gc_unix: Arc::new(AtomicU64::new(0)),
            gc_metrics: Arc::new(ResultStoreGcMetrics::default()),
            client: Client::builder()
                .connect_timeout(Duration::from_secs(10))
                .timeout(Duration::from_secs(30))
                .build()
                .expect("s3 result store client should build"),
        }
    }

    pub(super) fn persist(
        &self,
        job_id: &str,
        result: &ScanArchiveResponse,
    ) -> Result<PersistedResult> {
        let payload = serde_json::to_vec(result).context("failed to serialize job result")?;
        if payload.len() <= self.inline_max_bytes {
            return Ok(PersistedResult { inline_result: Some(result.clone()), reference: None });
        }

        let created_at_unix = unix_now();
        let key = self.artifact_key(job_id, created_at_unix);
        self.put_bytes(&key, &payload, "application/json")?;

        Ok(PersistedResult {
            inline_result: None,
            reference: Some(JobResultReference {
                kind: JobResultReferenceKind::S3,
                location: key,
                size_bytes: payload.len(),
                created_at_unix,
            }),
        })
    }

    pub(super) fn load(
        &self,
        reference: &JobResultReference,
    ) -> Result<Option<ScanArchiveResponse>> {
        ensure_reference_kind(reference, JobResultReferenceKind::S3, "s3")?;
        let response = self.send_request(Method::GET, Some(&reference.location), &[], &[], None)?;
        if response.status() == StatusCode::NOT_FOUND {
            return Ok(None);
        }
        let response = expect_status(response, &[StatusCode::OK], "read s3 result payload")?;
        let bytes = run_reqwest_future(response.bytes())
            .context("failed to read s3 result payload body")?;
        let result = serde_json::from_slice(bytes.as_ref()).with_context(|| {
            format!("failed to deserialize s3 result payload for {}", reference.location)
        })?;
        Ok(Some(result))
    }

    pub(super) fn delete(&self, reference: &JobResultReference) -> Result<()> {
        ensure_reference_kind(reference, JobResultReferenceKind::S3, "s3")?;
        let response =
            self.send_request(Method::DELETE, Some(&reference.location), &[], &[], None)?;
        let _ = expect_status(
            response,
            &[StatusCode::NO_CONTENT, StatusCode::OK, StatusCode::NOT_FOUND],
            "delete s3 result payload",
        )?;
        Ok(())
    }

    pub(super) fn readiness_check(&self) -> Result<()> {
        self.validate_config()?;
        let key = self.healthcheck_key();
        self.put_bytes(&key, &[], "application/octet-stream")?;
        self.delete_key(&key)?;
        Ok(())
    }

    pub(super) fn maybe_run_gc(&self, now_unix: i64) -> Result<usize> {
        if !should_run_gc(&self.last_gc_unix, self.gc.interval, now_unix) {
            return Ok(0);
        }
        self.run_gc(now_unix)
    }

    pub(super) fn run_gc(&self, now_unix: i64) -> Result<usize> {
        match self.run_gc_inner(now_unix) {
            Ok(deleted) => {
                self.gc_metrics.record_run(deleted);
                Ok(deleted)
            }
            Err(err) => {
                self.gc_metrics.record_failure();
                Err(err)
            }
        }
    }

    pub(super) fn maintenance_stats(&self) -> ResultStoreMaintenanceStats {
        self.gc_metrics.snapshot()
    }

    fn run_gc_inner(&self, now_unix: i64) -> Result<usize> {
        let cutoff = now_unix.saturating_sub(duration_to_i64_secs(self.gc.retention));
        let mut continuation = None;
        let mut deleted = 0_usize;

        loop {
            let page = self.list_objects(continuation.as_deref())?;
            for key in &page.keys {
                if artifact_is_expired(Some(key.as_str()), cutoff) {
                    self.delete_key(key)?;
                    deleted = deleted.saturating_add(1);
                }
            }
            if !page.is_truncated {
                return Ok(deleted);
            }
            continuation = page.next_token;
        }
    }

    fn put_bytes(&self, key: &str, payload: &[u8], content_type: &str) -> Result<()> {
        let response =
            self.send_request(Method::PUT, Some(key), &[], payload, Some(content_type))?;
        let _ = expect_status(
            response,
            &[StatusCode::OK, StatusCode::CREATED, StatusCode::NO_CONTENT],
            "write s3 result payload",
        )?;
        Ok(())
    }

    fn delete_key(&self, key: &str) -> Result<()> {
        let response = self.send_request(Method::DELETE, Some(key), &[], &[], None)?;
        let _ = expect_status(
            response,
            &[StatusCode::NO_CONTENT, StatusCode::OK, StatusCode::NOT_FOUND],
            "delete s3 object",
        )?;
        Ok(())
    }

    fn list_objects(&self, continuation: Option<&str>) -> Result<S3ListPage> {
        let mut query = vec![
            ("list-type".to_owned(), "2".to_owned()),
            ("max-keys".to_owned(), "1000".to_owned()),
        ];
        if !self.key_prefix.is_empty() {
            query.push(("prefix".to_owned(), format!("{}/", self.key_prefix)));
        }
        if let Some(token) = continuation {
            query.push(("continuation-token".to_owned(), token.to_owned()));
        }

        let response = self.send_request(Method::GET, None, &query, &[], None)?;
        let response = expect_status(response, &[StatusCode::OK], "list s3 result artifacts")?;
        let body = run_reqwest_future(response.text())
            .context("failed to read s3 list objects response body")?;
        parse_s3_list_page(&body)
    }

    fn send_request(
        &self,
        method: Method,
        key: Option<&str>,
        query: &[(String, String)],
        payload: &[u8],
        content_type: Option<&str>,
    ) -> Result<reqwest::Response> {
        let config = self.validate_config()?;
        let target = self.build_target(config, key, query)?;
        let payload_hash = sha256_hex(payload);
        let timestamp = OffsetDateTime::now_utc();
        let amz_date = timestamp
            .format(&format_description!("[year][month][day]T[hour][minute][second]Z"))
            .context("failed to format s3 request timestamp")?;
        let date_stamp = timestamp
            .format(&format_description!("[year][month][day]"))
            .context("failed to format s3 request date stamp")?;

        let mut signed_headers = vec![
            ("host".to_owned(), target.host_header.clone()),
            ("x-amz-content-sha256".to_owned(), payload_hash.clone()),
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
            .header("authorization", authorization);
        if let Some(token) = config.session_token {
            request = request.header("x-amz-security-token", token);
        }
        if let Some(value) = content_type {
            request = request.header("content-type", value);
        }
        run_reqwest_future(request.body(payload.to_vec()).send())
            .with_context(|| format!("failed to execute s3 request {}", target.url))
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
        let endpoint = non_empty_string(self.endpoint.as_deref(), "s3 endpoint")?;
        let bucket = non_empty_string(self.bucket.as_deref(), "s3 bucket")?;
        let access_key_id = non_empty_string(self.access_key_id.as_deref(), "s3 access key id")?;
        let secret_access_key =
            non_empty_string(self.secret_access_key.as_deref(), "s3 secret access key")?;
        let region = non_empty_string(Some(self.region.as_str()), "s3 region")?;

        Ok(ValidatedS3Config {
            endpoint,
            bucket,
            access_key_id,
            secret_access_key,
            session_token: self.session_token.as_deref(),
            region,
        })
    }

    fn artifact_key(&self, job_id: &str, created_at_unix: i64) -> String {
        let file_name = artifact_file_name(job_id, created_at_unix);
        if self.key_prefix.is_empty() {
            file_name
        } else {
            format!("{}/{}", self.key_prefix, file_name)
        }
    }

    fn healthcheck_key(&self) -> String {
        let file_name = format!(".healthcheck-{}-{}", std::process::id(), unix_now());
        if self.key_prefix.is_empty() {
            file_name
        } else {
            format!("{}/{}", self.key_prefix, file_name)
        }
    }
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

fn parse_s3_list_page(body: &str) -> Result<S3ListPage> {
    let mut reader = Reader::from_str(body);
    reader.config_mut().trim_text(true);
    let mut buf = Vec::new();
    let mut current_tag = Vec::new();
    let mut inside_contents = false;
    let mut page = S3ListPage::default();

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
            Event::Text(event) => {
                let text = event
                    .decode()
                    .context("failed to decode s3 list objects XML text")?
                    .into_owned();
                match current_tag.as_slice() {
                    b"Key" if inside_contents => page.keys.push(text),
                    b"IsTruncated" => page.is_truncated = text == "true",
                    b"NextContinuationToken" => page.next_token = Some(text),
                    _ => {}
                }
            }
            Event::Eof => break,
            _ => {}
        }
        buf.clear();
    }

    Ok(page)
}

fn expect_status(
    response: reqwest::Response,
    expected: &[StatusCode],
    action: &str,
) -> Result<reqwest::Response> {
    let status = response.status();
    if expected.contains(&status) {
        return Ok(response);
    }
    let body = run_reqwest_future(response.text())
        .unwrap_or_else(|_| "<failed to read response body>".to_owned());
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

fn run_reqwest_future<T>(
    future: impl Future<Output = std::result::Result<T, reqwest::Error>> + Send + 'static,
) -> std::result::Result<T, reqwest::Error>
where
    T: Send + 'static,
{
    if tokio::runtime::Handle::try_current().is_ok() {
        std::thread::spawn(move || {
            tokio::runtime::Builder::new_current_thread()
                .enable_all()
                .build()
                .expect("temporary tokio runtime should build")
                .block_on(future)
        })
        .join()
        .expect("temporary reqwest worker thread should not panic")
    } else {
        tokio::runtime::Builder::new_current_thread()
            .enable_all()
            .build()
            .expect("temporary tokio runtime should build")
            .block_on(future)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::service::config::{ResultStoreBackendKind, ResultStoreConfig};

    #[test]
    fn provider_compatibility_targets_cover_common_s3_endpoints() {
        let cases = [
            (
                "minio",
                "http://127.0.0.1:9000",
                true,
                "prod/results",
                "127.0.0.1:9000",
                "http://127.0.0.1:9000/result-bucket/prod/results/result-job-00000001-123.json",
                "/result-bucket/prod/results/result-job-00000001-123.json",
            ),
            (
                "localstack",
                "http://localhost:4566",
                true,
                "prod/results",
                "localhost:4566",
                "http://localhost:4566/result-bucket/prod/results/result-job-00000001-123.json",
                "/result-bucket/prod/results/result-job-00000001-123.json",
            ),
            (
                "aws-s3",
                "https://s3.us-east-1.amazonaws.com",
                false,
                "prod/results",
                "result-bucket.s3.us-east-1.amazonaws.com",
                "https://result-bucket.s3.us-east-1.amazonaws.com/prod/results/result-job-00000001-123.json",
                "/prod/results/result-job-00000001-123.json",
            ),
            (
                "cloudflare-r2",
                "https://account123.r2.cloudflarestorage.com",
                false,
                "prod/results",
                "result-bucket.account123.r2.cloudflarestorage.com",
                "https://result-bucket.account123.r2.cloudflarestorage.com/prod/results/result-job-00000001-123.json",
                "/prod/results/result-job-00000001-123.json",
            ),
            (
                "digitalocean-spaces",
                "https://nyc3.digitaloceanspaces.com",
                false,
                "prod/results",
                "result-bucket.nyc3.digitaloceanspaces.com",
                "https://result-bucket.nyc3.digitaloceanspaces.com/prod/results/result-job-00000001-123.json",
                "/prod/results/result-job-00000001-123.json",
            ),
            (
                "ceph-rgw-behind-prefix",
                "https://s3.example.com/storage",
                true,
                "prod/results",
                "s3.example.com",
                "https://s3.example.com/storage/result-bucket/prod/results/result-job-00000001-123.json",
                "/storage/result-bucket/prod/results/result-job-00000001-123.json",
            ),
        ];

        for (
            provider,
            endpoint,
            path_style,
            key_prefix,
            expected_host_header,
            expected_url,
            expected_canonical_uri,
        ) in cases
        {
            let store = provider_store(endpoint, path_style, key_prefix);
            let config = store.validate_config().expect("config should validate");
            let key = store.artifact_key("job-00000001", 123);
            let target = store
                .build_target(config, Some(&key), &[])
                .unwrap_or_else(|err| panic!("provider {provider} should build target: {err}"));

            assert_eq!(target.host_header, expected_host_header, "{provider}");
            assert_eq!(target.url, expected_url, "{provider}");
            assert_eq!(target.canonical_uri, expected_canonical_uri, "{provider}");
            assert!(
                target.canonical_query.is_empty(),
                "{provider} should not synthesize a query string"
            );
        }
    }

    #[test]
    fn provider_compatibility_normalizes_key_prefix_and_query_order() {
        let store = provider_store("https://s3.us-east-1.amazonaws.com", false, "/team/results/");
        let config = store.validate_config().expect("config should validate");
        let key = store.artifact_key("job-00000077", 456);
        let target = store
            .build_target(
                config,
                Some(&key),
                &[
                    ("prefix".to_owned(), "team/results/".to_owned()),
                    ("list-type".to_owned(), "2".to_owned()),
                    ("continuation-token".to_owned(), "next token".to_owned()),
                ],
            )
            .expect("target should build");

        assert_eq!(
            target.url,
            "https://result-bucket.s3.us-east-1.amazonaws.com/team/results/result-job-00000077-456.json?continuation-token=next%20token&list-type=2&prefix=team%2Fresults%2F"
        );
        assert_eq!(
            target.canonical_query,
            "continuation-token=next%20token&list-type=2&prefix=team%2Fresults%2F"
        );
    }

    fn provider_store(endpoint: &str, path_style: bool, key_prefix: &str) -> S3ResultStore {
        S3ResultStore::new(
            &ResultStoreConfig {
                backend: ResultStoreBackendKind::S3,
                s3_endpoint: Some(endpoint.to_owned()),
                s3_region: "us-east-1".to_owned(),
                s3_bucket: Some("result-bucket".to_owned()),
                s3_key_prefix: key_prefix.to_owned(),
                s3_access_key_id: Some("access".to_owned()),
                s3_secret_access_key: Some("secret".to_owned()),
                s3_session_token: Some("session-token".to_owned()),
                s3_path_style: path_style,
                ..ResultStoreConfig::default()
            },
            ResultArtifactGcPolicy {
                retention: Duration::from_secs(3600),
                interval: Duration::from_secs(300),
            },
        )
    }
}
