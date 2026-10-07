package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

const strictHTTPSessionID = "c72554f2-1234-5678-abcd-123456789abc"
const strictHTTPDeviceID = "d61f76d0aabbccdd00112233445566778899aabbccddeeff0011223344556677"

func TestMessagesStrictBindingOffKeepsOfficialReselection(t *testing.T) {
	gin.SetMode(gin.TestMode)
	groupID := int64(2101)
	group := strictHTTPGroup(groupID)
	blocked := strictHTTPAccount(1101, groupID, "blocked")
	blocked.Schedulable = false
	warmup := strictHTTPAccount(1102, groupID, "warmup")
	cfg := &strictMessagesTestConfig{Config: config.Config{RunMode: config.RunModeSimple}}
	h, cleanup := newStrictMessagesHandler(t, cfg, group, []*service.Account{blocked, warmup}, &strictMessagesAccountRepo{byID: map[int64]*service.Account{
		blocked.ID: blocked,
		warmup.ID:  warmup,
	}}, nil)
	defer cleanup()

	rec, selected := postStrictMessages(t, h, group, groupID, nil, "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, warmup.ID, selected)
	require.NotContains(t, rec.Body.String(), "session_binding_error")
	require.Contains(t, rec.Body.String(), `"stop_reason":"end_turn"`)
	require.Empty(t, rec.Header().Get("X-Sub2API-Bound-Account-Id"))
}

func TestMessagesStrictBindingOnDoesNotReselect(t *testing.T) {
	gin.SetMode(gin.TestMode)
	groupID := int64(2102)
	group := strictHTTPGroup(groupID)
	bound := strictHTTPAccount(1201, groupID, "bound")
	other := strictHTTPAccount(1202, groupID, "other")
	cache := &fakeSchedulerCache{accounts: []*service.Account{bound}}
	repo := &strictMessagesAccountRepo{byID: map[int64]*service.Account{bound.ID: bound, other.ID: other}}
	store := service.NewMemoryStrictSessionBindingStore()
	cfg := &strictMessagesTestConfig{Config: config.Config{RunMode: config.RunModeSimple}}
	cfg.binding.Enabled = true
	h, cleanup := newStrictMessagesHandlerWithCache(t, cfg, group, cache, repo, store)
	defer cleanup()

	var slogBuf, zapBuf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&slogBuf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	zapLogger := zap.New(zapcore.NewCore(
		zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()),
		zapcore.AddSync(&zapBuf),
		zapcore.DebugLevel,
	))

	first, selected := postStrictMessages(t, h, group, groupID, zapLogger, strictHTTPMetadata())
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	require.Equal(t, bound.ID, selected)
	require.Contains(t, first.Body.String(), `"stop_reason":"end_turn"`)

	stored := requireStrictStoreAccount(t, store, cfg, groupID)
	require.Equal(t, bound.ID, stored)

	resetAt := time.Now().Add(time.Hour)
	bound.RateLimitResetAt = &resetAt
	cache.accounts = []*service.Account{bound, other}

	second, selected := postStrictMessages(t, h, group, groupID, zapLogger, strictHTTPMetadata())
	require.Equal(t, http.StatusServiceUnavailable, second.Code, second.Body.String())
	require.NotEqual(t, other.ID, selected)
	require.Contains(t, second.Body.String(), `"type":"session_binding_error"`)
	require.Contains(t, second.Body.String(), `"code":"strict_session_account_unavailable"`)
	require.NotContains(t, second.Body.String(), `"stop_reason":"end_turn"`)
	require.NotContains(t, second.Body.String(), strictHTTPSessionID)
	require.Empty(t, second.Header().Get("X-Sub2API-Bound-Account-Id"))
	require.Equal(t, bound.ID, requireStrictStoreAccount(t, store, cfg, groupID))

	logged := slogBuf.String() + zapBuf.String()
	require.NotContains(t, logged, strictHTTPSessionID)
	require.NotContains(t, logged, strictHTTPDeviceID)
	require.NotContains(t, logged, strictHTTPMetadata())
	require.Contains(t, logged, "session_fp")
}

