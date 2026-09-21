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

## Секреты

Репозиторий публичный. Токен бота, токен T-Invest, логин-пароль прокси и ключи
живут только в `.env`; в коде и тестах — заведомо ненастоящие значения
(адреса из документационных диапазонов RFC 5737, токены без префикса `AA`).

```bash
bash scripts/check-secrets.sh          # то же, что проверяет CI на каждый push
git config core.hooksPath .githooks    # включить проверку перед коммитом
```

Утёкшее значение считается сгоревшим: его перевыпускают у @BotFather
(`/revoke`) или в кабинете сервиса, а не «прячут» следующим коммитом — история
git остаётся публичной.

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
