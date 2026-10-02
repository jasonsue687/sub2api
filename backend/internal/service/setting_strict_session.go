package service

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const strictSessionBindingCacheTTL = 30 * time.Second

type cachedStrictSessionBinding struct {
	override  bool
	cfg       config.GatewayStrictSessionBindingConfig
	expiresAt int64
}

func applyStrictSessionBindingSettings(result *SystemSettings, settings map[string]string) {
	if result == nil {
		return
	}
	_, overridePresent := settings[SettingKeyStrictSessionBindingOverride]
	result.StrictSessionBindingOverride = overridePresent && settings[SettingKeyStrictSessionBindingOverride] == "true"
	result.StrictSessionBindingEnabled = settings[SettingKeyStrictSessionBindingEnabled] == "true"
	result.StrictSessionEndUserHeader = strings.TrimSpace(settings[SettingKeyStrictSessionEndUserHeader])
	result.StrictSessionEndUserHeaderTrusted = settings[SettingKeyStrictSessionEndUserHeaderTrusted] == "true"
	result.StrictSessionSessionHeader = strings.TrimSpace(settings[SettingKeyStrictSessionSessionHeader])
	if result.StrictSessionSessionHeader == "" {
		result.StrictSessionSessionHeader = "X-Session-Id"
	}
	result.StrictSessionSameAccountRetryLimit = parseStrictSessionRetryLimit(settings[SettingKeyStrictSessionSameAccountRetryLimit])
	result.StrictSessionFallbackOrder = strings.TrimSpace(settings[SettingKeyStrictSessionFallbackOrder])
	if result.StrictSessionFallbackOrder == "" {
		result.StrictSessionFallbackOrder = config.StrictFallbackOrderGroupFirst
	}
	result.StrictSessionFallbackGroupID = parseStrictSessionGroupID(settings[SettingKeyStrictSessionFallbackGroupID])
	result.StrictSessionThirdPartyEnabled = settings[SettingKeyStrictSessionThirdPartyEnabled] == "true"
	result.StrictSessionThirdPartyBaseURL = strings.TrimSpace(settings[SettingKeyStrictSessionThirdPartyBaseURL])
	result.StrictSessionThirdPartyAPIKey = settings[SettingKeyStrictSessionThirdPartyAPIKey]
	result.StrictSessionThirdPartyAPIKeyConfigured = strings.TrimSpace(result.StrictSessionThirdPartyAPIKey) != ""
	result.StrictSessionThirdPartyTimeoutSeconds = parseStrictSessionNonNegative(settings[SettingKeyStrictSessionThirdPartyTimeoutSeconds])
}

func parseStrictSessionRetryLimit(raw string) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return -1
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return -1
	}
	return value
}

func parseStrictSessionGroupID(raw string) int64 {
	value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || value < 0 {
		return 0
	}
	return value
}

func parseStrictSessionNonNegative(raw string) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value < 0 {
		return 0
	}
	return value
}

func (settings *SystemSettings) strictSessionBindingConfig() config.GatewayStrictSessionBindingConfig {
	if settings == nil {
		return config.GatewayStrictSessionBindingConfig{FallbackOrder: config.StrictFallbackOrderGroupFirst, SameAccountRetryLimit: -1}
	}
	return config.GatewayStrictSessionBindingConfig{
		Enabled:               settings.StrictSessionBindingEnabled,
		EndUserHeader:         settings.StrictSessionEndUserHeader,
		EndUserHeaderTrusted:  settings.StrictSessionEndUserHeaderTrusted,
		SessionHeader:         settings.StrictSessionSessionHeader,
		SameAccountRetryLimit: settings.StrictSessionSameAccountRetryLimit,
		FallbackOrder:         settings.StrictSessionFallbackOrder,
		FallbackGroupID:       settings.StrictSessionFallbackGroupID,
		ThirdParty: config.GatewayStrictThirdPartyConfig{
			Enabled:        settings.StrictSessionThirdPartyEnabled,
			BaseURL:        settings.StrictSessionThirdPartyBaseURL,
			APIKey:         settings.StrictSessionThirdPartyAPIKey,
			TimeoutSeconds: settings.StrictSessionThirdPartyTimeoutSeconds,
		},
	}
}

