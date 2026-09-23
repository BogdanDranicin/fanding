-- Подгрузка цен входа и выхода (23.09.2026).
--
-- Автор пишет «закрыл Сбер» без цены — цену берём с биржи на момент выхода
-- сообщения. Публичные данные MOEX идут с задержкой 15 минут, поэтому цена
-- подгружается не сразу, а из очереди, когда момент сообщения «доехал» до ISS.
-- Цена, написанная в сообщении, не трогается: очередь заполняет только пустое.
ALTER TABLE trade_positions ADD COLUMN IF NOT EXISTS entry_auto BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE trade_positions ADD COLUMN IF NOT EXISTS close_auto BOOLEAN NOT NULL DEFAULT FALSE;

CREATE TABLE IF NOT EXISTS trade_price_fills (
    id          BIGSERIAL   PRIMARY KEY,
    position_id BIGINT      NOT NULL REFERENCES trade_positions (id) ON DELETE CASCADE,
    field       TEXT        NOT NULL CHECK (field IN ('entry', 'close')),
    ticker      TEXT        NOT NULL,
    at          TIMESTAMPTZ NOT NULL,
    attempts    INT         NOT NULL DEFAULT 0,
    next_try    TIMESTAMPTZ NOT NULL DEFAULT now(),
    done_at     TIMESTAMPTZ,
    error       TEXT        NOT NULL DEFAULT '',
    UNIQUE (position_id, field)
);

CREATE INDEX IF NOT EXISTS idx_trade_price_fills_pending ON trade_price_fills (next_try) WHERE done_at IS NULL;

-- Позиции, накопленные до этой миграции, тоже получают цены.
INSERT INTO trade_price_fills (position_id, field, ticker, at)
SELECT id, 'entry', ticker, opened_at FROM trade_positions
WHERE entry_price IS NULL AND ticker <> '?'
ON CONFLICT DO NOTHING;

INSERT INTO trade_price_fills (position_id, field, ticker, at)
SELECT id, 'close', ticker, closed_at FROM trade_positions
WHERE status = 'closed' AND close_price IS NULL AND closed_at IS NOT NULL AND ticker <> '?'
ON CONFLICT DO NOTHING;
