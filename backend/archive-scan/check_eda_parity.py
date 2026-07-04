#!/usr/bin/env python3
"""
check_eda_parity.py — минимальная проверка NDJSON vs Parquet.

- Считает строки в NDJSON без загрузки в память.
- Пытается получить число строк в Parquet:
    * через pyarrow (metadata.num_rows) или fastparquet
    * если библиотек нет, проверяет магию/размер и сообщает, что count неизвестен
- Код выхода:
    0 — всё ок или нестрогий режим без точного паркет-счёта
    2 — обнаружено расхождение числа строк
    3 — строгий режим (--strict) и не удалось получить число строк Parquet
"""

import argparse
import sys
from pathlib import Path


def count_ndjson_lines(path: Path) -> int:
    n = 0
    with path.open("rb") as f:
        for _ in f:
            n += 1
    return n

def parquet_num_rows(path: Path):
    # Пытаемся pyarrow
    try:
        import pyarrow.parquet as pq  # type: ignore
        pf = pq.ParquetFile(str(path))
        return int(pf.metadata.num_rows)
    except Exception:
        pass
    # Пытаемся fastparquet
    try:
        import fastparquet  # type: ignore
        pf = fastparquet.ParquetFile(str(path))
        return int(pf.count)
    except Exception:
        pass
    # Нет движка — вернём None
    return None

def parquet_magic_and_size_ok(path: Path) -> bool:
    if not path.exists():
        return False
    size = path.stat().st_size
    if size < 8:
        return False
    with path.open("rb") as f:
        head = f.read(4)
        f.seek(-4, 2)
        tail = f.read(4)
    return head == b"PAR1" and tail == b"PAR1"

def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("ndjson", type=Path, help="Путь к NDJSON")
    ap.add_argument("parquet", type=Path, help="Путь к Parquet")
    ap.add_argument("--strict", action="store_true",
                    help="Считать ошибкой невозможность прочитать число строк Parquet")
    args = ap.parse_args()

    ndjson_path = args.ndjson
    parquet_path = args.parquet

    if not ndjson_path.exists():
        print(f"[ERR] NDJSON не найден: {ndjson_path}", file=sys.stderr)
        return 2
    if not parquet_path.exists():
        print(f"[ERR] Parquet не найден: {parquet_path}", file=sys.stderr)
        return 2

    ndjson_rows = count_ndjson_lines(ndjson_path)
    pq_rows = parquet_num_rows(parquet_path)

    print(f"NDJSON:  {ndjson_path} — {ndjson_rows} строк")
    if pq_rows is not None:
        print(f"Parquet: {parquet_path} — {pq_rows} строк (по метаданным)")
    else:
        ok_magic = parquet_magic_and_size_ok(parquet_path)
        size = parquet_path.stat().st_size
        state = "OK" if ok_magic else "BAD"
        print(f"Parquet: {parquet_path} — {size} байт, magic={state}. "
              f"Число строк определить нельзя (нет pyarrow/fastparquet).")

    # Решение о коде выхода
    if pq_rows is None:
        if args.strict:
            print("[FAIL] Строгий режим: число строк в Parquet неизвестно.")
            return 3
        print("[WARN] Число строк в Parquet неизвестно. Установи `pyarrow` или `fastparquet` для точной сверки.")
        return 0

    if ndjson_rows != pq_rows:
        print(f"[FAIL] Несовпадение: NDJSON={ndjson_rows} vs Parquet={pq_rows}")
        return 2

    print("[OK] Количество строк совпадает.")
    return 0

if __name__ == "__main__":
    sys.exit(main())
