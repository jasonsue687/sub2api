package anthropicaudit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestRedactPromptsKeepsOrderAndBilling(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4-5","max_tokens":32000,"stream":true,"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.0.14; cc_entrypoint=cli","cache_control":{"type":"ephemeral"}},{"type":"text","text":"system prompt secret"}],"messages":[{"role":"user","content":"hello prompt"}]}`)
	out, state, truncated, _ := RedactBody(body)
	if state != "parsed" || truncated {
		t.Fatalf("state %s truncated %v", state, truncated)
	}
	if gjson.GetBytes(out, "model").String() != "claude-sonnet-4-5" || gjson.GetBytes(out, "max_tokens").Int() != 32000 || !gjson.GetBytes(out, "stream").Bool() {
		t.Fatalf("scalars changed: %s", out)
	}
	if !strings.Contains(string(out), "x-anthropic-billing-header: cc_version=2.0.14; cc_entrypoint=cli") {
		t.Fatalf("billing block removed: %s", out)
	}
	if strings.Contains(string(out), "system prompt secret") || strings.Contains(string(out), "hello prompt") {
		t.Fatalf("prompt leaked: %s", out)
	}
	if !gjson.GetBytes(out, "system.1.text._redacted").Bool() || gjson.GetBytes(out, "system.1.text.len").Int() == 0 || gjson.GetBytes(out, "system.1.text.sha256").String() == "" {
		t.Fatalf("system placeholder: %s", out)
	}
	if gjson.GetBytes(out, "system.0.cache_control.type").String() != "ephemeral" || gjson.GetBytes(out, "messages.0.role").String() != "user" {
		t.Fatalf("structure lost: %s", out)
	}
	modelAt := strings.Index(string(out), `"model"`)
	maxAt := strings.Index(string(out), `"max_tokens"`)
	systemAt := strings.Index(string(out), `"system"`)
	if modelAt < 0 || modelAt > maxAt || maxAt > systemAt {
		t.Fatalf("key order changed: %s", out)
	}
}

func TestRedactToolNamesSignaturesAndMedia(t *testing.T) {
	const toolName = "bash"
	body := []byte(`{
      "messages":[{"role":"assistant","content":[
        {"type":"thinking","thinking":"plan","signature":"sig-value-123"},
        {"type":"redacted_thinking","data":"opaque"},
        {"type":"tool_use","id":"toolu_1","name":"bash","input":{"cmd":"ls"}},
        {"type":"tool_result","tool_use_id":"toolu_1","is_error":false,"content":[{"type":"text","text":"listed"}]},
        {"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}},
        {"type":"document","source":{"type":"url","media_type":"text/plain","url":"https://secret.example/doc"},"text":"doc text"}
      ]}],
      "tools":[{"type":"custom","name":"bash","description":"run commands","input_schema":{"type":"object","properties":{"cmd":{}}}}],
      "tool_choice":{"type":"tool","name":"bash"},
      "thinking":{"type":"enabled","budget_tokens":100}
    }`)
	out, state, _, _ := RedactBody(body)
	if state != "parsed" {
		t.Fatal(state)
	}
	text := string(out)
	for _, leaked := range []string{toolName, "run commands", "sig-value-123", "plan", "listed", "AAAA", "https://secret.example/doc", "doc text", "opaque", `"cmd"`} {
		if strings.Contains(text, leaked) {
			t.Fatalf("leaked %q in %s", leaked, text)
		}
	}
	sum := sha256.Sum256([]byte(toolName))
	full := hex.EncodeToString(sum[:])
	if strings.Contains(text, full) || strings.Contains(text, full[:16]) {
		t.Fatalf("tool name hash is reversible material: %s", text)
	}
	if gjson.GetBytes(out, "messages.0.content.2.id").String() != "toolu_1" || gjson.GetBytes(out, "messages.0.content.3.tool_use_id").String() != "toolu_1" || gjson.GetBytes(out, "messages.0.content.3.is_error").Bool() {
		t.Fatalf("ids dropped: %s", text)
	}
	if !gjson.GetBytes(out, "messages.0.content.3.is_error").Exists() || gjson.GetBytes(out, "messages.0.content.3.is_error").Bool() {
		t.Fatal("is_error")
	}
	if gjson.GetBytes(out, "messages.0.content.4.source.type").String() != "base64" || gjson.GetBytes(out, "messages.0.content.4.source.media_type").String() != "image/png" {
		t.Fatalf("image source metadata: %s", text)
	}
	if !gjson.GetBytes(out, "messages.0.content.0.signature._redacted").Bool() || gjson.GetBytes(out, "messages.0.content.0.signature.len").Int() == 0 || gjson.GetBytes(out, "messages.0.content.0.signature.sha256").String() == "" {
		t.Fatalf("signature placeholder: %s", text)
	}
	if gjson.GetBytes(out, "tools.0.type").String() != "custom" || gjson.GetBytes(out, "tool_choice.type").String() != "tool" {
		t.Fatalf("tool type lost: %s", text)
	}
	if gjson.GetBytes(out, "thinking.budget_tokens").Int() != 100 || gjson.GetBytes(out, "thinking.type").String() != "enabled" {
		t.Fatalf("thinking config changed: %s", text)
	}
	if !gjson.GetBytes(out, "tools.0.name._redacted").Bool() || gjson.GetBytes(out, "tools.0.name.sha256").Exists() {
		t.Fatalf("tool name placeholder: %s", text)
	}
}

