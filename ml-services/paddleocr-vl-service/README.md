# paddleocr-vl-service

Сервис распознает текст и структуру на сканах и изображениях.

В текущем контуре это единственный OCR-движок.

## Роль

`ocr-service` вызывает этот адрес:

```text
http://paddleocr-vl-service:8088
```

Переменные в compose:

```text
OCR_ENGINE_URL=http://paddleocr-vl-service:8088
OCR_MODEL=PaddleOCR-VL
```

## Что извлекается

1. Текст.
2. Порядок чтения.
3. Разделы.
4. Таблицы.
5. Формулы.

Результат возвращается как Markdown. Это помогает последующему разбиению на
фрагменты.

## Контракт

Запрос:

```json
{"model":"PaddleOCR-VL","image_b64":"...","mime":"application/pdf"}
```

Ответ:

```json
{"text":"...","pages":["page 1 markdown", "page 2 markdown"]}
```

PDF обрабатывается постранично. Текст страниц объединяется в порядке чтения.

## Запуск

Обычно сервис запускает `make up`.

Отдельный запуск:

```bash
docker compose -f infra/docker-compose.yml up -d paddleocr-vl-service
```

При первом старте веса скачиваются в `models/paddlex`.

## Настройки

1. `PADDLEOCR_VL_DEVICE`: устройство. В compose по умолчанию `gpu`.
2. `OCR_MODEL`: имя модели для `ocr-service`.
3. `OCR_ENGINE_URL`: адрес OCR-движка.

Качество на конкретном железе нужно проверить при первом запуске.
