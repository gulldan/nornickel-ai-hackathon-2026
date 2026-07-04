# syntax=docker/dockerfile:1.7

FROM rust:1.95.0-slim-trixie AS builder

WORKDIR /src

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        build-essential \
        ca-certificates \
        pkg-config \
    && rm -rf /var/lib/apt/lists/*

COPY Cargo.toml Cargo.lock ./
COPY src ./src

RUN --mount=type=cache,id=workbook-parser-cargo-registry,target=/usr/local/cargo/registry \
    --mount=type=cache,id=workbook-parser-cargo-git,target=/usr/local/cargo/git \
    --mount=type=cache,id=workbook-parser-target,target=/src/target \
    cargo build --release \
    && set -eux; \
    mkdir -p /runtime-rootfs/usr/local/bin; \
    cp target/release/workbook_parser_service /runtime-rootfs/usr/local/bin/; \
    for bin in /runtime-rootfs/usr/local/bin/workbook_parser_service; do \
        ldd "$bin" | awk '/=> \// { print $3 } /^\// { print $1 }'; \
    done | sort -u | while read -r lib; do \
        test -n "$lib"; \
        dest_dir="$(readlink -f "$(dirname "$lib")")"; \
        dest_path="$dest_dir/$(basename "$lib")"; \
        mkdir -p "/runtime-rootfs$dest_dir"; \
        cp -Lv "$lib" "/runtime-rootfs$dest_path"; \
    done

FROM gcr.io/distroless/cc-debian13:nonroot

WORKDIR /work
COPY --from=builder /runtime-rootfs/ /

EXPOSE 8095
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/workbook_parser_service"]
