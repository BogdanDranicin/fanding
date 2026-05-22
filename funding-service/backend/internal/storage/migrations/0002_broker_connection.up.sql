CREATE TABLE IF NOT EXISTS broker_connection (
    id          SERIAL      PRIMARY KEY,
    sso_session TEXT        NOT NULL,
    device_id   TEXT        NOT NULL,
    expires_at  TIMESTAMPTZ NOT NULL,
    updated_at  TIMESTAMPTZ DEFAULT now()
);
