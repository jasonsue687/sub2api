package repository

import (
	"context"
	"encoding/json"

	"github.com/Wei-Shaw/sub2api/internal/pkg/anthropicaudit"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// A single SQL snapshot keeps the summary, variants and page consistent. Summary
// and variant counts cover the entire window; the mismatch switch filters rows only.
const anthropicRequestsSQL = `
WITH base AS MATERIALIZED (
 SELECT id, created_at, COALESCE(request_id,'') AS request_id,
 COALESCE(client_request_id,'') AS client_request_id, extra->'audit' AS audit
 FROM ops_system_logs
 WHERE component=$1 AND account_id=$2 AND created_at >= $3 AND created_at < $4
), filtered AS (
 SELECT * FROM base WHERE NOT $5::boolean OR audit->>'consistency'='mismatch'
), page AS (
 SELECT * FROM filtered ORDER BY created_at DESC,id DESC LIMIT $6 OFFSET $7
), variants AS (
 SELECT audit->>'identity_signature' AS signature, count(*) AS count,
 max(audit->'headers'->>'user-agent') AS user_agent,
 max(audit->>'cc_entrypoint') AS entrypoint,
 max(substring(audit->>'cc_version' from '^\d+\.\d+\.\d+')) AS version,
 max(audit->>'device_hash') AS device_hash,
 min(created_at) AS first_seen,max(created_at) AS last_seen
 FROM base GROUP BY audit->>'identity_signature' ORDER BY count(*) DESC,audit->>'identity_signature' LIMIT 20
)
SELECT jsonb_build_object(
 'summary', (SELECT jsonb_build_object(
  'attempts',count(*),
  'correlated_requests',count(DISTINCT NULLIF(client_request_id,'')),
  'uncorrelated_attempts',count(*) FILTER (WHERE client_request_id=''),
  'matched',count(*) FILTER (WHERE audit->>'consistency'='matched'),
  'mismatched',count(*) FILTER (WHERE audit->>'consistency'='mismatch'),
  'unknown',count(*) FILTER (WHERE audit->>'consistency'='unknown'),
  'http_failures',count(*) FILTER (WHERE (audit->>'status')::int >= 400),
  'transport_errors',count(*) FILTER (WHERE COALESCE(audit->>'error_class','')<>''),
  'identity_variants',count(DISTINCT audit->>'identity_signature'),
  'parameter_variants',count(DISTINCT NULLIF(audit->>'parameter_signature',''))
 ) FROM base),
 'total',(SELECT count(*) FROM filtered),
 'records',COALESCE((SELECT jsonb_agg(to_jsonb(page) ORDER BY created_at DESC,id DESC) FROM page),'[]'::jsonb),
 'variants',COALESCE((SELECT jsonb_agg(to_jsonb(variants) ORDER BY count DESC,signature) FROM variants),'[]'::jsonb)
)`

func (r *opsRepository) ListAnthropicRequests(ctx context.Context, f *service.AnthropicRequestFilter) (*service.AnthropicRequestList, error) {
	var raw []byte
	err := r.db.QueryRowContext(ctx, anthropicRequestsSQL, anthropicaudit.Component, f.AccountID, f.StartTime, f.EndTime, f.OnlyMismatch, f.PageSize, (f.Page-1)*f.PageSize).Scan(&raw)
	if err != nil {
		return nil, err
	}
	var result service.AnthropicRequestList
	if err = json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	return &result, nil
}
