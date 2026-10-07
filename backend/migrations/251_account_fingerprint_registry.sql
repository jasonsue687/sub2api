-- Stages 1/2 only: observations and administrative bindings. The gateway does
-- not consult either table when choosing its outbound identity.
CREATE TABLE account_fingerprint_records (
    id BIGSERIAL PRIMARY KEY,
    fingerprint_key VARCHAR(64) NOT NULL UNIQUE,
    source VARCHAR(16) NOT NULL CHECK (source IN ('request', 'cache')),
    fingerprint JSONB NOT NULL,
    incoming_headers JSONB NOT NULL DEFAULT '{}',
    client_id_origin VARCHAR(16) NOT NULL CHECK (client_id_origin IN ('client', 'generated', 'cache')),
    source_account_id BIGINT REFERENCES accounts(id) ON DELETE SET NULL,
    request_count BIGINT NOT NULL DEFAULT 0 CHECK (request_count >= 0),
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_account_fingerprint_records_last_seen ON account_fingerprint_records(last_seen_at DESC, id DESC);

CREATE TABLE account_fingerprint_bindings (
    account_id BIGINT PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
    fingerprint_id BIGINT NOT NULL REFERENCES account_fingerprint_records(id) ON DELETE RESTRICT,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_account_fingerprint_bindings_fingerprint ON account_fingerprint_bindings(fingerprint_id);
