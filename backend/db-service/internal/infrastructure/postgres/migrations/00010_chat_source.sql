-- +goose Up
-- Страница-источник запроса («search»); для старых записей — пустая строка.
ALTER TABLE chats ADD COLUMN source TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE chats DROP COLUMN source;
