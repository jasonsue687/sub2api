package anthropicmock

import (
	"bytes"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"
)

var (
	bearerPattern = regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._\-+/=]{8,}`)
	skAntPattern  = regexp.MustCompile(`sk-ant-[A-Za-z0-9_\-]{8,}`)
	skPattern     = regexp.MustCompile(`sk-[A-Za-z0-9_\-]{16,}`)
)

func sensitiveKey(key string) bool {
	k := strings.ToLower(key)
	k = strings.ReplaceAll(k, "-", "")
	k = strings.ReplaceAll(k, "_", "")
	switch {
	case strings.Contains(k, "password"):
		return true
	case strings.Contains(k, "secret"):
		return true
	case strings.Contains(k, "token"):
		return true
	case strings.Contains(k, "apikey"):
		return true
	case strings.Contains(k, "authorization"):
		return true
	case strings.Contains(k, "cookie"):
		return true
	case k == "key":
		return true
	default:
		return false
	}
}

func sensitiveHeader(key string) bool {
	if sensitiveKey(key) {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "authorization", "cookie", "set-cookie", "proxy-authorization", "x-api-key", "api-key":
		return true
	default:
		return false
	}
}

// RedactBody removes credential-shaped values. JSON objects are walked so
// token fields are replaced without keeping the original bytes.
func RedactBody(contentType string, body []byte) []byte {
	if len(body) == 0 {
		return nil
	}
	trimmed := bytes.TrimSpace(body)
	ct := strings.ToLower(contentType)
	looksJSON := strings.Contains(ct, "json") || bytes.HasPrefix(trimmed, []byte("{")) || bytes.HasPrefix(trimmed, []byte("["))
	if looksJSON && json.Valid(trimmed) {
		var value any
		if json.Unmarshal(trimmed, &value) == nil {
			encoded, err := json.Marshal(redactJSON(value))
			if err == nil {
				return encoded
			}
		}
	}
	return redactStringBytes(body)
}

func redactJSON(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			if sensitiveKey(key) {
				out[key] = "[redacted]"
				continue
			}
			out[key] = redactJSON(item)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = redactJSON(item)
		}
		return out
	case string:
		return string(redactStringBytes([]byte(v)))
	default:
		return value
	}
}

func redactStringBytes(body []byte) []byte {
	out := bearerPattern.ReplaceAll(body, []byte("Bearer [redacted]"))
	out = skAntPattern.ReplaceAll(out, []byte("[redacted]"))
	out = skPattern.ReplaceAll(out, []byte("[redacted]"))
	return out
}

func redactHeaders(req *http.Request) map[string]string {
	out := make(map[string]string)
	if req == nil {
		return out
	}
	count := 0
	for key, values := range req.Header {
		if count >= 100 {
			break
		}
		count++
		joined := strings.Join(values, ", ")
		if sensitiveHeader(key) {
			out[key] = "[redacted]"
			continue
		}
		out[key] = bound(joined, 1024)
	}
	return out
}

func safeURL(req *http.Request) string {
	if req == nil || req.URL == nil {
		return ""
	}
	u := *req.URL
	u.User = nil
	q := u.Query()
	for key, vals := range q {
		if !sensitiveKey(key) {
			continue
		}
		for i := range vals {
			vals[i] = "[redacted]"
		}
		q[key] = vals
	}
	u.RawQuery = q.Encode()
	if u.Scheme == "" {
		u.Scheme = "https"
	}
	text := u.String()
	return bound(text, 2048)
}

func bound(v string, n int) string {
	if len(v) <= n {
		return v
	}
	return v[:n]
}

// Preview returns a short UTF-8 view of body for the admin page.
func Preview(body []byte, n int) string {
	if n <= 0 || len(body) == 0 {
		return ""
	}
	if !utf8.Valid(body) {
		return "<binary>"
	}
	if len(body) <= n {
		return string(body)
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(body[cut]) {
		cut--
	}
	return string(body[:cut]) + "…"
}

func allowInboundHeaders(h http.Header) map[string]string {
	out := make(map[string]string)
	if h == nil {
		return out
	}
	for _, key := range []string{"Content-Type", "User-Agent", "Anthropic-Version", "Anthropic-Beta", "Accept", "X-App"} {
		value := strings.TrimSpace(h.Get(key))
		if value == "" || sensitiveHeader(key) {
			continue
		}
		out[key] = bound(redactHeaderValue(value), 180)
	}
	return out
}

func redactHeaderValue(value string) string {
	return string(redactStringBytes([]byte(value)))
}
