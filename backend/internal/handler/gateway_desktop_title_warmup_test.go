//go:build unit

package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const desktopTitleModel = "claude-haiku-4-5-20251001"

// Synthetic description, retaining the desktop instruction shape that the old
// generate/write/create/provide prefix check missed.
const desktopTitlePrompt = "You are coming up with a succinct title (2-5 words) for this session. Reply with <title>your title</title>. <description>Build a sample workspace.</description>"

func desktopTitleRequest() map[string]any {
	return map[string]any{
		"model": desktopTitleModel, "max_tokens": 200,
		"system":   "You write short session titles. Reply with only the tagged fields the prompt asks for.",
		"messages": []any{map[string]any{"role": "user", "content": desktopTitlePrompt}},
	}
}

func TestDesktopTitleDetection(t *testing.T) {
	textBlocks := func(text string) any { return []any{map[string]any{"type": "text", "text": text}} }
	setContent := func(content any) func(map[string]any) {
		return func(req map[string]any) { req["messages"] = []any{map[string]any{"role": "user", "content": content}} }
	}
	cases := []struct {
		name string
		edit func(map[string]any)
		want InterceptType
	}{
		{"desktop wording", nil, InterceptTypeWarmup},
		{"arbitrary wording", setContent("Name this session briefly."), InterceptTypeWarmup},
		{"text blocks", setContent(textBlocks(desktopTitlePrompt)), InterceptTypeWarmup},
		{"system blocks", func(r map[string]any) { r["system"] = textBlocks(r["system"].(string)) }, InterceptTypeWarmup},
		{"system whitespace", func(r map[string]any) { r["system"] = " \n" + strings.ReplaceAll(r["system"].(string), " ", "\n\t") }, InterceptTypeWarmup},
		{"empty tools", func(r map[string]any) { r["tools"] = []any{}; r["tool_choice"] = nil }, InterceptTypeWarmup},
		{"opus", func(r map[string]any) { r["model"] = "claude-opus-4-6" }, InterceptTypeNone},
		{"different token budget", func(r map[string]any) { r["max_tokens"] = 201 }, InterceptTypeNone},
		{"generic system", func(r map[string]any) { r["system"] = "You are a helpful assistant." }, InterceptTypeNone},
		{"title keyword only", func(r map[string]any) { r["system"] = "Discuss a title." }, InterceptTypeNone},
		{"extra system instructions", func(r map[string]any) { r["system"] = r["system"].(string) + " Also answer other questions." }, InterceptTypeNone},
		{"missing system", func(r map[string]any) { delete(r, "system") }, InterceptTypeNone},
		{"null system", func(r map[string]any) { r["system"] = nil }, InterceptTypeNone},
		{"untyped system block", func(r map[string]any) { r["system"] = []any{map[string]any{"text": r["system"]}} }, InterceptTypeNone},
		{"empty content", setContent(" \n"), InterceptTypeNone},
		{"null content", setContent(nil), InterceptTypeNone},
		{"empty blocks", setContent([]any{}), InterceptTypeNone},
		{"untyped content", setContent([]any{map[string]any{"text": desktopTitlePrompt}}), InterceptTypeNone},
		{"image content", setContent([]any{map[string]any{"type": "text", "text": desktopTitlePrompt}, map[string]any{"type": "image", "source": map[string]any{"type": "url", "url": "https://example.com/sample.png"}}}), InterceptTypeNone},
		{"tool use content", setContent([]any{map[string]any{"type": "tool_use", "name": "example"}}), InterceptTypeNone},
		{"tools declared", func(r map[string]any) { r["tools"] = []any{map[string]any{"name": "example"}} }, InterceptTypeNone},
		{"tool choice", func(r map[string]any) { r["tool_choice"] = map[string]any{"type": "auto"} }, InterceptTypeNone},
		{"assistant message", func(r map[string]any) {
			r["messages"] = []any{map[string]any{"role": "assistant", "content": desktopTitlePrompt}}
		}, InterceptTypeNone},
		{"multiple messages", func(r map[string]any) {
			r["messages"] = append(r["messages"].([]any), map[string]any{"role": "assistant", "content": "previous context"})
		}, InterceptTypeNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := desktopTitleRequest()
			if tc.edit != nil {
				tc.edit(req)
			}
			body, err := json.Marshal(req)
			require.NoError(t, err)
			assert.Equal(t, tc.want, detectInterceptType(body, req["model"].(string), req["max_tokens"].(int), false))
		})
	}
}

