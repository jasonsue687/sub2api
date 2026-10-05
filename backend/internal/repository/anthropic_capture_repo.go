package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/anthropicaudit"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

const insertAnthropicCaptureSQL = `
INSERT INTO anthropic_request_captures (
  direction, client_request_id, request_id, account_id, user_id, api_key_id,
  api_key_name, username, endpoint, client_path, model, stream, attempt_seq,
  retry_reason, account_switch_count, attempt_count, status, error_class,
  upstream_request_id, duration_ms, headers_ms, headers, body, body_state,
  summary, consistency, truncated, original_bytes, created_at
) VALUES (
  $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,
  $22::jsonb,$23::jsonb,$24,$25::jsonb,$26,$27,$28,$29
)`

func insertAnthropicCapture(ctx context.Context, db *sql.DB, rec anthropicaudit.Record) error {
	if db == nil {
		return fmt.Errorf("nil db")
	}
	created := rec.CreatedAt
	if created.IsZero() {
		created = time.Now().UTC()
	}
	_, err := db.ExecContext(ctx, insertAnthropicCaptureSQL,
		rec.Direction, rec.ClientRequestID, rec.RequestID,
		captureNullInt(rec.AccountID), captureNullInt(rec.UserID), captureNullInt(rec.APIKeyID),
		rec.APIKeyName, rec.Username, rec.Endpoint, rec.ClientPath, rec.Model, captureNullBool(rec.Stream),
		captureNullAttempt(rec.Direction, rec.AttemptSeq), rec.RetryReason, rec.AccountSwitchCount, rec.AttemptCount,
		rec.Status, rec.ErrorClass, rec.UpstreamRequestID, rec.DurationMS, rec.HeadersMS,
		captureNullJSON(rec.Headers), captureNullJSON(rec.Body), rec.BodyState, captureNullJSON(rec.Summary),
		rec.Consistency, rec.Truncated, rec.OriginalBytes, created.UTC(),
	)
	return err
}

func captureNullInt(v int64) any {
	if v <= 0 {
		return nil
	}
	return v
}

func captureNullBool(v *bool) any {
	if v == nil {
		return nil
	}
	return *v
}

func captureNullAttempt(direction string, seq int) any {
	if direction != anthropicaudit.DirectionOutbound || seq <= 0 {
		return nil
	}
	return seq
}

func captureNullJSON(raw []byte) any {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	return string(raw)
}

