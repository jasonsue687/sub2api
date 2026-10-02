package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

const strictSessionBindingSelectList = `
	id, binding_key, session_fingerprint, account_id, protocol, api_key_id, group_id, end_user_fingerprint, created_at`

// createStrictSessionBindingSQL 在一条语句里完成插入或读回已有行。
// DO UPDATE 把 account_id 设回原值：不改写赢家，但会等待冲突事务并 RETURNING 那一行。
// DO NOTHING 加同语句 SELECT 会用插入前的快照，并发输家可能看到 0 行。
const createStrictSessionBindingSQL = `
INSERT INTO strict_session_bindings (
	binding_key, session_fingerprint, account_id, protocol, api_key_id, group_id, end_user_fingerprint
) VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (binding_key) DO UPDATE
SET account_id = strict_session_bindings.account_id
RETURNING ` + strictSessionBindingSelectList

const getStrictSessionBindingSQL = `
SELECT ` + strictSessionBindingSelectList + `
FROM strict_session_bindings
WHERE binding_key = $1`

type strictSessionBindingRepository struct {
	db *sql.DB
}

// NewStrictSessionBindingRepository 创建永久绑定仓储。
// account_id 没有外键，删除账号不会级联删除绑定。
func NewStrictSessionBindingRepository(db *sql.DB) service.StrictSessionBindingStore {
	return &strictSessionBindingRepository{db: db}
}

func (r *strictSessionBindingRepository) Get(ctx context.Context, bindingKey string) (*service.StrictSessionBinding, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("%w: database is not configured", service.ErrStrictSessionStore)
	}
	row := r.db.QueryRowContext(ctx, getStrictSessionBindingSQL, bindingKey)
	binding, err := scanStrictSessionBinding(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrStrictBindingNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %w", service.ErrStrictSessionStore, err)
	}
	return binding, nil
}

func (r *strictSessionBindingRepository) Create(ctx context.Context, binding *service.StrictSessionBinding) (*service.StrictSessionBinding, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("%w: database is not configured", service.ErrStrictSessionStore)
	}
	if binding == nil || binding.BindingKey == "" || binding.AccountID <= 0 {
		return nil, fmt.Errorf("%w: invalid binding", service.ErrStrictSessionStore)
	}
	var groupID sql.NullInt64
	if binding.GroupID != nil {
		groupID = sql.NullInt64{Int64: *binding.GroupID, Valid: true}
	}
	var endUser sql.NullString
	if binding.EndUserFingerprint != "" {
		endUser = sql.NullString{String: binding.EndUserFingerprint, Valid: true}
	}
	row := r.db.QueryRowContext(ctx, createStrictSessionBindingSQL,
		binding.BindingKey,
		binding.SessionFingerprint,
		binding.AccountID,
		binding.Protocol,
		binding.APIKeyID,
		groupID,
		endUser,
	)
	stored, err := scanStrictSessionBinding(row)
	if errors.Is(err, sql.ErrNoRows) {
		// 插入冲突后行又不存在，不能当成新会话重新选号。
		return nil, fmt.Errorf("%w: binding disappeared during create", service.ErrStrictSessionStore)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %w", service.ErrStrictSessionStore, err)
	}
	return stored, nil
}

type strictSessionBindingScanner interface {
	Scan(dest ...any) error
}

func scanStrictSessionBinding(row strictSessionBindingScanner) (*service.StrictSessionBinding, error) {
	var (
		binding service.StrictSessionBinding
		groupID sql.NullInt64
		endUser sql.NullString
	)
	err := row.Scan(
		&binding.ID,
		&binding.BindingKey,
		&binding.SessionFingerprint,
		&binding.AccountID,
		&binding.Protocol,
		&binding.APIKeyID,
		&groupID,
		&endUser,
		&binding.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	if groupID.Valid {
		binding.GroupID = &groupID.Int64
	}
	if endUser.Valid {
		binding.EndUserFingerprint = endUser.String
	}
	return &binding, nil
}
