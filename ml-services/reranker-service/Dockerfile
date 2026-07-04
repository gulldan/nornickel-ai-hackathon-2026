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
    TOKENIZERS_PARALLELISM=false \
    VIRTUAL_ENV=/opt/venv \
    PATH="/opt/venv/bin:$PATH" \
    TORCH_DEVICE=cuda \
    RERANKER_MODEL=Qwen/Qwen3-Reranker-0.6B \
    RERANKER_MAX_TOKENS=4096 \
    RERANKER_BATCH=8

WORKDIR /app

COPY --from=deps /opt/venv /opt/venv
COPY reranker_server.py .

EXPOSE 8086
CMD ["uvicorn", "reranker_server:app", "--host", "0.0.0.0", "--port", "8086"]
