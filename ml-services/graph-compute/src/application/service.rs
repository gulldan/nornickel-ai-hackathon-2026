//! [`ClusterService`] wires a [`VectorSource`] to the pure clustering and
//! bridge-scoring pipelines.
//!
//! Mirrors the `recluster` flow in `worker.py`: scroll → owner-scope filter →
//! aggregate → build the kNN graph + community partition. That shared prefix is
//! factored into [`ClusterService::prepare`] so clustering ([`ClusterService::run`])
//! and discovery ([`ClusterService::score_bridges`]) operate on the identical
//! graph.

use std::future::Future;

use ahash::{AHashMap, AHashSet};

use crate::domain::bridge::{self, BridgeConfig, BridgeOutput};
use crate::domain::cluster::{
    build_communities, cluster_from_prepared, ClusterOutput, PreviousCluster,
};
use crate::domain::pagerank::{self, RankConfig, RankOutput};
use crate::domain::paths::{self, PathConfig, PathOutput};
use crate::domain::vector::{aggregate_docs, chunk_nodes, ChunkPoint, DocVector};
use crate::domain::{CommunityDetector, DomainConfig, Granularity, Graph};

/// Source of document chunk vectors (Qdrant in production, a mock in tests).
/// Futures are `Send` so the gRPC/HTTP transports can drive them.
pub trait VectorSource: Send + Sync {
    /// Fetch every chunk point for `owner_id` (empty = shared corpus) within
    /// `collection`. Implementations apply server-side owner scoping where they
    /// can; the service applies the document-list scoping on top.
    fn fetch(
        &self,
        owner_id: &str,
        collection: &str,
    ) -> impl Future<Output = anyhow::Result<Vec<ChunkPoint>>> + Send;
}

/// Owner-scoped document descriptor: id + filename for the representative map.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct DocumentRef {
    pub id: String,
    pub filename: String,
}

/// A fully-described, stateless clustering request.
#[derive(Clone, Debug, Default)]
pub struct RunInput {
    pub owner_id: String,
    pub collection: String,
    pub config: DomainConfig,
    pub documents: Vec<DocumentRef>,
    pub previous_clusters: Vec<PreviousCluster>,
}

/// A fully-described, stateless bridge-scoring request. `config` builds the same
/// kNN + Leiden graph as clustering; `bridge` holds the discovery-layer knobs.
#[derive(Clone, Debug, Default)]
pub struct BridgeInput {
    pub owner_id: String,
    pub collection: String,
    pub config: DomainConfig,
    pub bridge: BridgeConfig,
    pub documents: Vec<DocumentRef>,
}

/// A fully-described, stateless Personalized-PageRank request (HippoRAG-2).
///
/// `config` builds the same kNN graph; `seed_ids` personalize the teleport vector
/// toward the query's resolved nodes; `rank` holds damping/iterations/top-n.
/// Owner scoping is applied server-side by the [`VectorSource`] (no doc list).
#[derive(Clone, Debug, Default)]
pub struct RankInput {
    pub owner_id: String,
    pub collection: String,
    pub config: DomainConfig,
    pub seed_ids: Vec<String>,
    pub rank: RankConfig,
}

/// A fully-described, stateless connecting-path request (PathRAG).
///
/// `config` builds the same kNN graph; `source_ids`/`target_ids` are the chain
/// endpoints; `paths` holds the max-paths/max-hops knobs. Owner scoping is
/// applied server-side by the [`VectorSource`] (no doc list).
#[derive(Clone, Debug, Default)]
pub struct PathInput {
    pub owner_id: String,
    pub collection: String,
    pub config: DomainConfig,
    pub source_ids: Vec<String>,
    pub target_ids: Vec<String>,
    pub paths: PathConfig,
}

/// The shared prefix of every run: prepared document vectors, their kNN graph and
/// the community partition (node-index lists) over that graph.
struct Prepared {
    docs: Vec<DocVector>,
    graph: Graph,
    communities: Vec<Vec<usize>>,
}

