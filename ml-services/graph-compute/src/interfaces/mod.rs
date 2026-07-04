//! Transport adapters: gRPC ([`grpc`]) and a JSON HTTP mirror ([`http`]).
//!
//! Both translate their wire types into [`crate::application::RunInput`] and
//! drive the same [`crate::application::ClusterService`], recording per-RPC
//! counters and wall time in the shared [`metrics`] registry.

pub mod grpc;
pub mod http;
pub mod metrics;
