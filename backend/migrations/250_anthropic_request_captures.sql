-- Inbound and outbound Anthropic request captures (redacted JSON).
-- Kept off ops_system_logs so list queries never read request bodies and the
-- shared log table is not bloated by up-to-64KiB documents.
-- Number 250 leaves 242-249 for in-flight work (including migration 242).

CREATE TABLE IF NOT EXISTS anthropic_request_captures (
    id BIGSERIAL PRIMARY KEY,
    direction TEXT NOT NULL,
    client_request_id TEXT NOT NULL DEFAULT '',
    request_id TEXT NOT NULL DEFAULT '',
    account_id BIGINT,
    user_id BIGINT,
    api_key_id BIGINT,
    api_key_name TEXT NOT NULL DEFAULT '',
    username TEXT NOT NULL DEFAULT '',
    endpoint TEXT NOT NULL DEFAULT '',
    client_path TEXT NOT NULL DEFAULT '',
    model TEXT NOT NULL DEFAULT '',
    stream BOOLEAN,
    attempt_seq INT,
    retry_reason TEXT NOT NULL DEFAULT '',
    account_switch_count INT NOT NULL DEFAULT 0,
    attempt_count INT NOT NULL DEFAULT 0,
    status INT NOT NULL DEFAULT 0,
    error_class TEXT NOT NULL DEFAULT '',
    upstream_request_id TEXT NOT NULL DEFAULT '',
    duration_ms BIGINT NOT NULL DEFAULT 0,
    headers_ms BIGINT NOT NULL DEFAULT 0,
    headers JSONB,
    body JSONB,
    body_state TEXT NOT NULL DEFAULT '',
    summary JSONB,
    consistency TEXT NOT NULL DEFAULT '',
    truncated BOOLEAN NOT NULL DEFAULT FALSE,
    original_bytes INT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_anthropic_captures_created_at
    ON anthropic_request_captures (created_at);

CREATE INDEX IF NOT EXISTS idx_anthropic_captures_client_request
    ON anthropic_request_captures (client_request_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_anthropic_captures_account_created
    ON anthropic_request_captures (account_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_anthropic_captures_request_id
    ON anthropic_request_captures (request_id);
