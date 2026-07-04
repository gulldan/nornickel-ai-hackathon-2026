//! tonic server implementing `rag.graph.v1.GraphCompute`. Translates the proto
//! request into a [`RunInput`], runs the service and maps the domain output back
//! to proto.

use std::net::SocketAddr;
use std::sync::Arc;

use tonic::transport::Server;
use tonic::{Request, Response, Status};

use crate::application::{
    BridgeInput, ClusterService, DocumentRef, PathInput, RankInput, RunInput, VectorSource,
};
use crate::domain::bridge as dombridge;
use crate::domain::cluster as dom;
use crate::domain::config::{Algorithm, Granularity};
use crate::domain::pagerank::{self, RankConfig, RankOverrides};
use crate::domain::paths::{self, PathConfig, PathOverrides};
use crate::domain::{CommunityDetector, ConfigOverrides, DomainConfig};
use crate::interfaces::metrics::{self, Rpc};
use crate::proto::graph_compute_server::{GraphCompute, GraphComputeServer};
use crate::proto::{
    Bridge, BridgeConfig, BridgeRequest, BridgeResponse, BridgeScores, Cluster, ClusterConfig,
    ClusterMetrics, ClusterRequest, ClusterResponse, ComputeStats, GraphPath, Lineage, Mediator,
    PathRequest, PathResponse, RankRequest, RankResponse, RankedNode, Representative,
};

/// gRPC adapter over a [`ClusterService`].
#[derive(Clone, Debug)]
pub struct GraphComputeHandler<V, D> {
    service: Arc<ClusterService<V, D>>,
}

impl<V, D> GraphComputeHandler<V, D> {
    /// Wrap a shared service for serving.
    pub const fn new(service: Arc<ClusterService<V, D>>) -> Self {
        Self { service }
    }
}

#[tonic::async_trait]
impl<V, D> GraphCompute for GraphComputeHandler<V, D>
where
    V: VectorSource + 'static,
    D: CommunityDetector + 'static,
{
    async fn cluster(
        &self,
        request: Request<ClusterRequest>,
    ) -> Result<Response<ClusterResponse>, Status> {
        let input = request_to_input(request.into_inner());
        let output = metrics::timed(Rpc::Cluster, self.service.run(input))
            .await
            .map_err(|err| Status::internal(format!("clustering failed: {err}")))?;
        Ok(Response::new(output_to_response(output)))
    }

    async fn score_bridges(
        &self,
        request: Request<BridgeRequest>,
    ) -> Result<Response<BridgeResponse>, Status> {
        let input = bridge_request_to_input(request.into_inner());
        let output = metrics::timed(Rpc::Bridges, self.service.score_bridges(input))
            .await
            .map_err(|err| Status::internal(format!("bridge scoring failed: {err}")))?;
        Ok(Response::new(bridge_output_to_response(output)))
    }

    async fn rank(&self, request: Request<RankRequest>) -> Result<Response<RankResponse>, Status> {
        let input = rank_request_to_input(request.into_inner());
        let output = metrics::timed(Rpc::Rank, self.service.rank(input))
            .await
            .map_err(|err| Status::internal(format!("ranking failed: {err}")))?;
        Ok(Response::new(rank_output_to_response(output)))
    }

    async fn paths(&self, request: Request<PathRequest>) -> Result<Response<PathResponse>, Status> {
        let input = path_request_to_input(request.into_inner());
        let output = metrics::timed(Rpc::Paths, self.service.paths(input))
            .await
            .map_err(|err| Status::internal(format!("path reasoning failed: {err}")))?;
        Ok(Response::new(path_output_to_response(output)))
    }
}

/// Serve the gRPC API until `shutdown` resolves.
pub async fn serve<V, D>(
    service: Arc<ClusterService<V, D>>,
    addr: SocketAddr,
    shutdown: impl std::future::Future<Output = ()> + Send + 'static,
) -> anyhow::Result<()>
where
    V: VectorSource + 'static,
    D: CommunityDetector + 'static,
{
    let handler = GraphComputeHandler::new(service);
    Server::builder()
        .add_service(GraphComputeServer::new(handler))
        .serve_with_shutdown(addr, shutdown)
        .await?;
    Ok(())
}

