//go:build unit

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

const titleTemplate = "Please generate a concise title (2-5 words) for the following description. Return only <title>your title</title>.\n<description>Set up a sample workspace.</description>"

func titleBody(text string, blocks, stream bool) []byte {
	var system, content any = "You are a helpful title generation assistant.", text
	if blocks {
		system = []any{map[string]string{"type": "text", "text": system.(string)}}
		content = []any{map[string]string{"type": "text", "text": text}}
	}
	data, _ := json.Marshal(map[string]any{"model": "claude-sonnet-4-5", "max_tokens": 128, "stream": stream, "system": system, "messages": []any{map[string]any{"role": "user", "content": content}}})
	return data
}

func TestWarmupTitleDetection(t *testing.T) {
	require.Equal(t, InterceptTypeWarmup, detectInterceptType([]byte(`{"system":"Analyze if this message indicates a new conversation topic. If it does, extract a 2-3 word title","messages":[{"role":"user","content":"old topic"},{"role":"assistant","content":"ok"},{"role":"user","content":"new topic"}]}`), "claude-sonnet-4-5", 128, false))
	require.Equal(t, InterceptTypeWarmup, detectInterceptType([]byte(`{"messages":[{"role":"user","content":"previous context"},{"role":"assistant","content":"ok"},{"role":"user","content":"Please write a 5-10 word title for the following conversation: sample"}]}`), "claude-sonnet-4-5", 128, false))

	for _, blocks := range []bool{false, true} {
		require.Equal(t, InterceptTypeWarmup, detectInterceptType(titleBody(titleTemplate, blocks, false), "claude-sonnet-4-5", 128, false))
		for _, text := range []string{"Can you explain this template?\n" + titleTemplate, "The word title is in this conversation.", "Here is quoted code: ```" + titleTemplate + "```", "Please generate a title for my document."} {
			require.Equal(t, InterceptTypeNone, detectInterceptType(titleBody(text, blocks, false), "claude-sonnet-4-5", 128, false), text)
		}
	}
	require.Equal(t, InterceptTypeWarmup, detectInterceptType([]byte(`{"messages":[{"role":"user","content":"Warmup"}]}`), "claude-sonnet-4-5", 128, false))
	require.Equal(t, InterceptTypeWarmup, detectInterceptType(titleBody("Please write a 5-10 word title for the following conversation: sample", false, false), "claude-sonnet-4-5", 128, false))
	require.Equal(t, InterceptTypeNone, detectInterceptType([]byte(`{"messages":[{"role":"assistant","content":"Warmup"},{"role":"user","content":"hello"}]}`), "claude-sonnet-4-5", 128, false))
}

type forbiddenWarmupBindingStore struct{ t *testing.T }

func (s *forbiddenWarmupBindingStore) Get(context.Context, string) (*service.StrictSessionBinding, error) {
	s.t.Fatal("warmup read a session binding")
	return nil, nil
}
func (s *forbiddenWarmupBindingStore) Create(context.Context, *service.StrictSessionBinding) (*service.StrictSessionBinding, error) {
	s.t.Fatal("warmup created a session binding")
	return nil, nil
}

type warmupConcurrencyCache struct {
	fakeConcurrencyCache
	t     *testing.T
	users int
}

func (s *warmupConcurrencyCache) AcquireAccountSlot(context.Context, int64, int, string) (bool, error) {
	s.t.Fatal("warmup acquired account capacity")
	return false, nil
}
func (s *warmupConcurrencyCache) AcquireUserSlot(context.Context, int64, int, string) (bool, error) {
	s.users++
	return true, nil
}

func postWarmup(t *testing.T, h *GatewayHandler, group *service.Group, body []byte, session bool) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.Group, group))
	if session {
		c.Request.Header.Set("X-Session-Id", strictHTTPSessionID)
	}
	apiKey := &service.APIKey{ID: 3101, UserID: 4101, GroupID: &group.ID, Status: service.StatusActive, User: &service.User{ID: 4101, Concurrency: 10, Balance: 100}, Group: group}
	c.Set(string(middleware.ContextKeyAPIKey), apiKey)
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 4101, Concurrency: 10})
	h.Messages(c)
	_, selected := c.Get(opsAccountIDKey)
	require.False(t, selected, "warmup must never select a subscription account")
	return rec
}

