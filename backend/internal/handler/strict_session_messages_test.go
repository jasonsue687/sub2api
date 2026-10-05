package handler

import (
	"context"
	"fmt"
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

func TestStrictSameAccountRetryLimitZeroDisablesRetries(t *testing.T) {
	fs := NewFailoverState(10, true)
	fs.EnableStrictBinding(0)
	mock := &mockTempUnscheduler{}
	err := &service.UpstreamFailoverError{
		StatusCode:               http.StatusTooManyRequests,
		RetryableOnSameAccount:   true,
		SameAccountRetryDelay:    time.Millisecond,
		SameAccountRetryDeadline: time.Now().Add(time.Minute),
	}
	action := fs.HandleFailoverError(context.Background(), mock, 7, service.PlatformAnthropic, 5, err)
	require.Equal(t, FailoverExhausted, action)
	require.Zero(t, fs.SameAccountRetryCount[7])
	require.Zero(t, fs.SwitchCount)
	require.Empty(t, fs.FailedAccountIDs)
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

func TestStrictRetryCapPreservesAccountLimit(t *testing.T) {
	for _, accountLimit := range []int{0, 1} {
		t.Run(fmt.Sprintf("account_limit=%d", accountLimit), func(t *testing.T) {
			fs := NewFailoverState(10, true)
			fs.EnableStrictBinding(3)
			upstreamErr := &service.UpstreamFailoverError{
				StatusCode: http.StatusBadGateway, RetryableOnSameAccount: true,
				SameAccountRetryDelay: time.Millisecond,
			}
			for attempt := 0; attempt < accountLimit; attempt++ {
				require.Equal(t, FailoverContinue, fs.HandleFailoverError(context.Background(), &mockTempUnscheduler{}, 7, service.PlatformAnthropic, accountLimit, upstreamErr))
			}
			require.Equal(t, FailoverExhausted, fs.HandleFailoverError(context.Background(), &mockTempUnscheduler{}, 7, service.PlatformAnthropic, accountLimit, upstreamErr))
			require.Equal(t, accountLimit, fs.SameAccountRetryCount[7])
			require.Zero(t, fs.SwitchCount)
		})
	}
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
	sessionID := "c72554f2-1234-5678-abcd-123456789abc"
	c.Request.Header.Set("X-Session-Id", sessionID)
	handler.respondStrictSessionError(c, http.StatusServiceUnavailable, strictSessionErrorAccountUnavailable, "rate_limited", false)
	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	require.Equal(t, strictSessionErrorAccountUnavailable, recorder.Header().Get(strictSessionErrorHeader))
	require.Empty(t, recorder.Header().Get("X-Sub2API-Bound-Account-Id"))
	require.Contains(t, recorder.Body.String(), `"type":"session_binding_error"`)
	require.Contains(t, recorder.Body.String(), `"code":"strict_session_account_unavailable"`)
	require.Contains(t, recorder.Body.String(), `"reason":"rate_limited"`)
	require.NotContains(t, recorder.Body.String(), sessionID)
	require.NotContains(t, recorder.Body.String(), "42")
}

func TestStrictUpstreamFailureReturnsAccountUnavailable(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			fs := NewFailoverState(10, true)
			fs.EnableStrictBinding(0)
			h := &GatewayHandler{}
			action := h.handleStrictUpstreamFailover(c, fs, &strictSessionRuntime{Active: true}, nil, &service.Account{ID: 7, Platform: service.PlatformAnthropic}, &service.UpstreamFailoverError{StatusCode: status}, "bound", false, c.Writer.Size(), nil)
			require.Equal(t, strictFlowStop, action)
			require.Equal(t, http.StatusServiceUnavailable, rec.Code)
			require.Equal(t, strictSessionErrorAccountUnavailable, rec.Header().Get(strictSessionErrorHeader))
			require.Contains(t, rec.Body.String(), `"reason":"upstream_exhausted"`)
			require.Zero(t, fs.SwitchCount)
			require.Empty(t, fs.FailedAccountIDs)
		})
	}
}
