package service

import (
	"fmt"
	"regexp"
	"strings"
	"sync/atomic"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/tidwall/gjson"
)

// 指纹统一沿用账号缓存的完整 UA；billing 的版本和入口对齐最终出站 UA。
// billing 块缺少或无法解析入口、或多个 billing 块入口不一致时，拒绝发出请求。
// 诊断只记账号 ID、冲突种类和计数，不记正文、完整请求、Token、Cookie 或设备/会话标识。
const (
	fingerprintEntrypointKindUnparseable = "billing_cc_entrypoint_unparseable"
	fingerprintEntrypointKindConflict    = "billing_cc_entrypoint_conflict"
	fingerprintEntrypointKindUnalignable = "cannot_align_user_agent_entrypoint"
)

var fingerprintEntrypointConflictCount atomic.Uint64

var billingEntrypointTokenPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// claudeCLIExternalEntrypointPattern 取 "(external, <entrypoint>)" 的入口 token。
// 后面可以还有 ", agent-sdk/..." 这类缓存 UA 自带的后缀。
var claudeCLIExternalEntrypointPattern = regexp.MustCompile(`(?i)^claude-cli/\d+\.\d+\.\d+[ \t]+\(external, ([A-Za-z0-9_-]+)(?:, [^()]+)?\)$`)

// FingerprintEntrypointConflictError 表示 billing 无法安全对齐最终出站 UA。
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
		"fingerprint entrypoint conflict account=%d kind=%s count=%d",
		accountID, kind, count)
	return &FingerprintEntrypointConflictError{Kind: kind}
}

func claudeCLIExternalEntrypoint(ua string) (string, bool) {
	match := claudeCLIExternalEntrypointPattern.FindStringSubmatch(strings.TrimSpace(ua))
	if len(match) != 2 || !billingEntrypointTokenPattern.MatchString(match[1]) {
		return "", false
	}
	return match[1], true
}

func inspectBillingEntrypoint(blocks []billingHeaderTextBlock) (string, billingEntrypointState) {
	if len(blocks) == 0 {
		return "", billingEntrypointAbsent
	}
	var entrypoint string
	for i, block := range blocks {
		got, state := parseBillingBlockEntrypoint(block.text)
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

type billingHeaderTextBlock struct {
	path string
	text string
}

// Keep paths as well as text so validation and rewriting cover the same shapes.
func billingHeaderTextBlocks(body []byte) []billingHeaderTextBlock {
	if !gjson.ValidBytes(body) {
		return nil
	}
	system := gjson.GetBytes(body, "system")
	if !system.Exists() {
		return nil
	}
	var blocks []billingHeaderTextBlock
	appendBlock := func(path, text string) {
		if strings.HasPrefix(text, "x-anthropic-billing-header:") {
			blocks = append(blocks, billingHeaderTextBlock{path: path, text: text})
		}
	}
	switch {
	case system.Type == gjson.String:
		appendBlock("system", system.String())
	case system.IsArray():
		system.ForEach(func(index, item gjson.Result) bool {
			path := fmt.Sprintf("system.%d", index.Int())
			switch item.Type {
			case gjson.String:
				appendBlock(path, item.String())
			default:
				if text := item.Get("text"); text.Exists() && text.Type == gjson.String {
					appendBlock(path+".text", text.String())
				}
			}
			return true
		})
	}
	return blocks
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
