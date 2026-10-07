//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func warmupCredentials() map[string]any {
	return map[string]any{"intercept_warmup_requests": true, "warmup_mode": "forward", "warmup_protocol": "openai", "warmup_base_url": "https://example.test/v1", "warmup_api_key": "test-only-secret", "warmup_model": "deepseek-chat", "warmup_timeout_seconds": 30}
}

func TestWarmupCredentialsValidationAndSecretMerge(t *testing.T) {
	require.NoError(t, ValidateWarmupCredentials(map[string]any{"intercept_warmup_requests": true}))
	require.Equal(t, "mock", WarmupConfigFromCredentials(nil).Mode)
	for _, test := range []struct {
		key   string
		value any
	}{
		{"warmup_mode", "fallback"}, {"warmup_protocol", "unknown"}, {"warmup_model", ""}, {"warmup_api_key", ""},
		{"warmup_base_url", "https://user:secret@example.test"}, {"warmup_base_url", "https://example.test?key=secret"},
		{"warmup_base_url", "file:///tmp/api"}, {"warmup_timeout_seconds", 0}, {"warmup_timeout_seconds", 121}, {"warmup_timeout_seconds", 1.2},
	} {
		t.Run(test.key+"/invalid", func(t *testing.T) {
			c := warmupCredentials()
			c[test.key] = test.value
			require.Error(t, ValidateWarmupCredentials(c))
		})
	}
	stored := warmupCredentials()
	incoming := warmupCredentials()
	delete(incoming, "warmup_api_key")
	merged := MergePreservingSensitiveCreds(stored, incoming)
	require.Equal(t, "test-only-secret", merged["warmup_api_key"])
	require.NoError(t, ValidateWarmupCredentials(merged))
	require.True(t, IsSensitiveCredentialKey("warmup_api_key"))
	incoming["warmup_api_key"] = "rotated"
	require.Equal(t, "rotated", MergePreservingSensitiveCreds(stored, incoming)["warmup_api_key"])
}

func TestForwardWarmupProtocols(t *testing.T) {
	for _, protocol := range []string{"openai", "anthropic"} {
		for _, blocks := range []bool{false, true} {
			t.Run(protocol+map[bool]string{false: "/strings", true: "/blocks"}[blocks], func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					require.Empty(t, r.Header.Get("X-Session-Id"))
					var req map[string]any
					require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
					require.Equal(t, "external-title-model", req["model"])
					require.Equal(t, false, req["stream"])
					require.NotContains(t, req, "metadata")
					require.NotContains(t, req, "tools")
					messages := req["messages"].([]any)
					if protocol == "openai" {
						require.Equal(t, "/v1/chat/completions", r.URL.Path)
						require.Equal(t, "Bearer configured-test-key", r.Header.Get("Authorization"))
						require.Empty(t, r.Header.Get("x-api-key"))
						require.Len(t, messages, 2)
						require.Equal(t, "title instructions", messages[0].(map[string]any)["content"])
						_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"Workspace Setup"},"finish_reason":"stop"}],"usage":{"prompt_tokens":17,"completion_tokens":3,"prompt_tokens_details":{"cached_tokens":5}}}`))
					} else {
						require.Equal(t, "/v1/messages", r.URL.Path)
						require.Equal(t, "configured-test-key", r.Header.Get("x-api-key"))
						require.Equal(t, "2023-06-01", r.Header.Get("anthropic-version"))
						require.Empty(t, r.Header.Get("Authorization"))
						require.Len(t, messages, 1)
						require.Equal(t, "title instructions", req["system"])
						_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"Workspace Setup"}],"stop_reason":"end_turn","usage":{"input_tokens":12,"output_tokens":3,"cache_read_input_tokens":5}}`))
					}
				}))
				defer server.Close()
				var system, content any = "title instructions", "sample description"
				if blocks {
					system = []any{map[string]string{"type": "text", "text": "title instructions"}}
					content = []any{map[string]string{"type": "text", "text": "sample description"}}
				}
				body, _ := json.Marshal(map[string]any{"system": system, "messages": []any{map[string]any{"role": "user", "content": content}}, "metadata": map[string]string{"user_id": "never-forward"}, "max_tokens": 128})
				cfg := WarmupConfig{Protocol: protocol, BaseURL: server.URL, APIKey: "configured-test-key", Model: "external-title-model", TimeoutSeconds: 3}
				result, err := forwardWarmup(context.Background(), cfg, body)
				require.NoError(t, err)
				require.Equal(t, "Workspace Setup", result.Text)
				require.Equal(t, 12, result.Usage.InputTokens)
				require.Equal(t, 3, result.Usage.OutputTokens)
				require.Equal(t, 5, result.Usage.CacheReadInputTokens)
			})
		}
	}
}

