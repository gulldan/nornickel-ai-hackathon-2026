# graph-compute

`graph-compute` это Rust-сервис для графовых расчетов.

Он не хранит состояние. Каждый запуск полностью определяется запросом.

Сервис читает векторы из Qdrant и возвращает результат воркерам.

## Кто вызывает

1. `cluster-worker` для тем.
2. `discovery-worker` для мостов между темами в режиме `bridge`.
3. `raptor-worker` для глобального режима RAPTOR.

## RPC

## Cluster

`Cluster` группирует документы или фрагменты.

Режимы:

1. `document`: объединяет фрагменты документа в один вектор и строит темы.
2. `chunk`: работает с исходными фрагментами. Этот режим нужен для глобального
   RAPTOR.

Результат:

1. Темы.
2. Представительные документы.
3. Метрики качества.
4. Lineage, то есть связь с прошлой версией доски.

## ScoreBridges

`ScoreBridges` ищет пары тем, которые близки по смыслу, но слабо связаны
напрямую.

Результат:

1. Пара тем.
2. Документы-посредники.
3. Оценки близости, мостового положения, конвергенции и общего балла.

`discovery-worker` может превратить такой мост в гипотезу, если пройдены
проверки.

## Внутренняя структура

1. `src/domain`: чистые алгоритмы.
2. `src/application`: сервисный слой над источником векторов и детектором
   сообществ.
3. `src/infrastructure`: Qdrant и алгоритмы Leiden или Louvain.
4. `src/interfaces`: gRPC-сервер и HTTP-зеркало.

## Запуск и тесты

```bash
cargo build --features service --bins
cargo clippy --all-targets --all-features -- -D warnings
cargo test --all-features
cargo fmt
```

## HTTP-адреса

1. `POST /v1/cluster`.
2. `POST /v1/bridges`.
3. `GET /healthz`.
4. `GET /metrics`.

## Настройки

1. `GRPC_ADDR`: gRPC-адрес, в compose `:9093`.
2. `HTTP_ADDR`: HTTP-адрес, обычно `:8080`.
3. `QDRANT_URL`: адрес Qdrant.
4. `QDRANT_COLLECTION`: коллекция Qdrant, обычно `documents`.