func TestEarlyWarmupBypassesBindingButPreservesUserAdmission(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, stream := range []bool{false, true} {
		group := strictHTTPGroup(901)
		account := strictHTTPAccount(1, group.ID, "warmup")
		cfg := &strictMessagesTestConfig{Config: config.Config{RunMode: config.RunModeSimple}}
		cfg.binding.Enabled = true
		slots := &warmupConcurrencyCache{t: t}
		cfg.concurrency = service.NewConcurrencyService(slots)
		h, cleanup := newStrictMessagesHandler(t, cfg, group, []*service.Account{account}, nil, &forbiddenWarmupBindingStore{t: t})
		h.concurrencyHelper = NewConcurrencyHelper(cfg.concurrency, SSEPingFormatClaude, 0)
		for _, session := range []bool{false, true} {
			rec := postWarmup(t, h, group, titleBody(titleTemplate, false, stream), session)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			require.Contains(t, rec.Body.String(), "New Conversation")
			if stream {
				require.Contains(t, rec.Body.String(), "event: message_stop")
			} else {
				var response struct {
					Content []struct {
						Text string `json:"text"`
					} `json:"content"`
				}
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
				require.Equal(t, "<title>New Conversation</title>", response.Content[0].Text)
			}
		}
		require.Equal(t, 2, slots.users)
		ordinary := postWarmup(t, h, group, titleBody("Help me write a program", false, false), false)
		require.Equal(t, http.StatusBadRequest, ordinary.Code)
		require.Contains(t, ordinary.Body.String(), "strict_session_id_required")
		account.Credentials["intercept_warmup_requests"] = false
		disabled := postWarmup(t, h, group, titleBody(titleTemplate, false, false), false)
		require.Equal(t, http.StatusBadRequest, disabled.Code)
		require.Contains(t, disabled.Body.String(), "strict_session_id_required")
		cleanup()
	}
}

func TestEarlyWarmupForwardFailureNeverFallsBack(t *testing.T) {
	gin.SetMode(gin.TestMode)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("private-secret"))
	}))
	defer server.Close()
	group := strictHTTPGroup(902)
	account := strictHTTPAccount(1, group.ID, "forward")
	account.Credentials = map[string]any{"intercept_warmup_requests": true, "warmup_mode": "forward", "warmup_protocol": "openai", "warmup_base_url": server.URL, "warmup_api_key": "test-secret", "warmup_model": "deepseek-chat"}
	other := strictHTTPAccount(2, group.ID, "must-not-fallback")
	cfg := &strictMessagesTestConfig{Config: config.Config{RunMode: config.RunModeSimple}}
	cfg.binding.Enabled = true
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	h, cleanup := newStrictMessagesHandler(t, cfg, group, []*service.Account{other, account}, nil, &forbiddenWarmupBindingStore{t: t})
	defer cleanup()
	rec := postWarmup(t, h, group, titleBody(titleTemplate, false, false), false)
	require.Equal(t, http.StatusBadGateway, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "warmup_upstream_error")
	require.NotContains(t, rec.Body.String(), "secret")
	require.Equal(t, 1, calls)
	account.Credentials["warmup_model"] = ""
	rec = postWarmup(t, h, group, titleBody(titleTemplate, false, false), false)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Contains(t, rec.Body.String(), "warmup_configuration_error")
	require.Equal(t, 1, calls)
}

type warmupUsageRepo struct {
	service.UsageLogRepository
	logs []*service.UsageLog
}

func (r *warmupUsageRepo) CreateBestEffort(_ context.Context, log *service.UsageLog) error {
	r.logs = append(r.logs, log)
	return nil
}

