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
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestSettingHandlerStrictSessionBindingRoundTrip(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &settingHandlerRepoStub{values: map[string]string{"strict_session_third_party_api_key": "legacy-secret"}}
	svc := service.NewSettingService(repo, &config.Config{})
	h := NewSettingHandler(svc, nil, nil, nil, nil, nil, nil)
	put := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPut, "/api/v1/admin/settings", bytes.NewBufferString(body))
		c.Request.Header.Set("Content-Type", "application/json")
		h.UpdateSettings(c)
		return rec
	}
	saved := put(`{"strict_session_binding_enabled":true,"strict_session_session_header":"X-Conversation-Id","strict_session_same_account_retry_limit":2}`)
	require.Equal(t, http.StatusOK, saved.Code, saved.Body.String())
	require.Equal(t, "2", repo.values[service.SettingKeyStrictSessionSameAccountRetryLimit])
	require.Equal(t, "X-Conversation-Id", repo.values[service.SettingKeyStrictSessionSessionHeader])
	require.NotContains(t, saved.Body.String(), "legacy-secret")
	require.NotContains(t, saved.Body.String(), "strict_session_third_party")
	require.NotContains(t, saved.Body.String(), "strict_session_fallback")
	// Old admin clients cannot re-enable internal routing; unknown fields are ignored.
	legacy := put(`{"strict_session_fallback_group_id":44,"strict_session_third_party_enabled":true,"strict_session_third_party_api_key":"new-secret"}`)
	require.Equal(t, http.StatusOK, legacy.Code, legacy.Body.String())
	require.NotContains(t, repo.values, "strict_session_fallback_group_id")
	require.NotContains(t, repo.values, "strict_session_third_party_enabled")
	require.Equal(t, "legacy-secret", repo.values["strict_session_third_party_api_key"])
	rejected := put(`{"strict_session_same_account_retry_limit":-2}`)
	require.Equal(t, http.StatusBadRequest, rejected.Code, rejected.Body.String())
	omitted := put(`{"registration_enabled":true}`)
	require.Equal(t, http.StatusOK, omitted.Code, omitted.Body.String())
	require.Equal(t, "2", repo.values[service.SettingKeyStrictSessionSameAccountRetryLimit])
	require.Equal(t, "X-Conversation-Id", repo.values[service.SettingKeyStrictSessionSessionHeader])
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
