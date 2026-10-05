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
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// 这些用例走 buildUpstreamRequest / buildCountTokensRequest，再用 mock transport
// 读取最终出站的 Header 与 Body。完整 UA 沿用账号指纹，billing 的版本和入口
// 与最终 UA 对齐，当前客户端的 agent-sdk 等后缀不会替换账号缓存。
func TestOAuthFingerprintEntrypointFinalRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const (
		cacheVersion  = "2.1.284"
		clientVersion = "2.1.268"
		chat          = "ping"
		cachedOS      = "CachedOS"
		clientOS      = "ClientOS"
	)
	// 缓存版本和请求版本都必须不低于运行期下限，否则指纹下限抬升会改写被测版本。
	require.GreaterOrEqual(t, CompareVersions(cacheVersion, claude.EffectiveCLIVersion()), 0)
	require.GreaterOrEqual(t, CompareVersions(clientVersion, claude.EffectiveCLIVersion()), 0)

	cacheUA := func(entrypoint string) string {
		return "claude-cli/" + cacheVersion + " (external, " + entrypoint + ")"
	}
	clientUA := func(entrypoint string) string {
		return "claude-cli/" + clientVersion + " (external, " + entrypoint + ")"
	}

	type endpointKind string
	const (
		endpointMessages    endpointKind = "messages"
		endpointCountTokens endpointKind = "count_tokens"
	)

	tests := []struct {
		name            string
		endpoints       []endpointKind
		mimic           bool
		disableFP       bool
		cacheEntrypoint string
		clientUA        string // empty: 请求不带 User-Agent
		billingVersion  string
		billingEP       string // empty 且 unparseable/invalidJSON/multi 都为 false：没有 billing 块
		unparseable     bool
		invalidJSON     bool
		multiEntrypoint bool
		complexClientUA string
		wantErrKind     string
		wantUA          string
		wantBodyEP      string // 空表示 body 里不应出现 cc_entrypoint
		wantBodyVersion string // 空表示不检查 cc_version；"mimic" 表示等于 mimic UA 的版本
		wantStainlessOS string
		wantExactBody   string
	}{
		{
			name:            "agent_cache_cli_request",
			endpoints:       []endpointKind{endpointMessages, endpointCountTokens},
			cacheEntrypoint: "local-agent",
			clientUA:        clientUA("cli"),
			billingVersion:  clientVersion,
			billingEP:       "cli",
			wantUA:          cacheUA("local-agent"),
			wantBodyEP:      "local-agent",
			wantBodyVersion: cacheVersion,
			wantStainlessOS: cachedOS,
		},
		{
			name:            "cli_cache_agent_request",
			endpoints:       []endpointKind{endpointMessages, endpointCountTokens},
			cacheEntrypoint: "cli",
			clientUA:        clientUA("local-agent"),
			billingVersion:  clientVersion,
			billingEP:       "local-agent",
			wantUA:          cacheUA("cli"),
			wantBodyEP:      "cli",
			wantBodyVersion: cacheVersion,
			wantStainlessOS: cachedOS,
		},
		{
			name:            "same_entrypoint",
			endpoints:       []endpointKind{endpointMessages, endpointCountTokens},
			cacheEntrypoint: "cli",
			clientUA:        clientUA("cli"),
			billingVersion:  clientVersion,
			billingEP:       "cli",
			wantUA:          cacheUA("cli"),
			wantBodyEP:      "cli",
			wantBodyVersion: cacheVersion,
			wantStainlessOS: cachedOS,
		},
		{
			name:            "version_and_entrypoint_follow_cached_ua",
			endpoints:       []endpointKind{endpointMessages},
			cacheEntrypoint: "local-agent",
			clientUA:        clientUA("cli"),
			billingVersion:  clientVersion,
			billingEP:       "cli",
			wantUA:          cacheUA("local-agent"),
			wantBodyEP:      "local-agent",
			wantBodyVersion: cacheVersion,
			wantStainlessOS: cachedOS,
		},
		{
			name:            "body_entrypoint_without_client_ua",
			endpoints:       []endpointKind{endpointMessages},
			cacheEntrypoint: "local-agent",
			billingVersion:  clientVersion,
			billingEP:       "cli",
			wantUA:          cacheUA("local-agent"),
			wantBodyEP:      "local-agent",
			wantBodyVersion: cacheVersion,
			wantStainlessOS: cachedOS,
		},
		{
			name:            "incoming_agent_sdk_suffix_does_not_replace_cached_ua",
			endpoints:       []endpointKind{endpointMessages, endpointCountTokens},
			cacheEntrypoint: "cli",
			complexClientUA: "claude-cli/2.1.284 (external, local-agent, agent-sdk/0.3.284)",
			billingVersion:  cacheVersion,
			billingEP:       "local-agent",
			wantUA:          cacheUA("cli"),
			wantBodyEP:      "cli",
			wantBodyVersion: cacheVersion,
			wantStainlessOS: cachedOS,
		},
		{
			name:            "existing_cached_suffix_is_preserved",
			endpoints:       []endpointKind{endpointMessages, endpointCountTokens},
			cacheEntrypoint: "local-agent, agent-sdk/0.3.284",
			clientUA:        clientUA("cli"),
			billingVersion:  clientVersion,
			billingEP:       "cli",
			wantUA:          cacheUA("local-agent, agent-sdk/0.3.284"),
			wantBodyEP:      "local-agent",
			wantBodyVersion: cacheVersion,
			wantStainlessOS: cachedOS,
		},
		{
			name:            "mimic_uses_template_not_cached_entrypoint",
			endpoints:       []endpointKind{endpointMessages, endpointCountTokens},
			mimic:           true,
			cacheEntrypoint: "local-agent",
			clientUA:        clientUA("cli"),
			billingVersion:  clientVersion,
			billingEP:       "cli",
			wantUA:          claude.DefaultHeaders()["User-Agent"],
			wantBodyEP:      "cli",
			wantBodyVersion: "mimic",
			wantStainlessOS: "Linux",
		},
		{
			name:            "mimic_aligns_body_entrypoint_with_template",
			endpoints:       []endpointKind{endpointMessages, endpointCountTokens},
			mimic:           true,
			cacheEntrypoint: "cli",
			clientUA:        clientUA("local-agent"),
			billingVersion:  clientVersion,
			billingEP:       "local-agent",
			wantUA:          claude.DefaultHeaders()["User-Agent"],
			wantBodyEP:      "cli",
			wantBodyVersion: "mimic",
			wantStainlessOS: "Linux",
		},
		{
			name:            "fingerprint_unification_disabled",
			endpoints:       []endpointKind{endpointMessages, endpointCountTokens},
			disableFP:       true,
			cacheEntrypoint: "local-agent",
			clientUA:        clientUA("cli"),
			billingVersion:  clientVersion,
			billingEP:       "cli",
			wantUA:          clientUA("cli"),
			wantBodyEP:      "cli",
			wantBodyVersion: clientVersion,
			wantStainlessOS: clientOS,
		},
		{
			name:            "fingerprint_disabled_preserves_incoming_sdk_identity",
			endpoints:       []endpointKind{endpointMessages, endpointCountTokens},
			disableFP:       true,
			cacheEntrypoint: "cli",
			complexClientUA: "claude-cli/2.1.284 (external, local-agent, agent-sdk/0.3.284)",
			billingVersion:  cacheVersion,
			billingEP:       "local-agent",
			wantUA:          "claude-cli/2.1.284 (external, local-agent, agent-sdk/0.3.284)",
			wantBodyEP:      "local-agent",
			wantBodyVersion: cacheVersion,
			wantStainlessOS: clientOS,
		},
		{
			name:            "missing_billing_keeps_cached_ua_without_injecting_billing",
			endpoints:       []endpointKind{endpointMessages, endpointCountTokens},
			cacheEntrypoint: "local-agent",
			clientUA:        clientUA("cli"),
			wantUA:          cacheUA("local-agent"),
			wantStainlessOS: cachedOS,
		},
		{
			name:            "invalid_json_is_not_rewritten",
			endpoints:       []endpointKind{endpointMessages, endpointCountTokens},
			cacheEntrypoint: "local-agent",
			clientUA:        clientUA("cli"),
			invalidJSON:     true,
			wantUA:          cacheUA("local-agent"),
			wantExactBody:   "{",
			wantStainlessOS: cachedOS,
		},
		{
			name:            "unparseable_billing_is_rejected",
			endpoints:       []endpointKind{endpointMessages, endpointCountTokens},
			cacheEntrypoint: "local-agent",
			clientUA:        clientUA("cli"),
			unparseable:     true,
			wantErrKind:     fingerprintEntrypointKindUnparseable,
		},
		{
			name:            "incoming_ua_disagreement_is_resolved_by_cached_identity",
			endpoints:       []endpointKind{endpointMessages, endpointCountTokens},
			cacheEntrypoint: "cli",
			clientUA:        clientUA("local-agent"),
			billingVersion:  clientVersion,
			billingEP:       "cli",
			wantUA:          cacheUA("cli"),
			wantBodyEP:      "cli",
			wantBodyVersion: cacheVersion,
			wantStainlessOS: cachedOS,
		},
		{
			name:            "multiple_billing_entrypoints_rejected",
			endpoints:       []endpointKind{endpointMessages, endpointCountTokens},
			cacheEntrypoint: "cli",
			clientUA:        clientUA("cli"),
			multiEntrypoint: true,
			wantErrKind:     fingerprintEntrypointKindConflict,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, endpoint := range tt.endpoints {
				t.Run(string(endpoint), func(t *testing.T) {
					resetGatewayForwardingSettingsCacheForTest(t)
					cache := &stubIdentityCache{fingerprint: &Fingerprint{
						UserAgent:   cacheUA(tt.cacheEntrypoint),
						ClientID:    "fingerprint-client",
						StainlessOS: cachedOS,
						UpdatedAt:   time.Now().Unix(),
					}}
					cfg := &config.Config{}
					svc := &GatewayService{
						cfg:             cfg,
						identityService: NewIdentityService(cache),
					}
					if tt.disableFP {
						svc.settingService = NewSettingService(&gatewayTTLSettingRepo{data: map[string]string{
							SettingKeyEnableFingerprintUnification: "false",
						}}, cfg)
					}

					body := entrypointFixtureBody(t, tt.billingVersion, tt.billingEP, chat, tt.unparseable, tt.invalidJSON, tt.multiEntrypoint)
					c, _ := gin.CreateTestContext(httptest.NewRecorder())
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
					if tt.complexClientUA != "" {
						c.Request.Header.Set("User-Agent", tt.complexClientUA)
					} else if tt.clientUA != "" {
						c.Request.Header.Set("User-Agent", tt.clientUA)
					}
					c.Request.Header.Set("X-Stainless-OS", clientOS)
					c.Request.Header.Set("Cookie", "session=SESSION_MUST_NOT_LEAK")

					const token = "oauth-token-MUST_NOT_LEAK"
					account := &Account{ID: 42, Platform: PlatformAnthropic, Type: AccountTypeOAuth}
					transport := &captureRoundTripper{}
					var (
						req      *http.Request
						wireBody []byte
						err      error
					)
					beforeConflicts := fingerprintEntrypointConflictCount.Load()
					if endpoint == endpointMessages {
						req, wireBody, err = svc.buildUpstreamRequest(context.Background(), c, account, body, token, "oauth", "claude-sonnet-4-5", false, tt.mimic)
					} else {
						req, wireBody, err = svc.buildCountTokensRequest(context.Background(), c, account, body, token, "oauth", "claude-sonnet-4-5", tt.mimic)
					}

					if tt.wantErrKind != "" {
						require.Error(t, err)
						var conflict *FingerprintEntrypointConflictError
						require.ErrorAs(t, err, &conflict)
						require.Equal(t, tt.wantErrKind, conflict.Kind)
						require.Equal(t, beforeConflicts+1, fingerprintEntrypointConflictCount.Load())
						require.Nil(t, req)
						assertNoSensitiveDiagnostic(t, err.Error())
						require.Zero(t, transport.called)
						require.Equal(t, cacheUA(tt.cacheEntrypoint), cache.fingerprint.UserAgent)
						require.Zero(t, cache.setCalls)
						return
					}

					require.NoError(t, err)
					require.Equal(t, beforeConflicts, fingerprintEntrypointConflictCount.Load())
					require.NotNil(t, req)
					require.Equal(t, int64(len(wireBody)), req.ContentLength)
					require.NotNil(t, req.GetBody)
					retryBody, retryErr := req.GetBody()
					require.NoError(t, retryErr)
					retryBytes, retryErr := io.ReadAll(retryBody)
					require.NoError(t, retryErr)
					require.NoError(t, retryBody.Close())
					require.Equal(t, wireBody, retryBytes)
					resp, doErr := (&http.Client{Transport: transport}).Do(req)
					require.NoError(t, doErr)
					require.NoError(t, resp.Body.Close())
					require.Equal(t, 1, transport.called)
					require.Equal(t, wireBody, transport.body)
					require.Equal(t, gjson.GetBytes(body, "messages").Raw, gjson.GetBytes(transport.body, "messages").Raw)
					require.Equal(t, tt.wantUA, transport.ua)
					require.Equal(t, tt.wantStainlessOS, transport.stainlessOS)
					require.Equal(t, cacheUA(tt.cacheEntrypoint), cache.fingerprint.UserAgent, "账号缓存里的完整 UA 不应被本次请求的入口改写")
					require.Zero(t, cache.setCalls)

					if tt.wantExactBody != "" {
						require.Equal(t, tt.wantExactBody, string(transport.body))
					}
					if tt.wantBodyEP == "" {
						require.NotContains(t, string(transport.body), "cc_entrypoint=")
					} else {
						require.Contains(t, string(transport.body), "cc_entrypoint="+tt.wantBodyEP+";")
						if tt.cacheEntrypoint != tt.wantBodyEP {
							require.NotContains(t, string(transport.body), "cc_entrypoint="+tt.cacheEntrypoint+";")
						}
					}

					headerEP, headerOK := claudeCLIExternalEntrypoint(transport.ua)
					require.True(t, headerOK)
					if tt.wantBodyEP != "" {
						require.Equal(t, tt.wantBodyEP, headerEP, "同一次出站请求的 UA 入口与 cc_entrypoint 必须一致")
					}

					wantVersion := tt.wantBodyVersion
					if wantVersion == "mimic" {
						wantVersion = ExtractCLIVersion(tt.wantUA)
					}
					if wantVersion != "" {
						billingText := gjson.GetBytes(transport.body, "system.0.text").String()
						require.Contains(t, billingText, "cc_version="+wantVersion+".")
						require.Contains(t, billingText, "cc_version="+wantVersion+"."+computeClaudeCodeFingerprint(transport.body, wantVersion)+";")
						require.Equal(t, wantVersion, ExtractCLIVersion(transport.ua))
						if tt.billingVersion != "" && tt.billingVersion != wantVersion {
							require.NotContains(t, billingText, "cc_version="+tt.billingVersion)
						}
					}
					if strings.Contains(string(transport.body), "ping") || tt.wantExactBody != "" {
						// 正文和凭证不进入 UA 或错误文本。
						require.NotContains(t, transport.ua, "SESSION_MUST_NOT_LEAK")
					}
				})
			}
		})
	}
}

