# Funding service — план разработки для Claude Code

## О документе

Этот файл — пошаговый план разработки real-time сервиса для анализа ставок фандинга на MOEX с возможностью апгрейда источника данных. План разбит на **много маленьких задач**, каждую из которых можно дать Claude Code как отдельный промпт. Это специально сделано так, чтобы каждая задача укладывалась в лимиты одной сессии и имела чёткий критерий готовности.

**Как использовать:** идёшь по шагам сверху вниз. Для каждой задачи открываешь Claude Code, копируешь раздел задачи (включая контекст и критерий готовности) и просишь реализовать. После завершения задачи коммитишь изменения, потом переходишь к следующей.

## Описание продукта

Сервис показывает в реальном времени:

- Прогноз фандинга по USDRUBF, EURRUBF — на основе VWAP с MOEX и курса с Forex
- Фандинг по USDRUBF, EURRUBF после публикации курса ЦБ — на основе VWAP с MOEX и официального курса
- Фандинг по CNYRUBF — напрямую с MOEX
- Текущий курс USDT/RUB

Уведомления через Telegram-бота о публикации нового официального курса ЦБ.

## Архитектурные принципы

1. **Источники данных скрыты за интерфейсом** `MarketDataSource`. Сегодня — бесплатный MOEX ISS REST с polling. Завтра — платный FAST/FIX через брокера. Замена источника не должна требовать переписывания остального кода.
2. **Внутренний контракт** — поток объектов `Tick` (символ, цена, timestamp, тип). Всё что выше ingestion-слоя работает только с тиками.
3. **Бэкенд на Go**, фронтенд на React + TypeScript. Между ними — WebSocket с бинарными сообщениями (MessagePack или собственный формат).
4. **Storage** — PostgreSQL. История тиков и фандингов для бэктестов и отображения вчерашних значений.
5. **Деплой** — Docker Compose, один сервер. На < 100 клиентах этого хватит. Масштабирование — потом.

## Стек

| Слой | Технология | Зачем |
|---|---|---|
| Бэкенд | Go 1.22+ | Concurrency, низкая задержка, простой деплой |
| База данных | PostgreSQL 16 + TimescaleDB | История тиков и курсов, time-series запросы |
| Кэш / pub-sub | Redis (опционально на старте) | Можно отложить до второго этапа |
| Транспорт клиентам | WebSocket (gorilla/websocket) | Стандарт для real-time |
| Сериализация | MessagePack | Быстрее JSON, поддерживается везде |
| Фронтенд | React 18 + TypeScript + Vite | Стандарт |
| Состояние | Zustand | Лёгкое, без бойлерплейта Redux |
| Telegram | go-telegram-bot-api/v5 | Стабильная библиотека |
| Сборка / деплой | Docker + docker-compose | Простой и воспроизводимый деплой |

## Структура репозитория

```
funding-service/
├── backend/
│   ├── cmd/server/main.go
│   ├── internal/
│   │   ├── source/         (MarketDataSource + реализации)
│   │   ├── funding/        (расчёт VWAP, фандинга)
│   │   ├── storage/        (PostgreSQL)
│   │   ├── ws/             (WebSocket gateway)
│   │   ├── telegram/       (бот)
│   │   ├── api/            (HTTP API для UI)
│   │   └── config/
│   ├── go.mod
│   └── Dockerfile
├── frontend/
│   ├── src/
│   │   ├── components/
│   │   ├── hooks/
│   │   ├── store/
│   │   ├── api/
│   │   └── App.tsx
│   ├── package.json
│   ├── vite.config.ts
│   └── Dockerfile
├── docker-compose.yml
├── .env.example
└── README.md
```

---

# Этап 0 — Инициализация проекта

## Задача 0.1 — Создать структуру репозитория

**Контекст:** ничего ещё нет. Нужно создать корневую структуру и базовые конфиги.

**Что сделать:**

1. Создать корневую папку `funding-service/` с подпапками `backend/` и `frontend/`.
2. Создать `.gitignore` для Go, Node, Docker.
3. Создать `README.md` с кратким описанием проекта и инструкцией по запуску (заглушка, потом обновим).
4. Создать `.env.example` со всеми переменными окружения, которые понадобятся:
   - `POSTGRES_USER`, `POSTGRES_PASSWORD`, `POSTGRES_DB`
   - `TELEGRAM_BOT_TOKEN`
   - `MOEX_POLL_INTERVAL_MS` (по умолчанию 250)
   - `BACKEND_PORT` (по умолчанию 8080)
   - `LOG_LEVEL` (по умолчанию `info`)
5. Создать пустой `docker-compose.yml` с тремя сервисами (`backend`, `frontend`, `postgres`) — пока с placeholder-командами, доработаем дальше.
6. Инициализировать git репозиторий, сделать initial commit.

**Критерий готовности:** структура папок создана, `.env.example` содержит все указанные переменные, `git status` чистый.

---

## Задача 0.2 — Инициализировать Go-модуль и базовый main.go

**Контекст:** структура из 0.1 готова. Нужен бэкенд, который умеет стартовать, читать конфиг и логировать.

**Что сделать:**

1. В `backend/` выполнить `go mod init github.com/<username>/funding-service/backend` (имя модуля сообщи мне, если непонятно — поставь `github.com/funding-service/backend`).
2. Подключить зависимости:
   - `github.com/joho/godotenv` для чтения `.env`
   - `github.com/rs/zerolog` для логирования
   - `github.com/kelseyhightower/envconfig` для парсинга env в структуру
3. Создать `internal/config/config.go` со структурой `Config` и функцией `Load()`:
   - все поля из `.env.example`
   - валидация (обязательные поля)
