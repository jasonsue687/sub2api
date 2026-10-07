package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

type accountFingerprintRepository struct {
	db  *sql.DB
	rdb *redis.Client
}

func NewAccountFingerprintRepository(db *sql.DB, rdb *redis.Client) service.AccountFingerprintStore {
	return &accountFingerprintRepository{db: db, rdb: rdb}
}

func (r *accountFingerprintRepository) Observe(ctx context.Context, record *service.FingerprintRecord) error {
	fp, err := json.Marshal(record.Fingerprint)
	if err != nil {
		return err
	}
	headers := record.IncomingHeaders
	if headers == nil {
		headers = map[string]string{}
	}
	raw, err := json.Marshal(headers)
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, `INSERT INTO account_fingerprint_records
		(fingerprint_key, source, fingerprint, incoming_headers, client_id_origin, source_account_id, request_count)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (fingerprint_key) DO UPDATE SET
		request_count = account_fingerprint_records.request_count + EXCLUDED.request_count,
		last_seen_at = GREATEST(account_fingerprint_records.last_seen_at, NOW())`, record.Key, record.Source, string(fp), string(raw), record.ClientIDOrigin, record.SourceAccountID, record.RequestCount)
	return err
}

const fingerprintRecordColumns = `f.id, f.source, f.fingerprint, f.incoming_headers,
	f.client_id_origin, f.source_account_id, f.request_count, f.first_seen_at, f.last_seen_at,
	COALESCE((SELECT jsonb_agg(jsonb_build_object('id', a.id, 'name', a.name, 'fingerprint_id', b.fingerprint_id) ORDER BY a.id)
	FROM account_fingerprint_bindings b JOIN accounts a ON a.id=b.account_id
	WHERE b.fingerprint_id=f.id AND a.deleted_at IS NULL), '[]'::jsonb)`

const fingerprintRecordFilter = `($1 = '' OR f.id::text = $1 OR f.fingerprint->>'UserAgent' ILIKE '%' || $1 || '%'
	OR f.incoming_headers->>'user-agent' ILIKE '%' || $1 || '%'
	OR f.fingerprint->>'ClientID' ILIKE '%' || $1 || '%') AND ($2 = '' OR f.source = $2)`

func (r *accountFingerprintRepository) List(ctx context.Context, page, size int, search, source string) ([]service.FingerprintRecord, int64, error) {
	var total int64
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM account_fingerprint_records f WHERE `+fingerprintRecordFilter, search, source).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.db.QueryContext(ctx, `SELECT `+fingerprintRecordColumns+` FROM account_fingerprint_records f WHERE `+fingerprintRecordFilter+` ORDER BY f.last_seen_at DESC, f.id DESC LIMIT $3 OFFSET $4`, search, source, size, (page-1)*size)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()
	items := make([]service.FingerprintRecord, 0)
	for rows.Next() {
		item, err := scanFingerprintRecord(rows)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, *item)
	}
	return items, total, rows.Err()
}

func (r *accountFingerprintRepository) Get(ctx context.Context, id int64) (*service.FingerprintRecord, error) {
	return scanFingerprintRecord(r.db.QueryRowContext(ctx, `SELECT `+fingerprintRecordColumns+` FROM account_fingerprint_records f WHERE f.id=$1`, id))
}

func scanFingerprintRecord(row interface{ Scan(...any) error }) (*service.FingerprintRecord, error) {
	var record service.FingerprintRecord
	var fp, headers, accounts []byte
	err := row.Scan(&record.ID, &record.Source, &fp, &headers, &record.ClientIDOrigin,
		&record.SourceAccountID, &record.RequestCount, &record.FirstSeenAt, &record.LastSeenAt, &accounts)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrFingerprintRecordNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(fp, &record.Fingerprint); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(headers, &record.IncomingHeaders); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(accounts, &record.BoundAccounts); err != nil {
		return nil, err
	}
	return &record, nil
}

const fingerprintEligibleAccount = `a.platform='anthropic' AND a.type='oauth' AND a.deleted_at IS NULL`

func (r *accountFingerprintRepository) Accounts(ctx context.Context, search string, page, size int) ([]service.FingerprintAccount, int64, error) {
	filter := fingerprintEligibleAccount + ` AND ($1='' OR a.id::text=$1 OR a.name ILIKE '%' || $1 || '%')`
	var total int64
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM accounts a WHERE `+filter, search).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.db.QueryContext(ctx, `SELECT a.id,a.name,b.fingerprint_id FROM accounts a
		LEFT JOIN account_fingerprint_bindings b ON b.account_id=a.id WHERE `+filter+` ORDER BY a.id LIMIT $2 OFFSET $3`, search, size, (page-1)*size)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()
	items := make([]service.FingerprintAccount, 0)
	for rows.Next() {
		var a service.FingerprintAccount
		if err := rows.Scan(&a.ID, &a.Name, &a.FingerprintID); err != nil {
			return nil, 0, err
		}
		items = append(items, a)
	}
	return items, total, rows.Err()
}

