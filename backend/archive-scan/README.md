# archive-scan

`archive-scan` это Rust-сервис и CLI для архивов.

В основном контуре он распаковывает архивы для `archive-worker`. Извлеченные
файлы пишутся в S3-совместимое хранилище, затем `archive-worker` регистрирует их
как документы.

## Режимы

1. CLI сканирует архивы в локальной папке.
2. CLI распаковывает архивы в локальную папку.
3. HTTP-сервис принимает архив и извлекает файлы.
4. В Docker Compose сервис пишет извлеченные файлы в SeaweedFS.
5. Для Kubernetes есть режим с S3 и Postgres.

## Сборка

CLI:

```bash
cargo build --release --bin archive_scan
```

CLI и HTTP-сервис:

```bash
cargo build --release --features service --bins
```

Docker:

```bash
docker build -t archive-scan:local .
docker run --rm -p 3000:3000 archive-scan:local
```

## Тесты

```bash
cargo test --all-features
cargo clippy --workspace --all-targets --all-features --no-deps -- -D warnings
```

Интеграционные тесты используют Docker, если он доступен.

## Сканирование через CLI

```bash
./target/release/archive_scan /data/archives --fast-only
```

Сохранить метаданные:

```bash
./target/release/archive_scan /data/archives \
  --fast-only \
  --out-ndjson rows.ndjson \
  --out-parquet rows.parquet
```

## Распаковка через CLI

```bash
./target/release/archive_scan extract /data/archives \
  --out-dir /data/extracted \
  --fast-only \
  --metadata-ndjson extracted.ndjson
```

`--full-hash` нужен, если требуется полный BLAKE3-хэш содержимого.

## HTTP-сервис

Локальный запуск с файловым хранилищем:

```bash
ARCHIVE_SCAN_SERVICE_ADDR=127.0.0.1:3000 \
ARCHIVE_SCAN_EXTRACT_STORE_BACKEND=filesystem \
ARCHIVE_SCAN_EXTRACT_STORE_DIR=/tmp/archive-scan/extracted \
ARCHIVE_SCAN_EXTRACT_METADATA_BACKEND=filesystem \
ARCHIVE_SCAN_EXTRACT_METADATA_DIR=/tmp/archive-scan/metadata \
./target/release/archive_scan_server
```

Загрузить архив:

```bash
curl -sS http://127.0.0.1:3000/v1/extract/upload \
  -F archive=@sample.zip \
  -F fast_only=true \
  -F include_entries=false | jq .
```

## Полезные адреса

1. `GET /healthz`.
2. `GET /readyz`.
3. `GET /openapi.json`.
4. `GET /docs`.
5. `GET /metrics`.
6. `POST /v1/extract/upload`.
7. `POST /v1/scan/source`.
8. `POST /v1/jobs`.
9. `GET /v1/jobs/{job_id}`.
10. `GET /v1/jobs/{job_id}/result`.

## Настройки Docker Compose

В основном контуре используются:

1. `ARCHIVE_SCAN_SERVICE_ADDR=0.0.0.0:3000`.
2. `ARCHIVE_SCAN_FAST_ONLY=1`.
3. `ARCHIVE_SCAN_EXTRACT_STORE_BACKEND=s3`.
4. `ARCHIVE_SCAN_EXTRACT_STORE_S3_ENDPOINT=http://localstack:4566`.
5. `ARCHIVE_SCAN_EXTRACT_STORE_S3_BUCKET=documents`.
6. `ARCHIVE_SCAN_EXTRACT_STORE_S3_KEY_PREFIX=extracted`.
7. `ARCHIVE_SCAN_OBJECT_SOURCE_MAX_BYTES`: максимум размера архива.

`archive-worker` обращается к сервису по `ARCHIVE_SCAN_URL=http://archive-scan:3000`.

## Надежность

Сервис не должен хранить важное состояние только в памяти контейнера.

Для устойчивого режима используйте:

1. S3 для извлеченных файлов.
2. Postgres для метаданных извлечения.
3. Postgres или Redis для состояния асинхронных задач.
4. Readiness и liveness probes в Kubernetes.
