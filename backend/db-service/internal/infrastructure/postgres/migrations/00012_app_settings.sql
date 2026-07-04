-- +goose Up
-- Глобальные рантайм-настройки: ключ = имя env-переменной, значение переопределяет
-- её без редеплоя. Отсутствие строки = поведение по env/дефолту.
CREATE TABLE app_settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE app_settings;
