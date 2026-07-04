-- Метаданные документа из парсеров (author/published_at/source_ref); пустая
-- строка — значение не извлечено. Колонки добавляются в конец таблицы через
-- ALTER, чтобы sqlc разместил их последними в сгенерированной строке.

-- +goose Up
ALTER TABLE documents ADD COLUMN IF NOT EXISTS author TEXT NOT NULL DEFAULT '';
ALTER TABLE documents ADD COLUMN IF NOT EXISTS published_at TEXT NOT NULL DEFAULT '';
ALTER TABLE documents ADD COLUMN IF NOT EXISTS source_ref TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE documents DROP COLUMN IF EXISTS author;
ALTER TABLE documents DROP COLUMN IF EXISTS published_at;
ALTER TABLE documents DROP COLUMN IF EXISTS source_ref;