func TestEarlyWarmupForwardSuccessJSONAndSSE(t *testing.T) {
	gin.SetMode(gin.TestMode)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"Sample Workspace"},"finish_reason":"stop"}],"usage":{"prompt_tokens":17,"completion_tokens":3}}`))
	}))
	defer server.Close()
	group := strictHTTPGroup(903)
	account := strictHTTPAccount(1, group.ID, "forward")
	account.Credentials = map[string]any{"intercept_warmup_requests": true, "warmup_mode": "forward", "warmup_protocol": "openai", "warmup_base_url": server.URL, "warmup_api_key": "test-secret", "warmup_model": "claude-haiku-4-5-20251001"}
	logs := &warmupUsageRepo{}
	cfg := &strictMessagesTestConfig{Config: config.Config{RunMode: config.RunModeSimple}, warmupUsage: logs}
	cfg.binding.Enabled = true
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	h, cleanup := newStrictMessagesHandler(t, cfg, group, []*service.Account{account}, nil, &forbiddenWarmupBindingStore{t: t})
	defer cleanup()
	for _, stream := range []bool{false, true} {
		rec := postWarmup(t, h, group, titleBody(titleTemplate, true, stream), false)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		if stream {
			require.Contains(t, rec.Body.String(), "event: message_start")
			require.Contains(t, rec.Body.String(), "event: message_stop")
			require.Contains(t, rec.Body.String(), `"input_tokens":17`)
			require.Contains(t, rec.Body.String(), `"output_tokens":3`)
			require.Contains(t, rec.Body.String(), "Sample Workspace")
		} else {
			var response struct {
				Model   string `json:"model"`
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
				Usage service.ClaudeUsage `json:"usage"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
			require.Equal(t, "claude-sonnet-4-5", response.Model)
			require.Equal(t, "<title>Sample Workspace</title>", response.Content[0].Text)
			require.Equal(t, 17, response.Usage.InputTokens)
			require.Equal(t, 3, response.Usage.OutputTokens)
		}
	}
	require.Len(t, logs.logs, 2)
	for _, log := range logs.logs {
		require.Equal(t, 17, log.InputTokens)
		require.Equal(t, 3, log.OutputTokens)
		require.Equal(t, "claude-sonnet-4-5", log.RequestedModel)
		require.Equal(t, "claude-haiku-4-5-20251001", log.Model)
		require.Equal(t, "/v1/messages/warmup", *log.InboundEndpoint)
	}
}

func TestEarlyWarmupConfigurationScopeAndPriority(t *testing.T) {
	group := strictHTTPGroup(904)
	first := strictHTTPAccount(10, group.ID, "first")
	first.Priority = 5
	second := strictHTTPAccount(20, group.ID, "second")
	second.Priority = 5
	second.Credentials["warmup_mode"] = "forward"
	foreign := strictHTTPAccount(1, 999, "foreign")
	foreign.Priority = 0
	foreign.Credentials["warmup_mode"] = "forward"
	cfg := &strictMessagesTestConfig{Config: config.Config{RunMode: config.RunModeSimple}}
	cfg.binding.Enabled = true
	h, cleanup := newStrictMessagesHandler(t, cfg, group, []*service.Account{foreign, second, first}, nil, &forbiddenWarmupBindingStore{t: t})
	defer cleanup()
	rec := postWarmup(t, h, group, titleBody(titleTemplate, false, false), false)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	first.Priority = 6
	rec = postWarmup(t, h, group, titleBody(titleTemplate, false, false), false)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Contains(t, rec.Body.String(), "warmup_configuration_error")
}

func TestEarlyWarmupStillChecksBalance(t *testing.T) {
	group := strictHTTPGroup(905)
	account := strictHTTPAccount(1, group.ID, "warmup")
	cfg := &strictMessagesTestConfig{Config: config.Config{RunMode: config.RunModeStandard}}
	cfg.binding.Enabled = true
	h, cleanup := newStrictMessagesHandler(t, cfg, group, []*service.Account{account}, nil, &forbiddenWarmupBindingStore{t: t})
	defer cleanup()
	h.billingCacheService = service.NewBillingCacheService(newHandlerInflightCache(-1), nil, nil, nil, nil, nil, &cfg.Config, nil)
	defer h.billingCacheService.Stop()
	rec := postWarmup(t, h, group, titleBody(titleTemplate, false, false), false)
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "insufficient balance")
	require.NotContains(t, rec.Body.String(), "New Conversation")
}

func TestSendMockInterceptStreamHaikuKeepsProbeSemantics(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	sendMockInterceptStream(c, "claude-haiku-4-5", InterceptTypeMaxTokensOneHaiku)
	require.Contains(t, rec.Body.String(), `"text":"#"`)
	require.Contains(t, rec.Body.String(), `"stop_reason":"max_tokens"`)
	require.NotContains(t, rec.Body.String(), "New Conversation")
}

func TestEarlyWarmupPreservesGeminiMixedScheduling(t *testing.T) {
	group := strictHTTPGroup(906)
	group.Platform = service.PlatformGemini
	account := strictHTTPAccount(1, group.ID, "mixed-antigravity")
	h, cleanup := newTestGatewayHandler(t, group, []*service.Account{account})
	defer cleanup()
	rec := postWarmup(t, h, group, titleBody(titleTemplate, false, false), false)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "New Conversation")
}
