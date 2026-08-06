-- Forex funding убран из сервиса (06.08.2026). Источник TwelveData в проде так и
-- не был подключён (роутинг в cmd/server никогда его не заводил), поэтому колонка
-- всегда оставалась NULL — сносим её вместе с кодом, чтобы схема не врала.
ALTER TABLE funding_snapshots
    DROP COLUMN IF EXISTS forex_funding;
