#!/usr/bin/env python3
"""Periodic RAG evaluation worker.

Runs evaluate.py on a timer and when an admin requests a run through
POST /admin/metrics/run. The worker is deliberately outside main-service because
the scorecard is long-running, model-dependent work.
"""

import os
import subprocess
import sys
import time

import redis


def _bool(name: str, default: bool) -> bool:
    v = os.environ.get(name)
    if v is None:
        return default
    return v.strip().lower() not in ("", "0", "false", "no")


def _redis() -> redis.Redis:
    host, _, port = os.environ.get("VALKEY_ADDR", "valkey:6379").partition(":")
    return redis.Redis(
        host=host or "valkey",
        port=int(port or 6379),
        password=os.environ.get("VALKEY_PASSWORD") or None,
        decode_responses=True,
    )


INTERVAL = int(os.environ.get("EVAL_INTERVAL_SEC", "3600"))
CHECK_INTERVAL = int(os.environ.get("EVAL_CHECK_INTERVAL_SEC", "10"))
STARTUP_DELAY = int(os.environ.get("EVAL_STARTUP_DELAY_SEC", "120"))
TRIGGER_KEY = os.environ.get("EVAL_TRIGGER_KEY", "rag:eval:trigger")
STATUS_KEY = os.environ.get("EVAL_STATUS_KEY", "rag:eval:last_status")
TIMEOUT = int(os.environ.get("EVAL_TIMEOUT_SEC", "1800"))


def run_once(rdb: redis.Redis, reason: str) -> None:
    started = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
    rdb.set(STATUS_KEY, f"running {started} reason={reason}")
    print(f"[eval-service] starting evaluation reason={reason}", flush=True)
    env = os.environ.copy()
    env.setdefault("RAG_GATEWAY", "http://nginx/api/v1")
    env.setdefault("EVAL_EMBED_URL", "http://f2llm-service:8085/v1/embeddings")
    env.setdefault("EVAL_PROM_URL", "http://prometheus:9090")
    try:
        cp = subprocess.run(
            [sys.executable, "/app/evaluate.py"],
            env=env,
            timeout=TIMEOUT,
            check=False,
        )
        finished = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
        rdb.set(STATUS_KEY, f"exit={cp.returncode} {finished} reason={reason}")
        print(f"[eval-service] finished exit={cp.returncode}", flush=True)
    except Exception as exc:
        failed = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
        rdb.set(STATUS_KEY, f"error={type(exc).__name__} {failed} reason={reason}")
        print(f"[eval-service] failed: {exc}", flush=True)


def main() -> None:
    rdb = _redis()
    while True:
        try:
            rdb.ping()
            break
        except Exception as exc:
            print(f"[eval-service] waiting for Valkey: {exc}", flush=True)
            time.sleep(3)

    last_trigger = rdb.get(TRIGGER_KEY)
    # INTERVAL <= 0 disables scheduled evaluation. The service still stays up for
    # explicit admin-triggered runs and does not spend LLM quota on a timer.
    scheduled = INTERVAL > 0
    if not scheduled:
        next_due = float("inf")
    elif _bool("EVAL_RUN_ON_START", True):
        next_due = time.monotonic() + STARTUP_DELAY
    else:
        next_due = time.monotonic() + INTERVAL

    print(
        f"[eval-service] up interval={INTERVAL}s trigger={TRIGGER_KEY} "
        f"scheduled={scheduled} startup_delay={STARTUP_DELAY}s",
        flush=True,
    )
    while True:
        trigger = rdb.get(TRIGGER_KEY)
        now = time.monotonic()
        if trigger and trigger != last_trigger:
            last_trigger = trigger
            run_once(rdb, "admin-trigger")
            next_due = time.monotonic() + INTERVAL if scheduled else float("inf")
        elif now >= next_due:
            run_once(rdb, "schedule")
            next_due = time.monotonic() + INTERVAL
        time.sleep(CHECK_INTERVAL)


if __name__ == "__main__":
    main()