4. Создать `cmd/server/main.go`:
   - читает `.env`
   - вызывает `config.Load()`
   - настраивает zerolog (`info` или `debug` по `LOG_LEVEL`)
   - логирует "service starting" с версией конфига
   - блокируется на `select{}` (заглушка, дальше тут будет HTTP-сервер)
5. Добавить `backend/Dockerfile` (multi-stage, alpine, < 30 MB).

**Критерий готовности:**

- `go build ./...` из `backend/` собирается без ошибок
- `go run ./cmd/server` стартует и пишет лог "service starting"
- `docker build -t funding-backend ./backend` собирается

---

## Задача 0.3 — Инициализировать фронтенд (React + Vite + TypeScript)

**Контекст:** бэкенд из 0.2 готов. Теперь нужен фронт-каркас.

**Что сделать:**

1. В `frontend/` инициализировать Vite-проект на React + TypeScript: `npm create vite@latest . -- --template react-ts`.
2. Установить:
   - `zustand` — состояние
   - `clsx` — утилита для классов
   - `@msgpack/msgpack` — декодинг WebSocket-сообщений
3. Удалить дефолтный шаблон Vite (логи, картинки React), оставить только `App.tsx` с пустым `<div>` и заголовком "Funding Service".
4. Настроить `vite.config.ts`:
   - proxy для `/api` → `http://backend:8080`
   - proxy для `/ws` → `ws://backend:8080`
5. Настроить базовые CSS-переменные в `index.css` для тёмной темы (фон, текст, акценты, цвета зелёный/красный для подсветки). Используй переменные `--bg-primary`, `--bg-secondary`, `--text-primary`, `--text-muted`, `--accent-up` (зелёный), `--accent-down` (красный).
6. Добавить `frontend/Dockerfile` (multi-stage: build → nginx).

**Критерий готовности:**

- `npm run dev` из `frontend/` стартует и показывает "Funding Service"
- `docker build -t funding-frontend ./frontend` собирается
- TypeScript строгий режим включён в `tsconfig.json` (`"strict": true`)

---

## Задача 0.4 — Docker Compose с Postgres и TimescaleDB

**Контекст:** есть бэкенд и фронт, но они изолированы. Нужно поднять всё одной командой.

**Что сделать:**

1. Обновить `docker-compose.yml`:
   - сервис `postgres` на образе `timescale/timescaledb:latest-pg16`, volume для данных, экспорт порта 5432 на хост (на 5433, чтобы не конфликтовать с локальной БД)
   - сервис `backend` со сборкой из `./backend`, зависимостью от postgres, портом 8080
   - сервис `frontend` со сборкой из `./frontend`, портом 5173 (dev) или 80 (prod nginx)
   - общая сеть `funding-net`
2. Создать `backend/init.sql` с командой `CREATE EXTENSION IF NOT EXISTS timescaledb;` (выполняется при первом старте Postgres).
3. Прокинуть в postgres init-скрипты через volume `./backend/init.sql:/docker-entrypoint-initdb.d/init.sql`.
4. Обновить `README.md`: `cp .env.example .env`, заполнить, `docker compose up`.

**Критерий готовности:**

- `docker compose up` поднимает все три сервиса без ошибок
- Бэкенд логирует "service starting"
- Фронт доступен на `http://localhost:5173` (или 80)
- `docker compose exec postgres psql -U $POSTGRES_USER -d $POSTGRES_DB -c "SELECT extname FROM pg_extension;"` показывает `timescaledb`

---

# Этап 1 — Абстракция источника данных и MOEX ISS

## Задача 1.1 — Определить интерфейс MarketDataSource и типы

**Контекст:** все источники данных (MOEX, Forex, ЦБ) должны быть скрыты за одним интерфейсом, чтобы их можно было заменять.

**Что сделать:**

1. Создать `backend/internal/source/types.go`:
   - тип `TickKind` (enum: `KindLastPrice`, `KindBid`, `KindAsk`, `KindOfficialRate`, `KindVWAP`)
   - структура `Tick`:
     ```go
     type Tick struct {
         Symbol    string
         Price     float64
         Volume    float64
         Kind      TickKind
         Timestamp time.Time
         Source    string
     }
     ```
   - константы для символов: `SymbolUSDRUBF`, `SymbolEURRUBF`, `SymbolCNYRUBF`, `SymbolUSDTRUB`, `SymbolEURUSD`, `SymbolUSDCNH`, `SymbolUSDRubOfficial`, `SymbolEURRubOfficial`
2. Создать `backend/internal/source/source.go`:
   - интерфейс `MarketDataSource`:
     ```go
     type MarketDataSource interface {
         Name() string
         Subscribe(ctx context.Context, symbols []string) (<-chan Tick, error)
         Close() error
     }
     ```
3. Покрыть юнит-тестами создание `Tick` и проверку валидности `TickKind` (минимум — пара ассертов, что enum не сломается).

**Критерий готовности:**

- `go test ./internal/source/...` зелёный
- Интерфейс и типы экспортируются и используются в `main.go` хотя бы как импорт-заглушка

---

## Задача 1.2 — Реализация MOEX ISS REST source (часть 1: HTTP-клиент)

**Контекст:** есть интерфейс. Теперь нужна первая реализация — polling MOEX ISS.

**Документация MOEX ISS:** базовый URL `https://iss.moex.com/iss/`. Эндпоинты для фьючерсов:
- USDRUBF: `https://iss.moex.com/iss/engines/futures/markets/forts/securities/USDRUBF.json`
- EURRUBF: `https://iss.moex.com/iss/engines/futures/markets/forts/securities/EURRUBF.json`
- CNYRUBF: `https://iss.moex.com/iss/engines/futures/markets/forts/securities/CNYRUBF.json`

