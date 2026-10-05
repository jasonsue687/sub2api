//go:build unit

package handler

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMessagesStrictAdmissionPolicyChangesPreserveBinding(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, loadAware := range []bool{false, true} {
		for _, reason := range []string{"claude_code_only", "privacy_not_set"} {
			name := "legacy/" + reason
			if loadAware {
				name = "load-aware/" + reason
			}
			t.Run(name, func(t *testing.T) {
				group := strictHTTPGroup(2101)
				fallback := strictHTTPGroup(2102)
				group.FallbackGroupID = &fallback.ID
				account := strictHTTPAccount(1101, group.ID, "bound")
				account.Extra["privacy_mode"] = service.AntigravityPrivacySet
				group.RequirePrivacySet = true
				fallbackAccount := strictHTTPAccount(1102, fallback.ID, "fallback")
				cfg := &strictMessagesTestConfig{Config: config.Config{RunMode: config.RunModeStandard}, concurrency: service.NewConcurrencyService(&fakeConcurrencyCache{})}
				cfg.binding.Enabled = true
				cfg.Gateway.Scheduling.LoadBatchEnabled = loadAware
				store := service.NewMemoryStrictSessionBindingStore()
				repo := &strictMessagesAccountRepo{byID: map[int64]*service.Account{account.ID: account, fallbackAccount.ID: fallbackAccount}}
				h, cleanup := newStrictMessagesHandlerWithGroups(t, cfg, group,
					map[int64]*service.Group{group.ID: group, fallback.ID: fallback},
					&fakeSchedulerCache{accounts: []*service.Account{account, fallbackAccount}}, repo, store)
				t.Cleanup(cleanup)
				// A local warmup completes admission without contacting any upstream.
				first, selected := postStrictMessages(t, h, group, group.ID, nil, strictHTTPMetadata())
				require.Equal(t, http.StatusOK, first.Code, first.Body.String())
				require.Equal(t, account.ID, selected)
				if reason == "claude_code_only" {
					group.ClaudeCodeOnly = true
				} else {
					delete(account.Extra, "privacy_mode")
				}
				denied, selected := postStrictMessages(t, h, group, group.ID, nil, strictHTTPMetadata())
				assert.Equal(t, http.StatusServiceUnavailable, denied.Code, denied.Body.String())
				assert.Equal(t, "strict_session_account_unavailable", denied.Header().Get("X-Sub2API-Error-Code"))
				assert.Contains(t, denied.Body.String(), `"reason":"`+reason+`"`)
				assert.Zero(t, selected)
				assert.Equal(t, account.ID, requireStrictStoreAccount(t, store, cfg, group.ID))

				newMetadata := strings.ReplaceAll(strictHTTPMetadata(), strictHTTPSessionID, "new-session-admission-test")
				unbound, selected := postStrictMessages(t, h, group, group.ID, nil, newMetadata)
				assert.Equal(t, http.StatusServiceUnavailable, unbound.Code, unbound.Body.String())
				assert.Zero(t, selected, "must not enter the configured fallback group")
				plan, err := service.ResolveStrictSessionPlan(cfg.binding, service.StrictSessionIdentityInput{MetadataUserID: newMetadata})
				require.NoError(t, err)
				_, err = store.Get(context.Background(), plan.BindingKey)
				assert.ErrorIs(t, err, service.ErrStrictBindingNotFound)

				group.ClaudeCodeOnly = false
				account.Extra["privacy_mode"] = service.AntigravityPrivacySet
				recovered, selected := postStrictMessages(t, h, group, group.ID, nil, strictHTTPMetadata())
				assert.Equal(t, http.StatusOK, recovered.Code, recovered.Body.String())
				assert.Equal(t, account.ID, selected)
			})
		}
	}
}

func TestMessagesStrictAdmissionPreservesCreditOverages(t *testing.T) {
	gin.SetMode(gin.TestMode)
	group := strictHTTPGroup(2101)
	account := strictHTTPAccount(1101, group.ID, "bound")
	account.Extra["allow_overages"] = true
	reset := map[string]any{"rate_limit_reset_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}
	limits := map[string]any{"claude-sonnet-4-5": reset}
	account.Extra["model_rate_limits"] = limits
	require.Positive(t, account.GetRateLimitRemainingTimeWithContext(context.Background(), "claude-sonnet-4-5"))
	cfg := &strictMessagesTestConfig{Config: config.Config{RunMode: config.RunModeStandard}}
	cfg.binding.Enabled = true
	store := service.NewMemoryStrictSessionBindingStore()
	repo := &strictMessagesAccountRepo{byID: map[int64]*service.Account{account.ID: account}}
	h, cleanup := newStrictMessagesHandler(t, cfg, group, []*service.Account{account}, repo, store)
	t.Cleanup(cleanup)
	for _, stage := range []string{"first assignment", "bound"} {
		rec, selected := postStrictMessages(t, h, group, group.ID, nil, strictHTTPMetadata())
		require.Equal(t, http.StatusOK, rec.Code, "%s: %s", stage, rec.Body.String())
		assert.Equal(t, account.ID, selected)
	}
	for _, stage := range []string{"overages disabled", "credits exhausted"} {
		account.Extra["allow_overages"] = stage != "overages disabled"
		if stage == "credits exhausted" {
			limits["AICredits"] = reset
		}
		rec, selected := postStrictMessages(t, h, group, group.ID, nil, strictHTTPMetadata())
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code, "%s: %s", stage, rec.Body.String())
		assert.Contains(t, rec.Body.String(), `"reason":"rate_limited"`)
		assert.Zero(t, selected)
		assert.Equal(t, account.ID, requireStrictStoreAccount(t, store, cfg, group.ID))
	}
}