// List rows never select headers or bodies. Detail does.
const listAnthropicSessionsSQL = `
WITH rows AS (
  SELECT c.id, c.direction, c.client_request_id, c.request_id, c.account_id,
    COALESCE(acc.name, '') AS account_name, c.user_id, c.api_key_id, c.api_key_name,
    c.username, c.endpoint, c.client_path, c.model, c.stream, c.attempt_seq,
    c.attempt_count, c.status, c.error_class, c.duration_ms, c.consistency, c.truncated, c.created_at
  FROM anthropic_request_captures c
  LEFT JOIN accounts acc ON acc.id = c.account_id
  WHERE c.created_at >= $1 AND c.created_at < $2
),
keyed AS (
  SELECT *, CASE WHEN client_request_id <> '' THEN 'c:' || client_request_id ELSE 'o:' || id::text END AS gkey
  FROM rows
),
inbound AS (
  SELECT DISTINCT ON (gkey) * FROM keyed WHERE direction = 'inbound'
  ORDER BY gkey, created_at DESC, id DESC
),
outbound_final AS (
  SELECT DISTINCT ON (gkey) * FROM keyed WHERE direction = 'outbound'
  ORDER BY gkey, CASE WHEN status >= 200 AND status < 300 THEN 0 ELSE 1 END, attempt_seq DESC NULLS LAST, id DESC
),
grouped AS (
  SELECT k.gkey,
    max(k.client_request_id) AS client_request_id,
    COALESCE(NULLIF(max(i.request_id), ''), max(k.request_id), '') AS request_id,
    min(k.created_at) AS created_at,
    bool_or(k.direction = 'inbound') AS has_inbound,
    count(*) FILTER (WHERE k.direction = 'outbound') AS outbound_rows,
    COALESCE(max(i.attempt_count), 0) AS inbound_attempt_count,
    COALESCE(NULLIF(max(i.endpoint), ''), NULLIF(max(k.client_path), ''), max(k.endpoint), '') AS endpoint,
    COALESCE(max(i.model), '') AS inbound_model,
    COALESCE(max(f.model), '') AS outbound_model,
    COALESCE(bool_or(i.stream), bool_or(k.stream) FILTER (WHERE k.direction = 'outbound'), false) AS stream,
    COALESCE(max(i.status), max(f.status), 0) AS status,
    COALESCE(NULLIF(max(i.error_class), ''), NULLIF(max(f.error_class), ''), '') AS error_class,
    COALESCE(max(f.account_id), max(i.account_id), 0) AS account_id,
    COALESCE(NULLIF(max(f.account_name), ''), NULLIF(max(i.account_name), ''), '') AS account_name,
    COALESCE(max(i.duration_ms), max(f.duration_ms), 0) AS duration_ms,
    COALESCE(max(i.user_id), max(k.user_id), 0) AS user_id,
    COALESCE(max(i.api_key_id), max(k.api_key_id), 0) AS api_key_id,
    COALESCE(NULLIF(max(i.api_key_name), ''), max(k.api_key_name), '') AS api_key_name,
    COALESCE(NULLIF(max(i.username), ''), max(k.username), '') AS username,
    CASE
      WHEN bool_or(k.consistency = 'mismatch') FILTER (WHERE k.direction = 'outbound') THEN 'mismatch'
      WHEN bool_or(k.consistency <> 'matched') FILTER (WHERE k.direction = 'outbound') THEN 'unknown'
      WHEN bool_or(k.consistency = 'matched') FILTER (WHERE k.direction = 'outbound') THEN 'matched'
      ELSE 'unknown'
    END AS outbound_consistency,
    bool_or(k.truncated) AS truncated
  FROM keyed k
  LEFT JOIN inbound i ON i.gkey = k.gkey
  LEFT JOIN outbound_final f ON f.gkey = k.gkey
  GROUP BY k.gkey
),
scored AS (
  SELECT *, GREATEST(outbound_rows, inbound_attempt_count) AS attempt_count,
    CASE
      WHEN outbound_consistency = 'mismatch' THEN 'mismatch'
      WHEN inbound_attempt_count > outbound_rows THEN 'unknown'
      ELSE outbound_consistency
    END AS consistency
  FROM grouped
),
windowed AS (
  SELECT * FROM scored
  WHERE $3::bigint = 0 OR EXISTS (
    SELECT 1 FROM keyed kx WHERE kx.gkey = scored.gkey AND kx.account_id = $3
  )
),
filtered AS (
  SELECT * FROM windowed
  WHERE (NOT $4::boolean OR attempt_count > 1)
    AND (NOT $5::boolean OR status >= 400 OR error_class <> '')
    AND (NOT $6::boolean OR consistency = 'mismatch')
    AND ($7 = '' OR inbound_model = $7 OR outbound_model = $7)
    AND ($8 = '' OR client_request_id ILIKE $8 OR request_id ILIKE $8 OR api_key_name ILIKE $8 OR username ILIKE $8
         OR ($9::bigint > 0 AND (api_key_id = $9 OR user_id = $9)))
)
SELECT jsonb_build_object(
  'summary', (SELECT jsonb_build_object(
    'inbound_requests', count(*) FILTER (WHERE has_inbound),
    'outbound_attempts', COALESCE(sum(outbound_rows), 0),
    'multi_attempts', count(*) FILTER (WHERE attempt_count > 1),
    'failed_requests', count(*) FILTER (WHERE status >= 400 OR error_class <> '')
  ) FROM windowed),
  'total', (SELECT count(*) FROM filtered),
  'records', COALESCE((SELECT jsonb_agg(jsonb_build_object(
    'client_request_id', client_request_id,
    'request_id', request_id,
    'created_at', created_at,
    'api_key_id', api_key_id,
    'api_key_name', api_key_name,
    'username', username,
    'user_id', user_id,
    'endpoint', endpoint,
    'inbound_model', inbound_model,
    'outbound_model', outbound_model,
    'stream', stream,
    'status', status,
    'account_id', account_id,
    'account_name', account_name,
    'attempt_count', attempt_count,
    'duration_ms', duration_ms,
    'has_inbound', has_inbound,
    'has_outbound', outbound_rows > 0,
    'error_class', error_class,
    'consistency', consistency,
    'truncated', truncated
  ) ORDER BY created_at DESC) FROM (
    SELECT * FROM filtered ORDER BY created_at DESC, gkey DESC LIMIT $10 OFFSET $11
  ) page), '[]'::jsonb),
  'accounts', COALESCE((SELECT jsonb_agg(jsonb_build_object('id', account_id, 'name', account_name) ORDER BY account_id)
    FROM (SELECT DISTINCT account_id, account_name FROM rows WHERE account_id IS NOT NULL AND account_id > 0) a), '[]'::jsonb),
  'models', COALESCE((SELECT jsonb_agg(model ORDER BY model) FROM (SELECT DISTINCT model FROM rows WHERE model <> '') m), '[]'::jsonb)
)`