// StrictSessionBindingOverride 在管理员保存过后返回数据库配置。
// 读失败时返回 false，调用方继续使用进程启动时的 yaml / 环境变量。
func (s *SettingService) StrictSessionBindingOverride(ctx context.Context) (config.GatewayStrictSessionBindingConfig, bool) {
	if s == nil {
		return config.GatewayStrictSessionBindingConfig{}, false
	}
	if cached, ok := s.strictSessionBindingCache.Load().(*cachedStrictSessionBinding); ok && cached != nil && cached.expiresAt > time.Now().UnixNano() {
		if !cached.override {
			return config.GatewayStrictSessionBindingConfig{}, false
		}
		return cached.cfg, true
	}
	if s.settingRepo == nil {
		return config.GatewayStrictSessionBindingConfig{}, false
	}
	dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	stored, err := s.settingRepo.GetAll(dbCtx)
	if err != nil {
		return config.GatewayStrictSessionBindingConfig{}, false
	}
	parsed := &SystemSettings{}
	applyStrictSessionBindingSettings(parsed, stored)
	cfg := parsed.strictSessionBindingConfig()
	s.strictSessionBindingCache.Store(&cachedStrictSessionBinding{
		override:  parsed.StrictSessionBindingOverride,
		cfg:       cfg,
		expiresAt: time.Now().Add(strictSessionBindingCacheTTL).UnixNano(),
	})
	if !parsed.StrictSessionBindingOverride {
		return config.GatewayStrictSessionBindingConfig{}, false
	}
	return cfg, true
}

func (s *SettingService) storeStrictSessionBindingCache(settings *SystemSettings) {
	if s == nil || settings == nil {
		return
	}
	s.strictSessionBindingCache.Store(&cachedStrictSessionBinding{
		override:  settings.StrictSessionBindingOverride,
		cfg:       settings.strictSessionBindingConfig(),
		expiresAt: time.Now().Add(strictSessionBindingCacheTTL).UnixNano(),
	})
}

// StrictSessionBindingAdminView 是管理页看到的生效配置。密钥只报告是否已配置。
type StrictSessionBindingAdminView struct {
	Source                   string
	Enabled                  bool
	EndUserHeader            string
	EndUserHeaderTrusted     bool
	SessionHeader            string
	SameAccountRetryLimit    int
	FallbackOrder            string
	FallbackGroupID          int64
	ThirdPartyEnabled        bool
	ThirdPartyBaseURL        string
	ThirdPartyKeyConfigured  bool
	ThirdPartyTimeoutSeconds int
}

func (s *SettingService) StrictSessionBindingAdminView(stored *SystemSettings) StrictSessionBindingAdminView {
	cfg := config.GatewayStrictSessionBindingConfig{FallbackOrder: config.StrictFallbackOrderGroupFirst, SameAccountRetryLimit: -1, SessionHeader: "X-Session-Id"}
	source := "config"
	if stored != nil && stored.StrictSessionBindingOverride {
		cfg = stored.strictSessionBindingConfig()
		source = "database"
	} else if s != nil && s.cfg != nil {
		cfg = s.cfg.Gateway.StrictSessionBinding
	}
	if strings.TrimSpace(cfg.FallbackOrder) == "" {
		cfg.FallbackOrder = config.StrictFallbackOrderGroupFirst
	}
	if strings.TrimSpace(cfg.SessionHeader) == "" {
		cfg.SessionHeader = "X-Session-Id"
	}
	return StrictSessionBindingAdminView{
		Source:                   source,
		Enabled:                  cfg.Enabled,
		EndUserHeader:            cfg.EndUserHeader,
		EndUserHeaderTrusted:     cfg.EndUserHeaderTrusted,
		SessionHeader:            cfg.SessionHeader,
		SameAccountRetryLimit:    cfg.SameAccountRetryLimit,
		FallbackOrder:            cfg.FallbackOrder,
		FallbackGroupID:          cfg.FallbackGroupID,
		ThirdPartyEnabled:        cfg.ThirdParty.Enabled,
		ThirdPartyBaseURL:        cfg.ThirdParty.BaseURL,
		ThirdPartyKeyConfigured:  strings.TrimSpace(cfg.ThirdParty.APIKey) != "",
		ThirdPartyTimeoutSeconds: cfg.ThirdParty.TimeoutSeconds,
	}
}