Ответ — JSON с полями `securities` (статичная инфа) и `marketdata` (текущие цены). В `marketdata.data[0]` есть `LAST`, `BID`, `OFFER`, `VOLTODAY` и поле `SYSTIME` с серверным временем биржи.

**Что сделать:**

1. Создать `backend/internal/source/moexiss/client.go`:
   - структура `Client` с `httpClient *http.Client` (с persistent connection: `MaxIdleConns: 10`, `IdleConnTimeout: 90 * time.Second`)
   - метод `FetchSecurity(ctx context.Context, board, market, engine, symbol string) (*RawResponse, error)`
   - структура `RawResponse` с распарсенными `marketdata` (columns + data)
2. Парсер JSON должен корректно сопоставлять `columns: ["SECID","LAST","BID",...]` с `data: [["USDRUBF",81.91,81.90,...]]` и возвращать map.
3. Поддержать ETag и `If-None-Match` для сокращения трафика: если 304 — возвращать `ErrNotModified`.
4. Таймауты: 1 сек на запрос, 3 retry с экспоненциальной задержкой.

**Критерий готовности:**

- Тест с замоканным HTTP-сервером, отдающим валидный JSON MOEX ISS, парсит ответ корректно
- Тест с 304 возвращает `ErrNotModified`
- `go test ./internal/source/moexiss/...` зелёный

---

## Задача 1.3 — MOEX ISS source (часть 2: реализация MarketDataSource)

**Контекст:** HTTP-клиент готов. Теперь обёртка, реализующая интерфейс `MarketDataSource`.

**Что сделать:**

1. Создать `backend/internal/source/moexiss/source.go`:
   - структура `Source` хранит `client *Client`, `pollInterval time.Duration`, `logger zerolog.Logger`
   - конструктор `New(pollInterval time.Duration, logger zerolog.Logger) *Source`
   - метод `Name() string` возвращает `"moex-iss"`
   - метод `Subscribe(ctx, symbols) (<-chan Tick, error)`:
     - стартует горутину на каждый символ
     - каждые `pollInterval` (250 мс по умолчанию) дёргает `client.FetchSecurity`
     - сравнивает `LAST` с предыдущим; если изменился — шлёт `Tick` в канал
     - аналогично для `BID`, `OFFER` (как `KindBid`, `KindAsk`)
     - использует `time.Ticker`; останавливается при `ctx.Done()`
   - метод `Close() error` отменяет внутренний контекст
2. Дедупликация: хранить последнее значение `LAST/BID/OFFER` для каждого символа в `sync.Map`, шлём `Tick` только если значение реально изменилось.
3. Логировать каждую успешную итерацию на `debug`, ошибки — на `warn`.

**Критерий готовности:**

- Запустить `Subscribe([USDRUBF, EURRUBF, CNYRUBF])` против реального MOEX ISS — в канал должны капать тики
- Если MOEX недоступен — горутина не падает, логирует ошибки и продолжает попытки
- `Close()` корректно завершает все горутины

---

## Задача 1.4 — Forex source через TwelveData (или альтернатива)

**Контекст:** нужны курсы EUR/USD и USD/CNH с Forex для расчёта кросс-курсов. На MVP — бесплатный план TwelveData (8 запросов/мин, хватит для одного-двух символов с polling раз в 10 сек). Альтернатива — Tinkoff Invest API через брокерский счёт.

**Что сделать:**

1. Создать `backend/internal/source/forex/twelvedata.go`:
   - структура `Source` реализует `MarketDataSource`
   - конструктор `New(apiKey string, pollInterval time.Duration, logger zerolog.Logger)`
   - метод `Subscribe`: polling `https://api.twelvedata.com/price?symbol=EUR/USD,USD/CNH&apikey=...` каждые 10 сек
   - парсит ответ и шлёт `Tick{Kind: KindLastPrice}` на каждый символ
2. Добавить переменную `TWELVEDATA_API_KEY` в `.env.example` и `config.go`.
3. Если ключа нет — source отказывается стартовать с понятной ошибкой.

**Критерий готовности:**

- С валидным ключом цены EUR/USD и USD/CNH капают в канал
- Без ключа — `Subscribe` возвращает понятную ошибку
- Тест с замоканным HTTP-ответом проходит

---

## Задача 1.5 — Source для официального курса ЦБ

**Контекст:** ЦБ публикует курс по будням примерно в 11:30 МСК на `https://www.cbr.ru/scripts/XML_daily.asp`. Это XML с курсами всех валют. Нам нужны USD и EUR.

**Что сделать:**

1. Создать `backend/internal/source/cbr/source.go`:
   - polling `https://www.cbr.ru/scripts/XML_daily.asp` с разным интервалом:
     - вне окна 11:25-11:45 МСК — раз в 5 минут
     - в окне 11:25-11:45 МСК — раз в 200 мс
   - парсит XML (стандартная библиотека `encoding/xml`)
   - детектит, что дата `Date` атрибута Root отличается от ранее виденной — это сигнал, что курс обновился
   - шлёт два `Tick` с `Kind: KindOfficialRate` для USD/RUB и EUR/RUB
2. **Важно:** XML от ЦБ в кодировке windows-1251, нужно правильно настроить декодер. Используй `golang.org/x/text/encoding/charmap` для конвертации.
3. Эмитить **отдельное событие** при детекте новой публикации (для алертов в Telegram). Это можно сделать через канал `OnNewPublication chan time.Time` на структуре Source — потом подпишемся из dispatcher.

**Критерий готовности:**

