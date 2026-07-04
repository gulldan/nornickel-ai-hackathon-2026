-- Класс документа: '' — обычный, 'hypotheses' — готовые гипотезы/идеи (итоги
-- мозгового штурма); такие документы исключаются из retrieval генерации и
-- проверки гипотез. Бэкофилл — эвристика по имени/заголовку ('%ипотез%'
-- покрывает Гипотез/гипотез без зависимости от локали); переиндексация
-- (admin requeue) перезаписывает значение честной классификацией.

-- +goose Up
ALTER TABLE documents ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT '';
UPDATE documents SET kind = 'hypotheses'
WHERE filename LIKE '%ипотез%' OR title LIKE '%ипотез%';

-- +goose Down
ALTER TABLE documents DROP COLUMN IF EXISTS kind;
