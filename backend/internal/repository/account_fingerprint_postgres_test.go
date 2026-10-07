package repository

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	_ "github.com/lib/pq"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// FINGERPRINT_TEST_DSN points to a disposable PostgreSQL database. A private
// schema isolates this test even when other repository tests use the same DB.
func TestAccountFingerprintRegistryPostgres(t *testing.T) {
	dsn := os.Getenv("FINGERPRINT_TEST_DSN")
	if dsn == "" {
		t.Skip("FINGERPRINT_TEST_DSN is not set")
	}
	ctx := context.Background()
	root, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = root.Close() })
	schema := "fingerprint_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = root.ExecContext(ctx, `CREATE SCHEMA `+schema)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = root.ExecContext(ctx, `DROP SCHEMA `+schema+` CASCADE`) })
	u, err := url.Parse(dsn)
	require.NoError(t, err)
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	db, err := sql.Open("postgres", u.String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.ExecContext(ctx, `CREATE TABLE accounts(id BIGINT PRIMARY KEY,name TEXT NOT NULL,platform TEXT NOT NULL,type TEXT NOT NULL,deleted_at TIMESTAMPTZ);
	INSERT INTO accounts VALUES (14,'Claude A','anthropic','oauth',NULL),(15,'Claude B','anthropic','oauth',NULL),(16,'Claude C','anthropic','oauth',NULL),(17,'Claude D','anthropic','oauth',NULL),(18,'Deleted','anthropic','oauth',NOW()),(19,'API key','anthropic','apikey',NULL),(20,'OpenAI','openai','oauth',NULL);`)
	require.NoError(t, err)
	migration, err := os.ReadFile("../../migrations/251_account_fingerprint_registry.sql")
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, string(migration))
	require.NoError(t, err)
	mini := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	repo := NewAccountFingerprintRepository(db)

	t.Run("concurrent observations preserve one identity and count every request", func(t *testing.T) {
		const count = 8
		var wg sync.WaitGroup
		errs := make([]error, count)
		for i := range count {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				errs[i] = repo.Observe(ctx, &service.FingerprintRecord{
					Key: "same-incoming-identity", Source: "request", ClientIDOrigin: "generated", RequestCount: 1,
					Fingerprint: service.Fingerprint{ClientID: uuid.NewString(), UserAgent: "claude-cli/2.1.284", StainlessOS: "Linux"},
				})
			}(i)
		}
		wg.Wait()
		for _, err := range errs {
			require.NoError(t, err)
		}
		items, total, err := repo.List(ctx, 1, 20, "", "request")
		require.NoError(t, err)
		require.Equal(t, int64(1), total)
		require.Len(t, items, 1)
		assert.Equal(t, int64(count), items[0].RequestCount)
		device := items[0].Fingerprint.ClientID
		require.NoError(t, repo.Observe(ctx, &service.FingerprintRecord{Key: "same-incoming-identity", Source: "request", ClientIDOrigin: "generated", RequestCount: 1, Fingerprint: service.Fingerprint{ClientID: "replacement-must-not-win", UserAgent: "changed-default"}}))
		stored, err := repo.Get(ctx, items[0].ID)
		require.NoError(t, err)
		assert.Equal(t, device, stored.Fingerprint.ClientID)
		assert.Equal(t, "claude-cli/2.1.284", stored.Fingerprint.UserAgent)
		assert.Equal(t, int64(count+1), stored.RequestCount)
	})

	t.Run("both management views share binding and invalid changes preserve it", func(t *testing.T) {
		require.NoError(t, NewIdentityCache(rdb).SetFingerprint(ctx, 14, &service.Fingerprint{ClientID: "cached-device", UserAgent: "claude-cli/2.1.100"}))
		require.NoError(t, repo.Observe(ctx, &service.FingerprintRecord{
			Key: "second-incoming-identity", Source: "request", ClientIDOrigin: "client", RequestCount: 1,
			Fingerprint: service.Fingerprint{ClientID: "second-device", UserAgent: "claude-cli/2.1.284"},
		}))
		items, total, err := repo.List(ctx, 1, 20, "", "")
		require.NoError(t, err)
		require.Equal(t, int64(2), total)
		first, second := items[0].ID, items[1].ID
		require.NoError(t, repo.Bind(ctx, 14, &first))
		bound, err := repo.Binding(ctx, 14)
		require.NoError(t, err)
		require.Equal(t, first, bound.ID)
		assert.Len(t, bound.BoundAccounts, 1)
		missing := int64(999999)
		assert.ErrorIs(t, repo.Bind(ctx, 14, &missing), service.ErrFingerprintRecordNotFound)
		bound, err = repo.Binding(ctx, 14)
		require.NoError(t, err)
		assert.Equal(t, first, bound.ID)
		for _, id := range []int64{18, 19, 20, 999} {
			assert.ErrorIs(t, repo.Bind(ctx, id, &first), service.ErrFingerprintAccountInvalid)
		}
		require.NoError(t, repo.Bind(ctx, 14, &second))
		old, err := repo.Get(ctx, first)
		require.NoError(t, err)
		assert.Empty(t, old.BoundAccounts)
		accounts, count, err := repo.Accounts(ctx, "Claude A", 1, 20)
		require.NoError(t, err)
		require.Equal(t, int64(1), count)
		require.Len(t, accounts, 1)
		require.NotNil(t, accounts[0].FingerprintID)
		assert.Equal(t, second, *accounts[0].FingerprintID)
		require.NoError(t, repo.Bind(ctx, 14, nil))
		bound, err = repo.Binding(ctx, 14)
		require.NoError(t, err)
		assert.Nil(t, bound)
		// Bindings never touch the current cache used by outbound requests.
		cached, err := NewIdentityCache(rdb).GetFingerprint(ctx, 14)
		require.NoError(t, err)
		assert.Equal(t, "cached-device", cached.ClientID)
	})
}