- Парсит реальный ответ cbr.ru, шлёт корректные тики USD и EUR
- При смене даты Root шлёт сигнал о новой публикации
- Корректно обрабатывает windows-1251

---

## Задача 1.6 — Multiplex source (объединение нескольких источников в один поток)

**Контекст:** у нас будут три источника (MOEX, Forex, ЦБ). Funding engine должен подписаться на единый поток, не зная про источники.

**Что сделать:**

1. Создать `backend/internal/source/multiplex/multiplex.go`:
   - структура `Source` хранит `sources []MarketDataSource`
   - метод `Subscribe(ctx, symbols)` фильтрует символы по принадлежности каждому источнику (через карту "какой символ откуда") и подписывает соответствующий source
   - объединяет каналы fan-in через горутины в один выходной канал
   - `Close()` закрывает все sources
2. Карта принадлежности символов хранится в конфиге (можно захардкодить в multiplex):
   - MOEX: `USDRUBF`, `EURRUBF`, `CNYRUBF`, `USDTRUB`
   - Forex: `EURUSD`, `USDCNH`
   - CBR: `USDRubOfficial`, `EURRubOfficial`
3. Тест: подключаются два мок-source'а, multiplex отдаёт тики от обоих в одном канале.

**Критерий готовности:**

- Multiplex корректно собирает тики из всех источников
- При закрытии контекста все вложенные горутины завершаются
- Тест зелёный

---

# Этап 2 — Расчёт VWAP и фандинга

## Задача 2.1 — VWAP-калькулятор в скользящем окне

**Контекст:** VWAP — средневзвешенная по объёму цена за определённый период. Для фандинга обычно берётся VWAP за минуту или произвольный интервал.

**Что сделать:**

1. Создать `backend/internal/funding/vwap.go`:
   - структура `VWAPCalculator` с параметрами `window time.Duration` (по умолчанию 1 минута)
   - метод `Add(price, volume float64, ts time.Time)` — добавляет сделку в окно
   - метод `Value(now time.Time) (float64, bool)` — возвращает VWAP с момента `now - window` по `now`; bool=false если данных нет
   - под капотом — circular buffer или deque, удаляющий элементы старше окна
2. Concurrency-safe (mutex или atomic).
3. Тесты на разные сценарии: пустой буфер, выход элементов за окно, корректность взвешивания.

**Критерий готовности:**

- Тесты проверяют: VWAP пустого окна вернёт `_, false`; VWAP с известными значениями совпадает с ручным расчётом
- Бенчмарк `Add + Value` стабильно быстрее 1 микросекунды

---

## Задача 2.2 — Funding engine: расчёт прогноза и пост-фандинга

**Контекст:** теперь самое содержательное. По описанию из ТЗ:

- **Прогноз фандинга (Forex)** для USDRUBF: разница между VWAP(USDRUBF на MOEX) и `EURUSD × EURRUBF` (или подобный кросс) — точная формула зависит от инструмента. Для **USDRUBF**: фандинг = `VWAP(USDRUBF) - USDRUB_forex`, где `USDRUB_forex` собирается из доступных кроссов. Для **EURRUBF** аналогично.
- **MOEX фандинг** для CNYRUBF — берётся как разница `VWAP(CNYRUBF) - последний LAST` или приходит напрямую (уточнить у пользователя; на MVP — считать как `VWAP - last_price`).
- **CB фандинг** для USDRUBF: `VWAP(USDRUBF) - официальный_курс_USD_от_ЦБ` (когда курс уже опубликован).

**Что сделать:**

1. Создать `backend/internal/funding/engine.go`:
   - структура `Engine` хранит мапу `symbol -> *VWAPCalculator`, последние известные цены источников, последний официальный курс ЦБ
   - метод `Ingest(tick source.Tick)` — обновляет соответствующий калькулятор / кэш
   - метод `Snapshot() FundingSnapshot` — считает все три типа фандинга для USDRUBF, EURRUBF, и MOEX-фандинг для CNYRUBF, возвращает структуру с текущими значениями
2. Структура `FundingSnapshot`:
   ```go
   type FundingSnapshot struct {
       Timestamp     time.Time
       USDRUBF       InstrumentFunding
       EURRUBF       InstrumentFunding
       CNYRUBF       InstrumentFunding
       USDTRUBPrice  float64
   }
   type InstrumentFunding struct {
       VWAP            float64
       LastPrice       float64
       ForexFunding    *float64 // nil если форекс ещё не подгрузился
       MOEXFunding     *float64
       CBFunding       *float64 // nil до публикации курса ЦБ
       OfficialRate    *float64
   }
   ```
3. **Важно:** на текущий момент точные формулы фандинга нужно согласовать с пользователем. Сейчас в коде сделать заглушки с TODO-комментариями и реализовать упрощённую версию: `Forex funding = VWAP(symbol) - cross_rate_from_forex`, `MOEX funding = VWAP(symbol) - last_price(symbol)`, `CB funding = VWAP(symbol) - official_rate`. После MVP — уточнить формулы и переписать.

**Критерий готовности:**

- Тест: подаём серию тиков, проверяем что `Snapshot()` возвращает корректные значения VWAP и фандингов
- `CBFunding` равен nil пока не пришёл хотя бы один официальный курс
- `ForexFunding` равен nil пока не пришёл хотя бы один forex-тик

---

## Задача 2.3 — Запускающая горутина: source → engine

**Контекст:** есть source (выдаёт тики) и engine (принимает тики и считает фандинг). Нужен код, который их связывает и периодически публикует снэпшот.

**Что сделать:**

