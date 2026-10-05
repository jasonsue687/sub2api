package anthropicaudit

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestInboundPreservesOriginalParametersAndOmitsContent(t *testing.T) {
	raw := ` { "model":"client-alias", "max_tokens":4096, "temperature":1.00, "stream":false, "system":[{"type":"text","text":"original system","cache_control":{"type":"ephemeral"}}], "metadata":{"user_id":"user_device_account_account_session_session"}, "custom":{"number":9007199254740993,"text":"\u4e2d"}, "messages":[{"role":"user","content":"PRIVATE_MESSAGE"}], "tools":[{"name":"PRIVATE_TOOL"}], "tool_choice":{"type":"tool","name":"PRIVATE_TOOL"} } `
	expected := strings.ReplaceAll(raw, `[{"role":"user","content":"PRIVATE_MESSAGE"}]`, `"[OMITTED]"`)
	expected = strings.ReplaceAll(expected, `[{"name":"PRIVATE_TOOL"}]`, `"[OMITTED]"`)
	expected = strings.ReplaceAll(expected, `{"type":"tool","name":"PRIVATE_TOOL"}`, `"[OMITTED]"`)
	body, state, truncated, original := CaptureInboundBody([]byte(raw))
	require.Equal(t, "parsed", state)
	assert.False(t, truncated)
	assert.Equal(t, len(raw), original)
	assert.Equal(t, expected, string(body))
	assert.NotContains(t, string(body), "PRIVATE_")
	assert.NotContains(t, string(body), "_redacted")
	assert.NotContains(t, string(body), "sha256")
}

func TestInboundOmissionsHandleDuplicateKeysAndAllValueTypes(t *testing.T) {
	for _, value := range []string{`null`, `[]`, `{}`, `"private"`, `false`, `123`} {
		t.Run(value, func(t *testing.T) {
			raw := `{"messages":` + value + `,"tools":` + value + `,"tool_choice":` + value + `,"messages":["second private value"]}`
			body, state, _, _ := CaptureInboundBody([]byte(raw))
			require.Equal(t, "parsed", state)
			assert.Equal(t, `{"messages":"[OMITTED]","tools":"[OMITTED]","tool_choice":"[OMITTED]","messages":"[OMITTED]"}`, string(body))
		})
	}
	body, _, _, _ := CaptureInboundBody([]byte(`{"model":"m","other":{"messages":"keep nested unknown parameter"}}`))
	assert.JSONEq(t, `{"model":"m","other":{"messages":"keep nested unknown parameter"}}`, string(body))
}

func TestInboundMasksCredentialsWithoutChangingIdentity(t *testing.T) {
	raw := `{"max_tokens":0,"thinking":{"budget_tokens":0},"metadata":{"user_id":"raw-session"},"mcp_servers":[{"authorization_token":"private"}],"custom":{"api_key.value":"private","password":"private"}}`
	body, state, _, _ := CaptureInboundBody([]byte(raw))
	require.Equal(t, "parsed", state)
	assert.JSONEq(t, `{"max_tokens":0,"thinking":{"budget_tokens":0},"metadata":{"user_id":"raw-session"},"mcp_servers":[{"authorization_token":"***"}],"custom":{"api_key.value":"***","password":"***"}}`, string(body))
	assert.NotContains(t, string(body), "private")
}

func TestInboundHeadersPreserveSessionLongAndRepeatedValues(t *testing.T) {
	longValue := strings.Repeat("a", 5000)
	headers := http.Header{
		"User-Agent": {"curl/8.0"}, "Authorization": {"Bearer private"}, "Cookie": {"session=private"},
		"X-Claude-Code-Session-Id": {"original-session"}, "X-Custom": {"one", "two"}, "X-Long": {longValue},
	}
	raw, truncated := CaptureInboundHeaders(headers)
	assert.False(t, truncated)
	var pairs []HeaderPair
	require.NoError(t, json.Unmarshal(raw, &pairs))
	assert.Contains(t, pairs, HeaderPair{Name: "User-Agent", Value: "curl/8.0"})
	assert.Contains(t, pairs, HeaderPair{Name: "Authorization", Value: "Bearer ***"})
	assert.Contains(t, pairs, HeaderPair{Name: "Cookie", Value: "***"})
	assert.Contains(t, pairs, HeaderPair{Name: "X-Claude-Code-Session-Id", Value: "original-session"})
	assert.Contains(t, pairs, HeaderPair{Name: "X-Custom", Value: "one"})
	assert.Contains(t, pairs, HeaderPair{Name: "X-Custom", Value: "two"})
	assert.Contains(t, pairs, HeaderPair{Name: "X-Long", Value: longValue})
	assert.NotContains(t, string(raw), "private")
	assert.Equal(t, "session=private", headers.Get("Cookie"))
}

func TestInboundRetainsCaptureLimitsAndDoesNotSummarizeOmittedFields(t *testing.T) {
	body, state, _, _ := CaptureInboundBody([]byte(`{"messages":`))
	assert.Nil(t, body)
	assert.Equal(t, "invalid_json", state)
	body, state, truncated, _ := CaptureInboundBody([]byte(`{"system":"` + strings.Repeat("a", MaxStoredBytes) + `"}`))
	assert.Equal(t, "parsed", state)
	assert.True(t, truncated)
	assert.True(t, gjson.GetBytes(body, "_truncated").Bool())
	rec := finishJob(captureJob{record: Record{Direction: DirectionInbound}, body: []byte(`{"model":"m","messages":[{}],"tools":[{}],"tool_choice":{"type":"tool","name":"private"}}`)})
	for _, key := range []string{"messages_count", "tools_count", "tool_choice"} {
		assert.NotContains(t, string(rec.Summary), key)
	}
	assert.Equal(t, "m", rec.Model)
}

func TestOutboundRedactsDocumentCitationsAndServerToolInput(t *testing.T) {
	raw := []byte(`{"messages":[{"role":"user","content":[{"type":"document","title":"PRIVATE_TITLE","context":"PRIVATE_CONTEXT","source":{"type":"content","content":[{"type":"text","text":"PRIVATE_DOCUMENT"}]}},{"type":"text","text":"answer","citations":[{"cited_text":"PRIVATE_CITATION"}]},{"type":"server_tool_use","name":"PRIVATE_TOOL","input":{"query":"PRIVATE_QUERY"}}]}]}`)
	body, state, _, _ := RedactBody(raw)
	require.Equal(t, "parsed", state)
	assert.NotContains(t, string(body), "PRIVATE_")
	assert.Equal(t, "document", gjson.GetBytes(body, "messages.0.content.0.type").String())
	assert.Equal(t, "server_tool_use", gjson.GetBytes(body, "messages.0.content.2.type").String())
}