func (r *opsRepository) ListAnthropicSessions(ctx context.Context, f *service.AnthropicSessionFilter) (*service.AnthropicSessionList, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("nil ops repository")
	}
	var raw []byte
	err := r.db.QueryRowContext(ctx, listAnthropicSessionsSQL,
		f.StartTime, f.EndTime, f.AccountID, f.OnlyMulti, f.OnlyFailed, f.OnlyMismatch,
		f.Model, f.QueryLike, f.QueryID, f.PageSize, (f.Page-1)*f.PageSize,
	).Scan(&raw)
	if err != nil {
		return nil, err
	}
	var result service.AnthropicSessionList
	if err = json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	if result.Records == nil {
		result.Records = []service.AnthropicSessionRow{}
	}
	if result.Accounts == nil {
		result.Accounts = []service.AnthropicAccountOption{}
	}
	if result.Models == nil {
		result.Models = []string{}
	}
	return &result, nil
}

const getAnthropicSessionSQL = `
SELECT c.direction, c.client_request_id, c.request_id, COALESCE(c.account_id, 0), COALESCE(acc.name, ''),
  COALESCE(c.user_id, 0), COALESCE(c.api_key_id, 0), c.api_key_name, c.username, c.endpoint, c.client_path,
  c.model, c.stream, COALESCE(c.attempt_seq, 0), c.retry_reason, c.account_switch_count, c.attempt_count,
  c.status, c.error_class, c.upstream_request_id, c.duration_ms, c.headers_ms, c.headers, c.body, c.body_state,
  c.summary, c.consistency, c.truncated, c.original_bytes, c.created_at
FROM anthropic_request_captures c
LEFT JOIN accounts acc ON acc.id = c.account_id
WHERE c.client_request_id = $1 AND c.created_at >= $2
ORDER BY CASE WHEN c.direction = 'inbound' THEN 0 ELSE 1 END, c.attempt_seq NULLS LAST, c.id`

