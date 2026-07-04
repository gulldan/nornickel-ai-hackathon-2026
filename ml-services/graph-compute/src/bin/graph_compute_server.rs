//! `graph_compute_server` — serves the gRPC and HTTP transports concurrently
//! over one stateless [`ClusterService`], shutting down gracefully on a signal.

use std::sync::Arc;

use anyhow::Result;
use graph_compute::application::ClusterService;
use graph_compute::config::ServiceConfig;
use graph_compute::infrastructure::leiden::LeidenDetector;
use graph_compute::infrastructure::qdrant::QdrantVectorSource;
use graph_compute::interfaces::{grpc, http};
use tracing_subscriber::EnvFilter;

#[tokio::main]
async fn main() -> Result<()> {
    init_tracing();

    let config = ServiceConfig::from_env()?;
    tracing::info!(
        grpc_addr = %config.grpc_addr,
        http_addr = %config.http_addr,
        qdrant_url = %config.qdrant_url,
        qdrant_collection = %config.qdrant_collection,
        "starting graph_compute_server",
    );

    let source = QdrantVectorSource::new(config.qdrant_url.clone())?;
    let service = Arc::new(ClusterService::new(source, LeidenDetector));

    let grpc_server = grpc::serve(Arc::clone(&service), config.grpc_addr, shutdown_signal());
    let http_server = http::serve(service, config.http_addr, shutdown_signal());

    tokio::try_join!(grpc_server, http_server)?;
    Ok(())
}

fn init_tracing() {
    let filter = EnvFilter::try_from_env("GRAPH_COMPUTE_LOG")
        .or_else(|_| EnvFilter::try_from_default_env())
        .unwrap_or_else(|_| EnvFilter::new("info,graph_compute=info"));
    tracing_subscriber::fmt().with_env_filter(filter).json().flatten_event(true).init();
}

async fn shutdown_signal() {
    let ctrl_c = async {
        let _ = tokio::signal::ctrl_c().await;
    };

    #[cfg(unix)]
    let terminate = async {
        match tokio::signal::unix::signal(tokio::signal::unix::SignalKind::terminate()) {
            Ok(mut signal) => {
                signal.recv().await;
            }
            Err(_) => std::future::pending::<()>().await,
        }
    };

    #[cfg(not(unix))]
    let terminate = std::future::pending::<()>();

    tokio::select! {
        () = ctrl_c => {},
        () = terminate => {},
    }
}