func TestMessagesStrictBindingSurvivesAPIKeyChangesAndChecksCurrentGroup(t *testing.T) {
	gin.SetMode(gin.TestMode)
	groupID := int64(2103)
	group := strictHTTPGroup(groupID)
	bound := strictHTTPAccount(1301, groupID, "bound")
	other := strictHTTPAccount(1302, groupID, "other")
	cache := &fakeSchedulerCache{accounts: []*service.Account{bound}}
	repo := &strictMessagesAccountRepo{byID: map[int64]*service.Account{bound.ID: bound, other.ID: other}}
	store := service.NewMemoryStrictSessionBindingStore()
	cfg := &strictMessagesTestConfig{Config: config.Config{RunMode: config.RunModeStandard}}
	cfg.binding.Enabled = true
	h, cleanup := newStrictMessagesHandlerWithCache(t, cfg, group, cache, repo, store)
	t.Cleanup(cleanup)

	first, selected := postStrictMessagesWithAPIKey(t, h, group, groupID, nil, strictHTTPMetadata(), 3101)
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	assert.Equal(t, bound.ID, selected)

	// A fresh official selection would now pick the other account. Only the
	// session ID is carried forward: both the API key and metadata shape change.
	cache.accounts = []*service.Account{other}
	sessionOnly := `{"session_id":"` + strictHTTPSessionID + `"}`
	resumed, selected := postStrictMessagesWithAPIKey(t, h, group, groupID, nil, sessionOnly, 3102)
	require.Equal(t, http.StatusOK, resumed.Code, resumed.Body.String())
	assert.Equal(t, bound.ID, selected)

	otherGroup := strictHTTPGroup(groupID + 1)
	denied, selected := postStrictMessagesWithAPIKey(t, h, otherGroup, otherGroup.ID, nil, sessionOnly, 3103)
	assert.Equal(t, http.StatusServiceUnavailable, denied.Code, denied.Body.String())
	assert.Zero(t, selected, "a globally shared session ID must not bypass current group membership")
	assert.Contains(t, denied.Body.String(), "strict_session_account_unavailable")
	assert.Equal(t, bound.ID, requireStrictStoreAccount(t, store, cfg, groupID))

	newSession := `{"session_id":"11111111-2222-3333-4444-555555555555"}`
	fresh, selected := postStrictMessagesWithAPIKey(t, h, group, groupID, nil, newSession, 3102)
	require.Equal(t, http.StatusOK, fresh.Code, fresh.Body.String())
	assert.Equal(t, other.ID, selected)
}

func TestMessagesStrictBindingRejectsMissingOrInvalidSessionID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	groupID := int64(2104)
	group := strictHTTPGroup(groupID)
	cfg := &strictMessagesTestConfig{Config: config.Config{RunMode: config.RunModeSimple}}
	cfg.binding.Enabled = true
	h, cleanup := newStrictMessagesHandler(t, cfg, group, nil, &strictMessagesAccountRepo{}, service.NewMemoryStrictSessionBindingStore())
	t.Cleanup(cleanup)
	for _, metadata := range []string{"", `{"device_id":"device"}`, `{"session_id":42}`, `{"session_id":`} {
		rec, selected := postStrictMessages(t, h, group, groupID, nil, metadata)
		assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		assert.Equal(t, "strict_session_id_required", rec.Header().Get("X-Sub2API-Error-Code"))
		assert.Contains(t, rec.Body.String(), "stable_session_id_required")
		assert.Zero(t, selected)
	}
}

