package repository

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestStrictSessionBindingRepositoryReadFailureIsNotAbsence(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectQuery("FROM strict_session_bindings").WillReturnError(sql.ErrConnDone)

	repo := NewStrictSessionBindingRepository(db)
	_, err = repo.Get(context.Background(), "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	require.ErrorIs(t, err, service.ErrStrictSessionStore)
	require.NotErrorIs(t, err, service.ErrStrictBindingNotFound)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestStrictSessionBindingRepositoryNotFoundIsExplicit(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectQuery("FROM strict_session_bindings").WillReturnError(sql.ErrNoRows)

	repo := NewStrictSessionBindingRepository(db)
	_, err = repo.Get(context.Background(), "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	require.ErrorIs(t, err, service.ErrStrictBindingNotFound)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCreateSQLReturnsStoredRowOnConflict(t *testing.T) {
	normalized := strings.Join(strings.Fields(strings.ToUpper(createStrictSessionBindingSQL)), " ")
	require.Contains(t, normalized, "ON CONFLICT (BINDING_KEY) DO UPDATE")
	require.Contains(t, normalized, "SET ACCOUNT_ID = STRICT_SESSION_BINDINGS.ACCOUNT_ID")
	require.Contains(t, normalized, "RETURNING")
	require.NotContains(t, normalized, "DO NOTHING")
}

func TestStrictSessionBindingCacheHasNoTTLAndMissesFallBack(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	cache := NewStrictSessionBindingCache(rdb)
	db := service.NewMemoryStrictSessionBindingStore()
	store := service.NewCachedStrictSessionBindingStore(db, cache)
	ctx := context.Background()
	created, err := store.Create(ctx, &service.StrictSessionBinding{
		BindingKey: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", AccountID: 11,
		SessionFingerprint: "abc123", Protocol: service.StrictSessionProtocol, APIKeyID: 2,
		CreatedAt: time.Now().Add(-48 * time.Hour),
	})
	require.NoError(t, err)
	require.Equal(t, int64(11), created.AccountID)

	key := strictSessionBindingCacheKey(created.BindingKey)
	require.Equal(t, strictSessionBindingCacheTTL, mr.TTL(key))
	mr.FastForward(strictSessionBindingCacheTTL + time.Second)
	require.False(t, mr.Exists(key))

	mr.FlushAll()
	got, err := store.Get(ctx, created.BindingKey)
	require.NoError(t, err)
	require.Equal(t, int64(11), got.AccountID)

	restarted := service.NewCachedStrictSessionBindingStore(db, NewStrictSessionBindingCache(rdb))
	got, err = restarted.Get(ctx, created.BindingKey)
	require.NoError(t, err)
	require.Equal(t, int64(11), got.AccountID)
}
