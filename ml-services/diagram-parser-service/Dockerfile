FROM ghcr.io/astral-sh/uv:0.10.8-python3.13-trixie-slim

ENV PYTHONUNBUFFERED=1 \
    VIRTUAL_ENV=/opt/venv \
    PATH="/opt/venv/bin:$PATH"

WORKDIR /app

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        libgl1 libglib2.0-0 libgomp1 \
    && rm -rf /var/lib/apt/lists/*

COPY ml-services/diagram-parser-service/pyproject.toml /app/diagram-parser-service/pyproject.toml
COPY ml-services/diagram-parser-service/diagram_parser_service /app/diagram-parser-service/diagram_parser_service

RUN python -m venv "$VIRTUAL_ENV" \
    && uv pip install --python "$VIRTUAL_ENV/bin/python" \
        -e /app/diagram-parser-service

EXPOSE 8089
CMD ["uvicorn", "diagram_parser_service.server:app", "--host", "0.0.0.0", "--port", "8089"]
