-- Нормальная авторизация через Telegram + роли (06.08.2026).
--
-- Было: браузер держал пару id+link_token в sessionStorage, то есть терял её при
-- закрытии вкладки, и уже привязанному пользователю сайт снова показывал виджет
-- «Привязать Telegram». Теперь личность браузера — отдельная строка sessions,
-- а users остаётся аккаунтом (одна строка на Telegram-чат). Несколько браузеров
-- одного человека указывают на один аккаунт: при повторной привязке чата сессии
-- переносятся на уже существующий аккаунт (см. Store.LinkTelegramChat).
--
-- is_admin выдаётся по списку TELEGRAM_ADMINS и пересчитывается при каждом /start:
-- вкладки «Журнал» и «Скорость» доступны только админам.
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS is_admin BOOLEAN NOT NULL DEFAULT false;

CREATE TABLE IF NOT EXISTS sessions (
    token        TEXT        PRIMARY KEY,
    user_id      BIGINT      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions (user_id);
