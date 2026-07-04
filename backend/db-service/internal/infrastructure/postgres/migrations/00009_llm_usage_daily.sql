-- Durable per-day LLM/OpenRouter usage ledger, one row per (day, model,
-- operation). Writers increment hot day-hashes in Valkey; main-service mirrors
-- the running daily totals here (idempotent SET on conflict), so this table is
-- the authoritative history for the Metrics dashboard (budget, tokens, requests
-- per model/operation over time). Cost is kept in nano-USD (int, exact, like the
-- other counters) — 0 for :free models but ready for paid routing.

-- +goose Up
CREATE TABLE llm_usage_daily (
    day               date        NOT NULL,
    model             text        NOT NULL,
    operation         text        NOT NULL,
    requests          bigint      NOT NULL DEFAULT 0,
    prompt_tokens     bigint      NOT NULL DEFAULT 0,
    completion_tokens bigint      NOT NULL DEFAULT 0,
    cost_nano_usd     bigint      NOT NULL DEFAULT 0,
    updated_at        timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (day, model, operation)
);
CREATE INDEX llm_usage_daily_day_idx ON llm_usage_daily (day);

-- +goose Down
DROP TABLE llm_usage_daily;