func TestStrictAccountRejectionAfterStreamStarted(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &GatewayHandler{}
	t.Run("stream flag with unchanged writer size", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		h.rejectStrictAccount(c, &strictSessionRuntime{}, nil, 7, "rate_limited", true, c.Writer.Size())
		require.Contains(t, rec.Body.String(), "strict_session_stream_interrupted")
	})

	t.Run("bytes written after the entry baseline", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		_, err := c.Writer.Write([]byte("data: started\n\n"))
		require.NoError(t, err)
		h.rejectStrictAccount(c, &strictSessionRuntime{}, nil, 7, "rate_limited", false, 0)
		require.Contains(t, rec.Body.String(), "strict_session_stream_interrupted")
	})
}

func newStrictGateway(cfg *strictMessagesTestConfig, repo service.AccountRepository, groups service.GroupRepository, snapshot *service.SchedulerSnapshotService, runtimeSettings ...*service.SettingService) *service.GatewayService {
	stored := strictHTTPSettings(cfg.binding)
	for key, value := range cfg.legacyFallback {
		stored.values[key] = value
	}
	settings := service.NewSettingService(stored, &cfg.Config)
	if len(runtimeSettings) > 0 {
		settings = runtimeSettings[0]
	}
	var usageBilling *service.BillingService
	if cfg.warmupUsage != nil {
		usageBilling = service.NewBillingService(&cfg.Config, nil)
	}
	return service.NewGatewayService(
		repo,
		groups,
		cfg.warmupUsage,
		nil,
		nil,
		nil,
		nil,
		nil,
		&cfg.Config,
		snapshot,
		cfg.concurrency,
		usageBilling,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		cfg.sessions,
		nil,
		nil,
		settings,
		nil,
		cfg.channels,
		nil,
		nil,
		nil,
		nil,
	)
}

func newStrictMessagesHandler(t *testing.T, cfg *strictMessagesTestConfig, group *service.Group, accounts []*service.Account, repo service.AccountRepository, store service.StrictSessionBindingStore) (*GatewayHandler, func()) {
	t.Helper()
	return newStrictMessagesHandlerWithCache(t, cfg, group, &fakeSchedulerCache{accounts: accounts}, repo, store)
}

func newStrictMessagesHandlerWithCache(t *testing.T, cfg *strictMessagesTestConfig, group *service.Group, cache *fakeSchedulerCache, repo service.AccountRepository, store service.StrictSessionBindingStore) (*GatewayHandler, func()) {
	t.Helper()
	return newStrictMessagesHandlerWithGroups(t, cfg, group, nil, cache, repo, store)
}

func newStrictMessagesHandlerWithGroups(t *testing.T, cfg *strictMessagesTestConfig, group *service.Group, groups map[int64]*service.Group, cache *fakeSchedulerCache, repo service.AccountRepository, store service.StrictSessionBindingStore) (*GatewayHandler, func()) {
	t.Helper()
	if cfg == nil {
		cfg = &strictMessagesTestConfig{Config: config.Config{RunMode: config.RunModeSimple}}
	}
	snapshot := service.NewSchedulerSnapshotService(cache, nil, repo, nil, nil)
	groupRepo := &fakeGroupRepo{group: group}
	if groups != nil {
		groupRepo.byID = groups
	}
	gw := newStrictGateway(cfg, repo, groupRepo, snapshot)
	gw.SetStrictSessionBindingStore(store)
	var billingCache service.BillingCache
	if cfg.RunMode != config.RunModeSimple {
		billingCache = newHandlerInflightCache(100)
	}
	billing := service.NewBillingCacheService(billingCache, nil, nil, nil, nil, nil, &cfg.Config, nil)
	h := &GatewayHandler{
		gatewayService:           gw,
		billingCacheService:      billing,
		concurrencyHelper:        NewConcurrencyHelper(service.NewConcurrencyService(&fakeConcurrencyCache{}), SSEPingFormatClaude, 0),
		cfg:                      &cfg.Config,
		maxAccountSwitches:       10,
		maxAccountSwitchesGemini: 3,
	}
	return h, func() { billing.Stop() }
}

