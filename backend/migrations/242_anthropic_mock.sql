-- Anthropic mock switch corpus and outbound capture.
-- The switch itself lives in settings and is created on first admin toggle.
-- Samples are deduplicated by dedupe_key. Credential headers are not stored.

CREATE TABLE IF NOT EXISTS anthropic_inbound_samples (
    id            BIGSERIAL PRIMARY KEY,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    dedupe_key    TEXT NOT NULL,
    method        TEXT NOT NULL,
    path          TEXT NOT NULL,
    client_label  TEXT NOT NULL DEFAULT '',
    content_type  TEXT NOT NULL DEFAULT '',
    headers       JSONB NOT NULL DEFAULT '{}'::jsonb,
    body          BYTEA,
    body_sha256   TEXT NOT NULL,
    CONSTRAINT anthropic_inbound_samples_dedupe_key_key UNIQUE (dedupe_key)
);

CREATE INDEX IF NOT EXISTS anthropic_inbound_samples_created_at_idx
    ON anthropic_inbound_samples (created_at DESC);

CREATE TABLE IF NOT EXISTS anthropic_mock_outbound (
    id             BIGSERIAL PRIMARY KEY,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    account_id     BIGINT NOT NULL DEFAULT 0,
    method         TEXT NOT NULL,
    url            TEXT NOT NULL,
    headers        JSONB NOT NULL DEFAULT '{}'::jsonb,
    body           BYTEA,
    body_truncated BOOLEAN NOT NULL DEFAULT FALSE,
    mock_reason    TEXT NOT NULL,
    status_code    INT NOT NULL
);

CREATE INDEX IF NOT EXISTS anthropic_mock_outbound_created_at_idx
    ON anthropic_mock_outbound (created_at DESC);