func (r *accountFingerprintRepository) Binding(ctx context.Context, accountID int64) (*service.FingerprintRecord, error) {
	var id *int64
	err := r.db.QueryRowContext(ctx, `SELECT b.fingerprint_id FROM accounts a
		LEFT JOIN account_fingerprint_bindings b ON b.account_id=a.id WHERE a.id=$1 AND `+fingerprintEligibleAccount, accountID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrFingerprintAccountInvalid
	}
	if err != nil || id == nil {
		return nil, err
	}
	return r.Get(ctx, *id)
}

func (r *accountFingerprintRepository) Bind(ctx context.Context, accountID int64, fingerprintID *int64) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var lockedID int64
	err = tx.QueryRowContext(ctx, `SELECT a.id FROM accounts a WHERE a.id=$1 AND `+fingerprintEligibleAccount+` FOR UPDATE`, accountID).Scan(&lockedID)
	if errors.Is(err, sql.ErrNoRows) {
		return service.ErrFingerprintAccountInvalid
	}
	if err != nil {
		return err
	}
	if fingerprintID == nil {
		_, err = tx.ExecContext(ctx, `DELETE FROM account_fingerprint_bindings WHERE account_id=$1`, accountID)
	} else {
		var exists bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM account_fingerprint_records WHERE id=$1)`, *fingerprintID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return service.ErrFingerprintRecordNotFound
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO account_fingerprint_bindings(account_id,fingerprint_id) VALUES($1,$2)
			ON CONFLICT(account_id) DO UPDATE SET fingerprint_id=EXCLUDED.fingerprint_id, updated_at=NOW()`, accountID, *fingerprintID)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

// Import only snapshots of existing OAuth accounts. Never call GetOrCreate:
// missing/corrupt cache entries must not mint replacement device identities.
func (r *accountFingerprintRepository) ImportCache(ctx context.Context) (*service.FingerprintImportResult, error) {
	result := &service.FingerprintImportResult{}
	rows, err := r.db.QueryContext(ctx, `SELECT a.id FROM accounts a WHERE `+fingerprintEligibleAccount+` ORDER BY a.id`)
	if err != nil {
		return result, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return result, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return result, err
	}
	cache := NewIdentityCache(r.rdb)
	for _, id := range ids {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		fp, err := cache.GetFingerprint(ctx, id)
		if errors.Is(err, redis.Nil) {
			result.Missing++
			continue
		}
		if err != nil || fp == nil || fp.ClientID == "" || fp.UserAgent == "" {
			result.Failed++
			continue
		}
		// Keep every identity field byte-for-byte, excluding cache TTL metadata.
		fp.UpdatedAt = 0
		record := &service.FingerprintRecord{
			Source: "cache", Fingerprint: *fp, ClientIDOrigin: "cache", SourceAccountID: &id,
			Key: service.FingerprintRecordKey(struct {
				Source      string
				AccountID   int64
				Fingerprint service.Fingerprint
			}{"cache", id, *fp}),
		}
		if err := r.Observe(ctx, record); err != nil {
			return result, fmt.Errorf("import fingerprint for account %d: %w", id, err)
		}
		result.Imported++
	}
	return result, nil
}