fn request_to_input(request: ClusterRequest) -> RunInput {
    let config = request
        .config
        .map_or_else(DomainConfig::default, |proto| DomainConfig::resolve(overrides(&proto)));
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
            .map(|prev| dom::PreviousCluster { id: prev.id, members: prev.members })
            .collect(),
    }
}

fn overrides(proto: &ClusterConfig) -> ConfigOverrides {
    ConfigOverrides {
        knn_k: (proto.knn_k != 0).then_some(proto.knn_k as usize),
        knn_block_size: (proto.knn_block_size != 0).then_some(proto.knn_block_size as usize),
        resolution: (proto.resolution != 0.0).then_some(proto.resolution),
        min_size: (proto.min_size != 0).then_some(proto.min_size as usize),
        sim_threshold: Some(proto.sim_threshold),
        mutual_knn: Some(proto.mutual_knn),
        chunk_weighting: Some(proto.chunk_weighting),
        chunk_lead_count: (proto.chunk_lead_count != 0).then_some(proto.chunk_lead_count as usize),
        chunk_w_lead: (proto.chunk_w_lead != 0.0).then_some(proto.chunk_w_lead),
        chunk_w_results: (proto.chunk_w_results != 0.0).then_some(proto.chunk_w_results),
        chunk_w_refs: (proto.chunk_w_refs != 0.0).then_some(proto.chunk_w_refs),
        lineage_overlap_min: (proto.lineage_overlap_min != 0.0)
            .then_some(proto.lineage_overlap_min),
        algorithm: (!proto.algorithm.is_empty()).then(|| Algorithm::parse(&proto.algorithm)),
        seed: proto.seed,
        max_iterations: proto.max_iterations.map(|value| value as usize),
        granularity: (!proto.granularity.is_empty())
            .then(|| Granularity::parse(&proto.granularity)),
    }
}

fn output_to_response(output: dom::ClusterOutput) -> ClusterResponse {
    ClusterResponse {
        clusters: output.clusters.into_iter().map(cluster_to_proto).collect(),
        stats: Some(ComputeStats {
            document_count: output.stats.document_count,
            edge_count: output.stats.edge_count,
            modularity: output.stats.modularity,
            compute_ms: output.stats.compute_ms,
        }),
    }
}

fn cluster_to_proto(cluster: dom::Cluster) -> Cluster {
    Cluster {
        members: cluster.members,
        fingerprint: cluster.fingerprint,
        signals: cluster.signals,
        chunk_count: cluster.chunk_count,
        metrics: Some(ClusterMetrics {
            size: cluster.metrics.size,
            avg_similarity: cluster.metrics.avg_similarity,
            modularity: cluster.metrics.modularity,
            modularity_contribution: cluster.metrics.modularity_contribution,
        }),
        representatives: cluster
            .representatives
            .into_iter()
            .map(|rep| Representative {
                document_id: rep.document_id,
                filename: rep.filename,
                snippet: rep.snippet,
            })
            .collect(),
        lineage: cluster.lineage.map(|lineage| Lineage {
            previous_cluster_id: lineage.previous_cluster_id,
            jaccard: lineage.jaccard,
            stability: lineage.stability,
            merged_from: lineage.merged_from,
            split_from: lineage.split_from,
        }),
    }
}

