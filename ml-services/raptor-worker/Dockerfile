# RAPTOR worker. Deps (grpcio/protobuf/redis) install from prebuilt wheels —
# incl. cp314 manylinux wheels for grpcio — so no compiler/toolchain is needed
# and slim stays enough. Embeddings/Qdrant/VLLM are reached over plain urllib.
FROM python:3.14-slim AS deps

ENV VIRTUAL_ENV=/opt/venv \
    PATH="/opt/venv/bin:$PATH" \
    PIP_NO_CACHE_DIR=1

WORKDIR /app

COPY requirements.txt .
RUN python -m venv "$VIRTUAL_ENV" \
    && pip install --upgrade pip \
    && pip install -r requirements.txt

FROM python:3.14-slim AS runtime

ENV PYTHONUNBUFFERED=1 \
    VIRTUAL_ENV=/opt/venv \
    PATH="/opt/venv/bin:$PATH"

WORKDIR /app

COPY --from=deps /opt/venv /opt/venv
COPY worker.py .
COPY genproto ./genproto

CMD ["python", "-u", "worker.py"]
