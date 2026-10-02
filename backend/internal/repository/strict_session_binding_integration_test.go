//go:build integration

package repository

import (
	"context"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestStrictSessionBindingIntegrationUniqueAndSurvivesMissingAccount(t *testing.T) {
	repo := NewStrictSessionBindingRepository(integrationDB)
	ctx := context.Background()
	bindingKey := "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM strict_session_bindings WHERE binding_key = $1", bindingKey)
	})

	const n = 8
	winners := make([]int64, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			row, err := repo.Create(ctx, &service.StrictSessionBinding{
				BindingKey:         bindingKey,
				SessionFingerprint: "finger",
				AccountID:          int64(9_000_000_000 + i),
				Protocol:           service.StrictSessionProtocol,
				APIKeyID:           1,
			})
			require.NoError(t, err)
			winners[i] = row.AccountID
		}(i)
	}
	wg.Wait()

	stored, err := repo.Get(ctx, bindingKey)
	require.NoError(t, err)
	require.NotZero(t, stored.AccountID)
	for _, id := range winners {
		require.Equal(t, stored.AccountID, id)
	}

	var accountRows int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM accounts WHERE id = $1", stored.AccountID).Scan(&accountRows))
	require.Zero(t, accountRows, "binding must be storable without an accounts foreign key")

	_, err = integrationDB.ExecContext(ctx, "DELETE FROM accounts WHERE id = $1", stored.AccountID)
	require.NoError(t, err)
	still, err := repo.Get(ctx, bindingKey)
	require.NoError(t, err)
	require.Equal(t, stored.AccountID, still.AccountID)
}
