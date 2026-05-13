# Funding Service

Real-time сервис для анализа ставок фандинга на MOEX.

## Что показывает

- Прогноз фандинга по USDRUBF, EURRUBF — на основе VWAP с MOEX и курса с Forex
- Фандинг по USDRUBF, EURRUBF после публикации курса ЦБ
- Фандинг по CNYRUBF — напрямую с MOEX
- Текущий курс USDT/RUB

Уведомления через Telegram-бота о публикации нового официального курса ЦБ.

## Быстрый старт

```bash
git clone <repo-url>
cd funding-service
cp .env.example .env
# Заполнить .env: POSTGRES_USER, POSTGRES_PASSWORD, POSTGRES_DB, TELEGRAM_BOT_TOKEN
docker compose up
```

Фронтенд доступен на `http://localhost:80`.  
Бэкенд API: `http://localhost:8080`.

### Проверка TimescaleDB

```bash
docker compose exec postgres psql -U $POSTGRES_USER -d $POSTGRES_DB -c "SELECT extname FROM pg_extension;"
```

## Стек

- **Backend:** Go 1.22+, PostgreSQL 16 + TimescaleDB, WebSocket, MessagePack
- **Frontend:** React 18 + TypeScript + Vite, Zustand
- **Deploy:** Docker Compose

## Структура

```
funding-service/
├── backend/       Go-сервер
├── frontend/      React-приложение
├── docker-compose.yml
└── .env.example
```

> Подробный план разработки — в `../funding-service-plan.md`.
