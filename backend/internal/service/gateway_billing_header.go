package service

import (
	"regexp"

	"github.com/tidwall/sjson"
)

// ccVersionInBillingRe matches the semver part of cc_version (X.Y.Z).
var ccVersionInBillingRe = regexp.MustCompile(`([:;]\s*cc_version\s*=\s*)\d+\.\d+\.\d+`)

var ccVersionWithFingerprintInBillingRe = regexp.MustCompile(`([:;]\s*cc_version\s*=\s*)\d+\.\d+\.\d+\.[0-9a-fA-F]{3}\b`)

var ccEntrypointInBillingRe = regexp.MustCompile(`([:;]\s*cc_entrypoint\s*=\s*)[A-Za-z0-9_-]+`)

// effectiveBillingUserAgent 选择同步 billing 身份时使用的完整 UA。
// OAuth mimicry 强制使用调用方传入的 mimicUserAgent（与出站 User-Agent 头同源、
// 同一次请求内取一次复用，保证 cc_version、cc_entrypoint 与出站头一致）。
// 其余情况使用账号指纹的完整 UA，与 ApplyFingerprint 保持同源。
func effectiveBillingUserAgent(mimicUserAgent, tokenType string, mimicClaudeCode bool, fingerprint *Fingerprint) string {
	if tokenType == "oauth" && mimicClaudeCode {
		return mimicUserAgent
	}
	if fingerprint == nil {
		return ""
	}
	return fingerprint.UserAgent
}

// syncBillingHeaderIdentity aligns existing billing blocks with the UA that will
// actually be sent, without changing that UA or the account fingerprint cache.
// Recompute recognized cc_version suffixes because their input includes the version.
// Run before constructing the HTTP request so Body, GetBody and ContentLength agree.
func syncBillingHeaderIdentity(body []byte, userAgent string, accountID int64) ([]byte, error) {
	version := ExtractCLIVersion(userAgent)
	if version == "" {
		return body, nil
	}

	blocks := billingHeaderTextBlocks(body)
	_, state := inspectBillingEntrypoint(blocks)
	switch state {
	case billingEntrypointAbsent:
		return body, nil
	case billingEntrypointUnparseable:
		return nil, newFingerprintEntrypointConflict(accountID, fingerprintEntrypointKindUnparseable)
	case billingEntrypointConflict:
		return nil, newFingerprintEntrypointConflict(accountID, fingerprintEntrypointKindConflict)
	}
	entrypoint, ok := claudeCLIExternalEntrypoint(userAgent)
	if !ok {
		return nil, newFingerprintEntrypointConflict(accountID, fingerprintEntrypointKindUnalignable)
	}

	replacement := "${1}" + version
	fingerprintedReplacement := replacement + "." + computeClaudeCodeFingerprint(body, version)
	for _, block := range blocks {
		newText := ccVersionWithFingerprintInBillingRe.ReplaceAllString(block.text, fingerprintedReplacement)
		newText = ccVersionInBillingRe.ReplaceAllString(newText, replacement)
		newText = ccEntrypointInBillingRe.ReplaceAllString(newText, "${1}"+entrypoint)
		if newText == block.text {
			continue
		}
		updated, err := sjson.SetBytes(body, block.path, newText)
		if err != nil {
			return nil, newFingerprintEntrypointConflict(accountID, fingerprintEntrypointKindUnalignable)
		}
		body = updated
	}

	return body, nil
}