func postStrictMessages(t *testing.T, h *GatewayHandler, group *service.Group, groupID int64, zapLogger *zap.Logger, metadataUserID string) (*httptest.ResponseRecorder, int64) {
	t.Helper()
	return postStrictMessagesWithAPIKey(t, h, group, groupID, zapLogger, metadataUserID, 3101)
}

func postStrictMessagesWithAPIKey(t *testing.T, h *GatewayHandler, group *service.Group, groupID int64, zapLogger *zap.Logger, metadataUserID string, apiKeyID int64, headers ...http.Header) (*httptest.ResponseRecorder, int64) {
	t.Helper()
	payload := map[string]any{
		"model":      "claude-sonnet-4-5",
		"max_tokens": 256,
		"messages": []any{
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "[SUGGESTION MODE: next action]"}}},
		},
	}
	if metadataUserID != "" {
		payload["metadata"] = map[string]any{"user_id": metadataUserID}
	}
	body, err := json.Marshal(payload)
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for _, extra := range headers {
		for key, values := range extra {
			for _, value := range values {
				req.Header.Add(key, value)
			}
		}
	}
	ctx := context.WithValue(req.Context(), ctxkey.Group, group)
	if zapLogger != nil {
		ctx = logger.IntoContext(ctx, zapLogger)
	}
	c.Request = req.WithContext(ctx)

	apiKey := &service.APIKey{
		ID:      apiKeyID,
		UserID:  4101,
		GroupID: &groupID,
		Status:  service.StatusActive,
		User:    &service.User{ID: 4101, Concurrency: 10, Balance: 100},
		Group:   group,
	}
	c.Set(string(middleware.ContextKeyAPIKey), apiKey)
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: apiKey.UserID, Concurrency: 10})
	h.Messages(c)

	selected, _ := c.Get(opsAccountIDKey)
	id, _ := selected.(int64)
	return rec, id
}

func requireStrictStoreAccount(t *testing.T, store service.StrictSessionBindingStore, cfg *strictMessagesTestConfig, groupID int64) int64 {
	t.Helper()
	plan, err := service.ResolveStrictSessionPlan(cfg.binding, service.StrictSessionIdentityInput{
		APIKeyID:       3101,
		GroupID:        &groupID,
		MetadataUserID: strictHTTPMetadata(),
	})
	require.NoError(t, err)
	row, err := store.Get(context.Background(), plan.BindingKey)
	require.NoError(t, err)
	return row.AccountID
}

func strictHTTPMetadata() string {
	return `{"device_id":"` + strictHTTPDeviceID + `","account_uuid":"","session_id":"` + strictHTTPSessionID + `"}`
}

func strictHTTPGroup(id int64) *service.Group {
	return &service.Group{
		ID:       id,
		Hydrated: true,
		Platform: service.PlatformAnthropic,
		Status:   service.StatusActive,
	}
}

func strictHTTPAccount(id, groupID int64, name string) *service.Account {
	return &service.Account{
		ID:       id,
		Name:     name,
		Platform: service.PlatformAntigravity,
		Type:     service.AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token":              "tok_xxx",
			"intercept_warmup_requests": true,
		},
		Extra:         map[string]any{"mixed_scheduling": true},
		Concurrency:   1,
		Priority:      int(id),
		Status:        service.StatusActive,
		Schedulable:   true,
		AccountGroups: []service.AccountGroup{{AccountID: id, GroupID: groupID}},
	}
}

type strictMessagesAccountRepo struct {
	byID map[int64]*service.Account
}

func (r *strictMessagesAccountRepo) GetByID(_ context.Context, id int64) (*service.Account, error) {
	if r != nil && r.byID != nil {
		if account, ok := r.byID[id]; ok {
			return account, nil
		}
	}
	return nil, service.ErrAccountNotFound
}