1. Создать `backend/internal/funding/runner.go`:
   - структура `Runner` с полями `source MarketDataSource`, `engine *Engine`, `snapshotInterval time.Duration` (по умолчанию 250 мс), `out chan<- FundingSnapshot`
   - метод `Run(ctx context.Context) error`:
     - подписывается на source со всеми нужными символами
     - в одной горутине читает тики и вызывает `engine.Ingest`
     - в другой — каждые 250 мс вызывает `engine.Snapshot()` и шлёт в `out`
     - завершается при `ctx.Done()`
2. Подключить к `cmd/server/main.go`:
   - создать multiplex source с MOEX и (опционально, если ключ задан) Forex + CBR
   - создать engine и runner
   - запустить runner; пока что снэпшоты просто логировать

**Критерий готовности:**

- `docker compose up` запускает сервис, в логах видно периодические снэпшоты с реальными значениями VWAP
- При выключении сервиса (Ctrl+C) всё корректно завершается без утечек горутин

---

# Этап 3 — Хранилище (PostgreSQL + TimescaleDB)

## Задача 3.1 — Схема БД и миграции

**Контекст:** нужно хранить тики (для бэктеста) и снэпшоты фандинга (для отображения исторических значений и графиков).

**Что сделать:**

1. Подключить `github.com/jackc/pgx/v5` и `github.com/golang-migrate/migrate/v4`.
2. Создать `backend/internal/storage/migrations/`:
   - `0001_init.up.sql`:
     - таблица `ticks(timestamp TIMESTAMPTZ, symbol TEXT, price NUMERIC(18,8), volume NUMERIC(18,8), kind TEXT, source TEXT)`, hypertable по `timestamp`, индекс по `(symbol, timestamp DESC)`
     - таблица `funding_snapshots(timestamp TIMESTAMPTZ, symbol TEXT, vwap NUMERIC(18,8), last_price NUMERIC(18,8), forex_funding NUMERIC(18,8), moex_funding NUMERIC(18,8), cb_funding NUMERIC(18,8), official_rate NUMERIC(18,8))`, hypertable
     - таблица `users(id BIGSERIAL PRIMARY KEY, telegram_chat_id BIGINT UNIQUE, telegram_username TEXT, link_token TEXT UNIQUE, created_at TIMESTAMPTZ DEFAULT now())`
     - таблица `cb_publications(date DATE PRIMARY KEY, usd_rate NUMERIC(18,8), eur_rate NUMERIC(18,8), detected_at TIMESTAMPTZ)`
   - `0001_init.down.sql` — обратные операции
3. Создать `backend/internal/storage/db.go`:
   - функция `Connect(ctx context.Context, dsn string) (*pgxpool.Pool, error)`
   - функция `Migrate(dsn string) error` — прогоняет миграции
4. Вызывать `Connect` и `Migrate` в `main.go` при старте.

**Критерий готовности:**

- `docker compose up` — миграции применяются автоматически
- `docker compose exec postgres psql -U $POSTGRES_USER -d $POSTGRES_DB -c "\dt"` показывает все таблицы
- `SELECT * FROM timescaledb_information.hypertables;` показывает `ticks` и `funding_snapshots`

---

## Задача 3.2 — Запись тиков и снэпшотов в БД

**Контекст:** Миграции есть, теперь надо писать данные.

**Что сделать:**

1. Создать `backend/internal/storage/ticks.go`:
   - метод `BatchInsertTicks(ctx, ticks []Tick) error` — через `COPY FROM` для производительности
2. Создать `backend/internal/storage/snapshots.go`:
   - метод `InsertSnapshot(ctx, snapshot FundingSnapshot) error`
3. Интегрировать с runner:
   - буферизировать тики в памяти (батч раз в 1 сек или 500 тиков, что наступит раньше) и сбрасывать в БД
   - снэпшоты записывать с реже — раз в 5 сек хватит для истории
4. **Важно:** запись в БД не должна блокировать поток тиков. Используй отдельную горутину с буферизированным каналом; при переполнении — drop oldest с warn-логом.

**Критерий готовности:**

- После 1 минуты работы `SELECT count(*) FROM ticks WHERE timestamp > now() - interval '1 minute'` показывает разумное число (десятки-сотни)
- `SELECT count(*) FROM funding_snapshots WHERE timestamp > now() - interval '1 minute'` примерно 12 (раз в 5 сек)
- Бэкенд не теряет производительности при недоступной БД (только пишет warn)

---

# Этап 4 — WebSocket gateway

## Задача 4.1 — Базовый WebSocket-сервер

**Контекст:** клиенты должны подписываться на снэпшоты по WebSocket.

**Что сделать:**

1. Подключить `github.com/coder/websocket` (современная альтернатива gorilla).
2. Создать `backend/internal/ws/hub.go`:
   - структура `Hub` хранит активных клиентов (map с mutex)
   - метод `Register(c *Client)`, `Unregister(c *Client)`
   - метод `Broadcast(msg []byte)` — рассылает всем клиентам через их каналы; если канал клиента заполнен — drop сообщения с warn-логом (медленный клиент не должен тормозить рассылку)
3. Создать `backend/internal/ws/client.go`:
   - структура `Client` хранит WebSocket-соединение и буферизированный канал (size 64)
   - метод `WritePump(ctx)` читает из канала и шлёт в сокет
   - метод `ReadPump(ctx)` читает входящие сообщения (для пингов и команд клиента)
4. Подключить к `main.go`:
   - HTTP-сервер на `BACKEND_PORT`
   - эндпоинт `GET /ws` — апгрейд в WebSocket, регистрация в hub
   - эндпоинт `GET /healthz` — возвращает 200 OK

**Критерий готовности:**

