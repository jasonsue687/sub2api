package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestSettingHandlerStrictSessionBindingRoundTripHidesAPIKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &settingHandlerRepoStub{values: map[string]string{}}
	cfg := &config.Config{}
	svc := service.NewSettingService(repo, cfg)
	reader := strictFallbackGroupReader{groups: map[int64]*service.Group{
		44: {ID: 44, Platform: service.PlatformAnthropic, Status: service.StatusActive, Hydrated: true},
	}}
	svc.SetDefaultSubscriptionGroupReader(reader)
	handler := NewSettingHandler(svc, nil, nil, nil, nil, nil, nil)

	get := func() map[string]any {
		t.Helper()
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/settings", nil)
		handler.GetSettings(c)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var resp response.Response
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		data, ok := resp.Data.(map[string]any)
		require.True(t, ok)
		require.NotContains(t, rec.Body.String(), "yaml-secret")
		require.NotContains(t, rec.Body.String(), "page-secret")
		return data
	}

	before := get()
	require.NotContains(t, before, "strict_session_binding_source")
	require.Equal(t, true, before["strict_session_binding_enabled"])
	require.Equal(t, false, before["strict_session_third_party_api_key_configured"])
	require.Equal(t, "", before["strict_session_third_party_base_url"])

	put := func(body map[string]any) *httptest.ResponseRecorder {
		t.Helper()
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPut, "/api/v1/admin/settings", bytes.NewReader(raw))
		c.Request.Header.Set("Content-Type", "application/json")
		handler.UpdateSettings(c)
		return rec
	}

	saved := put(map[string]any{
		"strict_session_binding_enabled":             true,
		"strict_session_fallback_order":              "third_party_first",
		"strict_session_fallback_group_id":           44,
		"strict_session_third_party_enabled":         true,
		"strict_session_third_party_base_url":        "https://page.example",
		"strict_session_third_party_api_key":         "page-secret",
		"strict_session_third_party_timeout_seconds": 15,
		"strict_session_same_account_retry_limit":    1,
		"strict_session_session_header":              "X-Session-Id",
	})
	require.Equal(t, http.StatusOK, saved.Code, saved.Body.String())
	require.NotContains(t, saved.Body.String(), "page-secret")
	require.NotContains(t, saved.Body.String(), "yaml-secret")
	require.Equal(t, "page-secret", repo.values[service.SettingKeyStrictSessionThirdPartyAPIKey])
	require.NotContains(t, repo.values, "strict_session_binding_override")

	after := get()
	require.NotContains(t, after, "strict_session_binding_source")
	require.Equal(t, true, after["strict_session_third_party_api_key_configured"])
	require.Equal(t, "https://page.example", after["strict_session_third_party_base_url"])
	require.Equal(t, "third_party_first", after["strict_session_fallback_order"])
	require.Equal(t, float64(44), after["strict_session_fallback_group_id"])

	kept := put(map[string]any{
		"strict_session_binding_enabled":      true,
		"strict_session_third_party_enabled":  true,
		"strict_session_third_party_base_url": "https://page.example",
		"strict_session_third_party_api_key":  "",
		"strict_session_fallback_group_id":    44,
		"strict_session_fallback_order":       "third_party_first",
	})
	require.Equal(t, http.StatusOK, kept.Code, kept.Body.String())
	require.NotContains(t, kept.Body.String(), "page-secret")
	require.Equal(t, "page-secret", repo.values[service.SettingKeyStrictSessionThirdPartyAPIKey])

	rejected := put(map[string]any{
		"strict_session_binding_enabled":     true,
		"strict_session_fallback_group_id":   99,
		"strict_session_fallback_order":      "group_first",
		"strict_session_third_party_enabled": false,
	})
	require.Equal(t, http.StatusBadRequest, rejected.Code, rejected.Body.String())
	require.Equal(t, "44", repo.values[service.SettingKeyStrictSessionFallbackGroupID])

	omitted := put(map[string]any{"registration_enabled": true})
	require.Equal(t, http.StatusOK, omitted.Code, omitted.Body.String())
	require.NotContains(t, repo.values, "strict_session_binding_override")
	require.Equal(t, "page-secret", repo.values[service.SettingKeyStrictSessionThirdPartyAPIKey])
	require.Equal(t, "44", repo.values[service.SettingKeyStrictSessionFallbackGroupID])
}

type strictFallbackGroupReader struct {
	groups map[int64]*service.Group
}

func (r strictFallbackGroupReader) GetByID(_ context.Context, id int64) (*service.Group, error) {
	if group, ok := r.groups[id]; ok {
		return group, nil
	}
	return nil, service.ErrGroupNotFound
}

type strictAdminFailingRepo struct {
	*settingHandlerRepoStub
	writeErr error
	readErr  error
}

func (r *strictAdminFailingRepo) SetMultiple(ctx context.Context, updates map[string]string) error {
	if r.writeErr != nil {
		return r.writeErr
	}
	return r.settingHandlerRepoStub.SetMultiple(ctx, updates)
}
func (r *strictAdminFailingRepo) GetAll(ctx context.Context) (map[string]string, error) {
	if r.readErr != nil {
		return nil, r.readErr
	}
	return r.settingHandlerRepoStub.GetAll(ctx)
}
func TestSettingHandlerStrictFailedSaveKeepsPolicyAndAdminAccessible(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &strictAdminFailingRepo{settingHandlerRepoStub: &settingHandlerRepoStub{values: map[string]string{}}, readErr: errors.New("offline")}
	svc := service.NewSettingService(repo, &config.Config{})
	_, err := svc.StrictSessionBindingConfig(context.Background())
	require.ErrorIs(t, err, service.ErrStrictSessionConfigUnavailable)
	repo.readErr = nil
	h := NewSettingHandler(svc, nil, nil, nil, nil, nil, nil)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/settings", nil)
	h.GetSettings(c)
	require.Equal(t, http.StatusOK, rec.Code, "admin remains reachable even while the runtime error cache is active")
	put := func(enabled bool) *httptest.ResponseRecorder {
		body, err := json.Marshal(map[string]any{"strict_session_binding_enabled": enabled})
		require.NoError(t, err)
		response := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(response)
		ctx.Request = httptest.NewRequest(http.MethodPut, "/api/v1/admin/settings", bytes.NewReader(body))
		ctx.Request.Header.Set("Content-Type", "application/json")
		h.UpdateSettings(ctx)
		return response
	}
	saved := put(false)
	require.Equal(t, http.StatusOK, saved.Code, saved.Body.String())
	cfg, err := svc.StrictSessionBindingConfig(context.Background())
	require.NoError(t, err)
	require.False(t, cfg.Enabled)
	restarted := service.NewSettingService(repo, &config.Config{})
	cfg, err = restarted.StrictSessionBindingConfig(context.Background())
	require.NoError(t, err)
	require.False(t, cfg.Enabled)
	repo.writeErr = errors.New("cannot commit")
	failed := put(true)
	require.Equal(t, http.StatusInternalServerError, failed.Code, failed.Body.String())
	cfg, err = svc.StrictSessionBindingConfig(context.Background())
	require.NoError(t, err)
	require.False(t, cfg.Enabled)
	require.Equal(t, "false", repo.values[service.SettingKeyStrictSessionBindingEnabled])
}