func (r *strictMessagesAccountRepo) Create(context.Context, *service.Account) error { return nil }
func (r *strictMessagesAccountRepo) GetByIDs(context.Context, []int64) ([]*service.Account, error) {
	return nil, nil
}
func (r *strictMessagesAccountRepo) ExistsByID(_ context.Context, id int64) (bool, error) {
	_, ok := r.byID[id]
	return ok, nil
}
func (r *strictMessagesAccountRepo) GetByCRSAccountID(context.Context, string) (*service.Account, error) {
	return nil, nil
}
func (r *strictMessagesAccountRepo) FindByExtraField(context.Context, string, any) ([]service.Account, error) {
	return nil, nil
}
func (r *strictMessagesAccountRepo) ListCRSAccountIDs(context.Context) (map[string]int64, error) {
	return nil, nil
}
func (r *strictMessagesAccountRepo) Update(context.Context, *service.Account) error { return nil }
func (r *strictMessagesAccountRepo) Delete(context.Context, int64) error            { return nil }
func (r *strictMessagesAccountRepo) List(context.Context, pagination.PaginationParams) ([]service.Account, *pagination.PaginationResult, error) {
	return nil, nil, nil
}
func (r *strictMessagesAccountRepo) ListWithFilters(context.Context, pagination.PaginationParams, string, string, string, string, int64, string) ([]service.Account, *pagination.PaginationResult, error) {
	return nil, nil, nil
}
func (r *strictMessagesAccountRepo) ListAllWithFilters(context.Context, string, string, string, string, int64, string) ([]service.Account, error) {
	return nil, nil
}
func (r *strictMessagesAccountRepo) ListByGroup(context.Context, int64) ([]service.Account, error) {
	return nil, nil
}
func (r *strictMessagesAccountRepo) ListActive(context.Context) ([]service.Account, error) {
	return nil, nil
}
func (r *strictMessagesAccountRepo) ListByPlatform(context.Context, string) ([]service.Account, error) {
	return nil, nil
}
func (r *strictMessagesAccountRepo) UpdateLastUsed(context.Context, int64) error { return nil }
func (r *strictMessagesAccountRepo) BatchUpdateLastUsed(context.Context, map[int64]time.Time) error {
	return nil
}
func (r *strictMessagesAccountRepo) SetError(context.Context, int64, string) error { return nil }
func (r *strictMessagesAccountRepo) ClearError(context.Context, int64) error       { return nil }
func (r *strictMessagesAccountRepo) SetSchedulable(context.Context, int64, bool) error {
	return nil
}
func (r *strictMessagesAccountRepo) AutoPauseExpiredAccounts(context.Context, time.Time) (int64, error) {
	return 0, nil
}
func (r *strictMessagesAccountRepo) BindGroups(context.Context, int64, []int64) error { return nil }
func (r *strictMessagesAccountRepo) ListSchedulable(context.Context) ([]service.Account, error) {
	return nil, nil
}
func (r *strictMessagesAccountRepo) ListSchedulableByGroupID(context.Context, int64) ([]service.Account, error) {
	return nil, nil
}
func (r *strictMessagesAccountRepo) ListSchedulableByPlatform(context.Context, string) ([]service.Account, error) {
	return nil, nil
}
func (r *strictMessagesAccountRepo) ListSchedulableByGroupIDAndPlatform(context.Context, int64, string) ([]service.Account, error) {
	return nil, nil
}
func (r *strictMessagesAccountRepo) ListSchedulableByPlatforms(context.Context, []string) ([]service.Account, error) {
	return nil, nil
}
func (r *strictMessagesAccountRepo) ListSchedulableByGroupIDAndPlatforms(context.Context, int64, []string) ([]service.Account, error) {
	return nil, nil
}
func (r *strictMessagesAccountRepo) ListSchedulableUngroupedByPlatform(context.Context, string) ([]service.Account, error) {
	return nil, nil
}
func (r *strictMessagesAccountRepo) ListSchedulableUngroupedByPlatforms(context.Context, []string) ([]service.Account, error) {
	return nil, nil
}
func (r *strictMessagesAccountRepo) ListModelAvailabilityCandidates(context.Context, *int64, []string, bool) ([]service.Account, error) {
	return nil, nil
}
func (r *strictMessagesAccountRepo) SetRateLimited(context.Context, int64, time.Time) error {
	return nil
}
func (r *strictMessagesAccountRepo) SetModelRateLimit(context.Context, int64, string, time.Time, ...string) error {
	return nil
}
func (r *strictMessagesAccountRepo) SetOverloaded(context.Context, int64, time.Time) error {
	return nil
}
func (r *strictMessagesAccountRepo) SetTempUnschedulable(context.Context, int64, time.Time, string) error {
	return nil
}
func (r *strictMessagesAccountRepo) ClearTempUnschedulable(context.Context, int64) error { return nil }
func (r *strictMessagesAccountRepo) ClearRateLimit(context.Context, int64) error         { return nil }
func (r *strictMessagesAccountRepo) ClearAntigravityQuotaScopes(context.Context, int64) error {
	return nil
}
func (r *strictMessagesAccountRepo) ClearModelRateLimits(context.Context, int64) error { return nil }
func (r *strictMessagesAccountRepo) UpdateSessionWindow(context.Context, int64, *time.Time, *time.Time, string) error {
	return nil
}
func (r *strictMessagesAccountRepo) UpdateSessionWindowEnd(context.Context, int64, time.Time) error {
	return nil
}
func (r *strictMessagesAccountRepo) UpdateExtra(context.Context, int64, map[string]any) error {
	return nil
}
func (r *strictMessagesAccountRepo) BulkUpdate(context.Context, []int64, service.AccountBulkUpdate) (int64, error) {
	return 0, nil
}
func (r *strictMessagesAccountRepo) IncrementQuotaUsed(context.Context, int64, float64) error {
	return nil
}
func (r *strictMessagesAccountRepo) ResetQuotaUsedAndClearRateLimitCooldown(context.Context, int64) error {
	return nil
}
func (r *strictMessagesAccountRepo) RevertProxyFallback(context.Context, int64) error { return nil }
func (r *strictMessagesAccountRepo) ListShadowsByParent(context.Context, int64) ([]*service.Account, error) {
	return nil, nil
}

