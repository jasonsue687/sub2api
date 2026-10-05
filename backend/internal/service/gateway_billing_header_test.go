package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestSyncBillingHeaderIdentity(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		userAgent string
		wantSub   string // substring expected in result
		unchanged bool   // expect body to remain the same
	}{
		{
			name:      "replaces cc_version and recomputes message-derived suffix",
			body:      `{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.81.df2; cc_entrypoint=cli; cch=00000;"},{"type":"text","text":"You are Claude Code.","cache_control":{"type":"ephemeral"}}],"messages":[]}`,
			userAgent: "claude-cli/2.1.22 (external, cli)",
			wantSub:   "cc_version=2.1.22." + computeClaudeCodeFingerprint([]byte(`{"messages":[]}`), "2.1.22"),
		},
		{
			name:      "no billing header in system",
			body:      `{"system":[{"type":"text","text":"You are Claude Code."}],"messages":[]}`,
			userAgent: "claude-cli/2.1.22 (external, cli)",
			unchanged: true,
		},
		{
			name:      "no system field",
			body:      `{"messages":[]}`,
			userAgent: "claude-cli/2.1.22 (external, cli)",
			unchanged: true,
		},
		{
			name:      "user-agent without version",
			body:      `{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.81; cc_entrypoint=cli; cch=00000;"}],"messages":[]}`,
			userAgent: "Mozilla/5.0",
			unchanged: true,
		},
		{
			name:      "empty user-agent",
			body:      `{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.81; cc_entrypoint=cli; cch=00000;"}],"messages":[]}`,
			userAgent: "",
			unchanged: true,
		},
		{
			name:      "version already matches",
			body:      `{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.22; cc_entrypoint=cli; cch=00000;"}],"messages":[]}`,
			userAgent: "claude-cli/2.1.22 (external, cli)",
			unchanged: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := syncBillingHeaderIdentity([]byte(tt.body), tt.userAgent, 42)
			require.NoError(t, err)
			if tt.unchanged {
				assert.Equal(t, tt.body, string(result), "body should remain unchanged")
			} else {
				assert.Contains(t, string(result), tt.wantSub)
				// Ensure old semver is gone
				assert.NotContains(t, string(result), "cc_version=2.1.81")
			}
		})
	}
}

func TestSyncBillingHeaderIdentity_RecomputesSuffixAndIsIdempotent(t *testing.T) {
	body := []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.81.df2; cc_entrypoint=cli;"}],"messages":[{"role":"user","content":"hello world"}]}`)
	version := "2.1.22"
	ua := "claude-cli/" + version + " (external, local-agent)"
	result, err := syncBillingHeaderIdentity(body, ua, 42)
	require.NoError(t, err)
	require.Contains(t, gjson.GetBytes(result, "system.0.text").String(),
		"cc_version="+version+"."+computeClaudeCodeFingerprint(body, version)+";")
	require.Contains(t, gjson.GetBytes(result, "system.0.text").String(), "cc_entrypoint=local-agent;")
	again, err := syncBillingHeaderIdentity(result, ua, 42)
	require.NoError(t, err)
	require.Equal(t, string(result), string(again))
	require.JSONEq(t, gjson.GetBytes(body, "messages").Raw, gjson.GetBytes(result, "messages").Raw)
}

func TestSyncBillingHeaderIdentity_SystemShapesAndFieldBoundaries(t *testing.T) {
	const (
		billing = "x-anthropic-billing-header: cc_version=2.1.81.abc; cc_entrypoint = local-agent ; cc_entrypoint=local-agent; cch=keep; other_cc_version=2.1.81.abc; other_cc_entrypoint=local-agent;"
		plain   = "Keep cc_entrypoint=local-agent and cc_version=2.1.81.abc in this instruction."
		ua      = "claude-cli/2.1.284 (external, cli)"
	)
	for _, tc := range []struct {
		name   string
		system any
		paths  []string
	}{
		{name: "system_string", system: billing},
		{name: "array_strings", system: []string{plain, billing}},
		{name: "multiple_text_blocks", system: []any{
			map[string]any{"type": "text", "text": plain, "cache_control": map[string]string{"type": "ephemeral"}},
			map[string]any{"type": "text", "text": billing},
			map[string]any{"type": "text", "text": billing},
		}, paths: []string{"system.1.text", "system.2.text"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, err := sjson.SetBytes([]byte(`{"messages":[{"role":"user","content":"hello world"}],"metadata":{"user_id":"keep"}}`), "system", tc.system)
			require.NoError(t, err)
			original := string(body)
			result, err := syncBillingHeaderIdentity(body, ua, 42)
			require.NoError(t, err)
			require.Equal(t, original, string(body), "do not mutate the caller's input buffer")
			if len(tc.paths) == 0 {
				require.Equal(t, original, string(result), "only object-array billing blocks are synchronized")
				return
			}
			for _, path := range tc.paths {
				text := gjson.GetBytes(result, path).String()
				require.Contains(t, text, "cc_version=2.1.284."+computeClaudeCodeFingerprint(body, "2.1.284")+";")
				require.Contains(t, text, "cc_entrypoint = cli ; cc_entrypoint=cli;")
				require.Contains(t, text, "cch=keep;")
				require.Contains(t, text, "other_cc_version=2.1.81.abc; other_cc_entrypoint=local-agent;")
			}
			for _, path := range []string{"messages", "metadata"} {
				require.Equal(t, gjson.GetBytes(body, path).Raw, gjson.GetBytes(result, path).Raw)
			}
			if len(tc.paths) > 1 {
				require.Equal(t, gjson.GetBytes(body, "system.0").Raw, gjson.GetBytes(result, "system.0").Raw)
			}
		})
	}
}

