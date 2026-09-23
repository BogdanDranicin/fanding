-- Сделки авторских каналов (23.09.2026).
--
-- tg-repost присылает сюда каждое сообщение каналов со сделками, бэкенд
-- разбирает его правилами по ключевым словам и ведёт позиции. Сообщение
-- хранится целиком: по нему видно, что именно понял разбор, и по нему же
-- ответ «Закрыл» узнаёт, к какой бумаге относится.
CREATE TABLE IF NOT EXISTS trade_messages (
    id            BIGSERIAL   PRIMARY KEY,
    channel_id    BIGINT      NOT NULL,
    channel_title TEXT        NOT NULL,
    msg_id        BIGINT      NOT NULL,
    posted_at     TIMESTAMPTZ NOT NULL,
    text          TEXT        NOT NULL DEFAULT '',
    has_media     BOOLEAN     NOT NULL DEFAULT FALSE,
    reply_to      BIGINT,
    tickers       TEXT[]      NOT NULL DEFAULT '{}',
    received_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (channel_id, msg_id)
);

CREATE TABLE IF NOT EXISTS trade_positions (
    id          BIGSERIAL        PRIMARY KEY,
    author      TEXT             NOT NULL,
    book        TEXT             NOT NULL DEFAULT '',
    ticker      TEXT             NOT NULL,
    direction   TEXT             NOT NULL,
    size        TEXT             NOT NULL DEFAULT '',
    entry_price DOUBLE PRECISION,
    stop        TEXT             NOT NULL DEFAULT '',
    targets     TEXT             NOT NULL DEFAULT '',
    status      TEXT             NOT NULL DEFAULT 'open',
    opened_at   TIMESTAMPTZ      NOT NULL,
    closed_at   TIMESTAMPTZ,
    close_price DOUBLE PRECISION,
    note        TEXT             NOT NULL DEFAULT '',
    manual      BOOLEAN          NOT NULL DEFAULT FALSE,
    last_text   TEXT             NOT NULL DEFAULT '',
    updated_at  TIMESTAMPTZ      NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_trade_positions_open ON trade_positions (author, status);

-- Лента событий: что и почему случилось с позициями. Из неё же страница
-- берёт всплывающие уведомления о новых сделках.
CREATE TABLE IF NOT EXISTS trade_events (
    id          BIGSERIAL        PRIMARY KEY,
    message_id  BIGINT           REFERENCES trade_messages (id) ON DELETE SET NULL,
    position_id BIGINT           REFERENCES trade_positions (id) ON DELETE SET NULL,
    author      TEXT             NOT NULL,
    ticker      TEXT             NOT NULL DEFAULT '',
    action      TEXT             NOT NULL,
    direction   TEXT             NOT NULL DEFAULT '',
    size        TEXT             NOT NULL DEFAULT '',
    price       DOUBLE PRECISION,
    clause      TEXT             NOT NULL DEFAULT '',
    manual      BOOLEAN          NOT NULL DEFAULT FALSE,
    silent      BOOLEAN          NOT NULL DEFAULT FALSE,
    at          TIMESTAMPTZ      NOT NULL,
    created_at  TIMESTAMPTZ      NOT NULL DEFAULT now()
);