func TestDesktopTitleRouting(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const taggedResult = "<title>Sample &amp; Workspace</title>\n<branch>sample-workspace</branch>"
	for _, mode := range []string{"mock", "forward"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				assert.Equal(t, "/v1/chat/completions", r.URL.Path)
				assert.Equal(t, "Bearer test-secret", r.Header.Get("Authorization"))
				var upstream struct {
					Model    string `json:"model"`
					Messages []struct {
						Role    string `json:"role"`
						Content string `json:"content"`
					} `json:"messages"`
				}
				if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&upstream)) {
					return
				}
				assert.Equal(t, "claude-haiku-4-5", upstream.Model)
				if assert.Len(t, upstream.Messages, 2) {
					assert.Equal(t, "system", upstream.Messages[0].Role)
					assert.Equal(t, desktopTitleRequest()["system"], upstream.Messages[0].Content)
					assert.Equal(t, "user", upstream.Messages[1].Role)
					assert.Contains(t, upstream.Messages[1].Content, "<description>")
				}
				assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": taggedResult}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 17, "completion_tokens": 3}}))
			}))
			defer server.Close()
			group := strictHTTPGroup(911)
			group.ClaudeCodeOnly = true
			account := strictHTTPAccount(1, group.ID, "desktop-title")
			account.Credentials = map[string]any{"intercept_warmup_requests": true, "warmup_mode": mode, "warmup_protocol": "openai", "warmup_base_url": server.URL, "warmup_api_key": "test-secret", "warmup_model": "claude-haiku-4-5"}
			logs := &warmupUsageRepo{}
			cfg := &strictMessagesTestConfig{Config: config.Config{RunMode: config.RunModeSimple}, warmupUsage: logs}
			cfg.binding.Enabled = true
			cfg.Security.URLAllowlist.AllowInsecureHTTP = true
			slots := &warmupConcurrencyCache{t: t}
			cfg.concurrency = service.NewConcurrencyService(slots)
			h, cleanup := newStrictMessagesHandler(t, cfg, group, []*service.Account{account}, nil, &forbiddenWarmupBindingStore{t: t})
			defer cleanup()
			h.concurrencyHelper = NewConcurrencyHelper(cfg.concurrency, SSEPingFormatClaude, 0)
			for _, stream := range []bool{false, true} {
				// Also cover overlap with the legacy XML detector: desktop tagged
				// output must not be escaped or wrapped a second time.
				for _, prompt := range []string{desktopTitlePrompt, titleTemplate} {
					req := desktopTitleRequest()
					req["stream"] = stream
					req["messages"] = []any{map[string]any{"role": "user", "content": prompt}}
					body, err := json.Marshal(req)
					require.NoError(t, err)
					rec := postWarmup(t, h, group, body, false)
					require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
					want := "<title>New Conversation</title>"
					if mode == "forward" {
						want = taggedResult
					}
					if stream {
						encoded, err := json.Marshal(want)
						require.NoError(t, err)
						assert.Contains(t, rec.Body.String(), `"text":`+string(encoded))
						assert.Contains(t, rec.Body.String(), "event: message_stop")
					} else {
						var response struct {
							Content []struct {
								Text string `json:"text"`
							} `json:"content"`
						}
						require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
						require.Len(t, response.Content, 1)
						assert.Equal(t, want, response.Content[0].Text)
					}
				}
			}
			assert.Equal(t, 4, slots.users)
			if mode == "forward" {
				assert.Equal(t, 4, calls)
				require.Len(t, logs.logs, 4)
				for _, log := range logs.logs {
					assert.Equal(t, desktopTitleModel, log.RequestedModel)
					assert.Equal(t, "claude-haiku-4-5", log.Model)
					assert.Equal(t, "/v1/messages/warmup", *log.InboundEndpoint)
					assert.Equal(t, 17, log.InputTokens)
					assert.Equal(t, 3, log.OutputTokens)
				}
			} else {
				assert.Zero(t, calls)
			}
			// The auxiliary exception must not relax Claude-Code-only admission
			// for legacy warmups or ordinary conversations.
			legacy := postWarmup(t, h, group, titleBody(titleTemplate, false, false), false)
			assert.Equal(t, http.StatusServiceUnavailable, legacy.Code)
			assert.Contains(t, legacy.Body.String(), "warmup_configuration_error")
			ordinary := postWarmup(t, h, group, titleBody("Help me write a program", false, false), false)
			assert.Equal(t, http.StatusBadRequest, ordinary.Code)
			assert.Contains(t, ordinary.Body.String(), "strict_session_id_required")
			account.Credentials["intercept_warmup_requests"] = false
			body, err := json.Marshal(desktopTitleRequest())
			require.NoError(t, err)
			rec := postWarmup(t, h, group, body, false)
			assert.Equal(t, http.StatusBadRequest, rec.Code)
			assert.Contains(t, rec.Body.String(), "strict_session_id_required")
			if mode == "forward" {
				assert.Equal(t, 4, calls, "rejected requests must not call the external provider")
			} else {
				assert.Zero(t, calls)
			}
		})
	}
}
