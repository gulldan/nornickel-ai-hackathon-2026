-- name: CreateUser :exec
INSERT INTO users (id, username, password_hash, roles, created_at)
VALUES ($1, $2, $3, $4, $5);

-- name: GetUserByUsername :one
SELECT * FROM users WHERE username = $1;

-- name: ListUsers :many
SELECT * FROM users ORDER BY created_at DESC;

-- name: CountUsers :one
SELECT count(*) FROM users;
