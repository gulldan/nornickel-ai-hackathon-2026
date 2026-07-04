//! Concrete adapters for the domain/application traits.

pub mod leiden;

#[cfg(feature = "service")]
pub mod qdrant;
