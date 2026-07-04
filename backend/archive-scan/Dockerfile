# syntax=docker/dockerfile:1.7

FROM rust:1.96.0-slim-trixie AS builder

ENV PATH=/usr/local/cargo/bin:${PATH}

WORKDIR /app

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        build-essential \
        ca-certificates \
        libarchive-dev \
        libssl-dev \
        pkg-config \
    && rm -rf /var/lib/apt/lists/*

COPY Cargo.toml Cargo.lock rust-toolchain.toml ./
COPY benches/hot_path.rs ./benches/hot_path.rs
RUN --mount=type=cache,id=archive-scan-cargo-registry,target=/usr/local/cargo/registry \
    --mount=type=cache,id=archive-scan-cargo-git,target=/usr/local/cargo/git \
    cargo fetch --locked

COPY src ./src

RUN --mount=type=cache,id=archive-scan-cargo-registry,target=/usr/local/cargo/registry \
    --mount=type=cache,id=archive-scan-cargo-git,target=/usr/local/cargo/git \
    --mount=type=cache,id=archive-scan-target,target=/app/target \
    cargo build --release --features service --bins \
    && set -eux; \
    mkdir -p /runtime-rootfs/usr/local/bin; \
    cp target/release/archive_scan /runtime-rootfs/usr/local/bin/; \
    cp target/release/archive_scan_server /runtime-rootfs/usr/local/bin/; \
    for bin in /runtime-rootfs/usr/local/bin/archive_scan /runtime-rootfs/usr/local/bin/archive_scan_server; do \
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

EXPOSE 3000

ENTRYPOINT ["/usr/local/bin/archive_scan_server"]
