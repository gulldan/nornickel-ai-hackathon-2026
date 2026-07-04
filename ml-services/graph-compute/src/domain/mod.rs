//! Pure clustering domain: vectors, kNN, graph, metrics, lineage and pipeline.
//!
//! Nothing here performs I/O or depends on proto/tonic; every function is
//! synchronous and deterministic for a fixed seed, so the same request always
//! yields the same board.

pub mod bridge;
pub mod cluster;
pub mod config;
pub mod fingerprint;
pub mod graph;
pub mod knn;
pub mod lineage;
pub mod metrics;
pub mod pagerank;
pub mod paths;
pub mod vector;

pub use bridge::{
    score_bridges, Bridge, BridgeConfig, BridgeOutput, BridgeOverrides, BridgeScores, Mediator,
};
pub use cluster::{
    cluster_documents, Cluster, ClusterMetrics, ClusterOutput, CommunityDetector, ComputeStats,
    Lineage, Representative,
};
pub use config::{Algorithm, ConfigOverrides, DomainConfig, Granularity, EDGE_EPS};
pub use graph::Graph;
pub use pagerank::{rank_nodes, RankConfig, RankOutput, RankOverrides, RankedNode};
pub use paths::{connecting_paths, GraphPath, PathConfig, PathOutput, PathOverrides};
pub use vector::{ChunkPoint, DocVector};
