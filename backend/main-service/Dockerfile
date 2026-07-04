# Self-contained build for this microservice. Build context is this folder, so
# the repository can be split out as-is.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,id=main-service-go-mod,target=/go/pkg/mod go mod download
ARG SERVICE_NAME=main-service
RUN printf '%s\n' "$SERVICE_NAME" > /tmp/service-name
COPY cmd ./cmd
COPY internal ./internal
RUN --mount=type=cache,id=main-service-go-mod,target=/go/pkg/mod --mount=type=cache,id=main-service-go-build,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/app ./cmd/...

FROM gcr.io/distroless/static-debian13:nonroot
WORKDIR /
COPY --from=build /out/app /app
USER nonroot:nonroot
ENTRYPOINT ["/app"]
