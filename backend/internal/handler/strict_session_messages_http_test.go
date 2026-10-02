package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
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
	cfg := &config.Config{RunMode: config.RunModeSimple}
	h, cleanup := newStrictMessagesHandler(t, cfg, group, []*service.Account{blocked, warmup}, &strictMessagesAccountRepo{byID: map[int64]*service.Account{
		blocked.ID: blocked,
		warmup.ID:  warmup,
	}}, nil)
	defer cleanup()

	rec, selected := postStrictMessages(t, h, group, groupID, nil, "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, warmup.ID, selected)
	require.NotContains(t, rec.Body.String(), "session_binding_error")
	require.Contains(t, rec.Body.String(), "New Conversation")
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
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Gateway.StrictSessionBinding.Enabled = true
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
	require.Contains(t, first.Body.String(), "New Conversation")

	stored := requireStrictStoreAccount(t, store, cfg, groupID)
	require.Equal(t, bound.ID, stored)

	resetAt := time.Now().Add(time.Hour)
	bound.RateLimitResetAt = &resetAt
	cache.accounts = []*service.Account{bound, other}

	second, selected := postStrictMessages(t, h, group, groupID, zapLogger, strictHTTPMetadata())
	require.Equal(t, http.StatusServiceUnavailable, second.Code, second.Body.String())
	require.NotEqual(t, other.ID, selected)
	require.Contains(t, second.Body.String(), `"type":"session_binding_error"`)
	require.Contains(t, second.Body.String(), `"code":"strict_session_fallback_required"`)
	require.NotContains(t, second.Body.String(), "New Conversation")
	require.NotContains(t, second.Body.String(), strictHTTPSessionID)
	require.Empty(t, second.Header().Get("X-Sub2API-Bound-Account-Id"))
	require.Equal(t, bound.ID, requireStrictStoreAccount(t, store, cfg, groupID))

	logged := slogBuf.String() + zapBuf.String()
	require.NotContains(t, logged, strictHTTPSessionID)
	require.NotContains(t, logged, strictHTTPDeviceID)
	require.NotContains(t, logged, strictHTTPMetadata())
	require.Contains(t, logged, "session_fp")
}

func TestDispatchStrictFallbackDoesNotCallThirdPartyAfterStreamStarted(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var hits int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"msg_third"}`))
	}))
	t.Cleanup(upstream.Close)

	cfg := &config.Config{}
	cfg.Gateway.StrictSessionBinding.Enabled = true
	cfg.Gateway.StrictSessionBinding.ThirdParty.Enabled = true
	cfg.Gateway.StrictSessionBinding.ThirdParty.BaseURL = upstream.URL
	cfg.Gateway.StrictSessionBinding.ThirdParty.APIKey = "relay-secret"
	gw := &service.GatewayService{}
	// cfg is unexported; construct through NewGatewayService so the third-party client is wired.
	gw = newStrictGateway(cfg, nil, nil)
	require.True(t, gw.StrictThirdPartyEnabled())
	h := &GatewayHandler{gatewayService: gw, cfg: cfg}

	t.Run("stream flag with unchanged writer size", func(t *testing.T) {
		hits = 0
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		h.dispatchStrictFallback(c, &strictSessionRuntime{}, nil, 7, "rate_limited", true, true, []byte(`{}`), true, c.Writer.Size())
		require.Zero(t, hits)
		require.Contains(t, rec.Body.String(), "strict_session_stream_interrupted")
	})

	t.Run("bytes written after the entry baseline", func(t *testing.T) {
		hits = 0
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		_, err := c.Writer.Write([]byte("data: started\n\n"))
		require.NoError(t, err)
		h.dispatchStrictFallback(c, &strictSessionRuntime{}, nil, 7, "rate_limited", true, true, []byte(`{}`), false, 0)
		require.Zero(t, hits)
		require.Contains(t, rec.Body.String(), "strict_session_stream_interrupted")
	})
}

func newStrictGateway(cfg *config.Config, repo service.AccountRepository, snapshot *service.SchedulerSnapshotService) *service.GatewayService {
	return service.NewGatewayService(
		repo,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		cfg,
		snapshot,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
	)
}

func newStrictMessagesHandler(t *testing.T, cfg *config.Config, group *service.Group, accounts []*service.Account, repo service.AccountRepository, store service.StrictSessionBindingStore) (*GatewayHandler, func()) {
	t.Helper()
	return newStrictMessagesHandlerWithCache(t, cfg, group, &fakeSchedulerCache{accounts: accounts}, repo, store)
}

func newStrictMessagesHandlerWithCache(t *testing.T, cfg *config.Config, group *service.Group, cache *fakeSchedulerCache, repo service.AccountRepository, store service.StrictSessionBindingStore) (*GatewayHandler, func()) {
	t.Helper()
	if cfg == nil {
		cfg = &config.Config{RunMode: config.RunModeSimple}
	}
	snapshot := service.NewSchedulerSnapshotService(cache, nil, nil, nil, nil)
	gw := newStrictGateway(cfg, repo, snapshot)
	gw.SetStrictSessionBindingStore(store)
	billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	h := &GatewayHandler{
		gatewayService:           gw,
		billingCacheService:      billing,
		concurrencyHelper:        NewConcurrencyHelper(service.NewConcurrencyService(&fakeConcurrencyCache{}), SSEPingFormatClaude, 0),
		cfg:                      cfg,
		maxAccountSwitches:       10,
		maxAccountSwitchesGemini: 3,
	}
	return h, func() { billing.Stop() }
}

func postStrictMessages(t *testing.T, h *GatewayHandler, group *service.Group, groupID int64, zapLogger *zap.Logger, metadataUserID string) (*httptest.ResponseRecorder, int64) {
	t.Helper()
	payload := map[string]any{
		"model":      "claude-sonnet-4-5",
		"max_tokens": 256,
		"messages": []any{
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "Warmup"}}},
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
	ctx := context.WithValue(req.Context(), ctxkey.Group, group)
	if zapLogger != nil {
		ctx = logger.IntoContext(ctx, zapLogger)
	}
	c.Request = req.WithContext(ctx)

	apiKey := &service.APIKey{
		ID:      3101,
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

func requireStrictStoreAccount(t *testing.T, store service.StrictSessionBindingStore, cfg *config.Config, groupID int64) int64 {
	t.Helper()
	plan, err := service.ResolveStrictSessionPlan(cfg, service.StrictSessionIdentityInput{
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
