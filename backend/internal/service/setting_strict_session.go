package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	SettingKeyStrictSessionBindingEnabled        = "strict_session_binding_enabled"
	SettingKeyStrictSessionSessionHeader         = "strict_session_session_header"
	SettingKeyStrictSessionSameAccountRetryLimit = "strict_session_same_account_retry_limit"
)

// strictSessionBindingState 挂在 SettingService 上，请求热路径只读这份缓存。
type strictSessionBindingState struct {
	strictSessionBindingMu    sync.Mutex
	strictSessionBindingCache atomic.Value // *cachedStrictSessionBinding
}

// StrictSessionBindingSettings 是管理页保存后的严格会话配置。
type StrictSessionBindingSettings struct {
	strictSessionBindingLoaded         bool // distinguishes a loaded/resolved config from a sparse settings update
	StrictSessionBindingEnabled        bool
	StrictSessionSessionHeader         string
	StrictSessionSameAccountRetryLimit int
}

// HasStrictSessionBindingSettings reports whether this settings object carries
// a loaded/resolved policy, rather than omitted fields in a sparse admin update.
func (settings *SystemSettings) HasStrictSessionBindingSettings() bool {
	return settings != nil && settings.strictSessionBindingLoaded
}

const strictSessionBindingCacheTTL = 30 * time.Second

type cachedStrictSessionBinding struct {
	err       error
	cfg       config.GatewayStrictSessionBindingConfig
	expiresAt int64
}

func (s *SettingService) applyStrictSessionBindingUpdates(ctx context.Context, settings *SystemSettings, updates map[string]string) error {
	if settings == nil || !settings.strictSessionBindingLoaded {
		return nil
	}
	cfg := settings.strictSessionBindingConfig()
	if err := cfg.NormalizeAndValidate(); err != nil {
		return infraerrors.BadRequest("INVALID_STRICT_SESSION_BINDING", err.Error())
	}
	applyResolvedStrictSessionBinding(settings, cfg)
	for key, value := range strictSessionBindingUpdates(settings) {
		updates[key] = value
	}
	return nil
}