func TestForwardWarmupErrorsAreBoundedAndRedacted(t *testing.T) {
	for _, code := range []int{http.StatusUnauthorized, http.StatusTooManyRequests, http.StatusFound} {
		calls := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			w.Header().Set("Location", "/must-not-follow")
			w.WriteHeader(code)
			_, _ = w.Write([]byte("secret upstream body"))
		}))
		_, err := forwardWarmup(context.Background(), WarmupConfig{Protocol: "openai", BaseURL: server.URL, APIKey: "secret", Model: "title", TimeoutSeconds: 1}, []byte(`{"messages":[{"role":"user","content":"Warmup"}]}`))
		require.Error(t, err)
		require.NotContains(t, err.Error(), "secret")
		require.Equal(t, 1, calls)
		server.Close()
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.Copy(io.Discard, r.Body); <-r.Context().Done() }))
	defer server.Close()
	cfg := WarmupConfig{Protocol: "openai", BaseURL: server.URL, Model: "title", TimeoutSeconds: 1}
	_, err := forwardWarmup(context.Background(), cfg, []byte(`{"messages":[]}`))
	require.ErrorIs(t, err, context.DeadlineExceeded)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = forwardWarmup(ctx, cfg, []byte(`{"messages":[]}`))
	require.True(t, errors.Is(err, context.Canceled))
}

func TestWarmupUsesOutboundURLPolicy(t *testing.T) {
	s := &GatewayService{cfg: &config.Config{}}
	s.cfg.Security.URLAllowlist.Enabled = true
	s.cfg.Security.URLAllowlist.UpstreamHosts = []string{"allowed.example.test"}
	_, err := s.ForwardWarmup(context.Background(), WarmupConfig{BaseURL: "http://127.0.0.1", Protocol: "openai"}, []byte(`{}`))
	require.ErrorContains(t, err, "outbound URL policy")
}

func TestWarmupUsageBillsRealExternalModelWithoutAccountQuota(t *testing.T) {
	logs := &openAIRecordUsageBestEffortLogRepoStub{}
	users := &openAIRecordUsageUserRepoStub{}
	svc := newGatewayRecordUsageServiceForTest(logs, users, &openAIRecordUsageSubRepoStub{})
	// Use an API-key account with quota so the test can detect accidental account billing.
	account := &Account{ID: 42, Type: AccountTypeAPIKey, Platform: PlatformAnthropic, Credentials: map[string]any{"quota_total": 100}}
	result := &ForwardResult{ExternalWarmup: true, RequestID: "warmup-test", Model: "claude-haiku-4-5-20251001", UpstreamModel: "claude-haiku-4-5-20251001", Usage: ClaudeUsage{InputTokens: 12, OutputTokens: 3, CacheReadInputTokens: 5}, Duration: time.Second}
	require.NoError(t, svc.RecordUsage(context.Background(), &RecordUsageInput{Result: result, APIKey: &APIKey{ID: 2}, User: &User{ID: 1}, Account: account, InboundEndpoint: "/v1/messages/warmup", ChannelUsageFields: ChannelUsageFields{OriginalModel: "claude-opus-4-6"}}))
	require.NotNil(t, logs.lastLog)
	require.Equal(t, 12, logs.lastLog.InputTokens)
	require.Equal(t, 3, logs.lastLog.OutputTokens)
	require.Equal(t, "claude-opus-4-6", logs.lastLog.RequestedModel)
	require.Equal(t, result.Model, logs.lastLog.Model)
	require.Positive(t, logs.lastLog.TotalCost)
	require.False(t, (&postUsageBillingParams{SkipAccountUsage: true, Account: account, Cost: &CostBreakdown{TotalCost: 1}}).shouldUpdateAccountQuota())
}

