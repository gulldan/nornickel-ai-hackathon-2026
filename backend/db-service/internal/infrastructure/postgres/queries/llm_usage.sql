-- name: SetLLMUsageDaily :exec
-- Mirror the running daily total for one (day, model, operation) from the Valkey
-- hot-hash into the durable ledger. SET (not add) on conflict so re-flushing the
-- same day is idempotent — Valkey holds the authoritative running total.
INSERT INTO llm_usage_daily
  (day, model, operation, requests, prompt_tokens, completion_tokens, cost_nano_usd, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, now())
ON CONFLICT (day, model, operation) DO UPDATE SET
  requests          = excluded.requests,
  prompt_tokens     = excluded.prompt_tokens,
  completion_tokens = excluded.completion_tokens,
  cost_nano_usd     = excluded.cost_nano_usd,
  updated_at        = now();

-- name: ListLLMUsageDaily :many
-- The durable ledger for a date range, oldest first, for the Metrics dashboard.
SELECT day, model, operation, requests, prompt_tokens, completion_tokens, cost_nano_usd
FROM llm_usage_daily
WHERE day >= sqlc.arg('from_day') AND day <= sqlc.arg('to_day')
ORDER BY day, model, operation;