func applyStrictSessionBindingSettings(result *SystemSettings, settings map[string]string) {
	if result == nil {
		return
	}
	result.strictSessionBindingLoaded = true
	result.StrictSessionBindingEnabled = config.DefaultStrictSessionBindingConfig().Enabled
	if raw, ok := settings[SettingKeyStrictSessionBindingEnabled]; ok {
		result.StrictSessionBindingEnabled = raw == "true"
	}
	result.StrictSessionSessionHeader = strings.TrimSpace(settings[SettingKeyStrictSessionSessionHeader])
	if result.StrictSessionSessionHeader == "" {
		result.StrictSessionSessionHeader = "X-Session-Id"
	}
	result.StrictSessionSameAccountRetryLimit = parseStrictSessionRetryLimit(settings[SettingKeyStrictSessionSameAccountRetryLimit])
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

func (settings *SystemSettings) strictSessionBindingConfig() config.GatewayStrictSessionBindingConfig {
	if settings == nil {
		return config.DefaultStrictSessionBindingConfig()
	}
	return config.GatewayStrictSessionBindingConfig{
		Enabled:               settings.StrictSessionBindingEnabled,
		SessionHeader:         settings.StrictSessionSessionHeader,
		SameAccountRetryLimit: settings.StrictSessionSameAccountRetryLimit,
	}
}

var ErrStrictSessionConfigUnavailable = errors.New("strict session configuration unavailable")

// parseStrictSessionBindingConfig distinguishes absent keys from malformed values.
// Never turn an unreadable policy into a disabled policy or a partially defaulted one.
func parseStrictSessionBindingConfig(stored map[string]string) (config.GatewayStrictSessionBindingConfig, error) {
	for _, key := range []string{SettingKeyStrictSessionBindingEnabled} {
		if raw, ok := stored[key]; ok && raw != "true" && raw != "false" {
			return config.GatewayStrictSessionBindingConfig{}, fmt.Errorf("invalid boolean setting %s", key)
		}
	}
	for _, key := range []string{SettingKeyStrictSessionSameAccountRetryLimit} {
		if raw, ok := stored[key]; ok {
			value, err := strconv.ParseInt(raw, 10, 64)
			minimum := int64(0)
			if key == SettingKeyStrictSessionSameAccountRetryLimit {
				minimum = -1
			}
			if err != nil || value < minimum {
				return config.GatewayStrictSessionBindingConfig{}, fmt.Errorf("invalid integer setting %s", key)
			}
		}
	}
	parsed := &SystemSettings{}
	applyStrictSessionBindingSettings(parsed, stored)
	cfg := parsed.strictSessionBindingConfig()
	if err := cfg.NormalizeAndValidate(); err != nil {
		return config.GatewayStrictSessionBindingConfig{}, err
	}
	return cfg, nil
}

// StrictSessionBindingConfig retains the entire last known good policy on refresh
// failure. A cold process without a successful read must not forward Messages.
func (s *SettingService) StrictSessionBindingConfig(ctx context.Context) (config.GatewayStrictSessionBindingConfig, error) {
	if s == nil || s.settingRepo == nil {
		return config.GatewayStrictSessionBindingConfig{}, ErrStrictSessionConfigUnavailable
	}
	if cached, ok := s.strictSessionBindingCache.Load().(*cachedStrictSessionBinding); ok && cached.expiresAt > time.Now().UnixNano() {
		return cached.cfg, cached.err
	}
	s.strictSessionBindingMu.Lock()
	defer s.strictSessionBindingMu.Unlock()
	cached, _ := s.strictSessionBindingCache.Load().(*cachedStrictSessionBinding)
	if cached != nil && cached.expiresAt > time.Now().UnixNano() {
		return cached.cfg, cached.err
	}
	dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	stored, err := s.settingRepo.GetAll(dbCtx)
	var cfg config.GatewayStrictSessionBindingConfig
	if err == nil {
		cfg, err = parseStrictSessionBindingConfig(stored)
	}
	if err != nil {
		slog.Warn("strict_session.config_refresh_failed", "error", err, "using_last_good", cached != nil && cached.err == nil)
		next := &cachedStrictSessionBinding{err: ErrStrictSessionConfigUnavailable, expiresAt: time.Now().Add(5 * time.Second).UnixNano()}
		if cached != nil && cached.err == nil {
			next.cfg, next.err = cached.cfg, nil
		}
		s.strictSessionBindingCache.Store(next)
		return next.cfg, next.err
	}
	s.storeStrictSessionBindingConfig(cfg)
	return cfg, nil
}

// Caller holds strictSessionBindingMu, serializing refresh with persistence.
func (s *SettingService) storeStrictSessionBindingConfig(cfg config.GatewayStrictSessionBindingConfig) {
	s.strictSessionBindingCache.Store(&cachedStrictSessionBinding{cfg: cfg, expiresAt: time.Now().Add(strictSessionBindingCacheTTL).UnixNano()})
}

// Publish only the policy actually committed by SetMultiple. An unrelated or
// failed settings save cannot replace the trusted policy with request zero values.
func (s *SettingService) persistSystemSettings(ctx context.Context, updates map[string]string) error {
	touched, complete := false, true
	for _, key := range strictSessionSettingKeys {
		_, present := updates[key]
		touched = touched || present
		complete = complete && present
	}
	if !touched {
		return s.settingRepo.SetMultiple(ctx, updates)
	}
	s.strictSessionBindingMu.Lock()
	defer s.strictSessionBindingMu.Unlock()
	stored := updates
	if !complete {
		var err error
		stored, err = s.settingRepo.GetAll(ctx)
		if err != nil {
			return err
		}
		if stored == nil {
			stored = map[string]string{}
		}
		for key, value := range updates {
			stored[key] = value
		}
	}
	cfg, err := parseStrictSessionBindingConfig(stored)
	if err != nil {
		return err
	}
	if err := s.settingRepo.SetMultiple(ctx, updates); err != nil {
		return err
	}
	s.storeStrictSessionBindingConfig(cfg)
	return nil
}

// StrictSessionBindingAdminView 是管理页看到的生效配置。
type StrictSessionBindingAdminView struct {
	Enabled               bool
	SessionHeader         string
	SameAccountRetryLimit int
}

func (s *SettingService) StrictSessionBindingAdminView(stored *SystemSettings) StrictSessionBindingAdminView {
	cfg := config.DefaultStrictSessionBindingConfig()
	if stored != nil && stored.strictSessionBindingLoaded {
		cfg = stored.strictSessionBindingConfig()
	}
	if strings.TrimSpace(cfg.SessionHeader) == "" {
		cfg.SessionHeader = "X-Session-Id"
	}
	return StrictSessionBindingAdminView{
		Enabled:               cfg.Enabled,
		SessionHeader:         cfg.SessionHeader,
		SameAccountRetryLimit: cfg.SameAccountRetryLimit,
	}
}

func strictSessionBindingUpdateMap(settings *SystemSettings) map[string]string {
	if settings == nil {
		return nil
	}
	header := strings.TrimSpace(settings.StrictSessionSessionHeader)
	if header == "" {
		header = "X-Session-Id"
	}
	updates := map[string]string{
		SettingKeyStrictSessionBindingEnabled:        strconv.FormatBool(settings.StrictSessionBindingEnabled),
		SettingKeyStrictSessionSessionHeader:         header,
		SettingKeyStrictSessionSameAccountRetryLimit: strconv.Itoa(settings.StrictSessionSameAccountRetryLimit),
	}
	return updates
}

func strictSessionBindingUpdates(settings *SystemSettings) map[string]string {
	if settings == nil || !settings.strictSessionBindingLoaded {
		return nil
	}
	return strictSessionBindingUpdateMap(settings)
}

// StrictSessionBindingPatch 只包含本次请求真正提交的严格会话字段。
type StrictSessionBindingPatch struct {
	Enabled               *bool
	SessionHeader         *string
	SameAccountRetryLimit *int
}

// ResolveStrictSessionBindingSave 把本次提交合并到当前生效配置上。
func (s *SettingService) ResolveStrictSessionBindingSave(ctx context.Context, previous *SystemSettings, patch StrictSessionBindingPatch) (config.GatewayStrictSessionBindingConfig, error) {
	cfg := config.DefaultStrictSessionBindingConfig()
	if previous != nil && previous.strictSessionBindingLoaded {
		cfg = previous.strictSessionBindingConfig()
	}
	if patch.Enabled != nil {
		cfg.Enabled = *patch.Enabled
	}
	if patch.SessionHeader != nil {
		cfg.SessionHeader = *patch.SessionHeader
	}
	if patch.SameAccountRetryLimit != nil {
		cfg.SameAccountRetryLimit = *patch.SameAccountRetryLimit
	}
	if err := cfg.NormalizeAndValidate(); err != nil {
		return config.GatewayStrictSessionBindingConfig{}, infraerrors.BadRequest("INVALID_STRICT_SESSION_BINDING", err.Error())
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
	settings.strictSessionBindingLoaded = true
	settings.StrictSessionBindingEnabled = cfg.Enabled
	settings.StrictSessionSessionHeader = cfg.SessionHeader
	settings.StrictSessionSameAccountRetryLimit = cfg.SameAccountRetryLimit
}

var strictSessionSettingKeys = []string{
	SettingKeyStrictSessionBindingEnabled,
	SettingKeyStrictSessionSessionHeader,
	SettingKeyStrictSessionSameAccountRetryLimit,
}

// KeepStrictSessionBindingKeys 让本次保存写出完整的严格会话配置，而不是被省略字段清掉。
func KeepStrictSessionBindingKeys(omitted OmittedSettingKeys) {
	for _, key := range strictSessionSettingKeys {
		delete(omitted, key)
	}
}
