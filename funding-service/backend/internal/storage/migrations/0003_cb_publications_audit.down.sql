ALTER TABLE cb_publications
    DROP COLUMN IF EXISTS updated_at,
    DROP COLUMN IF EXISTS cny_rate,
    DROP COLUMN IF EXISTS cb_funding_usd,
    DROP COLUMN IF EXISTS cb_funding_eur,
    DROP COLUMN IF EXISTS cny_funding,
    DROP COLUMN IF EXISTS predicted_funding_usd,
    DROP COLUMN IF EXISTS predicted_funding_eur,
    DROP COLUMN IF EXISTS predicted_cb_rate_usd,
    DROP COLUMN IF EXISTS predicted_cb_rate_eur,
    DROP COLUMN IF EXISTS winner_channel,
    DROP COLUMN IF EXISTS winner_latency_ms;