- Можно подключиться к `ws://localhost:8080/ws` через `wscat` или curl
- Сервер логирует подключения и отключения
- Несколько клиентов могут подключиться одновременно

---

## Задача 4.2 — Бинарный протокол снэпшотов через MessagePack

**Контекст:** JSON слишком медленный и многословный для real-time. MessagePack — компактный бинарный формат, поддерживается везде.

**Что сделать:**

1. Подключить `github.com/vmihailenco/msgpack/v5`.
2. Создать `backend/internal/ws/protocol.go`:
   - тип сообщения `WSMessage`:
     ```go
     type WSMessage struct {
         Type      string                 `msgpack:"type"`     // "snapshot" | "publication" | "ping"
         Timestamp int64                  `msgpack:"ts"`       // unix ms
         Payload   map[string]interface{} `msgpack:"payload"`
     }
     ```
   - функция `EncodeSnapshot(s FundingSnapshot) ([]byte, error)`
   - функция `EncodePublication(p Publication) ([]byte, error)`
3. Связать runner с hub: после получения снэпшота кодировать его и звать `hub.Broadcast`.
4. На фронтенде в задаче 5.2 будем декодировать.

**Критерий готовности:**

- Подключенный клиент получает бинарные сообщения раз в 250 мс
- Декодирование сообщения отдельным тестом-консьюмером показывает корректные поля

---

## Задача 4.3 — HTTP API: история и метаданные

**Контекст:** WebSocket — для real-time. Для исторических данных (например, фандинг за последний час при первой загрузке) удобнее REST.

**Что сделать:**

1. Создать `backend/internal/api/handlers.go`:
   - `GET /api/v1/snapshots/recent?limit=300` — последние N снэпшотов из БД (по умолчанию 300 = 25 минут с шагом 5 сек)
   - `GET /api/v1/instruments` — статичный список поддерживаемых инструментов с метаданными (символ, описание, источники фандинга)
   - `GET /api/v1/cb-publications?days=7` — последние публикации курса ЦБ
2. Использовать `chi` для роутинга: `github.com/go-chi/chi/v5`.
3. Middleware: логирование, CORS (на dev — `*`), recoverer.

**Критерий готовности:**

- `curl http://localhost:8080/api/v1/instruments` возвращает JSON со списком
- `curl http://localhost:8080/api/v1/snapshots/recent?limit=10` после минуты работы возвращает 10 объектов

---

# Этап 5 — Фронтенд: таблица и WebSocket

## Задача 5.1 — Zustand store и типы

**Контекст:** на фронте нужен централизованный стор для текущего снэпшота, истории, статуса соединения.

**Что сделать:**

1. Создать `frontend/src/types/funding.ts`:
   - типы зеркально к бэкенду (`FundingSnapshot`, `InstrumentFunding`) — поля совпадают по именам в msgpack
2. Создать `frontend/src/store/fundingStore.ts` на Zustand:
   - state: `current: FundingSnapshot | null`, `previous: FundingSnapshot | null` (для подсветки изменений), `wsStatus: 'connecting' | 'connected' | 'disconnected'`, `history: FundingSnapshot[]` (последние N снимков)
   - actions: `setSnapshot(s)`, `setWSStatus(s)`, `addToHistory(s)`
3. **Важно:** при `setSnapshot` сохраняй текущий как `previous`, чтобы потом можно было сравнить и подсветить изменившиеся ячейки.

**Критерий готовности:**

- Store создаётся, типы строгие, нет ts-ошибок
- Простой компонент-индикатор показывает значение `wsStatus`

---

## Задача 5.2 — WebSocket-хук с декодингом msgpack

**Контекст:** нужно подключиться к `/ws`, декодировать сообщения и обновлять стор.

**Что сделать:**

1. Создать `frontend/src/hooks/useWebSocket.ts`:
   - принимает URL, при монтировании подключается
   - входящие сообщения декодирует через `@msgpack/msgpack`
   - в зависимости от `type` обновляет соответствующую часть стора
   - реализует переподключение с экспоненциальным backoff (стартовая задержка 500 мс, максимум 30 сек)
   - корректная очистка при unmount
2. Подключить хук в `App.tsx`.
3. Показывать индикатор статуса соединения в правом верхнем углу (цветная точка: зелёный/жёлтый/красный).

**Критерий готовности:**

- Открыв страницу, в DevTools видно WebSocket-соединение с входящими бинарными сообщениями
- Точка статуса меняет цвет при отключении бэкенда и обратно при восстановлении
- Console.log временно показывает текущий снэпшот раз в 250 мс

---

## Задача 5.3 — Главная таблица (структура, без анимаций)

**Контекст:** теперь визуализация. По скриншоту: тёмная тема, таблица в центре, колонки — инструменты, строки — типы фандинга. Дополнительно сверху — текущий курс USDT/RUB.

**Что сделать:**

1. Создать `frontend/src/components/FundingTable.tsx`:
   - Заголовок таблицы — три колонки: USDRUBF, EURRUBF, CNYRUBF
   - Строки:
     - VWAP (последнее значение VWAP)
     - Last Price (последняя цена)
     - Forex funding (NULL для CNYRUBF — показывать "—")
     - MOEX funding
     - CB funding (NULL пока не было публикации — показывать "—")
   - Колонка USDT/RUB сверху отдельно — один большой блок с текущей ценой и иконкой направления изменения
2. Создать `frontend/src/components/USDTPriceCard.tsx` для отдельного блока USDT/RUB.
3. Стили — CSS modules или styled-components, не Tailwind (Tailwind для real-time с подсветками будет неудобен). Использовать CSS-переменные из 0.3.
4. Форматирование чисел: 4 знака после запятой для курсов, 6 знаков для фандингов. Использовать `Intl.NumberFormat`.

