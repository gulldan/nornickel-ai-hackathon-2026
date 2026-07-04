# Workbook Parser Service

Small HTTP service for structured Excel/workbook extraction.

It uses Rust `calamine` as the primary parser, so `.xls` and modern workbook
formats can be parsed without LibreOffice in the normal path. The service returns
anchored Markdown for indexing plus a JSON sidecar with cell-level provenance.

## API

```bash
curl -F "file=@book.xlsx" http://localhost:8095/v1/parse
```

Response fields:

- `text`: Markdown table windows with `source_uri=...` anchors.
- `metadata`: key/value fields for `DocumentParsed.metadata`.
- `sidecars`: sidecar artifacts to persist next to parsed text.

## Runtime

Environment:

- `HTTP_ADDR`: listen address, default `0.0.0.0:8095`.
- `WORKBOOK_MAX_UPLOAD_MB`: multipart upload cap, default `128`.

## Checks

```bash
cargo test
cargo clippy --all-targets -- -D warnings
```

## Notes

This service does not write to S3, RabbitMQ or Postgres. `office-parser` owns
storage and ingestion wiring. LibreOffice is intentionally not in the MVP path;
fallback conversion can be added separately if corpus eval proves it is needed.
