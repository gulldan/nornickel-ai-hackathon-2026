//! Compiles the gRPC contract only when the `service` feature is active so that
//! library/test builds (and offline `cargo build`) never require `protoc`.

fn main() -> Result<(), Box<dyn std::error::Error>> {
    if std::env::var_os("CARGO_FEATURE_SERVICE").is_none() {
        return Ok(());
    }

    println!("cargo:rerun-if-changed=../../backend/proto/graph/v1/graph.proto");
    tonic_prost_build::configure()
        .build_server(true)
        .build_client(false)
        .compile_protos(&["../../backend/proto/graph/v1/graph.proto"], &["../../backend/proto"])?;
    Ok(())
}