// ValidateStrictFallbackGroup 确认兜底分组存在，并且能承接 Claude Messages。
func (s *SettingService) ValidateStrictFallbackGroup(ctx context.Context, groupID int64) error {
	if groupID == 0 {
		return nil
	}
	if groupID < 0 {
		return infraerrors.BadRequest("INVALID_STRICT_SESSION_FALLBACK_GROUP", "strict_session_fallback_group_id must be >= 0")
	}
	if s == nil || s.defaultSubGroupReader == nil {
		return nil
	}
	group, err := s.defaultSubGroupReader.GetByID(ctx, groupID)
	if err != nil || group == nil {
		return infraerrors.BadRequest("INVALID_STRICT_SESSION_FALLBACK_GROUP", "strict_session_fallback_group_id does not exist")
	}
	if !group.IsActive() {
		return infraerrors.BadRequest("INVALID_STRICT_SESSION_FALLBACK_GROUP", "strict_session_fallback_group_id must be an active group")
	}
	switch group.Platform {
	case PlatformAnthropic, PlatformAntigravity:
		return nil
	default:
		return infraerrors.BadRequest("INVALID_STRICT_SESSION_FALLBACK_GROUP", "strict_session_fallback_group_id must be an anthropic or antigravity group")
	}
}

func strictSessionBindingUpdateMap(settings *SystemSettings) map[string]string {
	if settings == nil {
		return nil
	}
	order := strings.TrimSpace(settings.StrictSessionFallbackOrder)
	if order == "" {
		order = config.StrictFallbackOrderGroupFirst
	}
	header := strings.TrimSpace(settings.StrictSessionSessionHeader)
	if header == "" {
		header = "X-Session-Id"
	}
	updates := map[string]string{
		SettingKeyStrictSessionBindingOverride:          strconv.FormatBool(settings.StrictSessionBindingOverride),
		SettingKeyStrictSessionBindingEnabled:           strconv.FormatBool(settings.StrictSessionBindingEnabled),
		SettingKeyStrictSessionEndUserHeader:            strings.TrimSpace(settings.StrictSessionEndUserHeader),
		SettingKeyStrictSessionEndUserHeaderTrusted:     strconv.FormatBool(settings.StrictSessionEndUserHeaderTrusted),
		SettingKeyStrictSessionSessionHeader:            header,
		SettingKeyStrictSessionSameAccountRetryLimit:    strconv.Itoa(settings.StrictSessionSameAccountRetryLimit),
		SettingKeyStrictSessionFallbackOrder:            order,
		SettingKeyStrictSessionFallbackGroupID:          strconv.FormatInt(settings.StrictSessionFallbackGroupID, 10),
		SettingKeyStrictSessionThirdPartyEnabled:        strconv.FormatBool(settings.StrictSessionThirdPartyEnabled),
		SettingKeyStrictSessionThirdPartyBaseURL:        strings.TrimSpace(settings.StrictSessionThirdPartyBaseURL),
		SettingKeyStrictSessionThirdPartyTimeoutSeconds: strconv.Itoa(settings.StrictSessionThirdPartyTimeoutSeconds),
	}
	if strings.TrimSpace(settings.StrictSessionThirdPartyAPIKey) != "" {
		updates[SettingKeyStrictSessionThirdPartyAPIKey] = settings.StrictSessionThirdPartyAPIKey
	}
	return updates
}

func strictSessionBindingUpdates(settings *SystemSettings) map[string]string {
	if settings == nil || !settings.StrictSessionBindingOverride {
		return nil
	}
	return strictSessionBindingUpdateMap(settings)
}

// StrictSessionBindingPatch 只包含本次请求真正提交的严格会话字段。
// 空的第三方密钥表示保留已有密钥，不会清空。
type StrictSessionBindingPatch struct {
	Enabled                  *bool
	EndUserHeader            *string
	EndUserHeaderTrusted     *bool
	SessionHeader            *string
	SameAccountRetryLimit    *int
	FallbackOrder            *string
	FallbackGroupID          *int64
	ThirdPartyEnabled        *bool
	ThirdPartyBaseURL        *string
	ThirdPartyAPIKey         *string
	ThirdPartyTimeoutSeconds *int
}

