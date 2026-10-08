//go:build integration

package repository

import (
	"context"
	"os"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func (s *AccountRepoSuite) TestAccountDefaultSchedulingRequiresApproval() {
	created, err := s.client.Account.Create().
		SetName("default-paused").SetPlatform(service.PlatformAnthropic).
		SetType(service.AccountTypeOAuth).SetCredentials(map[string]any{}).
		Save(s.ctx)
	s.Require().NoError(err)
	s.Require().False(created.Schedulable)
	eligible, err := s.repo.ListSchedulable(s.ctx)
	s.Require().NoError(err)
	s.Require().Empty(eligible)
	s.Require().NoError(s.repo.SetSchedulable(s.ctx, created.ID, true))
	eligible, err = s.repo.ListSchedulable(s.ctx)
	s.Require().NoError(err)
	s.Require().Len(eligible, 1)
	s.Require().Equal(created.ID, eligible[0].ID)
}

func TestAccountSchedulingDefaultMigrationPreservesExistingAccounts(t *testing.T) {
	ctx := context.Background()
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })
	// A temporary table isolates the migration from the shared test database.
	_, err = tx.ExecContext(ctx, `CREATE TEMP TABLE accounts (
		id BIGINT PRIMARY KEY, schedulable BOOLEAN NOT NULL DEFAULT TRUE
	) ON COMMIT DROP;
	INSERT INTO accounts(id, schedulable) VALUES (1, TRUE), (2, FALSE);`)
	require.NoError(t, err)
	migration, err := os.ReadFile("../../migrations/252_accounts_default_unschedulable.sql")
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(migration))
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `INSERT INTO accounts(id) VALUES (3)`)
	require.NoError(t, err)
	for id, want := range map[int64]bool{1: true, 2: false, 3: false} {
		var got bool
		require.NoError(t, tx.QueryRowContext(ctx, `SELECT schedulable FROM accounts WHERE id=$1`, id).Scan(&got))
		require.Equal(t, want, got, "account %d", id)
	}
}
