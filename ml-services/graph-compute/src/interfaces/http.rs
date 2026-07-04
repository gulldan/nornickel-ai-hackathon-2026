//! axum HTTP mirror of the gRPC contract.
//!
//! Routes: `POST /v1/cluster`, `POST /v1/bridges` (JSON in/out), `GET /healthz`,
//! `GET /metrics`. The JSON uses the proto field names (snake_case). Unlike
//! proto3, omitted JSON fields are distinguishable from zero, so per-field
//! defaults apply (e.g. an omitted `mutual_knn` ⇒ `true`).

use std::net::SocketAddr;
use std::sync::Arc;

use axum::extract::State;
use axum::http::{header, StatusCode};
use axum::response::{IntoResponse, Response};
use axum::routing::{get, post};
use axum::{Json, Router};
use serde::Deserialize;

use crate::application::{
    BridgeInput, ClusterService, DocumentRef, PathInput, RankInput, RunInput, VectorSource,
};
use crate::domain::bridge::{BridgeConfig, BridgeOutput, BridgeOverrides};
use crate::domain::cluster::{ClusterOutput, PreviousCluster};
use crate::domain::config::{Algorithm, Granularity};
use crate::domain::pagerank::{RankConfig, RankOutput, RankOverrides};
use crate::domain::paths::{PathConfig, PathOutput, PathOverrides};
use crate::domain::{CommunityDetector, ConfigOverrides, DomainConfig};
use crate::interfaces::metrics::{self, Rpc};

#[derive(Debug, Default, Deserialize)]
struct ConfigDto {
    knn_k: Option<u32>,
    knn_block_size: Option<u32>,
    resolution: Option<f64>,
    min_size: Option<u32>,
    sim_threshold: Option<f64>,
    mutual_knn: Option<bool>,
    chunk_weighting: Option<bool>,
    chunk_lead_count: Option<u32>,
    chunk_w_lead: Option<f64>,
    chunk_w_results: Option<f64>,
    chunk_w_refs: Option<f64>,
    lineage_overlap_min: Option<f64>,
    algorithm: Option<String>,
    seed: Option<u64>,
    max_iterations: Option<u32>,
    granularity: Option<String>,
}

#[derive(Debug, Default, Deserialize)]
struct DocumentRefDto {
    #[serde(default)]
    id: String,
    #[serde(default)]
    filename: String,
}

#[derive(Debug, Default, Deserialize)]
struct PreviousClusterDto {
    #[serde(default)]
    id: String,
    #[serde(default)]
    members: Vec<String>,
}

#[derive(Debug, Default, Deserialize)]
struct ClusterHttpRequest {
    #[serde(default)]
    owner_id: String,
    #[serde(default)]
    collection: String,
    config: Option<ConfigDto>,
    #[serde(default)]
    documents: Vec<DocumentRefDto>,
    #[serde(default)]
    previous_clusters: Vec<PreviousClusterDto>,
}

#[derive(Debug, Default, Deserialize)]
struct BridgeConfigDto {
    cluster: Option<ConfigDto>,
    top_n: Option<u32>,
    min_affinity: Option<f64>,
    max_mediators: Option<u32>,
    min_convergence: Option<u32>,
    w_maverick: Option<f64>,
    w_bridging: Option<f64>,
    w_vanguard: Option<f64>,
}

#[derive(Debug, Default, Deserialize)]
struct BridgeHttpRequest {
    #[serde(default)]
    owner_id: String,
    #[serde(default)]
    collection: String,
    config: Option<BridgeConfigDto>,
    #[serde(default)]
    documents: Vec<DocumentRefDto>,
}

#[derive(Debug, Default, Deserialize)]
struct RankHttpRequest {
    #[serde(default)]
    owner_id: String,
    #[serde(default)]
    collection: String,
    config: Option<ConfigDto>,
    #[serde(default)]
    seed_ids: Vec<String>,
    top_n: Option<u32>,
    damping: Option<f64>,
    max_iterations: Option<u32>,
}

