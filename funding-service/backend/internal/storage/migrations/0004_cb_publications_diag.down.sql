ALTER TABLE cb_publications
    DROP COLUMN IF EXISTS settl_vwap_usd,
    DROP COLUMN IF EXISTS settl_vwap_eur,
    DROP COLUMN IF EXISTS moex_funding_usd,
    DROP COLUMN IF EXISTS moex_funding_eur,
    DROP COLUMN IF EXISTS cb_funding_no_deadband_usd,
    DROP COLUMN IF EXISTS cb_funding_no_deadband_eur;
