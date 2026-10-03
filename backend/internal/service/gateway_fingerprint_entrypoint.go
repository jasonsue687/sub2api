package service

import (
	"net/http"
	"regexp"
	"strings"
	"sync/atomic"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/tidwall/gjson"
)

// 非 mimic、指纹统一开启时，出站身份按三个作用域拆开：
//
//   - 账号稳定标识（ClientID、X-Stainless-*）仍来自账号指纹，由 ApplyFingerprint 写入。
//   - 软件版本仍来自账号指纹。syncBillingHeaderVersion 只改 cc_version 及其内容指纹后缀。
//   - 入口类型属于这一次请求。账号缓存 UA 里的 "(external, <entrypoint>)" 不得覆盖当前请求。
//
// 不改写 body 的 cc_entrypoint 来掩盖客户端差异。请求自己的 UA 入口和 billing 入口
// 对不上、billing 块缺少或无法解析入口、或多个 billing 块入口不一致时，拒绝发出请求。
// 诊断只记账号 ID、冲突种类和计数，不记正文、完整请求、Token、Cookie 或设备/会话标识。
const (
	fingerprintEntrypointKindUnparseable = "billing_cc_entrypoint_unparseable"
	fingerprintEntrypointKindConflict    = "billing_cc_entrypoint_conflict"
	fingerprintEntrypointKindDisagree    = "request_user_agent_and_billing_entrypoint_disagree"
	fingerprintEntrypointKindUnalignable = "cannot_align_user_agent_entrypoint"
)

var fingerprintEntrypointConflictCount atomic.Uint64

var billingEntrypointTokenPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// claudeCLIExternalEntrypointPattern 取 "(external, <entrypoint>)" 的入口 token。
// 后面可以还有 ", agent-sdk/..." 这类本次客户端自己的后缀。
var claudeCLIExternalEntrypointPattern = regexp.MustCompile(`(?i)^claude-cli/\d+\.\d+\.\d+[ \t]+\(external, ([A-Za-z0-9_-]+)`)

// FingerprintEntrypointConflictError 表示非 mimic 路径无法确定唯一的入口类型。
// Kind 是固定枚举，不含请求正文或凭证。
type FingerprintEntrypointConflictError struct {
	Kind string
}

func (e *FingerprintEntrypointConflictError) Error() string {
	if e == nil || e.Kind == "" {
		return "anthropic oauth fingerprint entrypoint conflict"
	}
	return "anthropic oauth fingerprint entrypoint conflict: " + e.Kind
}

type billingEntrypointState int

const (
	billingEntrypointAbsent billingEntrypointState = iota
	billingEntrypointOK
	billingEntrypointUnparseable
	billingEntrypointConflict
)

func newFingerprintEntrypointConflict(accountID int64, kind string) error {
	count := fingerprintEntrypointConflictCount.Add(1)
	logger.LegacyPrintf("service.gateway",
		"non-mimic fingerprint entrypoint conflict account=%d kind=%s count=%d",
		accountID, kind, count)
	return &FingerprintEntrypointConflictError{Kind: kind}
}

// alignNonMimicOAuthEntrypoint 在 ApplyFingerprint 之后调整出站 User-Agent。
// fingerprintUA 只提供账号级版本；入口来自当前请求。mimic 路径不要调用。
func alignNonMimicOAuthEntrypoint(req *http.Request, clientHeaders http.Header, body []byte, fingerprintUA string, accountID int64) error {
	version := ExtractCLIVersion(fingerprintUA)
	if version == "" || req == nil {
		return nil
	}

	bodyEP, state := inspectBillingEntrypoint(body)
	switch state {
	case billingEntrypointUnparseable:
		return newFingerprintEntrypointConflict(accountID, fingerprintEntrypointKindUnparseable)
	case billingEntrypointConflict:
		return newFingerprintEntrypointConflict(accountID, fingerprintEntrypointKindConflict)
	}

	clientUA := ""
	if clientHeaders != nil {
		clientUA = strings.TrimSpace(clientHeaders.Get("User-Agent"))
	}
	clientEP, clientOK := claudeCLIExternalEntrypoint(clientUA)
	if state == billingEntrypointOK && clientOK && clientEP != bodyEP {
		return newFingerprintEntrypointConflict(accountID, fingerprintEntrypointKindDisagree)
	}

	requestEP := ""
	switch {
	case state == billingEntrypointOK:
		requestEP = bodyEP
	case clientOK:
		requestEP = clientEP
	default:
		// 这次请求没有入口语义，账号指纹 UA 不会和 body 里的 cc_entrypoint 矛盾。
		return nil
	}

	aligned, ok := outboundUserAgentForRequestEntrypoint(clientUA, clientOK, fingerprintUA, version, requestEP)
	if !ok {
		return newFingerprintEntrypointConflict(accountID, fingerprintEntrypointKindUnalignable)
	}
	if aligned == "" {
		return nil
	}
	gotEP, gotOK := claudeCLIExternalEntrypoint(aligned)
	if !gotOK || gotEP != requestEP || ExtractCLIVersion(aligned) != version {
		return newFingerprintEntrypointConflict(accountID, fingerprintEntrypointKindUnalignable)
	}
	setHeaderRaw(req.Header, "User-Agent", aligned)
	return nil
}

