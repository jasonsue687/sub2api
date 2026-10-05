package anthropicaudit

import (
	"encoding/json"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// MaxStoredBytes is the per-document cap for a redacted body or header set.
const MaxStoredBytes = 64 << 10

const billingPrefix = "x-anthropic-billing-header:"

// RedactBody replaces prompt text, tool names, and secrets in place.
// Key order follows the original document. Invalid JSON is not returned.
func RedactBody(body []byte) (stored []byte, state string, truncated bool, original int) {
	original = len(body)
	if len(body) == 0 {
		return nil, "unavailable", false, 0
	}
	if !json.Valid(body) || !gjson.ParseBytes(body).IsObject() {
		return nil, "invalid_json", false, original
	}
	root := gjson.ParseBytes(body)
	out := append([]byte(nil), body...)
	var err error
	set := func(path string, raw []byte) {
		if err != nil || path == "" {
			return
		}
		out, err = sjson.SetRawBytes(out, path, raw)
	}
	// Mask secrets before replacing parent blobs, so a later placeholder
	// overwrites nested secrets instead of re-inserting them.
	redactSecrets(set, root, "")
	if sys := root.Get("system"); sys.Exists() {
		redactSystem(set, "system", sys)
	}
	if msgs := root.Get("messages"); msgs.IsArray() {
		for i, msg := range msgs.Array() {
			redactMessage(set, indexed("messages", i), msg)
		}
	}
	if tools := root.Get("tools"); tools.IsArray() {
		for i, tool := range tools.Array() {
			redactTool(set, indexed("tools", i), tool)
		}
	}
	if choice := root.Get("tool_choice"); choice.IsObject() {
		if name := choice.Get("name"); name.Exists() {
			set("tool_choice.name", namePlaceholder(name.String()))
		}
	}
	if uid := root.Get("metadata.user_id"); uid.Type == gjson.String {
		set("metadata.user_id", hashUserIDJSON(uid.String()))
	}
	if err != nil || !json.Valid(out) {
		return nil, "invalid_json", false, original
	}
	stored, truncated = capStored(out, original)
	return stored, "parsed", truncated, original
}

func redactSystem(set func(string, []byte), path string, sys gjson.Result) {
	if sys.Type == gjson.String {
		if strings.HasPrefix(sys.String(), billingPrefix) {
			return
		}
		set(path, textPlaceholder(sys.String(), true))
		return
	}
	if sys.IsArray() {
		for i, block := range sys.Array() {
			redactBlock(set, indexed(path, i), block)
		}
	}
}

func redactMessage(set func(string, []byte), path string, msg gjson.Result) {
	if !msg.IsObject() {
		return
	}
	if content := msg.Get("content"); content.Exists() {
		redactContent(set, path+".content", content)
	}
}

func redactContent(set func(string, []byte), path string, content gjson.Result) {
	if content.Type == gjson.String {
		set(path, textPlaceholder(content.String(), true))
		return
	}
	if content.IsArray() {
		for i, block := range content.Array() {
			redactBlock(set, indexed(path, i), block)
		}
	}
}

func redactBlock(set func(string, []byte), path string, block gjson.Result) {
	if block.Type == gjson.String {
		set(path, textPlaceholder(block.String(), true))
		return
	}
	if !block.IsObject() {
		return
	}
	if citations := block.Get("citations"); citations.Exists() {
		set(path+".citations", textPlaceholder(citations.Raw, true))
	}
	switch block.Get("type").String() {
	case "text":
		if text := block.Get("text"); text.Type == gjson.String && !strings.HasPrefix(text.String(), billingPrefix) {
			set(path+".text", textPlaceholder(text.String(), true))
		}
	case "thinking":
		if text := block.Get("thinking"); text.Type == gjson.String {
			set(path+".thinking", textPlaceholder(text.String(), true))
		}
		if sig := block.Get("signature"); sig.Type == gjson.String {
			set(path+".signature", textPlaceholder(sig.String(), true))
		}
	case "redacted_thinking":
		if data := block.Get("data"); data.Exists() {
			set(path+".data", textPlaceholder(data.Raw, true))
		}
	case "tool_use", "server_tool_use":
		if name := block.Get("name"); name.Exists() {
			set(path+".name", namePlaceholder(name.String()))
		}
		if input := block.Get("input"); input.Exists() {
			set(path+".input", textPlaceholder(input.Raw, true))
		}
	case "tool_result":
		if content := block.Get("content"); content.Exists() {
			redactContent(set, path+".content", content)
		}
	case "image", "document":
		for _, key := range []string{"title", "context"} {
			if value := block.Get(key); value.Exists() {
				set(path+"."+key, textPlaceholder(value.Raw, true))
			}
		}
		if src := block.Get("source"); src.IsObject() {
			if content := src.Get("content"); content.Exists() {
				set(path+".source.content", textPlaceholder(content.Raw, true))
			}
			if data := src.Get("data"); data.Exists() {
				set(path+".source.data", textPlaceholder(data.Raw, true))
			}
			if url := src.Get("url"); url.Exists() {
				set(path+".source.url", textPlaceholder(url.String(), true))
			}
		}
		if text := block.Get("text"); text.Type == gjson.String {
			set(path+".text", textPlaceholder(text.String(), true))
		}
	default:
		if text := block.Get("text"); text.Type == gjson.String && !strings.HasPrefix(text.String(), billingPrefix) {
			set(path+".text", textPlaceholder(text.String(), true))
		}
	}
}

func redactTool(set func(string, []byte), path string, tool gjson.Result) {
	if !tool.IsObject() {
		return
	}
	if name := tool.Get("name"); name.Exists() {
		set(path+".name", namePlaceholder(name.String()))
	}
	if desc := tool.Get("description"); desc.Exists() {
		set(path+".description", textPlaceholder(desc.Raw, true))
	}
	if schema := tool.Get("input_schema"); schema.Exists() {
		set(path+".input_schema", textPlaceholder(schema.Raw, true))
	}
}

func redactSecrets(set func(string, []byte), value gjson.Result, path string) {
	if value.IsObject() {
		value.ForEach(func(key, child gjson.Result) bool {
			next := key.String()
			if path != "" {
				next = path + "." + key.String()
			}
			if isSecretKey(key.String()) {
				set(next, []byte(`"***"`))
				return true
			}
			redactSecrets(set, child, next)
			return true
		})
		return
	}
	if value.IsArray() {
		for i, child := range value.Array() {
			redactSecrets(set, child, indexed(path, i))
		}
	}
}

func isSecretKey(key string) bool {
	k := strings.ToLower(strings.TrimSpace(key))
	switch k {
	case "max_tokens", "budget_tokens":
		return false
	}
	if strings.Contains(k, "secret") || strings.Contains(k, "password") || strings.Contains(k, "api_key") || strings.Contains(k, "apikey") {
		return true
	}
	if k == "token" || strings.HasSuffix(k, "_token") || strings.Contains(k, "authorization") {
		return true
	}
	return false
}

func textPlaceholder(raw string, withHash bool) []byte {
	payload := map[string]any{"_redacted": true, "len": utf8.RuneCountInString(raw)}
	if withHash {
		payload["sha256"] = shortHash(raw)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return []byte(`{"_redacted":true}`)
	}
	return encoded
}

// namePlaceholder records that a tool name existed without a reversible hash.
func namePlaceholder(raw string) []byte {
	encoded, err := json.Marshal(map[string]any{"_redacted": true, "len": utf8.RuneCountInString(raw)})
	if err != nil {
		return []byte(`{"_redacted":true}`)
	}
	return encoded
}

func hashUserIDJSON(raw string) []byte {
	formatted := formatHashedUserID(raw)
	if formatted == "" {
		return textPlaceholder(raw, true)
	}
	encoded, err := json.Marshal(formatted)
	if err != nil {
		return textPlaceholder(raw, true)
	}
	return encoded
}

func formatHashedUserID(raw string) string {
	var id struct {
		Device  string `json:"device_id"`
		Account string `json:"account_uuid"`
		Session string `json:"session_id"`
	}
	if json.Unmarshal([]byte(raw), &id) != nil {
		if match := oldIdentityPattern.FindStringSubmatch(raw); len(match) == 4 {
			id.Device, id.Account, id.Session = match[1], match[2], match[3]
		} else {
			return ""
		}
	}
	parts := make([]string, 0, 3)
	if hashed := hashID(id.Device); hashed != "" {
		parts = append(parts, "device:sha256:"+hashed)
	}
	if hashed := hashID(id.Account); hashed != "" {
		parts = append(parts, "account:sha256:"+hashed)
	}
	if hashed := hashID(id.Session); hashed != "" {
		parts = append(parts, "session:sha256:"+hashed)
	}
	return strings.Join(parts, " ")
}

func shortHash(raw string) string {
	hashed := hashID(raw)
	if len(hashed) > 16 {
		return hashed[:16]
	}
	return hashed
}

func indexed(prefix string, index int) string {
	return prefix + "." + itoa(index)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [16]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

func capStored(raw []byte, original int) ([]byte, bool) {
	if len(raw) <= MaxStoredBytes {
		return raw, false
	}
	preview := raw
	if len(preview) > MaxStoredBytes/2 {
		preview = preview[:MaxStoredBytes/2]
	}
	wrapper := map[string]any{
		"_truncated":     true,
		"original_bytes": original,
		"redacted_bytes": len(raw),
		"preview":        string(preview),
	}
	encoded, err := json.Marshal(wrapper)
	if err != nil {
		return []byte(`{"_truncated":true}`), true
	}
	return encoded, true
}

// HeaderPair is one stored header. Names keep the map key; values are masked.
type HeaderPair struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// RedactHeaders stores every header, masking secrets and hashing session ids.
func RedactHeaders(values map[string][]string) ([]byte, bool) {
	if len(values) == 0 {
		encoded, err := json.Marshal([]HeaderPair{})
		if err != nil {
			return []byte("[]"), false
		}
		return encoded, false
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sortHeaderKeys(keys)
	pairs := make([]HeaderPair, 0, len(keys))
	for _, key := range keys {
		pairs = append(pairs, HeaderPair{Name: key, Value: redactHeaderValue(key, strings.Join(values[key], ", "))})
	}
	encoded, err := json.Marshal(pairs)
	if err != nil {
		return []byte("[]"), false
	}
	if len(encoded) <= MaxStoredBytes {
		return encoded, false
	}
	wrapped, truncated := capStored(encoded, len(encoded))
	return wrapped, truncated
}

func sortHeaderKeys(keys []string) {
	sort.Slice(keys, func(i, j int) bool {
		return strings.ToLower(keys[i]) < strings.ToLower(keys[j])
	})
}

func redactHeaderValue(name, value string) string {
	if isSessionHeader(name) {
		if strings.TrimSpace(value) == "" {
			return ""
		}
		return "sha256:" + hashID(value)
	}
	if isSecretHeader(name) {
		return maskCredential(value)
	}
	if len(value) > 4096 {
		return string(textPlaceholder(value, true))
	}
	return value
}

func isSessionHeader(name string) bool {
	return strings.Contains(strings.ToLower(name), "session")
}

func isSecretHeader(name string) bool {
	n := strings.ToLower(name)
	switch n {
	case "authorization", "x-api-key", "cookie", "set-cookie", "proxy-authorization":
		return true
	}
	return strings.Contains(n, "token") || strings.Contains(n, "secret") || strings.Contains(n, "key")
}

func maskCredential(value string) string {
	trimmed := strings.TrimSpace(value)
	lower := strings.ToLower(trimmed)
	switch {
	case strings.HasPrefix(lower, "bearer "):
		return "Bearer ***"
	case strings.HasPrefix(lower, "basic "):
		return "Basic ***"
	default:
		return "***"
	}
}
