//! Orchestration over the [`VectorSource`] and
//! [`crate::domain::CommunityDetector`] traits. Keeps the domain pure: this
//! layer awaits I/O, then runs the synchronous clustering pipeline.

pub mod service;

pub use service::{
    BridgeInput, ClusterService, DocumentRef, PathInput, RankInput, RunInput, VectorSource,
};
