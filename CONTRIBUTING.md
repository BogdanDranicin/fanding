# Contributing

## Коммиты

Используем [Conventional Commits](https://www.conventionalcommits.org/):

```
feat: добавить новый endpoint
fix: исправить дедупликацию тиков
refactor: переименовать пакет
test: добавить тест для engine
docs: обновить README
```

Формат для задач из плана: `feat: <описание> (task X.Y)`.

## Архитектура

```
internal/
├── config/      — конфиг из env (envconfig)
├── source/      — MarketDataSource интерфейс + реализации (moexiss, cbr, forex, multiplex)
├── funding/     — VWAPCalculator, Engine, Runner
├── storage/     — pgxpool, миграции, Writer (bulk insert)
├── ws/          — Hub, Client, protocol (msgpack)
├── api/         — chi роутер, HTTP handlers
├── telegram/    — Bot (long polling), Dispatcher (алерты)
└── metrics/     — Prometheus counters/gauges/histograms
```

**Главный принцип:** источники данных скрыты за `MarketDataSource`. Замена MOEX ISS на FAST/FIX не затрагивает engine, storage, ws.

## Разработка

```bash
cd backend
go test ./...           # все тесты
go vet ./...            # статический анализ
docker compose up       # полный стек локально
```

Бэкенд живёт на `:8080`, фронтенд — `:80` (nginx проксирует `/api/` и `/ws`).

## Pull Requests

- Один PR — одна задача из плана
- Тесты обязательны для новой логики
- `go mod tidy` перед коммитом
- `.env` никогда не коммитить