#[derive(Debug, Default, Deserialize)]
struct PathHttpRequest {
    #[serde(default)]
    owner_id: String,
    #[serde(default)]
    collection: String,
    config: Option<ConfigDto>,
    #[serde(default)]
    source_ids: Vec<String>,
    #[serde(default)]
    target_ids: Vec<String>,
    max_paths: Option<u32>,
    max_hops: Option<u32>,
}

/// Serve the HTTP API until `shutdown` resolves.
pub async fn serve<V, D>(
    service: Arc<ClusterService<V, D>>,
    addr: SocketAddr,
    shutdown: impl std::future::Future<Output = ()> + Send + 'static,
) -> anyhow::Result<()>
where
    V: VectorSource + 'static,
    D: CommunityDetector + 'static,
{
    let app = Router::new()
        .route("/v1/cluster", post(cluster_handler::<V, D>))
        .route("/v1/bridges", post(bridge_handler::<V, D>))
        .route("/v1/rank", post(rank_handler::<V, D>))
        .route("/v1/paths", post(paths_handler::<V, D>))
        .route("/healthz", get(|| async { "ok" }))
        .route(
            "/metrics",
            get(|| async {
                (
                    [(header::CONTENT_TYPE, "text/plain; version=0.0.4")],
                    metrics::registry().render(),
                )
            }),
        )
        .with_state(service);

    let listener = tokio::net::TcpListener::bind(addr).await?;
    axum::serve(listener, app).with_graceful_shutdown(shutdown).await?;
    Ok(())
}

async fn cluster_handler<V, D>(
    State(service): State<Arc<ClusterService<V, D>>>,
    Json(request): Json<ClusterHttpRequest>,
) -> Result<Json<ClusterOutput>, ApiError>
where
    V: VectorSource + 'static,
    D: CommunityDetector + 'static,
{
    let output = metrics::timed(Rpc::Cluster, service.run(request_to_input(request)))
        .await
        .map_err(ApiError)?;
    Ok(Json(output))
}

async fn bridge_handler<V, D>(
    State(service): State<Arc<ClusterService<V, D>>>,
    Json(request): Json<BridgeHttpRequest>,
) -> Result<Json<BridgeOutput>, ApiError>
where
    V: VectorSource + 'static,
    D: CommunityDetector + 'static,
{
    let output =
        metrics::timed(Rpc::Bridges, service.score_bridges(request_to_bridge_input(request)))
            .await
            .map_err(ApiError)?;
    Ok(Json(output))
}

async fn rank_handler<V, D>(
    State(service): State<Arc<ClusterService<V, D>>>,
    Json(request): Json<RankHttpRequest>,
) -> Result<Json<RankOutput>, ApiError>
where
    V: VectorSource + 'static,
    D: CommunityDetector + 'static,
{
    let output = metrics::timed(Rpc::Rank, service.rank(request_to_rank_input(request)))
        .await
        .map_err(ApiError)?;
    Ok(Json(output))
}

async fn paths_handler<V, D>(
    State(service): State<Arc<ClusterService<V, D>>>,
    Json(request): Json<PathHttpRequest>,
) -> Result<Json<PathOutput>, ApiError>
where
    V: VectorSource + 'static,
    D: CommunityDetector + 'static,
{
    let output = metrics::timed(Rpc::Paths, service.paths(request_to_path_input(request)))
        .await
        .map_err(ApiError)?;
    Ok(Json(output))
}

fn request_to_input(request: ClusterHttpRequest) -> RunInput {
    let config = request
        .config
        .map_or_else(DomainConfig::default, |dto| DomainConfig::resolve(overrides(dto)));
    let collection =
        if request.collection.is_empty() { "documents".to_owned() } else { request.collection };
    RunInput {
        owner_id: request.owner_id,
        collection,
        config,
        documents: request
            .documents
            .into_iter()
            .map(|doc| DocumentRef { id: doc.id, filename: doc.filename })
            .collect(),
        previous_clusters: request
            .previous_clusters
            .into_iter()
            .map(|prev| PreviousCluster { id: prev.id, members: prev.members })
            .collect(),
    }
}

