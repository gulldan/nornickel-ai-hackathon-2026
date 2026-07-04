//! `graph-compute` — a stateless graph/clustering engine.
//!
//! The crate reads document chunk vectors from Qdrant, pools them into one
//! direction per document, builds a kNN cosine graph, runs Leiden (or Louvain)
//! community detection and returns clusters with quality metrics, representative
//! snippets and lineage versus a previous board. It holds no durable state —
//! every run is fully described by its request.
//!
//! Layout follows a light DDD split:
//! - [`domain`] — pure, synchronous algorithms (no I/O, no proto).
//! - [`application`] — orchestration ([`application::ClusterService`]) over the
//!   [`application::VectorSource`] and [`domain::CommunityDetector`] traits.
//! - [`infrastructure`] — concrete adapters (Qdrant source, rustworkx detector).
//! - [`interfaces`] — gRPC + HTTP transports (only with the `service` feature).
//!
//! The clustering logic is a faithful Rust port of the Python `cluster-worker`
//! so the emitted `clusters` payload stays compatible with the existing board.

pub mod application;
pub mod config;
pub mod domain;
pub mod infrastructure;

#[cfg(feature = "service")]
pub mod interfaces;

// Generated gRPC contract. Machine-generated code is exempt from the crate lint
// gates (it is not hand-written and we do not edit the fork/codegen output).
#[cfg(feature = "service")]
#[allow(
    clippy::all,
    clippy::pedantic,
    clippy::nursery,
    clippy::restriction,
    unreachable_pub,
    unused_qualifications,
    unused_lifetimes,
    rust_2018_idioms
)]
#[rustfmt::skip]
pub mod proto {
    //! Prost/tonic types generated from `proto/graph/v1/graph.proto`.
    tonic::include_proto!("rag.graph.v1");
}
