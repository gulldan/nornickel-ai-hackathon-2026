use super::{
    config::SourceDownloadConfig,
    error::{ServiceError, ServiceResult},
    model::{ScanSourceKind, ScanSourceRef},
    scanning::{archive_descriptor_from_path, validate_scan_path},
};
use crate::{engine::ArchiveDescriptor, lower_ext};
use anyhow::{anyhow, Context, Result};
use reqwest::{blocking::Client, Url};
use std::{
    io::{Read, Write},
    net::{IpAddr, Ipv4Addr, Ipv6Addr, SocketAddr, ToSocketAddrs},
    path::{Path, PathBuf},
    time::SystemTime,
};
use tempfile::{Builder, NamedTempFile};
use tokio::net::lookup_host;

pub(super) struct PreparedArchive {
    descriptor: ArchiveDescriptor,
    scan_path: PathBuf,
    _temp_file: Option<NamedTempFile>,
}

impl PreparedArchive {
    pub(super) fn from_local_path(path: PathBuf) -> Result<Self> {
        Ok(Self {
            descriptor: archive_descriptor_from_path(&path)?,
            scan_path: path,
            _temp_file: None,
        })
    }

    fn from_downloaded_file(
        source_url: &Url,
        archive_name: &str,
        temp_file: NamedTempFile,
    ) -> Result<Self> {
        Ok(Self {
            descriptor: archive_descriptor_from_downloaded_file(
                source_url.as_str(),
                archive_name,
                temp_file.path(),
            )?,
            scan_path: temp_file.path().to_path_buf(),
            _temp_file: Some(temp_file),
        })
    }

    pub(super) fn descriptor(&self) -> &ArchiveDescriptor {
        &self.descriptor
    }

    pub(super) fn scan_path(&self) -> &Path {
        &self.scan_path
    }
}

pub(super) async fn validate_scan_source(
    source: ScanSourceRef<'_>,
    config: &SourceDownloadConfig,
) -> ServiceResult<()> {
    match source.kind {
        ScanSourceKind::LocalPath | ScanSourceKind::SharedFilesystemPath => {
            let path = Path::new(
                source.path().expect("filesystem-backed sources should always expose a path"),
            );
            validate_scan_path(source, path)
        }
        ScanSourceKind::ObjectStorageUrl => {
            let source_url = parse_object_storage_url(
                source.url().expect("object-storage sources should always expose a URL"),
            )
            .map_err(|reason| ServiceError::invalid_source_reference(source.kind, reason))?;
            validate_object_storage_target_async(&source_url, config)
                .await
                .map_err(|reason| ServiceError::invalid_source_reference(source.kind, reason))
        }
    }
}

pub(super) fn prepare_archive_for_scan(
    source: ScanSourceRef<'_>,
    config: &SourceDownloadConfig,
) -> Result<PreparedArchive> {
    match source.kind {
        ScanSourceKind::LocalPath | ScanSourceKind::SharedFilesystemPath => {
            let path = PathBuf::from(
                source.path().expect("filesystem-backed sources should always expose a path"),
            );
            PreparedArchive::from_local_path(path)
        }
        ScanSourceKind::ObjectStorageUrl => download_object_storage_archive(
            source.url().expect("object-storage sources should always expose a URL"),
            config,
        ),
    }
}

pub(super) fn validate_runtime_source_config(config: &SourceDownloadConfig) -> Result<()> {
    if let Some(path) = config.temp_dir.as_deref() {
        std::fs::create_dir_all(path).with_context(|| {
            format!("failed to create object source temp directory {}", path.display())
        })?;
    }
    Ok(())
}

