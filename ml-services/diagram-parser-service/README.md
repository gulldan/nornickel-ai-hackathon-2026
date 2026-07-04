# diagram-parser-service

HTTP adapter for local technological-scheme parsing. The service accepts the
same image-description request shape as `vlm-service`, extracts diagram blocks
and connectors with OpenCV, and returns indexable text for RAG ingestion.

## OCR Modes

The compose default stays compatible with the previous diagram-parser flow:

```env
DIAGRAM_OCR_MODE=full-image
```

Enable the new PP-OCRv5 label path through the separate GPU service:

```env
VLM_ENGINE_URL=http://diagram-parser-paddle5-service:8089
```

The `diagram-parser-paddle5-service` image uses `Dockerfile.paddle5` and runs
PP-OCRv5 locally:

- detector: `PP-OCRv5_server_det`;
- recognizer: `eslav_PP-OCRv5_mobile_rec`;
- runtime: GPU in Docker by default;
- Paddle backend: `paddlepaddle-gpu==3.2.1` from the CUDA 12.9 Paddle index;
- cache: `/root/.paddlex`.

This mode is used for short labels inside technological diagrams. It does not
call `DIAGRAM_OCR_URL` and does not depend on `paddleocr-vl-service`. The base
`diagram-parser-service` keeps the previous `full-image`/tiled flow and its
existing `paddleocr-vl-service` dependency.

## Why PP-OCRv5

PP-OCRv5 is the current practical choice for the hackathon diagrams because the
checked configuration recognizes Cyrillic labels, which are required for the
provided RU materials.

We do not use PP-OCRv6 here because the checked v6 recognizer path did not give
the required Cyrillic coverage for this case. We also do not use Surya as the
default because Surya 2 full OCR requires a separate vLLM backend and brings a
much heavier dependency/runtime path than this service needs.

CUDA 12.9 is intentional: the CUDA 12.6 Paddle wheel failed on RTX 5080
Blackwell with a mismatched `sm_120` architecture. CUDA 12.9 also keeps the path
compatible with RTX 4090-class Ada GPUs when the host driver is new enough.

## Useful Env

```env
DIAGRAM_PADDLE5_LANG=ru
DIAGRAM_PADDLE5_DEVICE=gpu
DIAGRAM_PADDLE5_DET_MODEL=PP-OCRv5_server_det
DIAGRAM_PADDLE5_REC_MODEL=eslav_PP-OCRv5_mobile_rec
DIAGRAM_PADDLE5_REC_BATCH_SIZE=8
PADDLE_PDX_DISABLE_MODEL_SOURCE_CHECK=True
```

Legacy modes `full-image`, `tiles4`, `tiles9`, `blocks`, `adaptive` and
`hybrid` are still available for comparison and experiments. Modes that call
`DIAGRAM_OCR_URL` require an external OCR service.

## Compose

Existing flow:

```bash
docker compose --profile diagram-parser up -d diagram-parser-service vlm-service
```

PP-OCRv5 GPU flow:

```bash
docker compose --profile diagram-parser-paddle5 up -d diagram-parser-paddle5-service vlm-service
```

## Local Checks

```bash
uv run --project ml-services/diagram-parser-service ruff check ml-services/diagram-parser-service
PYTHONPATH=ml-services/diagram-parser-service \
  uv run --project ml-services/diagram-parser-service ty check ml-services/diagram-parser-service/diagram_parser_service
PYTHONPATH=ml-services/diagram-parser-service \
  uv run --project ml-services/diagram-parser-service python -m unittest discover -s ml-services/diagram-parser-service/tests -v
```
