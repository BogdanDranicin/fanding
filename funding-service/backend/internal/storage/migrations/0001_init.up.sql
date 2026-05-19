CREATE TABLE IF NOT EXISTS ticks (
    timestamp   TIMESTAMPTZ   NOT NULL,
    symbol      TEXT          NOT NULL,
    price       NUMERIC(18,8) NOT NULL,
    volume      NUMERIC(18,8) NOT NULL DEFAULT 0,
    kind        TEXT          NOT NULL,
    source      TEXT          NOT NULL DEFAULT ''
);

SELECT create_hypertable('ticks', 'timestamp', if_not_exists => TRUE);
CREATE INDEX IF NOT EXISTS idx_ticks_symbol_ts ON ticks (symbol, timestamp DESC);

CREATE TABLE IF NOT EXISTS funding_snapshots (
    timestamp     TIMESTAMPTZ   NOT NULL,
    symbol        TEXT          NOT NULL,
    vwap          NUMERIC(18,8),
    last_price    NUMERIC(18,8),
    forex_funding NUMERIC(18,8),
    moex_funding  NUMERIC(18,8),
    cb_funding    NUMERIC(18,8),
    official_rate NUMERIC(18,8)
);

SELECT create_hypertable('funding_snapshots', 'timestamp', if_not_exists => TRUE);

CREATE TABLE IF NOT EXISTS users (
    id                BIGSERIAL    PRIMARY KEY,
    telegram_chat_id  BIGINT       UNIQUE,
    telegram_username TEXT,
    link_token        TEXT         UNIQUE,
    created_at        TIMESTAMPTZ  DEFAULT now()
);

CREATE TABLE IF NOT EXISTS cb_publications (
    date        DATE          PRIMARY KEY,
    usd_rate    NUMERIC(18,8),
    eur_rate    NUMERIC(18,8),
    detected_at TIMESTAMPTZ
);
