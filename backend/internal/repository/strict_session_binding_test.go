package repository

import (
	"context"
	"database/sql"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStrictSessionBindingRepositoryReturnsOriginalAssignmentAcrossAPIKeys(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	createdAt := time.Now().UTC()
	key := strings.Repeat("a", 64)
	columns := []string{"id", "binding_key", "session_fingerprint", "account_id", "protocol", "api_key_id", "group_id", "created_at"}
	// A second API key proposes another account for the same session. The
	// database returns the existing account and the first request's audit data.
	mock.ExpectQuery(regexp.QuoteMeta(createStrictSessionBindingSQL)).
		WithArgs(key, "session-fp", int64(22), service.StrictSessionProtocol, int64(2), nil).
		WillReturnRows(sqlmock.NewRows(columns).AddRow(1, key, "session-fp", 11, service.StrictSessionProtocol, 1, nil, createdAt))
	repo := NewStrictSessionBindingRepository(db)
	stored, err := repo.Create(context.Background(), &service.StrictSessionBinding{
		BindingKey: key, SessionFingerprint: "session-fp", AccountID: 22,
		Protocol: service.StrictSessionProtocol, APIKeyID: 2,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(11), stored.AccountID)
	assert.Equal(t, int64(1), stored.APIKeyID)
	assert.Nil(t, stored.GroupID)
	assert.Equal(t, createdAt, stored.CreatedAt)
	require.NoError(t, mock.ExpectationsWereMet())
}

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
