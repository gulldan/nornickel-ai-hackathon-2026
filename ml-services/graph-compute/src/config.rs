//! Service runtime configuration read from the environment.

use std::net::SocketAddr;

use anyhow::{Context, Result};

/// Listen addresses and Qdrant connection for the server binary.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ServiceConfig {
    pub grpc_addr: SocketAddr,
    pub http_addr: SocketAddr,
    pub qdrant_url: String,
    pub qdrant_collection: String,
}

impl ServiceConfig {
    /// Load configuration from the environment, applying documented defaults.
    pub fn from_env() -> Result<Self> {
        Ok(Self {
            grpc_addr: socket_addr(&["GRPC_ADDR"], "0.0.0.0:9093")?,
            http_addr: socket_addr(&["HTTP_ADDR", "METRICS_ADDR"], "0.0.0.0:8080")?,
            qdrant_url: env_or("QDRANT_URL", "http://qdrant:6333"),
            qdrant_collection: env_or("QDRANT_COLLECTION", "documents"),
        })
    }
}

fn env_or(key: &str, default: &str) -> String {
    std::env::var(key).unwrap_or_else(|_| default.to_owned())
}

/// First non-empty value among `keys`, else `default`, parsed as a socket
/// address. A leading-colon form (`:9093`) binds all interfaces.
fn socket_addr(keys: &[&str], default: &str) -> Result<SocketAddr> {
    let raw = keys
        .iter()
        .find_map(|key| std::env::var(key).ok().filter(|value| !value.is_empty()))
        .unwrap_or_else(|| default.to_owned());
    let normalized =
        if let Some(port) = raw.strip_prefix(':') { format!("0.0.0.0:{port}") } else { raw };
    normalized.parse().with_context(|| format!("invalid socket address: {normalized}"))
}