/// Stateless service: holds only its collaborators, no board state.
#[derive(Clone, Debug)]
pub struct ClusterService<V, D> {
    source: V,
    detector: D,
}

impl<V: VectorSource, D: CommunityDetector> ClusterService<V, D> {
    /// Construct the service from a vector source and a community detector.
    pub const fn new(source: V, detector: D) -> Self {
        Self { source, detector }
    }

    /// Execute one clustering run end to end.
    pub async fn run(&self, input: RunInput) -> anyhow::Result<ClusterOutput> {
        let prepared = self
            .prepare(&input.owner_id, &input.collection, &input.config, &input.documents)
            .await?;
        let doc_filenames = filename_map(&input.documents);
        Ok(cluster_from_prepared(
            &prepared.docs,
            &doc_filenames,
            &prepared.graph,
            prepared.communities,
            &input.previous_clusters,
            &input.config,
        ))
    }

    /// Score cross-community bridges over the same graph the cluster run builds.
    pub async fn score_bridges(&self, input: BridgeInput) -> anyhow::Result<BridgeOutput> {
        let prepared = self
            .prepare(&input.owner_id, &input.collection, &input.config, &input.documents)
            .await?;
        let doc_filenames = filename_map(&input.documents);
        Ok(bridge::score_bridges(
            &prepared.docs,
            &doc_filenames,
            &prepared.graph,
            &prepared.communities,
            &input.bridge,
        ))
    }

    /// Rank the corpus by Personalized PageRank over the same graph the cluster
    /// run builds (HippoRAG-2). Seeds personalize the teleport vector.
    pub async fn rank(&self, input: RankInput) -> anyhow::Result<RankOutput> {
        let prepared = self.prepare(&input.owner_id, &input.collection, &input.config, &[]).await?;
        Ok(pagerank::rank_nodes(&prepared.docs, &prepared.graph, &input.seed_ids, &input.rank))
    }

    /// Find the top connecting paths between two node sets over the same graph
    /// (PathRAG).
    pub async fn paths(&self, input: PathInput) -> anyhow::Result<PathOutput> {
        let prepared = self.prepare(&input.owner_id, &input.collection, &input.config, &[]).await?;
        Ok(paths::connecting_paths(
            &prepared.docs,
            &prepared.graph,
            &input.source_ids,
            &input.target_ids,
            &input.paths,
        ))
    }

    /// Fetch, owner-scope, aggregate and build the kNN graph + community
    /// partition. The shared front half of [`Self::run`] and
    /// [`Self::score_bridges`].
    async fn prepare(
        &self,
        owner_id: &str,
        collection: &str,
        config: &DomainConfig,
        documents: &[DocumentRef],
    ) -> anyhow::Result<Prepared> {
        let mut points = self.source.fetch(owner_id, collection).await?;

        // Belt-and-suspenders owner scoping: when a document list is supplied,
        // keep only points whose document is actually owned (mirrors the Python
        // `points = [p for p in points if did in owned]`).
        if !documents.is_empty() {
            let owned: AHashSet<&str> = documents.iter().map(|doc| doc.id.as_str()).collect();
            points.retain(|point| owned.contains(point.document_id.as_ref()));
        }

        // Clustering altitude. Document granularity pools each document's chunks
        // into one node (the theme board); chunk granularity skips pooling and
        // makes each raw chunk its own node — the RAPTOR layer — so the returned
        // cluster members are chunk point ids.
        let docs = match config.granularity {
            Granularity::Document => aggregate_docs(&points, config),
            Granularity::Chunk => chunk_nodes(&points),
        };
        let (graph, communities) = build_communities(&docs, config, &self.detector);
        Ok(Prepared { docs, graph, communities })
    }
}

/// Document-id → filename map for backfilling representatives/mediators.
fn filename_map(documents: &[DocumentRef]) -> AHashMap<Box<str>, Box<str>> {
    documents.iter().map(|doc| (doc.id.as_str().into(), doc.filename.as_str().into())).collect()
}
