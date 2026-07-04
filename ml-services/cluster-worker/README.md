# cluster-worker

`cluster-worker` обновляет доску тем после изменения корпуса.

Пользователь загружает документы. После индексации воркер строит темы без
ручного запуска скриптов.

## Как работает

1. `main-service` обновляет `rag:corpus_epoch:shared` в Valkey после индексации.
2. `cluster-worker` ждет, пока загрузки закончатся.
3. Воркер вызывает `graph-compute` через `Cluster`.
4. `graph-compute` читает векторы из Qdrant и строит темы.
5. Воркер подписывает темы через LLM или через ключевые слова.
6. Воркер публикует темы через `POST /clusters/replace`.
7. Воркер запускает пересчет индекса технологичности.
8. При включенном `AUTO_HYPOTHESES` создает базовые гипотезы по крупным темам.

Если новый расчет не вернул темы, старая доска не очищается.

## Основные настройки

1. `RAG_GW`: адрес API, в compose `http://nginx/api/v1`.
2. `RAG_USER` и `RAG_PASS`: пользователь для публикации.
3. `VALKEY_URL`: адрес Valkey.
4. `EPOCH_KEY`: ключ версии корпуса.
5. `LAST_CLUSTERED_KEY`: последняя успешно обработанная версия.
6. `CHECK_INTERVAL`: период проверки.
7. `DEBOUNCE_SEC`: пауза перед запуском после изменения корпуса.
8. `GRAPH_COMPUTE_ADDR`: адрес `graph-compute`, в compose `graph-compute:9093`.
9. `KNN_K`, `RESOLUTION`, `MIN_SIZE`: параметры группировки.
10. `SIM_THRESHOLD`: минимальная близость для ребра графа.
11. `MUTUAL_KNN`: режим построения графа ближайших соседей.
12. `AUTO_HYPOTHESES`: создавать ли базовые гипотезы.
13. `AUTO_HYPOTHESIS_LIMIT`: лимит базовых гипотез.

## Проверка

1. Загрузите несколько документов.
2. Дождитесь статуса indexed.
3. Проверьте страницу `/summary`.
4. Проверьте логи:

```bash
docker compose -f infra/docker-compose.yml logs --tail=120 cluster-worker
```

Исходный код: `ml-services/cluster-worker`.