fn download_object_storage_archive(
    source_url: &str,
    config: &SourceDownloadConfig,
) -> Result<PreparedArchive> {
    let source_url = parse_object_storage_url(source_url).map_err(anyhow::Error::msg)?;
    validate_object_storage_target_blocking(&source_url, config).map_err(anyhow::Error::msg)?;
    let archive_name = archive_name_from_url(&source_url);
    let suffix =
        archive_name.rsplit_once('.').map(|(_, ext)| format!(".{ext}")).unwrap_or_default();
    let client = Client::builder()
        .connect_timeout(config.connect_timeout)
        .timeout(config.request_timeout)
        .build()
        .context("failed to construct HTTP client for object storage source")?;
    let mut response = client
        .get(source_url.clone())
        .send()
        .with_context(|| format!("failed to fetch archive from {source_url}"))?
        .error_for_status()
        .with_context(|| {
            format!("object storage source returned non-success status for {source_url}")
        })?;
    let mut builder = Builder::new();
    builder.prefix("archive-scan-object-store-").suffix(&suffix);
    let mut temp_file = match config.temp_dir.as_deref() {
        Some(temp_dir) => {
            std::fs::create_dir_all(temp_dir).with_context(|| {
                format!("failed to create object source temp directory {}", temp_dir.display())
            })?;
            builder.tempfile_in(temp_dir).with_context(|| {
                format!(
                    "failed to allocate temporary file for downloaded archive in {}",
                    temp_dir.display()
                )
            })?
        }
        None => builder
            .tempfile()
            .context("failed to allocate temporary file for downloaded archive")?,
    };

    let mut bytes_copied = 0_u64;
    let mut buffer = vec![0_u8; 64 * 1024].into_boxed_slice();
    loop {
        let read = response
            .read(&mut buffer)
            .with_context(|| format!("failed to stream archive payload from {source_url}"))?;
        if read == 0 {
            break;
        }
        bytes_copied = bytes_copied.saturating_add(u64::try_from(read).unwrap_or(u64::MAX));
        if let Some(limit) = config.max_bytes {
            if bytes_copied > limit {
                return Err(anyhow!(
                    "object storage source exceeded max download size limit of {limit} bytes"
                ));
            }
        }
        temp_file
            .as_file_mut()
            .write_all(&buffer[..read])
            .with_context(|| format!("failed to stream archive payload from {source_url}"))?;
    }
    temp_file.as_file_mut().flush().context("failed to flush downloaded archive to disk")?;

    PreparedArchive::from_downloaded_file(&source_url, &archive_name, temp_file)
}

fn parse_object_storage_url(source_url: &str) -> std::result::Result<Url, String> {
    let parsed = Url::parse(source_url)
        .map_err(|err| format!("`source.url` must be an absolute http(s) URL: {err}"))?;

    match parsed.scheme() {
        "http" | "https" => Ok(parsed),
        scheme => Err(format!("`source.url` must use http or https, got `{scheme}`")),
    }
}

async fn validate_object_storage_target_async(
    source_url: &Url,
    config: &SourceDownloadConfig,
) -> std::result::Result<(), String> {
    if config.allow_private_networks {
        return Ok(());
    }

    let addrs = resolve_object_storage_addrs_async(source_url).await?;
    ensure_public_object_storage_target(source_url, &addrs)
}

fn validate_object_storage_target_blocking(
    source_url: &Url,
    config: &SourceDownloadConfig,
) -> std::result::Result<(), String> {
    if config.allow_private_networks {
        return Ok(());
    }

    let addrs = resolve_object_storage_addrs_blocking(source_url)?;
    ensure_public_object_storage_target(source_url, &addrs)
}

async fn resolve_object_storage_addrs_async(
    source_url: &Url,
) -> std::result::Result<Vec<SocketAddr>, String> {
    let (host, port) = host_and_port(source_url)?;
    let addrs = lookup_host((host, port))
        .await
        .map_err(|err| format!("failed to resolve `source.url` host `{host}`: {err}"))?
        .collect::<Vec<_>>();
    if addrs.is_empty() {
        return Err(format!("`source.url` host `{host}` did not resolve to any addresses"));
    }
    Ok(addrs)
}

