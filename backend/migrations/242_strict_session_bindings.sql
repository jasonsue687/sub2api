-- Permanent Claude Messages session -> subscription account bindings.
-- Rows must survive account deletion, cache flushes, restarts, and feature
-- rollback. There is intentionally no foreign key and no TTL column.
-- "Permanent" means the application does not expire rows; operators still
-- need to back up this table with the rest of the database.

CREATE TABLE IF NOT EXISTS strict_session_bindings (
    id                   BIGSERIAL PRIMARY KEY,
    binding_key          CHAR(64) NOT NULL,
    session_fingerprint  VARCHAR(16) NOT NULL,
    account_id           BIGINT NOT NULL CHECK (account_id > 0),
    protocol             VARCHAR(32) NOT NULL,
    api_key_id           BIGINT NOT NULL CHECK (api_key_id > 0),
    group_id             BIGINT NULL,
    end_user_fingerprint VARCHAR(16) NULL,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Unique binding identity. Concurrent first requests collapse to one row.
CREATE UNIQUE INDEX IF NOT EXISTS uq_strict_session_bindings_key
    ON strict_session_bindings (binding_key);

-- Operational lookup only. Deleting an account must not cascade to this index.
CREATE INDEX IF NOT EXISTS idx_strict_session_bindings_account_id
    ON strict_session_bindings (account_id);

COMMENT ON TABLE strict_session_bindings IS
    'Permanent Claude Messages session bindings. Do not TTL-expire or cascade-delete with accounts.';

COMMENT ON COLUMN strict_session_bindings.binding_key IS
    'SHA-256 of tenant, protocol, optional end user, and session id. The raw session id is not stored.';

COMMENT ON COLUMN strict_session_bindings.account_id IS
    'Originally assigned subscription account. No foreign key: a deleted account must remain distinguishable from a session that was never assigned.';

COMMENT ON COLUMN strict_session_bindings.session_fingerprint IS
    'Short hash for logs. Not the session id and not reversible to the prompt.';
