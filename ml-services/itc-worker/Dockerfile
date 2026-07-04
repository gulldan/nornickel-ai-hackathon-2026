FROM python:3.14-slim AS deps

ENV PYTHONUNBUFFERED=1 \
    VIRTUAL_ENV=/opt/venv \
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
    PATH="/opt/venv/bin:$PATH" \
    ITC_INTERVAL_SEC=3600 \
    ITC_CHECK_INTERVAL_SEC=10 \
    ITC_STARTUP_DELAY_SEC=180 \
    ITC_RUN_ON_START=true

WORKDIR /app

COPY --from=deps /opt/venv /opt/venv
COPY itc.py worker.py itc_rubric.json ./

CMD ["python", "worker.py"]
