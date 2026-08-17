-- Роботы, найденные в ленте сделок: одна строка на серию («сессию») робота.
-- Строка живёт, пока робот печатает: last_seen и prints обновляются, а когда он
-- замолкает — active снимается, и строка остаётся историей.
CREATE TABLE IF NOT EXISTS robots (
    id           BIGSERIAL PRIMARY KEY,
    symbol       TEXT             NOT NULL,
    side         TEXT             NOT NULL,  -- B: агрессор покупал, S: продавал
    qty_min      DOUBLE PRECISION NOT NULL,  -- лотовка: границы размеров принтов
    qty_max      DOUBLE PRECISION NOT NULL,
    qty_typical  DOUBLE PRECISION NOT NULL,
    period_sec   DOUBLE PRECISION NOT NULL,  -- тайминг: период повторения
    jitter       DOUBLE PRECISION NOT NULL,  -- разброс интервалов вокруг периода
    prints       INTEGER          NOT NULL,
    beats        INTEGER          NOT NULL,
    confidence   DOUBLE PRECISION NOT NULL,
    price_first  DOUBLE PRECISION NOT NULL,
    price_last   DOUBLE PRECISION NOT NULL,
    first_seen   TIMESTAMPTZ      NOT NULL,  -- биржевое время первого принта серии
    last_seen    TIMESTAMPTZ      NOT NULL,
    detected_at  TIMESTAMPTZ      NOT NULL,  -- когда сервис впервые увидел серию
    updated_at   TIMESTAMPTZ      NOT NULL,
    active       BOOLEAN          NOT NULL DEFAULT TRUE
);

-- Страница показывает последних роботов, с фильтром по тикеру.
CREATE INDEX IF NOT EXISTS idx_robots_last_seen ON robots (last_seen DESC);
CREATE INDEX IF NOT EXISTS idx_robots_symbol_last_seen ON robots (symbol, last_seen DESC);
