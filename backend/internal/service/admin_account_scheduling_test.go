//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCreateAccountRequiresSchedulingApproval(t *testing.T) {
	for _, accountType := range []string{AccountTypeOAuth, AccountTypeAPIKey} {
		t.Run(accountType, func(t *testing.T) {
			ctx := context.Background()
			repo := newSparkShadowRepoStub()
			svc := &adminServiceImpl{accountRepo: repo}
			existing := &Account{Name: "existing", Status: StatusActive, Schedulable: true}
			require.NoError(t, repo.Create(ctx, existing))
			require.NoError(t, repo.BindGroups(ctx, existing.ID, []int64{42}))

			created, err := svc.CreateAccount(ctx, &CreateAccountInput{
				Name: "new", Platform: PlatformAnthropic, Type: accountType,
				Credentials: map[string]any{}, SkipDefaultGroupBind: true,
			})
			require.NoError(t, err)
			persisted, err := repo.GetByID(ctx, created.ID)
			require.NoError(t, err)
			require.Equal(t, StatusActive, persisted.Status)
			require.False(t, persisted.Schedulable)
			require.NoError(t, repo.BindGroups(ctx, created.ID, []int64{42}))
			eligible, err := repo.ListSchedulableByGroupID(ctx, 42)
			require.NoError(t, err)
			require.Len(t, eligible, 1)
			require.Equal(t, existing.ID, eligible[0].ID)

			enabled, err := svc.SetAccountSchedulable(ctx, created.ID, true)
			require.NoError(t, err)
			require.True(t, enabled.Schedulable)
			eligible, err = repo.ListSchedulableByGroupID(ctx, 42)
			require.NoError(t, err)
			require.Len(t, eligible, 2)
			require.True(t, existing.Schedulable)
		})
	}
}