**Критерий готовности:**

- На странице видна таблица с тремя колонками и пятью строками
- USDT/RUB сверху — крупный блок с ценой
- Значения обновляются в реальном времени (без анимаций пока)
- На пустых данных показывается "—" вместо `null`/`undefined`/`NaN`

---

## Задача 5.4 — Подсветка изменений в ячейках

**Контекст:** ключевая UX-фишка — мигание ячейки при обновлении (зелёный при росте, красный при падении). Эффект должен быть быстрым (300-500 мс), не отвлекающим, но заметным.

**Что сделать:**

1. Создать `frontend/src/hooks/useFlashOnChange.ts`:
   - принимает `value: number | null`
   - возвращает `flashClass: 'flash-up' | 'flash-down' | null`
   - сравнивает текущее и предыдущее значения; если выросло — `flash-up` на 400 мс, упало — `flash-down`
2. В CSS добавить keyframes:
   ```css
   @keyframes flash-up { 0% { background: var(--accent-up); } 100% { background: transparent; } }
   @keyframes flash-down { 0% { background: var(--accent-down); } 100% { background: transparent; } }
   .flash-up { animation: flash-up 400ms ease-out; }
   .flash-down { animation: flash-down 400ms ease-out; }
   ```
3. Применить хук к каждой ячейке таблицы.

**Критерий готовности:**

- При движении цены ячейки мигают зелёным или красным
- Эффект не накапливается — даже при быстрых обновлениях нет дёрганий
- На неизменённых ячейках ничего не мигает

---

## Задача 5.5 — Защита от перерисовок и батчинг через RAF

**Контекст:** при частых обновлениях React может тормозить. Нужно собрать обновления и применять их по `requestAnimationFrame`.

**Что сделать:**

1. Создать обёртку `frontend/src/lib/rafBatch.ts`:
   - функция `scheduleUpdate(fn)` — добавляет `fn` в очередь, дёргает один `requestAnimationFrame` за тик
   - один RAF обрабатывает все накопившиеся обновления
2. В `useWebSocket.ts` оборачивать `setSnapshot` через `scheduleUpdate`.
3. Профилировать в React DevTools: убедиться, что не происходит более 60 рендеров в секунду.

**Критерий готовности:**

- В React Profiler — рендеры не чаще 60 fps даже при 10+ тиках/сек
- Визуально нет «дёргания» при быстрых обновлениях
- CPU usage в DevTools < 20% на типичном ноутбуке

---

# Этап 6 — Telegram-бот

## Задача 6.1 — Базовый Telegram-бот: команды и привязка аккаунта

**Контекст:** пользователь в настройках профиля жмёт «привязать Telegram», получает уникальный токен / deep-link, переходит к боту, бот его регистрирует.

**Что сделать:**

1. Подключить `github.com/go-telegram-bot-api/telegram-bot-api/v5`.
2. Создать `backend/internal/telegram/bot.go`:
   - конструктор `New(token string, db *pgxpool.Pool, logger zerolog.Logger)`
   - метод `Run(ctx)` — long polling
   - обработчики команд:
     - `/start <token>` — ищет пользователя по `link_token`, если найден — обновляет `telegram_chat_id` и `telegram_username`, шлёт «Привет! Уведомления подключены»
     - `/start` без токена — шлёт инструкцию: «Зайдите на сайт и нажмите "Привязать Telegram"»
     - `/stop` — отвязывает аккаунт (обнуляет `telegram_chat_id`)
3. Запускать бота в `main.go` параллельно с HTTP-сервером.

**Критерий готовности:**

- Создать запись в `users` с `link_token = 'TEST'`
- Открыть `t.me/<botname>?start=TEST` — бот регистрирует
- В БД `telegram_chat_id` заполняется
- `/stop` корректно отвязывает

---

## Задача 6.2 — Endpoint для генерации link_token и API настроек

**Контекст:** на фронте будет страница настроек со ссылкой «привязать Telegram». Бэкенд должен выдать уникальный токен.

**Что сделать:**

1. На бэкенде:
   - `POST /api/v1/users` — создаёт user'а (на MVP без авторизации; просто возвращает id и token; user сохраняется в БД)
   - `GET /api/v1/users/:id/telegram-link` — возвращает ссылку `https://t.me/<botname>?start=<token>`
   - **Авторизация:** на MVP — простой токен в куке `user_token`, сохраняется на 1 год. Это упрощённая схема; нормальная авторизация (email/password или OAuth) — отдельная задача после MVP.
2. На фронте создать страницу `SettingsPage.tsx` с кнопкой «Привязать Telegram». При первом заходе автоматически вызывается `POST /api/v1/users`, токен сохраняется в куке. Кнопка ведёт по ссылке из API.

**Критерий готовности:**

- При первом заходе на сайт в куках появляется `user_token`
- На странице настроек кнопка «Привязать Telegram» открывает Telegram с ботом
- После привязки в БД у юзера заполнен `telegram_chat_id`

---

## Задача 6.3 — Отправка алертов о публикации курса ЦБ

**Контекст:** при детекте новой публикации курса ЦБ всем привязанным пользователям шлётся сообщение по образцу скриншота.

**Что сделать:**

1. Создать `backend/internal/telegram/dispatcher.go`:
   - метод `SendPublicationAlert(ctx, p Publication)`:
     - выбирает всех users с непустым `telegram_chat_id`
     - формирует сообщение по образцу: дата, курс USD, EUR, кросс-курсы, фандинги
     - шлёт каждому через `bot.Send` с rate limiting (макс 25 сообщений/сек, чтобы не упереться в лимит Telegram 30/сек)
