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
    TORCH_DEVICE=auto \
    F2LLM_MODEL=codefuse-ai/F2LLM-v2-0.6B \
    F2LLM_MAX_TOKENS=512

WORKDIR /app

COPY --from=deps /opt/venv /opt/venv
COPY f2llm_server.py .

EXPOSE 8085
CMD ["uvicorn", "f2llm_server:app", "--host", "0.0.0.0", "--port", "8085"]
