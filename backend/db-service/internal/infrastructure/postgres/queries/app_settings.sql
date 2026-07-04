-- name: ListAppSettings :many
SELECT key, value, updated_at
FROM app_settings
ORDER BY key;

-- name: UpsertAppSetting :exec
INSERT INTO app_settings (key, value, updated_at)
VALUES ($1, $2, now())
ON CONFLICT (key) DO UPDATE SET
  value      = excluded.value,
  updated_at = now();

-- name: DeleteAppSetting :exec
DELETE FROM app_settings WHERE key = $1;
