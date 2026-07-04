-- name: CreateChat :exec
INSERT INTO chats (id, owner_id, title, source, created_at) VALUES ($1, $2, $3, $4, $5);

-- name: GetChat :one
SELECT * FROM chats WHERE id = $1;

-- name: ListChatsByOwner :many
SELECT c.id, c.owner_id, c.title, c.source, c.created_at,
       coalesce(u.username, '') AS owner_username
FROM chats c
LEFT JOIN users u ON u.id = c.owner_id
WHERE c.owner_id = $1
ORDER BY c.created_at DESC
LIMIT $2 OFFSET $3;

-- name: CountChatsByOwner :one
SELECT count(*) FROM chats WHERE owner_id = $1;

-- name: ListChatsAll :many
SELECT c.id, c.owner_id, c.title, c.source, c.created_at,
       coalesce(u.username, '') AS owner_username
FROM chats c
LEFT JOIN users u ON u.id = c.owner_id
ORDER BY c.created_at DESC
LIMIT $1 OFFSET $2;

-- name: CountChats :one
SELECT count(*) FROM chats;

-- name: AddMessage :exec
INSERT INTO chat_messages (id, chat_id, role, content, sources, meta, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: ListMessagesByChat :many
SELECT * FROM chat_messages WHERE chat_id = $1 ORDER BY created_at ASC;