// ResolveStrictSessionBindingSave 把本次提交合并到当前生效配置上。
// 空密钥保留已保存或 yaml 中的密钥。调用方提交任一字段后，数据库成为生效来源。
func (s *SettingService) ResolveStrictSessionBindingSave(ctx context.Context, previous *SystemSettings, patch StrictSessionBindingPatch) (config.GatewayStrictSessionBindingConfig, error) {
	cfg := config.GatewayStrictSessionBindingConfig{
		FallbackOrder:         config.StrictFallbackOrderGroupFirst,
		SameAccountRetryLimit: -1,
		SessionHeader:         "X-Session-Id",
	}
	if previous != nil && previous.StrictSessionBindingOverride {
		cfg = previous.strictSessionBindingConfig()
	} else if s != nil && s.cfg != nil {
		cfg = s.cfg.Gateway.StrictSessionBinding
	}
	if patch.Enabled != nil {
		cfg.Enabled = *patch.Enabled
	}
	if patch.EndUserHeader != nil {
		cfg.EndUserHeader = *patch.EndUserHeader
	}
	if patch.EndUserHeaderTrusted != nil {
		cfg.EndUserHeaderTrusted = *patch.EndUserHeaderTrusted
	}
	if patch.SessionHeader != nil {
		cfg.SessionHeader = *patch.SessionHeader
	}
	if patch.SameAccountRetryLimit != nil {
		cfg.SameAccountRetryLimit = *patch.SameAccountRetryLimit
	}
	if patch.FallbackOrder != nil {
		cfg.FallbackOrder = *patch.FallbackOrder
	}
	if patch.FallbackGroupID != nil {
		cfg.FallbackGroupID = *patch.FallbackGroupID
	}
	if patch.ThirdPartyEnabled != nil {
		cfg.ThirdParty.Enabled = *patch.ThirdPartyEnabled
	}
	if patch.ThirdPartyBaseURL != nil {
		cfg.ThirdParty.BaseURL = *patch.ThirdPartyBaseURL
	}
	if patch.ThirdPartyAPIKey != nil && strings.TrimSpace(*patch.ThirdPartyAPIKey) != "" {
		cfg.ThirdParty.APIKey = strings.TrimSpace(*patch.ThirdPartyAPIKey)
	}
	if patch.ThirdPartyTimeoutSeconds != nil {
		cfg.ThirdParty.TimeoutSeconds = *patch.ThirdPartyTimeoutSeconds
	}
	if err := cfg.NormalizeAndValidate(); err != nil {
		return config.GatewayStrictSessionBindingConfig{}, infraerrors.BadRequest("INVALID_STRICT_SESSION_BINDING", err.Error())
	}
	if err := s.ValidateStrictFallbackGroup(ctx, cfg.FallbackGroupID); err != nil {
		return config.GatewayStrictSessionBindingConfig{}, err
	}
	return cfg, nil
}

// ApplyResolvedStrictSessionBinding 把校验后的配置写进即将落库的设置对象。
func ApplyResolvedStrictSessionBinding(settings *SystemSettings, cfg config.GatewayStrictSessionBindingConfig) {
	applyResolvedStrictSessionBinding(settings, cfg)
}

func applyResolvedStrictSessionBinding(settings *SystemSettings, cfg config.GatewayStrictSessionBindingConfig) {
	if settings == nil {
		return
	}
	settings.StrictSessionBindingOverride = true
	settings.StrictSessionBindingEnabled = cfg.Enabled
	settings.StrictSessionEndUserHeader = cfg.EndUserHeader
	settings.StrictSessionEndUserHeaderTrusted = cfg.EndUserHeaderTrusted
	settings.StrictSessionSessionHeader = cfg.SessionHeader
	settings.StrictSessionSameAccountRetryLimit = cfg.SameAccountRetryLimit
	settings.StrictSessionFallbackOrder = cfg.FallbackOrder
	settings.StrictSessionFallbackGroupID = cfg.FallbackGroupID
	settings.StrictSessionThirdPartyEnabled = cfg.ThirdParty.Enabled
	settings.StrictSessionThirdPartyBaseURL = cfg.ThirdParty.BaseURL
	settings.StrictSessionThirdPartyAPIKey = cfg.ThirdParty.APIKey
	settings.StrictSessionThirdPartyAPIKeyConfigured = strings.TrimSpace(cfg.ThirdParty.APIKey) != ""
	settings.StrictSessionThirdPartyTimeoutSeconds = cfg.ThirdParty.TimeoutSeconds
}

var strictSessionSettingKeys = []string{
	SettingKeyStrictSessionBindingOverride,
	SettingKeyStrictSessionBindingEnabled,
	SettingKeyStrictSessionEndUserHeader,
	SettingKeyStrictSessionEndUserHeaderTrusted,
	SettingKeyStrictSessionSessionHeader,
	SettingKeyStrictSessionSameAccountRetryLimit,
	SettingKeyStrictSessionFallbackOrder,
	SettingKeyStrictSessionFallbackGroupID,
	SettingKeyStrictSessionThirdPartyEnabled,
	SettingKeyStrictSessionThirdPartyBaseURL,
	SettingKeyStrictSessionThirdPartyAPIKey,
	SettingKeyStrictSessionThirdPartyTimeoutSeconds,
}

// KeepStrictSessionBindingKeys 让本次保存写出完整的严格会话配置，而不是被省略字段清掉。
func KeepStrictSessionBindingKeys(omitted OmittedSettingKeys) {
	for _, key := range strictSessionSettingKeys {
		delete(omitted, key)
	}
}