// outboundUserAgentForRequestEntrypoint 保留当前请求的入口后缀，只把版本换成账号指纹版本。
// 返回空字符串表示出站 UA 已经正确，调用方不用再写 header。
func outboundUserAgentForRequestEntrypoint(clientUA string, clientOK bool, fingerprintUA, version, requestEP string) (string, bool) {
	if clientOK {
		if ep, ok := claudeCLIExternalEntrypoint(clientUA); ok && ep == requestEP {
			return replaceClaudeCLIVersion(clientUA, version)
		}
	}
	if !clientOK {
		if cacheEP, ok := claudeCLIExternalEntrypoint(fingerprintUA); ok && cacheEP == requestEP {
			return "", true
		}
	}
	return "claude-cli/" + version + " (external, " + requestEP + ")", true
}

func replaceClaudeCLIVersion(ua, version string) (string, bool) {
	if version == "" || ExtractCLIVersion(ua) == "" {
		return "", false
	}
	replaced := claudeCLIUAVersionPrefixRegex.ReplaceAllString(ua, "${1}/"+version)
	if ExtractCLIVersion(replaced) != version {
		return "", false
	}
	return replaced, true
}

func claudeCLIExternalEntrypoint(ua string) (string, bool) {
	match := claudeCLIExternalEntrypointPattern.FindStringSubmatch(strings.TrimSpace(ua))
	if len(match) != 2 || !billingEntrypointTokenPattern.MatchString(match[1]) {
		return "", false
	}
	return match[1], true
}

func inspectBillingEntrypoint(body []byte) (string, billingEntrypointState) {
	texts := billingHeaderTexts(body)
	if len(texts) == 0 {
		return "", billingEntrypointAbsent
	}
	var entrypoint string
	for i, text := range texts {
		got, state := parseBillingBlockEntrypoint(text)
		if state != billingEntrypointOK {
			return "", state
		}
		if i > 0 && got != entrypoint {
			return "", billingEntrypointConflict
		}
		entrypoint = got
	}
	return entrypoint, billingEntrypointOK
}

func billingHeaderTexts(body []byte) []string {
	system := gjson.GetBytes(body, "system")
	if !system.Exists() {
		return nil
	}
	var raw []string
	switch {
	case system.Type == gjson.String:
		raw = append(raw, system.String())
	case system.IsArray():
		system.ForEach(func(_, item gjson.Result) bool {
			switch {
			case item.Type == gjson.String:
				raw = append(raw, item.String())
			default:
				if text := item.Get("text"); text.Exists() && text.Type == gjson.String {
					raw = append(raw, text.String())
				}
			}
			return true
		})
	}
	texts := make([]string, 0, len(raw))
	for _, text := range raw {
		if strings.HasPrefix(text, "x-anthropic-billing-header") {
			texts = append(texts, text)
		}
	}
	return texts
}

func parseBillingBlockEntrypoint(text string) (string, billingEntrypointState) {
	rest := strings.TrimPrefix(text, "x-anthropic-billing-header")
	var entrypoint string
	found := false
	for _, field := range strings.Split(rest, ";") {
		field = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(field), ":"))
		if field == "" {
			continue
		}
		key, val, ok := strings.Cut(field, "=")
		if !ok || strings.TrimSpace(key) != "cc_entrypoint" {
			continue
		}
		val = strings.TrimSpace(val)
		if !billingEntrypointTokenPattern.MatchString(val) {
			return "", billingEntrypointUnparseable
		}
		if found && entrypoint != val {
			return "", billingEntrypointConflict
		}
		found = true
		entrypoint = val
	}
	if !found {
		return "", billingEntrypointUnparseable
	}
	return entrypoint, billingEntrypointOK
}