var _ service.AccountRepository = (*strictMessagesAccountRepo)(nil)

func TestMessagesStrictRejectsWithoutUsingLegacyFallbackTargets(t *testing.T) {
	gin.SetMode(gin.TestMode)
	originID := int64(3101)
	fallbackID := int64(3201)
	origin := strictHTTPGroup(originID)
	fallback := strictHTTPGroup(fallbackID)
	bound := strictHTTPAccount(1301, originID, "bound")
	sibling := strictHTTPAccount(1302, originID, "sibling")
	fallbackAccount := strictHTTPAccount(1303, fallbackID, "fallback")
	var hits int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"msg_third"}`))
	}))
	t.Cleanup(upstream.Close)

	cfg := &strictMessagesTestConfig{Config: config.Config{RunMode: config.RunModeSimple}}
	cfg.binding.Enabled = true
	cfg.legacyFallback = map[string]string{
		"strict_session_fallback_order":       "group_first",
		"strict_session_fallback_group_id":    strconv.FormatInt(fallbackID, 10),
		"strict_session_third_party_enabled":  "true",
		"strict_session_third_party_base_url": upstream.URL,
		"strict_session_third_party_api_key":  "relay-secret",
	}

	cache := &fakeSchedulerCache{accounts: []*service.Account{bound, sibling, fallbackAccount}}
	repo := &strictMessagesAccountRepo{byID: map[int64]*service.Account{
		bound.ID: bound, sibling.ID: sibling, fallbackAccount.ID: fallbackAccount,
	}}
	store := service.NewMemoryStrictSessionBindingStore()
	h, cleanup := newStrictMessagesHandlerWithGroups(t, cfg, origin, map[int64]*service.Group{
		originID: origin, fallbackID: fallback,
	}, cache, repo, store)
	defer cleanup()

	first, selected := postStrictMessages(t, h, origin, originID, nil, strictHTTPMetadata())
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	require.Equal(t, bound.ID, selected)
	require.Equal(t, bound.ID, requireStrictStoreAccount(t, store, cfg, originID))

	resetAt := time.Now().Add(time.Hour)
	bound.RateLimitResetAt = &resetAt
	second, selected := postStrictMessages(t, h, origin, originID, nil, strictHTTPMetadata())
	require.Equal(t, http.StatusServiceUnavailable, second.Code, second.Body.String())
	require.Zero(t, selected)
	require.Equal(t, strictSessionErrorAccountUnavailable, second.Header().Get(strictSessionErrorHeader))
	require.Contains(t, second.Body.String(), `"reason":"rate_limited"`)
	require.NotContains(t, second.Body.String(), `"stop_reason":"end_turn"`)
	require.NotContains(t, second.Body.String(), "msg_third")
	require.Zero(t, hits)
	require.NotEqual(t, sibling.ID, selected)
	require.Equal(t, bound.ID, requireStrictStoreAccount(t, store, cfg, originID))

	bound.RateLimitResetAt = nil
	third, selected := postStrictMessages(t, h, origin, originID, nil, strictHTTPMetadata())
	require.Equal(t, http.StatusOK, third.Code, third.Body.String())
	require.Equal(t, bound.ID, selected)
	require.Equal(t, bound.ID, requireStrictStoreAccount(t, store, cfg, originID))
}

// Test inputs keep process settings separate from the database policy.
type strictMessagesTestConfig struct {
	warmupUsage service.UsageLogRepository
	config.Config
	binding        config.GatewayStrictSessionBindingConfig
	legacyFallback map[string]string
	channels       *service.ChannelService
	concurrency    *service.ConcurrencyService
	sessions       service.SessionLimitCache
}

type strictHTTPSettingsRepo struct {
	service.SettingRepository
	values map[string]string
	err    error
}

func (r *strictHTTPSettingsRepo) GetAll(context.Context) (map[string]string, error) {
	return r.values, r.err
}
func (r *strictHTTPSettingsRepo) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	values := make(map[string]string)
	for _, key := range keys {
		if value, ok := r.values[key]; ok {
			values[key] = value
		}
	}
	return values, r.err
}
func (r *strictHTTPSettingsRepo) GetValue(_ context.Context, key string) (string, error) {
	value, ok := r.values[key]
	if !ok {
		return "", service.ErrSettingNotFound
	}
	return value, nil
}
func strictHTTPSettings(cfg config.GatewayStrictSessionBindingConfig) *strictHTTPSettingsRepo {
	return &strictHTTPSettingsRepo{values: map[string]string{
		service.SettingKeyStrictSessionBindingEnabled:        strconv.FormatBool(cfg.Enabled),
		service.SettingKeyStrictSessionSessionHeader:         cfg.SessionHeaderOrDefault(),
		service.SettingKeyStrictSessionSameAccountRetryLimit: strconv.Itoa(cfg.SameAccountRetryLimit),
	}}
}

func TestMessagesStrictConfigurationUnavailableReturns503BeforeStreaming(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &strictHTTPSettingsRepo{err: errors.New("database unavailable")}
	settings := service.NewSettingService(repo, nil)
	cfg := &strictMessagesTestConfig{Config: config.Config{RunMode: config.RunModeSimple}}
	h := &GatewayHandler{gatewayService: newStrictGateway(cfg, nil, nil, nil, settings), cfg: &cfg.Config}
	// No scheduling, concurrency or upstream dependencies are supplied: this must
	// terminate before any of them, even when the caller asks for a stream.
	for _, stream := range []bool{false, true} {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		raw, err := json.Marshal(map[string]any{"model": "claude-sonnet-4-5", "stream": stream, "max_tokens": 256, "messages": []any{map[string]any{"role": "user", "content": "test"}}})
		require.NoError(t, err)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(raw))
		group := strictHTTPGroup(2101)
		key := &service.APIKey{ID: 1, UserID: 2, Group: group, GroupID: &group.ID, User: &service.User{ID: 2}}
		c.Set(string(middleware.ContextKeyAPIKey), key)
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 2})
		h.Messages(c)
		require.Equal(t, http.StatusServiceUnavailable, rec.Code)
		require.Equal(t, "strict_session_config_unavailable", rec.Header().Get("X-Sub2API-Error-Code"))
		require.Contains(t, rec.Body.String(), "config_unavailable")
	}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	plan, err := h.prepareStrictClaudeMessages(c, nil, service.PlatformGemini, "")
	require.NoError(t, err)
	require.False(t, plan.Active, "Gemini must not depend on the Claude binding configuration")
}

type strictHTTPChannelRepo struct {
	service.ChannelRepository
	channel service.Channel
}

func (r strictHTTPChannelRepo) ListAll(context.Context) ([]service.Channel, error) {
	return []service.Channel{r.channel}, nil
}
func (r strictHTTPChannelRepo) GetGroupPlatforms(context.Context, []int64) (map[int64]string, error) {
	return map[int64]string{r.channel.GroupIDs[0]: service.PlatformAnthropic}, nil
}

func TestMessagesStrictChannelRestrictionReturnsExplicitError(t *testing.T) {
	gid := int64(3501)
	group := strictHTTPGroup(gid)
	bound := strictHTTPAccount(1601, gid, "bound")
	other := strictHTTPAccount(1602, gid, "other")
	cfg := &strictMessagesTestConfig{Config: config.Config{RunMode: config.RunModeSimple}, binding: config.DefaultStrictSessionBindingConfig()}
	cfg.channels = service.NewChannelService(strictHTTPChannelRepo{channel: service.Channel{
		ID: 1, Status: service.StatusActive, GroupIDs: []int64{gid}, RestrictModels: true,
		BillingModelSource: service.BillingModelSourceRequested,
		ModelPricing:       []service.ChannelModelPricing{{Platform: service.PlatformAnthropic, Models: []string{"claude-opus-4"}}},
	}}, nil, nil, nil, nil)
	store := service.NewMemoryStrictSessionBindingStore()
	plan, err := service.ResolveStrictSessionPlan(cfg.binding, service.StrictSessionIdentityInput{GroupID: &gid, MetadataUserID: strictHTTPMetadata()})
	require.NoError(t, err)
	_, err = store.Create(context.Background(), &service.StrictSessionBinding{BindingKey: plan.BindingKey, AccountID: bound.ID})
	require.NoError(t, err)
	h, cleanup := newStrictMessagesHandler(t, cfg, group, []*service.Account{other}, &strictMessagesAccountRepo{byID: map[int64]*service.Account{bound.ID: bound, other.ID: other}}, store)
	t.Cleanup(cleanup)
	rec, selected := postStrictMessages(t, h, group, gid, nil, strictHTTPMetadata())
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	require.Equal(t, strictSessionErrorAccountUnavailable, rec.Header().Get(strictSessionErrorHeader))
	var payload struct {
		Error struct{ Type, Code, Reason string }
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Equal(t, strictSessionErrorType, payload.Error.Type)
	require.Equal(t, strictSessionErrorAccountUnavailable, payload.Error.Code)
	require.Equal(t, "channel_model_restricted", payload.Error.Reason)
	require.Zero(t, selected)
	require.Equal(t, bound.ID, requireStrictStoreAccount(t, store, cfg, gid))
}
