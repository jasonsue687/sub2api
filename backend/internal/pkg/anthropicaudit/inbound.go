package anthropicaudit

import (
	"encoding/json"
	"net/http"

	"github.com/tidwall/gjson"
)

// OmittedInboundValue marks a field whose entire value is excluded from capture.
const OmittedInboundValue = "[OMITTED]"

// CaptureInboundBody preserves original JSON values except the three excluded
// top-level fields and credentials. Replacements use source offsets so unknown
// fields, duplicate keys, number spelling, and string escapes remain unchanged.
func CaptureInboundBody(body []byte) ([]byte, string, bool, int) {
	if len(body) == 0 {
		return nil, "unavailable", false, 0
	}
	if !json.Valid(body) || !gjson.ParseBytes(body).IsObject() {
		return nil, "invalid_json", false, len(body)
	}
	var edits []inboundReplacement
	collectInboundReplacements(gjson.ParseBytes(body), true, &edits)
	out := make([]byte, 0, len(body))
	position := 0
	for _, edit := range edits {
		out = append(out, body[position:edit.start]...)
		out = append(out, edit.value...)
		position = edit.end
	}
	out = append(out, body[position:]...)
	stored, truncated := capStored(out, len(body))
	return stored, "parsed", truncated, len(body)
}

type inboundReplacement struct {
	start, end int
	value      string
}

func collectInboundReplacements(value gjson.Result, topLevel bool, edits *[]inboundReplacement) {
	if !value.IsObject() && !value.IsArray() {
		return
	}
	object := value.IsObject()
	value.ForEach(func(key, child gjson.Result) bool {
		replacement := ""
		if topLevel && (key.String() == "messages" || key.String() == "tools" || key.String() == "tool_choice") {
			replacement = `"` + OmittedInboundValue + `"`
		} else if object && isSecretKey(key.String()) {
			replacement = `"***"`
		}
		if replacement != "" {
			*edits = append(*edits, inboundReplacement{child.Index, child.Index + len(child.Raw), replacement})
		} else {
			collectInboundReplacements(child, false, edits)
		}
		return true
	})
}

// CaptureInboundHeaders keeps received header values, including session IDs
// and repeated values. Only credentials are masked; header order is unavailable.
func CaptureInboundHeaders(headers http.Header) ([]byte, bool) {
	keys := make([]string, 0, len(headers))
	for key := range headers {
		keys = append(keys, key)
	}
	sortHeaderKeys(keys)
	pairs := make([]HeaderPair, 0, len(headers))
	for _, key := range keys {
		for _, value := range headers[key] {
			if isSecretHeader(key) {
				value = maskCredential(value)
			}
			pairs = append(pairs, HeaderPair{Name: key, Value: value})
		}
	}
	raw, err := json.Marshal(pairs)
	if err != nil {
		return []byte("[]"), false
	}
	return capStored(raw, len(raw))
}
