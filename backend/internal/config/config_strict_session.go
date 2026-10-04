package config

import (
	"fmt"
	"strings"
)

// GatewayStrictSessionBindingConfig 控制 Claude Messages 会话永久绑定。
// 绑定写入数据库，不因 TTL、缓存清空或账号删除而消失。
type GatewayStrictSessionBindingConfig struct {
	// Enabled 默认 true。关闭时不读取、不写入绑定，调度保持官方行为。
	Enabled bool
	// SessionHeader 是 metadata.user_id / X-Claude-Code-Session-Id 之外的稳定会话头。
	// 默认 X-Session-Id。内容摘要永远不会被当作永久会话 ID。
	SessionHeader string
	// SameAccountRetryLimit 限制原账号上的可安全重试次数。
	// 0 禁用同账号重试。大于 0 时收紧上限。-1 沿用账号的 pool_mode_retry_count。
	SameAccountRetryLimit int
}

// SessionHeaderOrDefault 返回严格模式接受的附加会话头。
func (c GatewayStrictSessionBindingConfig) SessionHeaderOrDefault() string {
	header := strings.TrimSpace(c.SessionHeader)
	if header == "" {
		return "X-Session-Id"
	}
	return header
}

// NormalizeAndValidate 检查严格会话绑定配置。关闭时不改官方调度。
func (c *GatewayStrictSessionBindingConfig) NormalizeAndValidate() error {
	if c == nil {
		return nil
	}
	c.SessionHeader = strings.TrimSpace(c.SessionHeader)
	if !c.Enabled {
		return nil
	}
	if c.SameAccountRetryLimit < -1 {
		return fmt.Errorf("same_account_retry_limit must be -1, 0, or positive")
	}
	if c.SessionHeader != "" {
		if err := validateOptionalHTTPHeaderName(c.SessionHeader); err != nil {
			return fmt.Errorf("session_header: %w", err)
		}
	}
	return nil
}

func validateOptionalHTTPHeaderName(name string) error {
	if name == "" {
		return nil
	}
	if strings.ContainsAny(name, " \t\r\n:") {
		return fmt.Errorf("invalid header name %q", name)
	}
	return nil
}

// DefaultStrictSessionBindingConfig is used only after a successful database read.
func DefaultStrictSessionBindingConfig() GatewayStrictSessionBindingConfig {
	return GatewayStrictSessionBindingConfig{
		Enabled:               true,
		SessionHeader:         "X-Session-Id",
		SameAccountRetryLimit: -1,
	}
}