func TestBuildOAuthRequest_PreservesNonBlockSystem(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const (
		billing  = "x-anthropic-billing-header: cc_version=2.1.268.abc; cc_entrypoint=local-agent;"
		cachedUA = "claude-cli/2.1.284 (external, cli)"
	)
	for _, endpoint := range []string{"messages", "count_tokens"} {
		for _, tc := range []struct {
			name   string
			system any
		}{
			{name: "string_with_same_named_prompt_fields", system: billing + "\nKeep these literal values: ; cc_entrypoint=local-agent; cc_version=2.1.268.abc;"},
			{name: "string_with_conflicting_prompt_fields", system: billing + "\nKeep these literal values: ; cc_entrypoint=cli; cc_version=1.0.0;"},
			{name: "string_with_malformed_billing", system: "x-anthropic-billing-header: cc_version=2.1.268; cc_entrypoint=???;\nThis is ordinary system text."},
			{name: "string_array", system: []string{billing, "x-anthropic-billing-header: cc_version=1.0.0; cc_entrypoint=cli;"}},
		} {
			t.Run(endpoint+"/"+tc.name, func(t *testing.T) {
				resetGatewayForwardingSettingsCacheForTest(t)
				cache := &stubIdentityCache{fingerprint: &Fingerprint{
					UserAgent: cachedUA, ClientID: "test-client", UpdatedAt: time.Now().Unix(),
				}}
				svc := &GatewayService{cfg: &config.Config{}, identityService: NewIdentityService(cache)}
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/"+endpoint, nil)
				c.Request.Header.Set("User-Agent", "claude-cli/2.1.268 (external, local-agent, agent-sdk/0.3.268)")
				body, err := sjson.SetBytes([]byte(`{"model":"claude-haiku-4-5","messages":[{"role":"user","content":"hello world"}]}`), "system", tc.system)
				require.NoError(t, err)
				account := &Account{ID: 42, Platform: PlatformAnthropic, Type: AccountTypeOAuth}
				var req *http.Request
				var wireBody []byte
				if endpoint == "messages" {
					req, wireBody, err = svc.buildUpstreamRequest(context.Background(), c, account,
						body, "test-token", "oauth", "claude-haiku-4-5", false, false)
				} else {
					req, wireBody, err = svc.buildCountTokensRequest(context.Background(), c, account,
						body, "test-token", "oauth", "claude-haiku-4-5", false)
				}
				require.NoError(t, err, "ordinary system text must not trigger billing conflicts")
				require.Equal(t, int64(len(wireBody)), req.ContentLength)
				require.NotNil(t, req.GetBody)
				retryBody, err := req.GetBody()
				require.NoError(t, err)
				retryBytes, err := io.ReadAll(retryBody)
				require.NoError(t, err)
				require.NoError(t, retryBody.Close())
				require.Equal(t, wireBody, retryBytes)
				transport := &captureRoundTripper{}
				resp, err := (&http.Client{Transport: transport}).Do(req)
				require.NoError(t, err)
				require.NoError(t, resp.Body.Close())
				require.Equal(t, wireBody, transport.body)
				require.Equal(t, gjson.GetBytes(body, "system").Raw, gjson.GetBytes(transport.body, "system").Raw)
				require.Equal(t, cachedUA, transport.ua)
				require.Equal(t, cachedUA, cache.fingerprint.UserAgent)
				require.Zero(t, cache.setCalls)
			})
		}
	}
}

