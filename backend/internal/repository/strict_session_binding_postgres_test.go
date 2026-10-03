package repository

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

// TestStrictSessionBindingPostgresConcurrentCreate 在真实 Postgres 上验证并发首写。
// 默认测试不依赖 Docker。设置 STRICT_SESSION_TEST_DSN 后才会执行。
func TestStrictSessionBindingPostgresConcurrentCreate(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("STRICT_SESSION_TEST_DSN"))
	if dsn == "" {
		t.Skip("STRICT_SESSION_TEST_DSN is not set")
	}
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	require.NoError(t, db.PingContext(ctx))
	require.NoError(t, applyStrictSessionBindingTable(ctx, db))

	bindingKey := "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), "DELETE FROM strict_session_bindings WHERE binding_key = $1", bindingKey)
	})

	const n = 8
	winners := make([]int64, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			row, createErr := NewStrictSessionBindingRepository(db).Create(ctx, &service.StrictSessionBinding{
				BindingKey:         bindingKey,
				SessionFingerprint: "finger",
				AccountID:          int64(8_000_000_000 + i),
				Protocol:           service.StrictSessionProtocol,
				APIKeyID:           1,
			})
			errs[i] = createErr
			if createErr == nil && row != nil {
				winners[i] = row.AccountID
			}
		}(i)
	}
	wg.Wait()
	for i, createErr := range errs {
		require.NoError(t, createErr, "create %d", i)
	}
	stored, err := NewStrictSessionBindingRepository(db).Get(ctx, bindingKey)
	require.NoError(t, err)
	require.NotZero(t, stored.AccountID)
	for _, id := range winners {
		require.Equal(t, stored.AccountID, id)
	}
}

func applyStrictSessionBindingTable(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS strict_session_bindings (
    id                   BIGSERIAL PRIMARY KEY,
    binding_key          VARCHAR(64) NOT NULL,
    session_fingerprint  VARCHAR(16) NOT NULL,
    account_id           BIGINT NOT NULL CHECK (account_id > 0),
    protocol             VARCHAR(32) NOT NULL,
    api_key_id           BIGINT NOT NULL CHECK (api_key_id > 0),
    group_id             BIGINT NULL,
    end_user_fingerprint VARCHAR(16) NULL,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_strict_session_bindings_key
    ON strict_session_bindings (binding_key);
CREATE INDEX IF NOT EXISTS idx_strict_session_bindings_account_id
    ON strict_session_bindings (account_id);
`)
	return err
}
