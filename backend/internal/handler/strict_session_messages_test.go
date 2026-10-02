package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestMaxAccountSwitchesZeroKeepsDefault(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.MaxAccountSwitches = 0
	handler := NewGatewayHandler(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, cfg, nil)
	require.Equal(t, 10, handler.maxAccountSwitches)

	cfg.Gateway.MaxAccountSwitches = 4
	handler = NewGatewayHandler(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, cfg, nil)
	require.Equal(t, 4, handler.maxAccountSwitches)
}

func TestStrictFailoverDoesNotSwitchAccounts(t *testing.T) {
	fs := NewFailoverState(10, true)
	fs.EnableStrictBinding(0)
	mock := &mockTempUnscheduler{}
	err := &service.UpstreamFailoverError{StatusCode: http.StatusBadGateway}
	action := fs.HandleFailoverError(context.Background(), mock, 7, service.PlatformAnthropic, 3, err)
	require.Equal(t, FailoverExhausted, action)
	require.Equal(t, 0, fs.SwitchCount)
	require.Empty(t, fs.FailedAccountIDs)
	require.Empty(t, mock.calls)
	require.Equal(t, FailoverExhausted, fs.HandleSelectionExhausted(context.Background()))
}

func TestStrictFailoverRetriesSameAccountThenStops(t *testing.T) {
	fs := NewFailoverState(10, true)
	fs.EnableStrictBinding(1)
	mock := &mockTempUnscheduler{}
	err := &service.UpstreamFailoverError{
		StatusCode:             http.StatusBadGateway,
		RetryableOnSameAccount: true,
		SameAccountRetryDelay:  time.Millisecond,
	}
	require.Equal(t, FailoverContinue, fs.HandleFailoverError(context.Background(), mock, 7, service.PlatformAnthropic, 5, err))
	require.Equal(t, FailoverExhausted, fs.HandleFailoverError(context.Background(), mock, 7, service.PlatformAnthropic, 5, err))
	require.Equal(t, 0, fs.SwitchCount)
	require.Empty(t, fs.FailedAccountIDs)
}

func TestDisabledStrictFailoverStillSwitches(t *testing.T) {
	fs := NewFailoverState(10, false)
	mock := &mockTempUnscheduler{}
	err := &service.UpstreamFailoverError{StatusCode: http.StatusBadGateway}
	action := fs.HandleFailoverError(context.Background(), mock, 7, service.PlatformAnthropic, 0, err)
	require.Equal(t, FailoverContinue, action)
	require.Equal(t, 1, fs.SwitchCount)
	_, excluded := fs.FailedAccountIDs[7]
	require.True(t, excluded)
}

func TestStrictSessionErrorIsRecognizable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	handler := &GatewayHandler{}
	handler.respondStrictSessionError(c, http.StatusServiceUnavailable, strictSessionErrorFallbackRequired, "rate_limited", 42, false)
	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	require.Equal(t, strictSessionErrorFallbackRequired, recorder.Header().Get(strictSessionErrorHeader))
	require.Equal(t, "42", recorder.Header().Get(strictSessionAccountHeader))
	require.Contains(t, recorder.Body.String(), `"type":"session_binding_error"`)
	require.Contains(t, recorder.Body.String(), `"code":"strict_session_fallback_required"`)
	require.NotContains(t, recorder.Body.String(), "session-secret")
}