func TestSyncBillingHeaderIdentity_DoesNotRewriteLookalikeText(t *testing.T) {
	body := []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header-example: cc_version=2.1.81.abc; cc_entrypoint=local-agent;"}],"messages":[]}`)
	result, err := syncBillingHeaderIdentity(body, "claude-cli/2.1.284 (external, cli)", 42)
	require.NoError(t, err)
	require.Equal(t, string(body), string(result))
}

func TestSyncBillingHeaderIdentity_RejectsUnalignableUA(t *testing.T) {
	body := []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.81.abc; cc_entrypoint=cli;"}],"messages":[]}`)
	for _, ua := range []string{
		"claude-cli/2.1.284",
		"claude-cli/2.1.284 (external, cli.invalid)",
		"claude-cli/2.1.284 (external, cli",
	} {
		t.Run(ua, func(t *testing.T) {
			result, err := syncBillingHeaderIdentity(body, ua, 42)
			var conflict *FingerprintEntrypointConflictError
			require.ErrorAs(t, err, &conflict)
			require.Equal(t, fingerprintEntrypointKindUnalignable, conflict.Kind)
			require.Nil(t, result)
			assertNoSensitiveDiagnostic(t, err.Error())
		})
	}
}

func TestBuildOAuthRequest_BillingMatchesWireUserAgent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, endpoint := range []string{"messages", "count_tokens"} {
		for _, tc := range []struct {
			name      string
			mimic     bool
			identity  bool
			disableFP bool
		}{
			{name: "mimic_overrides_cached_version", mimic: true, identity: true},
			{name: "mimic_without_identity", mimic: true},
			{name: "mimic_with_fingerprint_disabled", mimic: true, identity: true, disableFP: true},
			{name: "passthrough_uses_cached_version", identity: true},
		} {
			t.Run(endpoint+"/"+tc.name, func(t *testing.T) {
				resetGatewayForwardingSettingsCacheForTest(t)
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
				body := []byte(`{"model":"claude-haiku-4-5","system":[{"type":"text","text":""}],"messages":[{"role":"user","content":"hello world"}]}`)
				billing, err := buildBillingAttributionText(body, "2.1.81")
				require.NoError(t, err)
				billing = strings.ReplaceAll(billing, "cc_entrypoint=cli;", "cc_entrypoint=local-agent;")
				body, err = sjson.SetBytes(body, "system.0.text", billing)
				require.NoError(t, err)

				cfg := &config.Config{}
				svc := &GatewayService{cfg: cfg}
				cachedUA := "claude-cli/2.9.0 (external, cli)"
				if tc.identity {
					svc.identityService = NewIdentityService(&stubIdentityCache{fingerprint: &Fingerprint{
						UserAgent: cachedUA, ClientID: "test-client", UpdatedAt: time.Now().Unix(),
					}})
				}
				if tc.disableFP {
					svc.settingService = NewSettingService(&gatewayTTLSettingRepo{data: map[string]string{
						SettingKeyEnableFingerprintUnification: "false",
					}}, cfg)
				}
				account := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeOAuth}
				var req *http.Request
				var wireBody []byte
				if endpoint == "messages" {
					req, wireBody, err = svc.buildUpstreamRequest(context.Background(), c, account,
						body, "test-token", "oauth", "claude-haiku-4-5", false, tc.mimic)
				} else {
					req, wireBody, err = svc.buildCountTokensRequest(context.Background(), c, account,
						body, "test-token", "oauth", "claude-haiku-4-5", tc.mimic)
				}
				require.NoError(t, err)
				defer func() { require.NoError(t, req.Body.Close()) }()
				wantUA := cachedUA
				if tc.mimic {
					wantUA = claude.DefaultHeaders()["User-Agent"]
				}
				require.Equal(t, wantUA, getHeaderRaw(req.Header, "User-Agent"))
				version := ExtractCLIVersion(wantUA)
				require.Contains(t, gjson.GetBytes(wireBody, "system.0.text").String(),
					"cc_version="+version+"."+computeClaudeCodeFingerprint(wireBody, version)+";")
				require.Contains(t, gjson.GetBytes(wireBody, "system.0.text").String(), "cc_entrypoint=cli;")
				actualBody, err := io.ReadAll(req.Body)
				require.NoError(t, err)
				require.Equal(t, wireBody, actualBody)
			})
		}
	}
}
