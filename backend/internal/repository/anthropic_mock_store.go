package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/anthropicmock"
)

const (
	anthropicMockGetSettingSQL = `SELECT value FROM settings WHERE key = $1`
	anthropicMockSetSettingSQL = `
INSERT INTO settings (key, value, updated_at)
VALUES ($1, $2, NOW())
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = NOW()`
	anthropicMockInsertSampleSQL = `
INSERT INTO anthropic_inbound_samples
    (dedupe_key, method, path, client_label, content_type, headers, body, body_sha256, created_at)
VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7, $8, $9)
ON CONFLICT (dedupe_key) DO NOTHING`
	anthropicMockCountSamplesSQL = `SELECT COUNT(*) FROM anthropic_inbound_samples`
	anthropicMockListSamplesSQL  = `
SELECT id, created_at, dedupe_key, method, path, client_label, content_type, headers, body, body_sha256
FROM anthropic_inbound_samples
ORDER BY id DESC
LIMIT $1 OFFSET $2`
	anthropicMockInsertOutboundSQL = `
INSERT INTO anthropic_mock_outbound
    (account_id, method, url, headers, body, body_truncated, mock_reason, status_code, created_at)
VALUES ($1, $2, $3, $4::jsonb, $5, $6, $7, $8, $9)`
	anthropicMockCountOutboundSQL = `SELECT COUNT(*) FROM anthropic_mock_outbound`
	anthropicMockListOutboundSQL  = `
SELECT id, created_at, account_id, method, url, headers, body, body_truncated, mock_reason, status_code
FROM anthropic_mock_outbound
ORDER BY id DESC
LIMIT $1 OFFSET $2`
)

type anthropicMockStore struct {
	db *sql.DB
}

// NewAnthropicMockStore persists mock settings, the inbound corpus, and outbound captures.
func NewAnthropicMockStore(db *sql.DB) anthropicmock.Store {
	return &anthropicMockStore{db: db}
}

func (s *anthropicMockStore) GetSetting(ctx context.Context, key string) (string, bool, error) {
	if s == nil || s.db == nil {
		return "", false, errors.New("anthropic mock store is not configured")
	}
	var value string
	err := s.db.QueryRowContext(ctx, anthropicMockGetSettingSQL, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return value, true, nil
}

func (s *anthropicMockStore) SetSetting(ctx context.Context, key, value string) error {
	if s == nil || s.db == nil {
		return errors.New("anthropic mock store is not configured")
	}
	_, err := s.db.ExecContext(ctx, anthropicMockSetSettingSQL, key, value)
	return err
}

func (s *anthropicMockStore) InsertSample(ctx context.Context, sample anthropicmock.Sample) (bool, error) {
	if s == nil || s.db == nil {
		return false, errors.New("anthropic mock store is not configured")
	}
	created := sample.CreatedAt
	if created.IsZero() {
		created = time.Now().UTC()
	}
	res, err := s.db.ExecContext(ctx, anthropicMockInsertSampleSQL,
		sample.DedupeKey, sample.Method, sample.Path, sample.ClientLabel, sample.ContentType,
		encodeMockHeaders(sample.Headers), sample.Body, sample.BodySHA256, created)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

func (s *anthropicMockStore) CountSamples(ctx context.Context) (int64, error) {
	return s.count(ctx, anthropicMockCountSamplesSQL)
}

func (s *anthropicMockStore) ListSamples(ctx context.Context, offset, limit int) ([]anthropicmock.Sample, int64, error) {
	total, err := s.CountSamples(ctx)
	if err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx, anthropicMockListSamplesSQL, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()
	items := make([]anthropicmock.Sample, 0)
	for rows.Next() {
		var item anthropicmock.Sample
		var headers []byte
		if err := rows.Scan(&item.ID, &item.CreatedAt, &item.DedupeKey, &item.Method, &item.Path, &item.ClientLabel, &item.ContentType, &headers, &item.Body, &item.BodySHA256); err != nil {
			return nil, 0, err
		}
		item.Headers = decodeMockHeaders(headers)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

func (s *anthropicMockStore) InsertOutbound(ctx context.Context, item anthropicmock.Outbound) error {
	if s == nil || s.db == nil {
		return errors.New("anthropic mock store is not configured")
	}
	created := item.CreatedAt
	if created.IsZero() {
		created = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, anthropicMockInsertOutboundSQL,
		item.AccountID, item.Method, item.URL, encodeMockHeaders(item.Headers), item.Body,
		item.BodyTruncated, item.MockReason, item.StatusCode, created)
	return err
}

func (s *anthropicMockStore) ListOutbound(ctx context.Context, offset, limit int) ([]anthropicmock.Outbound, int64, error) {
	total, err := s.count(ctx, anthropicMockCountOutboundSQL)
	if err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx, anthropicMockListOutboundSQL, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()
	items := make([]anthropicmock.Outbound, 0)
	for rows.Next() {
		var item anthropicmock.Outbound
		var headers []byte
		if err := rows.Scan(&item.ID, &item.CreatedAt, &item.AccountID, &item.Method, &item.URL, &headers, &item.Body, &item.BodyTruncated, &item.MockReason, &item.StatusCode); err != nil {
			return nil, 0, err
		}
		item.Headers = decodeMockHeaders(headers)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

func (s *anthropicMockStore) count(ctx context.Context, query string) (int64, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("anthropic mock store is not configured")
	}
	var n int64
	if err := s.db.QueryRowContext(ctx, query).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

func encodeMockHeaders(h map[string]string) []byte {
	if h == nil {
		h = map[string]string{}
	}
	encoded, err := json.Marshal(h)
	if err != nil {
		return []byte(`{}`)
	}
	return encoded
}

func decodeMockHeaders(raw []byte) map[string]string {
	if len(raw) == 0 {
		return map[string]string{}
	}
	var out map[string]string
	if json.Unmarshal(raw, &out) != nil || out == nil {
		return map[string]string{}
	}
	return out
}