fn resolve_object_storage_addrs_blocking(
    source_url: &Url,
) -> std::result::Result<Vec<SocketAddr>, String> {
    let (host, port) = host_and_port(source_url)?;
    let addrs = (host, port)
        .to_socket_addrs()
        .map_err(|err| format!("failed to resolve `source.url` host `{host}`: {err}"))?
        .collect::<Vec<_>>();
    if addrs.is_empty() {
        return Err(format!("`source.url` host `{host}` did not resolve to any addresses"));
    }
    Ok(addrs)
}

fn host_and_port(source_url: &Url) -> std::result::Result<(&str, u16), String> {
    let host =
        source_url.host_str().ok_or_else(|| "`source.url` must include a host".to_owned())?;
    let port = source_url
        .port_or_known_default()
        .ok_or_else(|| "`source.url` must include a valid port".to_owned())?;
    Ok((host, port))
}

fn ensure_public_object_storage_target(
    source_url: &Url,
    addrs: &[SocketAddr],
) -> std::result::Result<(), String> {
    if let Some(addr) = addrs.iter().find(|addr| is_disallowed_ip(addr.ip())) {
        return Err(format!(
            "`source.url` resolves to non-public address `{}`; set `ARCHIVE_SCAN_OBJECT_SOURCE_ALLOW_PRIVATE_NETWORKS=1` to allow private or loopback targets",
            addr.ip()
        ));
    }

    let _ = source_url;
    Ok(())
}

fn is_disallowed_ip(ip: IpAddr) -> bool {
    match ip {
        IpAddr::V4(addr) => is_disallowed_ipv4(addr),
        IpAddr::V6(addr) => is_disallowed_ipv6(addr),
    }
}

fn is_disallowed_ipv4(addr: Ipv4Addr) -> bool {
    let [first, second, _, _] = addr.octets();
    addr.is_private()
        || addr.is_loopback()
        || addr.is_link_local()
        || addr.is_broadcast()
        || addr.is_documentation()
        || addr.is_multicast()
        || addr.is_unspecified()
        || first == 0
        || (first == 100 && (64..=127).contains(&second))
        || (first == 198 && matches!(second, 18 | 19))
        || first >= 240
}

fn is_disallowed_ipv6(addr: Ipv6Addr) -> bool {
    if let Some(mapped) = addr.to_ipv4_mapped() {
        return is_disallowed_ipv4(mapped);
    }

    let [first, second, ..] = addr.segments();
    addr.is_loopback()
        || addr.is_unspecified()
        || addr.is_multicast()
        || addr.is_unique_local()
        || addr.is_unicast_link_local()
        || (first == 0x2001 && second == 0x0db8)
        || (first & 0xffc0) == 0xfec0
}

fn archive_name_from_url(source_url: &Url) -> String {
    source_url
        .path_segments()
        .and_then(|mut segments| segments.rfind(|segment| !segment.is_empty()).map(str::to_owned))
        .unwrap_or_else(|| "downloaded-archive".to_owned())
}

fn archive_descriptor_from_downloaded_file(
    display_path: &str,
    archive_name: &str,
    path: &Path,
) -> Result<ArchiveDescriptor> {
    let metadata = path.metadata().with_context(|| {
        format!("failed to read metadata for downloaded archive {}", path.display())
    })?;

    Ok(ArchiveDescriptor {
        archive_index: 0,
        archive_name: archive_name.to_owned().into_boxed_str(),
        archive_path: display_path.to_owned().into_boxed_str(),
        archive_ext: lower_ext(archive_name).into_owned().into_boxed_str(),
        archive_size: metadata.len(),
        archive_mtime_unix: metadata
            .modified()
            .unwrap_or(SystemTime::UNIX_EPOCH)
            .duration_since(SystemTime::UNIX_EPOCH)
            .map_or(0, |duration| i64::try_from(duration.as_secs()).unwrap_or(i64::MAX)),
    })
}
