package handler

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/anthropicaudit"
	pkghttputil "github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestAnthropicInboundCapturePreservesParametersFromAnyClient(t *testing.T) {
	t.Setenv("SUB2API_ANTHROPIC_INBOUND_AUDIT_ENABLED", "true")
	t.Setenv("SUB2API_ANTHROPIC_AUDIT_ACCOUNT_IDS", "*")
	t.Cleanup(func() {
		anthropicaudit.StartCapturePersist(func(context.Context, anthropicaudit.Record) error { return nil })
	})
	for _, endpoint := range []string{"/v1/messages", "/v1/messages/count_tokens", "/messages/count_tokens", "/antigravity/v1/messages"} {
		for _, userAgent := range []string{"claude-cli/2.1.284 (external, cli)", "claude-cli/2.1.284 (external, local-agent, agent-sdk/0.3.284)", "curl/8.0", ""} {
			t.Run(endpoint+"/"+userAgent, func(t *testing.T) {
				records := make(chan anthropicaudit.Record, 1)
				anthropicaudit.StartCapturePersist(func(_ context.Context, rec anthropicaudit.Record) error { records <- rec; return nil })
				router := gin.New()
				router.Use(middleware.ClientRequestID(), func(c *gin.Context) {
					c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: 42, UserID: 7, Name: "demo-key", User: &service.User{Username: "demo-user"}})
				})
				router.Use(AnthropicInboundCaptureMiddleware())
				raw := `{"model":"client-alias","stream":false,"system":"original system","metadata":{"user_id":"original-id"},"messages":[{"role":"user","content":"private"}],"tools":[{"name":"private-tool"}],"tool_choice":{"type":"auto"}}`
				router.POST(endpoint, func(c *gin.Context) {
					body, err := pkghttputil.ReadLenientJSONRequestBodyWithPrealloc(c.Request, 0)
					require.NoError(t, err)
					require.Equal(t, raw, string(body), "capture must not change the forwarded request")
					body, err = sjson.SetBytes(body, "model", "rewritten-upstream-model")
					require.NoError(t, err)
					c.Request.Header.Set("User-Agent", "rewritten-agent")
					c.Data(http.StatusOK, "application/json", body)
				})
				req := httptest.NewRequest(http.MethodPost, endpoint, strings.NewReader(raw))
				if userAgent != "" {
					req.Header.Set("User-Agent", userAgent)
				}
				response := httptest.NewRecorder()
				router.ServeHTTP(response, req)
				require.Equal(t, http.StatusOK, response.Code)
				assert.Contains(t, response.Body.String(), "rewritten-upstream-model")
				select {
				case rec := <-records:
					assert.Equal(t, "client-alias", rec.Model)
					assert.Equal(t, "original system", gjson.GetBytes(rec.Body, "system").String())
					assert.Equal(t, "original-id", gjson.GetBytes(rec.Body, "metadata.user_id").String())
					assert.Equal(t, endpoint, rec.Endpoint)
					assert.NotEmpty(t, rec.ClientRequestID)
					for _, key := range []string{"messages", "tools", "tool_choice"} {
						assert.Equal(t, anthropicaudit.OmittedInboundValue, gjson.GetBytes(rec.Body, key).String())
					}
					assert.NotContains(t, string(rec.Body), "private")
					assert.NotContains(t, string(rec.Headers), "rewritten-agent")
					var headers []anthropicaudit.HeaderPair
					require.NoError(t, json.Unmarshal(rec.Headers, &headers))
					if userAgent != "" {
						assert.Contains(t, headers, anthropicaudit.HeaderPair{Name: "User-Agent", Value: userAgent})
					}
				case <-time.After(2 * time.Second):
					t.Fatal("missing inbound capture")
				}
			})
		}
	}
}

func TestAnthropicInboundCaptureKeepsReceivedEncodingHeaders(t *testing.T) {
	t.Setenv("SUB2API_ANTHROPIC_INBOUND_AUDIT_ENABLED", "true")
	t.Setenv("SUB2API_ANTHROPIC_AUDIT_ACCOUNT_IDS", "*")
	records := make(chan anthropicaudit.Record, 1)
	anthropicaudit.StartCapturePersist(func(_ context.Context, rec anthropicaudit.Record) error { records <- rec; return nil })
	t.Cleanup(func() {
		anthropicaudit.StartCapturePersist(func(context.Context, anthropicaudit.Record) error { return nil })
	})
	var encoded bytes.Buffer
	writer := gzip.NewWriter(&encoded)
	_, err := writer.Write([]byte(`{"model":"original-model","messages":[]}`))
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: 1}) }, AnthropicInboundCaptureMiddleware())
	router.POST("/v1/messages", func(c *gin.Context) {
		assert.Empty(t, c.Request.Header.Get("Content-Encoding"))
		body, readErr := pkghttputil.ReadLenientJSONRequestBodyWithPrealloc(c.Request, 0)
		require.NoError(t, readErr)
		assert.Equal(t, "original-model", gjson.GetBytes(body, "model").String())
		c.Status(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", &encoded)
	req.Header.Set("Content-Encoding", "gzip")
	router.ServeHTTP(httptest.NewRecorder(), req)
	select {
	case rec := <-records:
		var headers []anthropicaudit.HeaderPair
		require.NoError(t, json.Unmarshal(rec.Headers, &headers))
		assert.Contains(t, headers, anthropicaudit.HeaderPair{Name: "Content-Encoding", Value: "gzip"})
		assert.Equal(t, "original-model", rec.Model)
	case <-time.After(2 * time.Second):
		t.Fatal("missing gzip inbound capture")
	}
}

func TestAnthropicInboundCaptureRunsBeforeJSONRepair(t *testing.T) {
	t.Setenv("SUB2API_ANTHROPIC_INBOUND_AUDIT_ENABLED", "true")
	t.Setenv("SUB2API_ANTHROPIC_AUDIT_ACCOUNT_IDS", "*")
	records := make(chan anthropicaudit.Record, 1)
	anthropicaudit.StartCapturePersist(func(_ context.Context, rec anthropicaudit.Record) error { records <- rec; return nil })
	t.Cleanup(func() {
		anthropicaudit.StartCapturePersist(func(context.Context, anthropicaudit.Record) error { return nil })
	})
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: 1}) }, AnthropicInboundCaptureMiddleware())
	router.POST("/v1/messages", func(c *gin.Context) {
		body, err := pkghttputil.ReadLenientJSONRequestBodyWithPrealloc(c.Request, 0)
		require.NoError(t, err)
		assert.True(t, json.Valid(body))
		c.Status(http.StatusOK)
	})
	raw := "{\"model\":\"m\",\"system\":\"unescaped\nnewline\",\"messages\":[]}"
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(raw)))
	select {
	case rec := <-records:
		assert.Equal(t, "invalid_json", rec.BodyState)
		assert.Empty(t, rec.Body, "repaired JSON must not masquerade as the original request")
		assert.Equal(t, len(raw), rec.OriginalBytes)
	case <-time.After(2 * time.Second):
		t.Fatal("missing invalid JSON capture")
	}
}