fn bridge_request_to_input(request: BridgeRequest) -> BridgeInput {
    let (config, bridge) = request.config.map_or_else(
        || (DomainConfig::default(), dombridge::BridgeConfig::default()),
        |proto| {
            let cluster = proto
                .cluster
                .as_ref()
                .map_or_else(DomainConfig::default, |cfg| DomainConfig::resolve(overrides(cfg)));
            (cluster, dombridge::BridgeConfig::resolve(bridge_overrides(&proto)))
        },
    );
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

fn bridge_overrides(proto: &BridgeConfig) -> dombridge::BridgeOverrides {
    dombridge::BridgeOverrides {
        top_n: (proto.top_n != 0).then_some(proto.top_n as usize),
        min_affinity: (proto.min_affinity != 0.0).then_some(proto.min_affinity),
        max_mediators: (proto.max_mediators != 0).then_some(proto.max_mediators as usize),
        min_convergence: (proto.min_convergence != 0).then_some(proto.min_convergence as usize),
        w_maverick: (proto.w_maverick != 0.0).then_some(proto.w_maverick),
        w_bridging: (proto.w_bridging != 0.0).then_some(proto.w_bridging),
        w_vanguard: (proto.w_vanguard != 0.0).then_some(proto.w_vanguard),
    }
}

fn bridge_output_to_response(output: dombridge::BridgeOutput) -> BridgeResponse {
    BridgeResponse {
        bridges: output.bridges.into_iter().map(bridge_to_proto).collect(),
        stats: Some(ComputeStats {
            document_count: output.stats.document_count,
            edge_count: output.stats.edge_count,
            modularity: output.stats.modularity,
            compute_ms: output.stats.compute_ms,
        }),
    }
}

fn bridge_to_proto(bridge: dombridge::Bridge) -> Bridge {
    Bridge {
        fingerprint: bridge.fingerprint,
        community_a: bridge.community_a,
        community_b: bridge.community_b,
        members_a: bridge.members_a,
        members_b: bridge.members_b,
        endpoint_a: bridge.endpoint_a,
        endpoint_b: bridge.endpoint_b,
        mediators: bridge
            .mediators
            .into_iter()
            .map(|mediator| Mediator {
                document_id: mediator.document_id,
                filename: mediator.filename,
                snippet: mediator.snippet,
            })
            .collect(),
        scores: Some(BridgeScores {
            affinity: bridge.scores.affinity,
            link_density: bridge.scores.link_density,
            maverick: bridge.scores.maverick,
            vanguard: bridge.scores.vanguard,
            bridging_centrality: bridge.scores.bridging_centrality,
            convergence: bridge.scores.convergence,
            composite: bridge.scores.composite,
        }),
    }
}

fn rank_request_to_input(request: RankRequest) -> RankInput {
    let config = request
        .config
        .map_or_else(DomainConfig::default, |proto| DomainConfig::resolve(overrides(&proto)));
    let collection =
        if request.collection.is_empty() { "documents".to_owned() } else { request.collection };
    let rank = RankConfig::resolve(RankOverrides {
        damping: (request.damping != 0.0).then_some(request.damping),
        max_iterations: (request.max_iterations != 0).then_some(request.max_iterations as usize),
        top_n: (request.top_n != 0).then_some(request.top_n as usize),
    });
    RankInput { owner_id: request.owner_id, collection, config, seed_ids: request.seed_ids, rank }
}

fn rank_output_to_response(output: pagerank::RankOutput) -> RankResponse {
    RankResponse {
        nodes: output
            .nodes
            .into_iter()
            .map(|node| RankedNode { id: node.id, score: node.score })
            .collect(),
        stats: Some(ComputeStats {
            document_count: output.stats.document_count,
            edge_count: output.stats.edge_count,
            modularity: output.stats.modularity,
            compute_ms: output.stats.compute_ms,
        }),
    }
}

fn path_request_to_input(request: PathRequest) -> PathInput {
    let config = request
        .config
        .map_or_else(DomainConfig::default, |proto| DomainConfig::resolve(overrides(&proto)));
    let collection =
        if request.collection.is_empty() { "documents".to_owned() } else { request.collection };
    let paths = PathConfig::resolve(PathOverrides {
        max_paths: (request.max_paths != 0).then_some(request.max_paths as usize),
        max_hops: (request.max_hops != 0).then_some(request.max_hops as usize),
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

fn path_output_to_response(output: paths::PathOutput) -> PathResponse {
    PathResponse {
        paths: output
            .paths
            .into_iter()
            .map(|path| GraphPath { node_ids: path.node_ids, score: path.score })
            .collect(),
        stats: Some(ComputeStats {
            document_count: output.stats.document_count,
            edge_count: output.stats.edge_count,
            modularity: output.stats.modularity,
            compute_ms: output.stats.compute_ms,
        }),
    }
}
