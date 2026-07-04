# syntax=docker/dockerfile:1.7

FROM oven/bun:1.3.14-alpine AS build

WORKDIR /app

COPY package.json bun.lock ./
# Keep the frontend build on Bun. In a clean Docker network, Bun's resolver can
# be flaky with the default registry/concurrency, and an occasional corrupted
# tarball ("Integrity check failed") would abort the whole `docker compose up
# --build` — leaving the stack on stale images. Pin the registry, reduce
# parallelism, and retry with a purged cache so a transient bad download
# self-heals instead of failing the deploy. Fail hard only after 3 attempts.
RUN for attempt in 1 2 3; do \
      bun install --frozen-lockfile \
        --registry https://registry.npmjs.org \
        --network-concurrency 4 && exit 0; \
      echo "bun install attempt $attempt failed; purging cache and retrying" >&2; \
      bun pm cache rm 2>/dev/null || true; \
      rm -rf "$HOME/.bun/install/cache" node_modules 2>/dev/null || true; \
      sleep 2; \
    done; \
    echo "bun install failed after 3 attempts" >&2; exit 1

COPY . .

# Empty public gateway means browser API/WebSocket traffic stays same-origin;
# the runtime nginx proxies /api to the backend gateway container.
ARG RAG_API=http://nginx
ARG RAG_PUBLIC_API=
ENV RAG_API=$RAG_API \
    RAG_PUBLIC_API=$RAG_PUBLIC_API
RUN bun run build

FROM nginx:1.31-alpine AS runtime

ENV API_UPSTREAM=http://nginx

COPY nginx.conf.template /etc/nginx/templates/default.conf.template
COPY --from=build /app/dist /usr/share/nginx/html

EXPOSE 80
