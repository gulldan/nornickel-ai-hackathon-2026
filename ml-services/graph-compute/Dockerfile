# syntax=docker/dockerfile:1.7
#
# Mirrors archive-scan's multi-stage distroless/cc build.
#
# Build CONTEXT = the rag-test repo root, so the crate AND the central proto/
# contract (compiled by build.rs at ../../backend/proto) are both visible:
#
#   docker compose -f infra/docker-compose.yml build graph-compute
#   # or, from rag-test/:  docker build -f ml-services/graph-compute/Dockerfile -t graph-compute .
#
# The rustworkx-core fork is a PINNED git dependency in Cargo.toml; cargo fetches
# it over https at build time, so the fork is NOT needed in the build context.
# The repo-root .dockerignore trims the context to backend/proto/ + ml-services/graph-compute/.

FROM rust:1.96.0-slim-trixie AS builder

ENV PATH=/usr/local/cargo/bin:${PATH}

WORKDIR /src/ml-services/graph-compute

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        build-essential \
        ca-certificates \
        cmake \
        protobuf-compiler \
        pkg-config \
        git \
    && rm -rf /var/lib/apt/lists/*

# Central proto contract (build.rs compiles ../proto) and the service crate.
COPY backend/proto /src/backend/proto
COPY ml-services/graph-compute /src/ml-services/graph-compute

RUN --mount=type=cache,id=graph-compute-cargo-registry,target=/usr/local/cargo/registry \
    --mount=type=cache,id=graph-compute-cargo-git,target=/usr/local/cargo/git \
    --mount=type=cache,id=graph-compute-target,target=/src/graph-compute/target \
    cargo build --release --features service --bins \
    && set -eux; \
    mkdir -p /runtime-rootfs/usr/local/bin; \
    cp target/release/graph_compute_server /runtime-rootfs/usr/local/bin/; \
    for bin in /runtime-rootfs/usr/local/bin/graph_compute_server; do \
        ldd "$bin" | awk '/=> \// { print $3 } /^\// { print $1 }'; \
    done | sort -u | while read -r lib; do \
        test -n "$lib"; \
        dest_dir="$(readlink -f "$(dirname "$lib")")"; \
        dest_path="$dest_dir/$(basename "$lib")"; \
        mkdir -p "/runtime-rootfs$dest_dir"; \
        cp -Lv "$lib" "/runtime-rootfs$dest_path"; \
    done

FROM gcr.io/distroless/cc-debian13:latest

WORKDIR /work

COPY --from=builder /runtime-rootfs/ /

# gRPC (primary, called by cluster-worker / future Go discovery-worker) + HTTP (debug/health).
EXPOSE 9093 8080

ENTRYPOINT ["/usr/local/bin/graph_compute_server"]
