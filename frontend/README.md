# Фронтенд

Фронтенд это одностраничный интерфейс платформы.

Он закрывает пять пользовательских задач:

1. Поиск и чат по базе знаний.
2. Управление документами.
3. Работа с KPI.
4. Доска гипотез и направлений.
5. Админка, метрики и настройки.

## Стек

1. React 19.
2. TypeScript в строгом режиме.
3. Vite 8.
4. Tailwind CSS 4.
5. React Router 7.
6. i18next для языков `ru`, `en` и `zh`.
7. Lucide для иконок.
8. Собственные UI-компоненты в `src/shared/ui`.

## Маршруты

1. `/login`: вход.
2. `/`: стартовый поиск.
3. `/search`: результат поиска.
4. `/history`: история запросов.
5. `/operator/documents`: документы.
6. `/summary`: темы корпуса.
7. `/clusters/:id`: карточка темы.
8. `/kpi`: цели и KPI.
9. `/hypotheses`: доска гипотез.
10. `/hypotheses/:id`: карточка гипотезы.
11. `/directions/:id`: карточка направления.
12. `/hypotheses/report`: отчет по портфелю.
13. `/hypotheses/:id/report`: отчет по одной гипотезе.
14. `/admin`: обзор стенда.
15. `/admin/metrics`: метрики.
16. `/admin/users`: пользователи.
17. `/admin/settings`: настройки.

## Локальная разработка

Установить зависимости:

```bash
bun install
```

Запустить Vite:

```bash
RAG_API=http://localhost:8080 bun run dev
```

То же через Makefile:

```bash
make frontend-dev
```

Для полного dev-контура:

```bash
make dev
```

## Проверки

```bash
bun run verify
```

Отдельные команды:

```bash
bun run typecheck
bun run lint
bun run fmt:check
bun run knip
bun run audit
bun run test
bun run build
bun run size
```

Общая команда из корня:

```bash
make frontend-check
```

## Проверка доступности

Playwright-аудит требует живой стенд:

```bash
E2E_BASE_URL=http://localhost:5173 E2E_USER=admin E2E_PASS=<пароль> bun run a11y
```

Пароль можно взять из `ADMIN_PASSWORD` в `infra/.env`.