func TestRedactUserIDAndSecrets(t *testing.T) {
	rawSession := "session-raw-value"
	body := []byte(`{"metadata":{"user_id":"user_dev1_account_acc1_session_` + rawSession + `"},"mcp_servers":[{"name":"local","authorization_token":"super-secret-token"}],"max_tokens":12}`)
	out, state, _, _ := RedactBody(body)
	if state != "parsed" {
		t.Fatal(state)
	}
	text := string(out)
	if strings.Contains(text, "super-secret-token") || strings.Contains(text, rawSession) || strings.Contains(text, "dev1") || strings.Contains(text, "acc1") {
		t.Fatalf("secret or identity leaked: %s", text)
	}
	if gjson.GetBytes(out, "mcp_servers.0.authorization_token").String() != "***" {
		t.Fatalf("token mask: %s", text)
	}
	if gjson.GetBytes(out, "mcp_servers.0.name").String() != "local" || gjson.GetBytes(out, "max_tokens").Int() != 12 {
		t.Fatalf("unrelated fields: %s", text)
	}
	stored := gjson.GetBytes(out, "metadata.user_id").String()
	if !strings.Contains(stored, "session:sha256:"+hashID(rawSession)) || !strings.Contains(stored, "device:sha256:"+hashID("dev1")) {
		t.Fatalf("user id hash: %s", stored)
	}
}

func TestRedactInvalidAndTruncation(t *testing.T) {
	if _, state, _, _ := RedactBody([]byte(`not-json`)); state != "invalid_json" {
		t.Fatal(state)
	}
	if _, state, _, _ := RedactBody(nil); state != "unavailable" {
		t.Fatal(state)
	}
	huge := `{"model":"m","notes":"` + strings.Repeat("a", MaxStoredBytes+1024) + `"}`
	out, state, truncated, original := RedactBody([]byte(huge))
	if state != "parsed" || !truncated || original <= MaxStoredBytes {
		t.Fatalf("state %s truncated %v original %d", state, truncated, original)
	}
	if !gjson.GetBytes(out, "_truncated").Bool() || gjson.GetBytes(out, "original_bytes").Int() != int64(original) {
		t.Fatalf("marker: %s", out)
	}
	if strings.Contains(string(out), strings.Repeat("a", 100)) && !strings.Contains(string(out), `"preview"`) {
		t.Fatal("truncated body should be wrapped")
	}
	var payload map[string]any
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatal(err)
	}
}

func TestRedactHeadersMaskAndHashSession(t *testing.T) {
	session := "session-abc"
	headers := map[string][]string{
		"Authorization":            {"Bearer sk-ant-oat01-secret"},
		"X-Api-Key":                {"sk-live-secret"},
		"Cookie":                   {"sid=secret"},
		"Proxy-Authorization":      {"Basic abc"},
		"X-Claude-Code-Session-Id": {session},
		"User-Agent":               {"claude-cli/2.0.14"},
		"Anthropic-Beta":           {"claude-code-20250219"},
		"X-Forwarded-For":          {"203.0.113.24"},
	}
	raw, truncated := RedactHeaders(headers)
	if truncated {
		t.Fatal("unexpected truncation")
	}
	text := string(raw)
	for _, leaked := range []string{"sk-ant-oat01-secret", "sk-live-secret", "sid=secret", session} {
		if strings.Contains(text, leaked) {
			t.Fatalf("leaked %q in %s", leaked, text)
		}
	}
	var pairs []HeaderPair
	if err := json.Unmarshal(raw, &pairs); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	var names []string
	for _, pair := range pairs {
		got[pair.Name] = pair.Value
		names = append(names, pair.Name)
	}
	if got["Authorization"] != "Bearer ***" || got["X-Api-Key"] != "***" || got["Cookie"] != "***" || got["Proxy-Authorization"] != "Basic ***" {
		t.Fatalf("masks: %#v", got)
	}
	if got["X-Claude-Code-Session-Id"] != "sha256:"+hashID(session) {
		t.Fatalf("session hash: %s", got["X-Claude-Code-Session-Id"])
	}
	if got["User-Agent"] != "claude-cli/2.0.14" || got["X-Forwarded-For"] != "203.0.113.24" {
		t.Fatalf("plain headers: %#v", got)
	}
	for i := 1; i < len(names); i++ {
		if strings.ToLower(names[i]) < strings.ToLower(names[i-1]) {
			t.Fatalf("unsorted: %v", names)
		}
	}
}
