# Funding Service

Real-time сервис для анализа ставок фандинга на MOEX. Показывает VWAP и расчётные фандинги по фьючерсам USDRUBF, EURRUBF, CNYRUBF и присылает уведомления в Telegram при выходе нового официального курса ЦБ.

## Что показывает

| Инструмент | VWAP | Forex funding | MOEX funding | CB funding |
|---|---|---|---|---|
| USDRUBF | ✓ | ✓ (TwelveData) | ✓ | ✓ (после публикации ЦБ) |
| EURRUBF | ✓ | ✓ | ✓ | ✓ |
| CNYRUBF | ✓ | — | ✓ | — |

Данные обновляются каждые 250 мс через WebSocket.

## Быстрый старт

```bash
git clone <repo-url>
cd funding-service
cp .env.example .env
```

Заполните `.env`:

```env
POSTGRES_USER=funding
POSTGRES_PASSWORD=<надёжный_пароль>
POSTGRES_DB=funding

# Опционально — без токена бот отключён, сервис работает
TELEGRAM_BOT_TOKEN=<токен от @BotFather>
TELEGRAM_BOT_USERNAME=<username бота без @>
```

```bash
docker compose up          # dev: backend на :8080, frontend на :80
# или
docker compose -f docker-compose.prod.yml up   # prod: только порт 80
```

Фронтенд: `http://localhost`.  
API: `http://localhost:8080/api/v1/instruments`.  
Метрики: `http://localhost:8080/metrics`.  
Healthcheck: `http://localhost:8080/healthz`.

## Как получить Telegram-токен

1. Откройте [@BotFather](https://t.me/BotFather) → `/newbot`
2. Укажите имя и username бота
3. Скопируйте токен в `TELEGRAM_BOT_TOKEN`
4. Скопируйте username (без @) в `TELEGRAM_BOT_USERNAME`

После запуска: зайдите в «Настройки» на сайте → нажмите «Привязать Telegram».

## Архитектура

```
MOEX ISS ──┐
CBR XML  ──┤  multiplex  →  runner  →  engine  →  [snapshots chan]
           │                   │                        │
           └──────────────  ticker(1s)           writer → TimescaleDB
                                                       │
                                              broadcaster(250ms) → hub → WS clients
                                              dispatcher ←─ OnNewPublication → Telegram
```

### Стек

| Слой | Технология |
|---|---|
| Backend | Go 1.26, chi, coder/websocket, pgx/v5, golang-migrate |
| Database | PostgreSQL 16 + TimescaleDB (hypertables) |
| Serialization | MessagePack (vmihailenco/msgpack/v5) |
| Metrics | Prometheus (client_golang) |
| Frontend | React 18 + TypeScript + Vite + Zustand |
| Deploy | Docker Compose + nginx |

## Тесты

```bash
# Backend
cd backend
go test ./...

# Frontend (TypeScript-проверка)
cd frontend
docker compose build frontend   # Node 20 в Docker
```

## HTTP API

| Метод | Путь | Описание |
|---|---|---|
| GET | `/api/v1/instruments` | Список инструментов |
| GET | `/api/v1/snapshots/recent?limit=N` | Последние N строк (default 300) |
| GET | `/api/v1/cb-publications?days=N` | Публикации ЦБ за N дней (default 7) |
| POST | `/api/v1/users` | Создать пользователя (возвращает id и link_token) |
| GET | `/api/v1/users/{id}/telegram-link` | Ссылка для привязки Telegram |

## Troubleshooting

**Миграции не применились**

```bash
docker compose logs backend | grep migration
# Проверить DSN: POSTGRES_HOST=postgres (имя сервиса в Docker)
```

**MOEX недоступен / 0 тиков**

MOEX ISS — бесплатный публичный API. При высокой нагрузке может быть недоступен.  
Проверить: `curl "https://iss.moex.com/iss/engines/currency/markets/selt/securities/USD000UTSTOM.json?iss.meta=off&iss.only=marketdata"`

**Telegram бот не отвечает**

- Убедитесь, что `TELEGRAM_BOT_TOKEN` задан верно
- Проверьте лог: `docker compose logs backend | grep telegram`
- Токен можно получить только у [@BotFather](https://t.me/BotFather)

**`/ws` не подключается через nginx**

Убедитесь, что в `nginx.conf` есть:
```nginx
proxy_http_version 1.1;
proxy_set_header Upgrade $http_upgrade;
proxy_set_header Connection "upgrade";
```