fn overrides(dto: ConfigDto) -> ConfigOverrides {
    ConfigOverrides {
        knn_k: dto.knn_k.map(|value| value as usize),
        knn_block_size: dto.knn_block_size.map(|value| value as usize),
        resolution: dto.resolution,
        min_size: dto.min_size.map(|value| value as usize),
        sim_threshold: dto.sim_threshold,
        mutual_knn: dto.mutual_knn,
        chunk_weighting: dto.chunk_weighting,
        chunk_lead_count: dto.chunk_lead_count.map(|value| value as usize),
        chunk_w_lead: dto.chunk_w_lead,
        chunk_w_results: dto.chunk_w_results,
        chunk_w_refs: dto.chunk_w_refs,
        lineage_overlap_min: dto.lineage_overlap_min,
        algorithm: dto.algorithm.as_deref().filter(|s| !s.is_empty()).map(Algorithm::parse),
        seed: dto.seed,
        max_iterations: dto.max_iterations.map(|value| value as usize),
        granularity: dto.granularity.as_deref().filter(|s| !s.is_empty()).map(Granularity::parse),
    }
}

fn request_to_bridge_input(request: BridgeHttpRequest) -> BridgeInput {
    let (config, bridge) = request
        .config
        .map_or_else(|| (DomainConfig::default(), BridgeConfig::default()), bridge_config);
    let collection =
        if request.collection.is_empty() { "documents".to_owned() } else { request.collection };
    BridgeInput {
        owner_id: request.owner_id,
        collection,
        config,
        bridge,
        documents: request
            .documents
            .into_iter()
            .map(|doc| DocumentRef { id: doc.id, filename: doc.filename })
            .collect(),
    }
}

/// Split a bridge DTO into the cluster-graph config and the bridge knobs.
fn bridge_config(dto: BridgeConfigDto) -> (DomainConfig, BridgeConfig) {
    let cluster =
        dto.cluster.map_or_else(DomainConfig::default, |cfg| DomainConfig::resolve(overrides(cfg)));
    let bridge = BridgeConfig::resolve(BridgeOverrides {
        top_n: dto.top_n.map(|value| value as usize),
        min_affinity: dto.min_affinity,
        max_mediators: dto.max_mediators.map(|value| value as usize),
        min_convergence: dto.min_convergence.map(|value| value as usize),
        w_maverick: dto.w_maverick,
        w_bridging: dto.w_bridging,
        w_vanguard: dto.w_vanguard,
    });
    (cluster, bridge)
}

fn request_to_rank_input(request: RankHttpRequest) -> RankInput {
    let config = request
        .config
        .map_or_else(DomainConfig::default, |dto| DomainConfig::resolve(overrides(dto)));
    let collection =
        if request.collection.is_empty() { "documents".to_owned() } else { request.collection };
    let rank = RankConfig::resolve(RankOverrides {
        damping: request.damping,
        max_iterations: request.max_iterations.map(|value| value as usize),
        top_n: request.top_n.map(|value| value as usize),
    });
    RankInput { owner_id: request.owner_id, collection, config, seed_ids: request.seed_ids, rank }
}

fn request_to_path_input(request: PathHttpRequest) -> PathInput {
    let config = request
        .config
        .map_or_else(DomainConfig::default, |dto| DomainConfig::resolve(overrides(dto)));
    let collection =
        if request.collection.is_empty() { "documents".to_owned() } else { request.collection };
    let paths = PathConfig::resolve(PathOverrides {
        max_paths: request.max_paths.map(|value| value as usize),
        max_hops: request.max_hops.map(|value| value as usize),
    });
    PathInput {
        owner_id: request.owner_id,
        collection,
        config,
        source_ids: request.source_ids,
        target_ids: request.target_ids,
        paths,
    }
}

/// Maps an internal error to a `500` with the error text.
struct ApiError(anyhow::Error);

impl IntoResponse for ApiError {
    fn into_response(self) -> Response {
        (StatusCode::INTERNAL_SERVER_ERROR, format!("graph-compute request failed: {}", self.0))
            .into_response()
    }
}