func (r *opsRepository) GetAnthropicSession(ctx context.Context, clientRequestID string, since time.Time) (*service.AnthropicSessionDetail, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("nil ops repository")
	}
	rows, err := r.db.QueryContext(ctx, getAnthropicSessionSQL, clientRequestID, since)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	detail := &service.AnthropicSessionDetail{}
	for rows.Next() {
		var item service.AnthropicCapture
		var stream sql.NullBool
		var headers, body, summary sql.NullString
		if err = rows.Scan(
			&item.Direction, &item.ClientRequestID, &item.RequestID, &item.AccountID, &item.AccountName,
			&item.UserID, &item.APIKeyID, &item.APIKeyName, &item.Username, &item.Endpoint, &item.ClientPath,
			&item.Model, &stream, &item.AttemptSeq, &item.RetryReason, &item.AccountSwitchCount, &item.AttemptCount,
			&item.Status, &item.ErrorClass, &item.UpstreamRequestID, &item.DurationMS, &item.HeadersMS,
			&headers, &body, &item.BodyState, &summary, &item.Consistency, &item.Truncated, &item.OriginalBytes, &item.CreatedAt,
		); err != nil {
			return nil, err
		}
		if stream.Valid {
			item.Stream = &stream.Bool
		}
		if headers.Valid {
			item.Headers = json.RawMessage(headers.String)
		}
		if body.Valid {
			item.Body = json.RawMessage(body.String)
		}
		if summary.Valid {
			item.Summary = json.RawMessage(summary.String)
		}
		if item.Direction == anthropicaudit.DirectionInbound && detail.Inbound == nil {
			copied := item
			detail.Inbound = &copied
			continue
		}
		if item.Direction == anthropicaudit.DirectionOutbound {
			detail.Attempts = append(detail.Attempts, item)
		}
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if detail.Inbound == nil && len(detail.Attempts) == 0 {
		return nil, sql.ErrNoRows
	}
	return detail, nil
}

func (r *opsRepository) deleteMatchingCaptures(ctx context.Context, filter *service.OpsSystemLogCleanupFilter) {
	if r == nil || r.db == nil || filter == nil {
		return
	}
	// Captures do not have these log fields. Dropping one of these predicates
	// would broaden a filtered cleanup and delete unrelated request records.
	if strings.TrimSpace(filter.Platform) != "" || strings.TrimSpace(filter.Level) != "" ||
		strings.TrimSpace(filter.Host) != "" || strings.TrimSpace(filter.Query) != "" {
		return
	}
	component := strings.TrimSpace(filter.Component)
	if component != "" && component != anthropicaudit.Component {
		return
	}
	scoped := filter.StartTime != nil || filter.EndTime != nil || strings.TrimSpace(filter.RequestID) != "" ||
		strings.TrimSpace(filter.ClientRequestID) != "" || filter.UserID != nil || filter.APIKeyID != nil ||
		filter.AccountID != nil || strings.TrimSpace(filter.Model) != ""
	if component == "" && !scoped {
		return
	}
	clauses := []string{"1=1"}
	args := []any{}
	add := func(clause string, value any) {
		args = append(args, value)
		clauses = append(clauses, fmt.Sprintf(clause, len(args)))
	}
	if filter.StartTime != nil && !filter.StartTime.IsZero() {
		add("created_at >= $%d", filter.StartTime.UTC())
	}
	if filter.EndTime != nil && !filter.EndTime.IsZero() {
		add("created_at < $%d", filter.EndTime.UTC())
	}
	if v := strings.TrimSpace(filter.RequestID); v != "" {
		add("request_id = $%d", v)
	}
	if v := strings.TrimSpace(filter.ClientRequestID); v != "" {
		add("client_request_id = $%d", v)
	}
	if filter.UserID != nil {
		add("user_id = $%d", *filter.UserID)
	}
	if filter.APIKeyID != nil {
		add("api_key_id = $%d", *filter.APIKeyID)
	}
	if filter.AccountID != nil {
		add("account_id = $%d", *filter.AccountID)
	}
	if v := strings.TrimSpace(filter.Model); v != "" {
		add("model = $%d", v)
	}
	query := "DELETE FROM anthropic_request_captures WHERE " + strings.Join(clauses, " AND ")
	if _, err := r.db.ExecContext(ctx, query, args...); err != nil && !isMissingCaptureTable(err) {
		return
	}
}

func isMissingCaptureTable(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "anthropic_request_captures") && strings.Contains(text, "does not exist")
}