func TestWarmupAccountSettingsPersistAndRejectInvalidEdits(t *testing.T) {
	repo := &updateAccountCredsRepoStub{account: &Account{ID: 22, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Status: StatusActive, Credentials: warmupCredentials()}}
	svc := &adminServiceImpl{accountRepo: repo}
	incoming := warmupCredentials()
	delete(incoming, "warmup_api_key")
	incoming["warmup_model"] = "new-title-model"
	updated, err := svc.UpdateAccount(context.Background(), 22, &UpdateAccountInput{Credentials: incoming})
	require.NoError(t, err)
	require.Equal(t, 1, repo.updateCalls)
	require.Equal(t, "test-only-secret", updated.Credentials["warmup_api_key"])
	require.Equal(t, "new-title-model", updated.Credentials["warmup_model"])
	incoming["warmup_model"] = ""
	_, err = svc.UpdateAccount(context.Background(), 22, &UpdateAccountInput{Credentials: incoming})
	require.Error(t, err)
	require.Equal(t, 1, repo.updateCalls)
}

func TestWarmupUsageVariantsAndMalformedResponses(t *testing.T) {
	for _, tc := range []struct {
		name, protocol, body string
		wantErr              bool
		cached, oneHour      int
	}{
		{name: "DeepSeek cache", protocol: "openai", body: `{"choices":[{"message":{"content":"Sample Title"}}],"usage":{"prompt_tokens":10,"completion_tokens":3,"prompt_cache_hit_tokens":6}}`, cached: 6},
		{name: "Anthropic cache TTL", protocol: "anthropic", body: `{"content":[{"type":"text","text":"Sample Title"}],"usage":{"input_tokens":10,"output_tokens":3,"cache_creation_input_tokens":5,"cache_creation":{"ephemeral_1h_input_tokens":5}}}`, oneHour: 5},
		{name: "missing tokens", protocol: "openai", body: `{"choices":[{"message":{"content":"Sample Title"}}],"usage":{}}`, wantErr: true},
		{name: "bad JSON", protocol: "openai", body: `<!doctype html>`, wantErr: true},
		{name: "negative usage", protocol: "openai", body: `{"choices":[{"message":{"content":"Sample Title"}}],"usage":{"prompt_tokens":1,"completion_tokens":-3}}`, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(tc.body)) }))
			defer server.Close()
			result, err := forwardWarmup(context.Background(), WarmupConfig{Protocol: tc.protocol, BaseURL: server.URL, Model: "title", TimeoutSeconds: 2}, []byte(`{"messages":[{"role":"user","content":"Warmup"}]}`))
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.cached, result.Usage.CacheReadInputTokens)
			require.Equal(t, tc.oneHour, result.Usage.CacheCreation1hTokens)
		})
	}
}

func TestResolveWarmupAccountHydratesSchedulerMetadata(t *testing.T) {
	groupID := int64(2)
	full := &Account{ID: 13, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Priority: 1, AccountGroups: []AccountGroup{{AccountID: 13, GroupID: groupID}}, Credentials: warmupCredentials()}
	metadata := *full
	metadata.Credentials = nil // Production scheduler lists omit all warmup credentials.
	cache := &snapshotHydrationCache{snapshot: []*Account{&metadata}, accounts: map[int64]*Account{13: full}}
	svc := &GatewayService{schedulerSnapshot: NewSchedulerSnapshotService(cache, nil, nil, nil, nil)}
	ctx := svc.withGroupContext(context.Background(), &Group{ID: groupID, Platform: PlatformAnthropic, ClaudeCodeOnly: true, Status: StatusActive, Hydrated: true})
	got, err := svc.ResolveWarmupAccount(ctx, &groupID, "claude-haiku-4-5-20251001", true)
	require.NoError(t, err)
	require.Same(t, full, got)
	require.Equal(t, "test-only-secret", WarmupConfigFromCredentials(got.Credentials).APIKey)
	for _, test := range []struct {
		name string
		edit func(*Account)
	}{
		{"disabled", func(a *Account) { a.Credentials["intercept_warmup_requests"] = false }},
		{"inactive", func(a *Account) { a.Status = StatusError }},
		{"unschedulable", func(a *Account) { a.Schedulable = false }},
		{"moved group", func(a *Account) { a.AccountGroups = []AccountGroup{{GroupID: 3}} }},
		{"different platform", func(a *Account) { a.Platform = PlatformOpenAI }},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := *full
			changed.Credentials = warmupCredentials()
			test.edit(&changed)
			cache.accounts[13] = &changed
			got, err := svc.ResolveWarmupAccount(ctx, &groupID, "claude-haiku-4-5-20251001", true)
			require.NoError(t, err)
			require.Nil(t, got)
		})
	}
}