2. Подписать dispatcher на канал `OnNewPublication` от CBR source.
3. **Формат сообщения** (примерно как на скриншоте):

```
НОВЫЕ ДАННЫЕ:
Дата: 2025-05-06

Межбанк:
Курс USD 81.9137
Курс EURO 92.9082

Кросс-курсы:
Курс ED 1.1342
Курс EURCNH 8.2201
Курс USDCNH 7.2474

Фандинги:
USDRUBF: 0.12287
EURRUBF: 0.1393623
```

**Критерий готовности:**

- При смене даты в XML от ЦБ всем привязанным юзерам приходит сообщение
- Дубликатов нет (одна публикация — одна рассылка)
- При сбое отправки одному юзеру — остальные получают

---

# Этап 7 — Деплой и обзор

## Задача 7.1 — Production Docker Compose и nginx

**Контекст:** dev-режим работает. Нужна production-сборка.

**Что сделать:**

1. Создать `docker-compose.prod.yml`:
   - фронтенд собирается в статику и раздаётся nginx
   - бэкенд за nginx как reverse proxy (`/api` и `/ws` проксируются)
   - конфиг nginx для WebSocket-апгрейда
2. Создать `frontend/nginx.conf` со всеми правилами
3. Обновить README с инструкцией prod-деплоя

**Критерий готовности:**

- `docker compose -f docker-compose.prod.yml up` поднимает всё на одном порту 80
- Фронт работает, WebSocket подключается через тот же домен
- Healthcheck `/healthz` возвращает 200

---

## Задача 7.2 — Простой мониторинг через /metrics

**Контекст:** на проде нужно понимать что происходит. Простой шаг — Prometheus-метрики.

**Что сделать:**

1. Подключить `github.com/prometheus/client_golang`
2. Эндпоинт `/metrics`
3. Метрики:
   - `funding_ticks_received_total{source, symbol}` — counter
   - `funding_ws_clients` — gauge
   - `funding_snapshot_latency_seconds` — histogram (время от последнего тика в снэпшоте до отправки)
   - `funding_cb_publications_detected_total` — counter
4. На MVP — без Prometheus-сервера, просто чтобы можно было через curl проверить.

**Критерий готовности:**

- `curl http://localhost:8080/metrics` возвращает Prometheus-формат
- Метрики растут со временем
- В логе видны все четыре метрики

---

## Задача 7.3 — Финальный README и onboarding-инструкция

**Что сделать:**

1. Обновить корневой `README.md`:
   - что такое сервис, какие фичи
   - быстрый старт: `git clone`, `cp .env.example .env`, `docker compose up`
   - как настроить Telegram-бота (где получить токен у `@BotFather`)
   - как получить ключ TwelveData
   - архитектура (картинка из чата с Клодом или mermaid-диаграмма)
   - тестирование: как запустить тесты бэкенда и фронта
   - troubleshooting: типичные проблемы (миграции не применились, MOEX недоступен, нет ключа Forex)
2. Добавить `CONTRIBUTING.md` с правилами коммитов (Conventional Commits) и архитектурой.

**Критерий готовности:**

- Сторонний человек может склонировать репо и запустить за < 10 минут
- В README есть все ссылки на доки (MOEX ISS, TwelveData, Telegram Bot API)

---

# Этап 8 — Что после MVP (бэклог)

Это не задачи для Claude Code, а ориентир что делать дальше:

- **Уточнить формулы фандинга** с пользователем-трейдером и переписать `engine.go`. Текущие формулы — упрощённые.
- **Переход на FAST/FIX через брокера.** Реализовать `internal/source/fix/source.go`, заменить MOEX-source через переменную окружения. Остальной код не трогается.
- **Графики.** Добавить chart для исторических фандингов (Recharts или Lightweight Charts от TradingView).
- **Бэктест.** Скрипт, который проигрывает исторические тики из БД через `funding.Engine` и считает что бы было, если бы вы торговали по сигналам.
- **Авторизация.** Заменить cookie-токен на нормальную auth (Clerk, Supabase Auth, или свой email-based).
- **Multi-tenancy.** Если будет больше 100 клиентов — выделить отдельный процесс для WebSocket-gateway, объединить через Redis Pub/Sub.
- **Кросс-курсы EURCNH, USDCNH.** Сейчас они только пробрасываются как тики; нужно их в таблицу или отдельный блок.
- **Real-time чарт для VWAP** — внутри ячейки маленький sparkline за последний час.
- **Уведомления на отклонение фандинга.** Алерт в Telegram, если фандинг превысил пороговое значение.

---

# Общие правила работы с Claude Code

1. **Одна задача — один промпт.** Не пытайся дать сразу две задачи; контекст ограничен.
2. **Перед запуском задачи** убедись, что предыдущая закоммичена. Если что-то пойдёт не так — `git reset --hard` вернёт состояние.
3. **Проси писать тесты вместе с кодом**, а не отдельно. В каждой задаче явно указан критерий готовности — он включает тесты.
4. **После каждой задачи** запускай `docker compose up` и проверяй, что сервис всё ещё стартует. Если что-то сломалось — это надо чинить до перехода к следующей задаче.
5. **Если задача оказалась слишком большой** для Claude Code — разбей её на две и попроси сделать первую половину.
6. **Используй MOEX ISS API только для разработки** — на продакшене будь готов получить rate-limit. Если работаешь активно, проси Claude Code добавить exponential backoff с jitter.
7. **Секреты не коммитить.** `.env` в `.gitignore`, в репо только `.env.example`.

Удачи!
