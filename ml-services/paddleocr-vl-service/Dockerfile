# Python 3.12 (not 3.14): paddlepaddle ships wheels for 3.9-3.12. The x86_64
# GPU wheel comes from the PaddlePaddle CUDA 12.6 index; arm64 DGX Spark uses
# the CPU wheel because paddlepaddle-gpu is not published for Linux aarch64.
FROM python:3.12-slim AS deps

ARG TARGETARCH

ENV PYTHONUNBUFFERED=1 \
    VIRTUAL_ENV=/opt/venv \
    PATH="/opt/venv/bin:$PATH" \
    PIP_NO_CACHE_DIR=1

WORKDIR /app

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        build-essential \
        libgl1 libglib2.0-0 libgomp1 \
    && rm -rf /var/lib/apt/lists/*

COPY requirements.txt .
RUN python -m venv "$VIRTUAL_ENV" \
    && pip install --upgrade pip \
    && arch="${TARGETARCH:-$(uname -m)}" \
    && case "$arch" in \
        arm64|aarch64) \
          pip install paddlepaddle==3.2.1 \
            -i https://www.paddlepaddle.org.cn/packages/stable/cpu/ ;; \
        *) \
          pip install paddlepaddle-gpu==3.2.1 \
            -i https://www.paddlepaddle.org.cn/packages/stable/cu126/ ;; \
      esac \
    && pip install -r requirements.txt

FROM python:3.12-slim AS runtime

ENV PYTHONUNBUFFERED=1 \
    VIRTUAL_ENV=/opt/venv \
    PATH="/opt/venv/bin:$PATH" \
    PADDLEOCR_VL_DEVICE=gpu

WORKDIR /app

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        libgl1 libglib2.0-0 libgomp1 \
    && rm -rf /var/lib/apt/lists/*

COPY --from=deps /opt/venv /opt/venv
COPY paddleocr_vl_server.py .

EXPOSE 8088
CMD ["uvicorn", "paddleocr_vl_server:app", "--host", "0.0.0.0", "--port", "8088"]
