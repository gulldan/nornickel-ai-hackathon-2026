#!/usr/bin/env python3
"""Periodic ITC worker.

Runs the deterministic ITC calculator on a timer and on an admin trigger. The
calculator writes cluster ITC into cluster params and hypothesis ITC through the
public API, so the UI sees fresh values without a manual script run.
"""

import json
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


INTERVAL = int(os.environ.get("ITC_INTERVAL_SEC", "3600"))
CHECK_INTERVAL = int(os.environ.get("ITC_CHECK_INTERVAL_SEC", "10"))
STARTUP_DELAY = int(os.environ.get("ITC_STARTUP_DELAY_SEC", "180"))
TRIGGER_KEY = os.environ.get("ITC_TRIGGER_KEY", "rag:itc:trigger")
STATUS_KEY = os.environ.get("ITC_STATUS_KEY", "rag:itc:last_status")
TIMEOUT = int(os.environ.get("ITC_TIMEOUT_SEC", "1800"))
WORKER_STATUS_KEY = "rag:worker:itc:status"

# Same helper in every worker (clusters/discovery/raptor/itc) — no shared lib yet.
_STATUS = {"r": None, "last_success_at": None, "run_started": None, "last_run_seconds": None}


def report_status(state, **fields):
    """Worker status snapshot in Valkey for GET /system/activity (best-effort)."""
    now = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
    if state == "running":
        if _STATUS["run_started"] is None:  # progress re-reports keep the original start
            _STATUS["run_started"] = time.monotonic()
    elif state == "idle":
        _STATUS["last_success_at"] = now
        if _STATUS["run_started"] is not None:
            _STATUS["last_run_seconds"] = round(time.monotonic() - _STATUS["run_started"], 3)
        _STATUS["run_started"] = None
    else:  # error: keep the previous duration, drop the aborted run's start
        _STATUS["run_started"] = None
    status = {
        "state": state,
        "epoch": None,
        "progress_done": None,
        "progress_total": None,
        "updated_at": now,
        "last_error": None,
        "last_success_at": _STATUS["last_success_at"],
        "last_run_seconds": _STATUS["last_run_seconds"],
    }
    status.update(fields)
    if _STATUS["r"] is None:
        return
    try:
        _STATUS["r"].set(WORKER_STATUS_KEY, json.dumps(status))
    except Exception as e:  # noqa: BLE001 — status is telemetry, never break the worker
        print(f"status report failed: {e}", file=sys.stderr, flush=True)


def run_once(rdb: redis.Redis, reason: str) -> None:
    started = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
    rdb.set(STATUS_KEY, f"running {started} reason={reason}")
    report_status("running")
    print(f"[itc-worker] starting ITC reason={reason}", flush=True)
    env = os.environ.copy()
    env.setdefault("RAG_GW", "http://nginx/api/v1")
    env.setdefault("EMB_URL", "http://f2llm-service:8085/v1/embeddings")
    try:
        cp = subprocess.run(
            [sys.executable, "/app/itc.py"],
            env=env,
            timeout=TIMEOUT,
            check=False,
        )
        finished = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
        rdb.set(STATUS_KEY, f"exit={cp.returncode} {finished} reason={reason}")
        print(f"[itc-worker] finished exit={cp.returncode}", flush=True)
        if cp.returncode == 0:
            report_status("idle")
        else:
            report_status("error", last_error=f"itc.py exit code {cp.returncode}")
    except Exception as exc:
        failed = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
        rdb.set(STATUS_KEY, f"error={type(exc).__name__} {failed} reason={reason}")
        print(f"[itc-worker] failed: {exc}", flush=True)
        report_status("error", last_error=str(exc))


def main() -> None:
    rdb = _redis()
    while True:
        try:
            rdb.ping()
            break
        except Exception as exc:
            print(f"[itc-worker] waiting for Valkey: {exc}", flush=True)
            time.sleep(3)
    _STATUS["r"] = rdb

    last_trigger = rdb.get(TRIGGER_KEY)
    next_due = time.monotonic() + STARTUP_DELAY
    if not _bool("ITC_RUN_ON_START", True):
        next_due = time.monotonic() + INTERVAL

    print(
        f"[itc-worker] up interval={INTERVAL}s trigger={TRIGGER_KEY} startup_delay={STARTUP_DELAY}s",
        flush=True,
    )
    while True:
        trigger = rdb.get(TRIGGER_KEY)
        now = time.monotonic()
        if trigger and trigger != last_trigger:
            last_trigger = trigger
            run_once(rdb, "admin-trigger")
            next_due = time.monotonic() + INTERVAL
        elif now >= next_due:
            run_once(rdb, "schedule")
            next_due = time.monotonic() + INTERVAL
        time.sleep(CHECK_INTERVAL)


if __name__ == "__main__":
    main()
