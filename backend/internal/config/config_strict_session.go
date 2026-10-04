package config

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/viper"
)

// GatewayStrictSessionBindingConfig 控制 Claude Messages 会话永久绑定。
// 绑定写入数据库，不因 TTL、缓存清空或账号删除而消失。
type GatewayStrictSessionBindingConfig struct {
	// Enabled 默认 false。关闭时不读取、不写入绑定，调度保持官方行为。
	Enabled bool `mapstructure:"enabled"`
	// SessionHeader 是 metadata.user_id / X-Claude-Code-Session-Id 之外的稳定会话头。
	// 默认 X-Session-Id。内容摘要永远不会被当作永久会话 ID。
	SessionHeader string `mapstructure:"session_header"`
	// SameAccountRetryLimit 限制原账号上的可安全重试次数。
	// 0 禁用同账号重试。大于 0 时收紧上限。-1 沿用账号的 pool_mode_retry_count。
	SameAccountRetryLimit int `mapstructure:"same_account_retry_limit"`
	// FallbackOrder 决定原账号不能承接时先尝试哪个回退目标。
	// group_first（默认）先走兜底分组，失败且响应未写出时再走第三方。
	// third_party_first 顺序相反。只配置了一个目标时，另一个会被跳过。
	FallbackOrder string `mapstructure:"fallback_order"`
	// FallbackGroupID 是 Sub2API 内的兜底分组。0 表示不启用。
	// 请求所属分组与它相同时，该请求不会使用这个目标，也不会回到原分组账号池。
	FallbackGroupID int64 `mapstructure:"fallback_group_id"`
	// ThirdParty 是独立于订阅账号池的中转目标。
	ThirdParty GatewayStrictThirdPartyConfig `mapstructure:"third_party"`
}

const (
	// StrictFallbackOrderGroupFirst 先尝试兜底分组，再尝试第三方。
	StrictFallbackOrderGroupFirst = "group_first"
	// StrictFallbackOrderThirdPartyFirst 先尝试第三方，再尝试兜底分组。
	StrictFallbackOrderThirdPartyFirst = "third_party_first"
)

// GatewayStrictThirdPartyConfig 是严格绑定失败后的独立中转，不会进入订阅账号池。
type GatewayStrictThirdPartyConfig struct {
	Enabled bool   `mapstructure:"enabled"`
	BaseURL string `mapstructure:"base_url"`
	APIKey  string `mapstructure:"api_key"`
	// TimeoutSeconds 是等待响应头以及两次读之间的空闲上限，不是整段响应体的总时长。
	// 0 使用内置默认：响应头 60 秒，流空闲 5 分钟。活跃的长流不会被总时长截断。
	TimeoutSeconds int `mapstructure:"timeout_seconds"`
}

// SessionHeaderOrDefault 返回严格模式接受的附加会话头。
func (c GatewayStrictSessionBindingConfig) SessionHeaderOrDefault() string {
	header := strings.TrimSpace(c.SessionHeader)
	if header == "" {
		return "X-Session-Id"
	}
	return header
}

// NormalizeAndValidate 在功能开启时检查第三方中转配置。关闭时不改官方调度。
func (c *GatewayStrictSessionBindingConfig) NormalizeAndValidate() error {
	if c == nil {
		return nil
	}
	c.SessionHeader = strings.TrimSpace(c.SessionHeader)
	c.FallbackOrder = strings.TrimSpace(c.FallbackOrder)
	c.ThirdParty.BaseURL = strings.TrimSpace(c.ThirdParty.BaseURL)
	c.ThirdParty.APIKey = strings.TrimSpace(c.ThirdParty.APIKey)
	if c.FallbackOrder == "" {
		c.FallbackOrder = StrictFallbackOrderGroupFirst
	}
	if !c.Enabled {
		return nil
	}
	if c.SameAccountRetryLimit < -1 {
		return fmt.Errorf("same_account_retry_limit must be -1, 0, or positive")
	}
	switch c.FallbackOrder {
	case StrictFallbackOrderGroupFirst, StrictFallbackOrderThirdPartyFirst:
	default:
		return fmt.Errorf("fallback_order must be %s or %s", StrictFallbackOrderGroupFirst, StrictFallbackOrderThirdPartyFirst)
	}
	if c.FallbackGroupID < 0 {
		return fmt.Errorf("fallback_group_id must be >= 0")
	}
	if c.SessionHeader != "" {
		if err := validateOptionalHTTPHeaderName(c.SessionHeader); err != nil {
			return fmt.Errorf("session_header: %w", err)
		}
	}
	if !c.ThirdParty.Enabled {
		return nil
	}
	if c.ThirdParty.APIKey == "" {
		return fmt.Errorf("third_party.api_key is required when third_party.enabled")
	}
	if c.ThirdParty.TimeoutSeconds < 0 {
		return fmt.Errorf("third_party.timeout_seconds must be >= 0")
	}
	u, err := url.Parse(c.ThirdParty.BaseURL)
	if err != nil || u == nil || !u.IsAbs() || strings.TrimSpace(u.Host) == "" {
		return fmt.Errorf("third_party.base_url must be an absolute http(s) url")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("third_party.base_url scheme must be http or https")
	}
	if u.User != nil {
		return fmt.Errorf("third_party.base_url must not embed credentials")
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

func setStrictSessionBindingDefaults() {
	viper.SetDefault("gateway.strict_session_binding.enabled", false)
	viper.SetDefault("gateway.strict_session_binding.session_header", "X-Session-Id")
	viper.SetDefault("gateway.strict_session_binding.same_account_retry_limit", -1)
	viper.SetDefault("gateway.strict_session_binding.fallback_order", StrictFallbackOrderGroupFirst)
	viper.SetDefault("gateway.strict_session_binding.fallback_group_id", 0)
	viper.SetDefault("gateway.strict_session_binding.third_party.enabled", false)
	viper.SetDefault("gateway.strict_session_binding.third_party.base_url", "")
	viper.SetDefault("gateway.strict_session_binding.third_party.api_key", "")
	viper.SetDefault("gateway.strict_session_binding.third_party.timeout_seconds", 0)
}
