# f2llm-service

Сервис строит смысловые векторы текста.

Его вызывают:

1. `chunk-splitter` при индексации документов.
2. `llm-service` при поиске ответа.
3. `eval-service` при оценке качества.
4. `itc-worker` при расчете индекса технологичности.
5. `discovery-worker` и `raptor-worker` для своих проверок.

## Адрес

```text
http://f2llm-service:8085/v1/embeddings
```

Ответ совместим с OpenAI-форматом для векторов.

## Модель

По умолчанию используется:

```text
codefuse-ai/F2LLM-v2-0.6B
```

Основные параметры:

1. Размер вектора: `1024`.
2. Pooling: last token.
3. Нормализация: L2.

## Запуск

Обычно сервис запускает `make up`.

Отдельный запуск:

```bash
docker compose -f infra/docker-compose.yml up -d f2llm-service
```

При первом старте модель скачивается в `models/huggingface`.

## Настройки

1. `F2LLM_MODEL`: модель внутри сервиса.
2. `EMBEDDINGS_MODEL`: имя модели, которое передают клиенты.
3. `EMBEDDING_DIM`: размер вектора. Значение должно совпадать с моделью.
4. `F2LLM_MAX_TOKENS`: лимит токенов.
5. `F2LLM_TORCH_DEVICE`: устройство, обычно `cuda`.

Если поменять модель или размер вектора, старые векторы в Qdrant нужно
перестроить.
