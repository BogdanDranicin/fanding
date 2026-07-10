-- Расширяем cb_publications до полного аудита публикации курсов ЦБ:
-- время публикации (detected_at уже есть), рассчитанные фандинги, прогноз и
-- диагностика гонки каналов. Одна строка на календарный день публикации (МСК).
ALTER TABLE cb_publications
    ADD COLUMN IF NOT EXISTS updated_at             TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS cny_rate               NUMERIC(18,8),
    ADD COLUMN IF NOT EXISTS cb_funding_usd         NUMERIC(18,8),
    ADD COLUMN IF NOT EXISTS cb_funding_eur         NUMERIC(18,8),
    ADD COLUMN IF NOT EXISTS cny_funding            NUMERIC(18,8),
    ADD COLUMN IF NOT EXISTS predicted_funding_usd  NUMERIC(18,8),
    ADD COLUMN IF NOT EXISTS predicted_funding_eur  NUMERIC(18,8),
    ADD COLUMN IF NOT EXISTS predicted_cb_rate_usd  NUMERIC(18,8),
    ADD COLUMN IF NOT EXISTS predicted_cb_rate_eur  NUMERIC(18,8),
    ADD COLUMN IF NOT EXISTS winner_channel         TEXT,
    ADD COLUMN IF NOT EXISTS winner_latency_ms      BIGINT;