func entrypointFixtureBody(t *testing.T, version, entrypoint, chat string, unparseable, invalidJSON, multi bool) []byte {
	t.Helper()
	if invalidJSON {
		return []byte("{")
	}
	body := []byte(`{"model":"claude-sonnet-4-5","max_tokens":32,"messages":[{"role":"user","content":"ping"}]}`)
	var err error
	body, err = sjson.SetBytes(body, "messages.0.content", chat)
	require.NoError(t, err)
	if unparseable {
		body, err = sjson.SetBytes(body, "messages.0.content", "CHAT_BODY_MUST_NOT_APPEAR_IN_DIAGNOSTICS")
		require.NoError(t, err)
		body, err = sjson.SetBytes(body, "metadata.user_id", `{"device_id":"device-MUST_NOT_LEAK","session_id":"session-MUST_NOT_LEAK"}`)
		require.NoError(t, err)
		body, err = sjson.SetRawBytes(body, "system", []byte(`[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.268.abc;"}]`))
		require.NoError(t, err)
		return body
	}
	if multi {
		body, err = sjson.SetRawBytes(body, "system", []byte(`[
			{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.268.abc; cc_entrypoint=cli;"},
			{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.268.abc; cc_entrypoint=local-agent;"}
		]`))
		require.NoError(t, err)
		return body
	}
	if version == "" && entrypoint == "" {
		return body
	}
	fp := computeClaudeCodeFingerprint(body, version)
	text := "x-anthropic-billing-header: cc_version=" + version + "." + fp + "; cc_entrypoint=" + entrypoint + ";"
	body, err = sjson.SetBytes(body, "system", []any{map[string]any{"type": "text", "text": text}})
	require.NoError(t, err)
	return body
}

func assertNoSensitiveDiagnostic(t *testing.T, text string) {
	t.Helper()
	for _, secret := range []string{
		"CHAT_BODY_MUST_NOT_APPEAR_IN_DIAGNOSTICS",
		"oauth-token-MUST_NOT_LEAK",
		"device-MUST_NOT_LEAK",
		"session-MUST_NOT_LEAK",
		"SESSION_MUST_NOT_LEAK",
		"x-anthropic-billing-header",
	} {
		require.NotContains(t, text, secret)
	}
}

type captureRoundTripper struct {
	called      int
	ua          string
	stainlessOS string
	body        []byte
}

func (c *captureRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	c.called++
	c.ua = getHeaderRaw(r.Header, "User-Agent")
	c.stainlessOS = getHeaderRaw(r.Header, "X-Stainless-OS")
	if r.Body != nil {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		c.body = body
	}
	return &http.Response{
		StatusCode: http.StatusNoContent,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("")),
		Request:    r,
	}, nil
}
