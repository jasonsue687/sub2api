-- Legacy InitializeDefaults inserted disabled settings with override=false.
-- Only discard that unused seed; an explicit admin save (override=true),
-- including enabled=false, must survive this migration and every restart.
DELETE FROM settings
WHERE key IN (
    'strict_session_binding_enabled', 'strict_session_session_header',
    'strict_session_same_account_retry_limit', 'strict_session_fallback_order',
    'strict_session_fallback_group_id', 'strict_session_third_party_enabled',
    'strict_session_third_party_base_url', 'strict_session_third_party_api_key',
    'strict_session_third_party_timeout_seconds'
)
AND EXISTS (SELECT 1 FROM settings WHERE key = 'strict_session_binding_override' AND value = 'false');

DELETE FROM settings WHERE key = 'strict_session_binding_override';
