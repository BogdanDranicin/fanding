ALTER TABLE funding_snapshots
    ADD COLUMN IF NOT EXISTS forex_funding NUMERIC(18,8);
