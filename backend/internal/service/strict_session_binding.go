package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

const (
	// StrictSessionProtocol 是 Claude Messages 入站协议维度。
	// 绑定键使用它，而不是最终选中的账号平台，避免混合调度把同一会话拆成新绑定。
	StrictSessionProtocol = "anthropic"

	strictSessionFingerprintBytes = 6
)

var (
	// ErrStrictBindingNotFound 表示数据库确认没有这行绑定。读失败不得返回它。
	ErrStrictBindingNotFound = errors.New("strict session binding not found")
	// ErrStrictSessionStore 表示绑定存储不可用。调用方不得因此重新选号。
	ErrStrictSessionStore = errors.New("strict session binding store unavailable")
	// ErrStrictSessionIDRequired 表示严格模式缺少稳定会话 ID。
	ErrStrictSessionIDRequired = errors.New("strict session id required")
	// ErrStrictEndUserRequired 表示配置了终端用户头但请求没有提供。
	ErrStrictEndUserRequired = errors.New("strict session end user required")
)

// StrictSessionBinding 是会话到原订阅账号的持久绑定。
// 不保存正文、凭据或完整会话 ID。
type StrictSessionBinding struct {
	ID                 int64
	BindingKey         string
	SessionFingerprint string
	AccountID          int64
	Protocol           string
	APIKeyID           int64
	GroupID            *int64
	EndUserFingerprint string
	CreatedAt          time.Time
}

// StrictSessionBindingStore 是绑定的持久事实来源。
// Redis 只能包在实现外面做加速，不能单独充当这个接口。
type StrictSessionBindingStore interface {
	Get(ctx context.Context, bindingKey string) (*StrictSessionBinding, error)
	// Create 原子写入。键已存在时必须返回已存在的那一行，不得改写 account_id。
	Create(ctx context.Context, binding *StrictSessionBinding) (*StrictSessionBinding, error)
}

// StrictSessionFallbackError 表示原账号不能承接，且禁止改选其他订阅账号。
type StrictSessionFallbackError struct {
	AccountID int64
	Reason    string
}

func (e *StrictSessionFallbackError) Error() string {
	if e == nil {
		return "strict session fallback"
	}
	return fmt.Sprintf("strict session bound account %d unavailable: %s", e.AccountID, e.Reason)
}

// StrictSessionIdentityInput 是从已鉴权请求提取的稳定身份，不含正文摘要。
type StrictSessionIdentityInput struct {
	APIKeyID            int64
	GroupID             *int64
	RequestPlatform     string
	MetadataUserID      string
	ClaudeCodeSessionID string
	SessionHeaderValue  string
	EndUserHeaderValue  string
}

// StrictSessionPlan 是一次 Claude Messages 请求的严格绑定计划。
// SessionID 只用于和既有粘性键对齐，日志必须使用 SessionFingerprint。
type StrictSessionPlan struct {
	Active                bool
	BindingKey            string
	SessionFingerprint    string
	EndUserFingerprint    string
	SessionID             string
	APIKeyID              int64
	GroupID               *int64
	Protocol              string
	RequestPlatform       string
	SameAccountRetryLimit int
	BoundAccountID        int64
}

// StrictSessionSelector 把官方选号、单账号承接检查和持久绑定拆开，便于单独测试。
type StrictSessionSelector struct {
	Store          StrictSessionBindingStore
	OfficialSelect func(ctx context.Context) (*AccountSelectionResult, error)
	LoadAccount    func(ctx context.Context, accountID int64) (*Account, error)
	BlockReason    func(ctx context.Context, account *Account) (string, bool)
	Acquire        func(ctx context.Context, account *Account) (*AccountSelectionResult, error)
	ReleaseSession func(ctx context.Context, account *Account)
}

// StrictBindingCache 是可选加速层。失败或未命中都必须回源数据库。
type StrictBindingCache interface {
	Get(ctx context.Context, bindingKey string) (accountID int64, ok bool, err error)
	Set(ctx context.Context, bindingKey string, accountID int64) error
}

type cachedStrictSessionBindingStore struct {
	persistent StrictSessionBindingStore
	cache      StrictBindingCache
}

// NewCachedStrictSessionBindingStore 用 Redis 加速数据库绑定。
// 缓存清空、TTL 或 Redis 故障都不会被当成“未绑定”。
func NewCachedStrictSessionBindingStore(persistent StrictSessionBindingStore, cache StrictBindingCache) StrictSessionBindingStore {
	return &cachedStrictSessionBindingStore{persistent: persistent, cache: cache}
}

func (s *cachedStrictSessionBindingStore) Get(ctx context.Context, bindingKey string) (*StrictSessionBinding, error) {
	if s == nil || s.persistent == nil {
		return nil, fmt.Errorf("%w: persistent store is not configured", ErrStrictSessionStore)
	}
	if s.cache != nil {
		accountID, ok, err := s.cache.Get(ctx, bindingKey)
		if err != nil {
			slog.Warn("strict_session.cache_read_failed", "session_fp", shortBindingKey(bindingKey), "error", err)
		} else if ok && accountID > 0 {
			return &StrictSessionBinding{BindingKey: bindingKey, AccountID: accountID}, nil
		}
	}
	binding, err := s.persistent.Get(ctx, bindingKey)
	if err != nil {
		return nil, err
	}
	if s.cache != nil && binding != nil && binding.AccountID > 0 {
		if cacheErr := s.cache.Set(ctx, bindingKey, binding.AccountID); cacheErr != nil {
			slog.Warn("strict_session.cache_fill_failed", "account_id", binding.AccountID, "session_fp", binding.SessionFingerprint, "error", cacheErr)
		}
	}
	return binding, nil
}

func (s *cachedStrictSessionBindingStore) Create(ctx context.Context, binding *StrictSessionBinding) (*StrictSessionBinding, error) {
	if s == nil || s.persistent == nil {
		return nil, fmt.Errorf("%w: persistent store is not configured", ErrStrictSessionStore)
	}
	winner, err := s.persistent.Create(ctx, binding)
	if err != nil {
		return nil, err
	}
	if s.cache != nil && winner != nil && winner.AccountID > 0 {
		if cacheErr := s.cache.Set(ctx, winner.BindingKey, winner.AccountID); cacheErr != nil {
			slog.Warn("strict_session.cache_write_failed", "account_id", winner.AccountID, "session_fp", winner.SessionFingerprint, "error", cacheErr)
		}
	}
	return winner, nil
}

// MemoryStrictSessionBindingStore 是进程内持久实现，测试和单测用。
// 它没有 TTL；调用方可以清空外层缓存而不影响这里的行。
type MemoryStrictSessionBindingStore struct {
	mu   sync.Mutex
	rows map[string]*StrictSessionBinding
	// GetErr / CreateErr 模拟存储故障。设置后不得被当成未绑定。
	GetErr    error
	CreateErr error
}

func NewMemoryStrictSessionBindingStore() *MemoryStrictSessionBindingStore {
	return &MemoryStrictSessionBindingStore{rows: map[string]*StrictSessionBinding{}}
}

func (s *MemoryStrictSessionBindingStore) Get(_ context.Context, bindingKey string) (*StrictSessionBinding, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.GetErr != nil {
		return nil, s.GetErr
	}
	row, ok := s.rows[bindingKey]
	if !ok {
		return nil, ErrStrictBindingNotFound
	}
	cloned := *row
	return &cloned, nil
}

func (s *MemoryStrictSessionBindingStore) Create(_ context.Context, binding *StrictSessionBinding) (*StrictSessionBinding, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.CreateErr != nil {
		return nil, s.CreateErr
	}
	if binding == nil || binding.BindingKey == "" || binding.AccountID <= 0 {
		return nil, fmt.Errorf("%w: invalid binding", ErrStrictSessionStore)
	}
	if existing, ok := s.rows[binding.BindingKey]; ok {
		cloned := *existing
		return &cloned, nil
	}
	cloned := *binding
	if cloned.CreatedAt.IsZero() {
		cloned.CreatedAt = time.Now().UTC()
	}
	s.rows[binding.BindingKey] = &cloned
	out := cloned
	return &out, nil
}

// ResolveStrictSessionPlan 只根据配置和稳定会话身份生成计划。
// 内容摘要、消息哈希不在输入里，因此不能变成永久会话键。
func ResolveStrictSessionPlan(cfg *config.Config, in StrictSessionIdentityInput) (*StrictSessionPlan, error) {
	if cfg == nil || !cfg.Gateway.StrictSessionBinding.Enabled {
		return &StrictSessionPlan{}, nil
	}
	bindingCfg := cfg.Gateway.StrictSessionBinding
	if strings.TrimSpace(bindingCfg.EndUserHeader) != "" && sanitizeSessionID(in.EndUserHeaderValue) == "" {
		return nil, ErrStrictEndUserRequired
	}
	sessionID, err := resolveStrictSessionID(in)
	if err != nil {
		return nil, err
	}
	endUser := ""
	endUserFingerprint := ""
	if strings.TrimSpace(bindingCfg.EndUserHeader) != "" {
		endUser = sanitizeSessionID(in.EndUserHeaderValue)
		endUserFingerprint = StrictSessionFingerprint(endUser)
	}
	plan := &StrictSessionPlan{
		Active:                true,
		BindingKey:            strictBindingKey(in.APIKeyID, StrictSessionProtocol, sessionID, endUser),
		SessionFingerprint:    StrictSessionFingerprint(sessionID),
		EndUserFingerprint:    endUserFingerprint,
		SessionID:             sessionID,
		APIKeyID:              in.APIKeyID,
		GroupID:               in.GroupID,
		Protocol:              StrictSessionProtocol,
		RequestPlatform:       in.RequestPlatform,
		SameAccountRetryLimit: bindingCfg.SameAccountRetryLimit,
	}
	return plan, nil
}

func resolveStrictSessionID(in StrictSessionIdentityInput) (string, error) {
	if parsed := ParseMetadataUserID(in.MetadataUserID); parsed != nil {
		if sessionID := sanitizeSessionID(parsed.SessionID); sessionID != "" {
			return sessionID, nil
		}
	}
	if sessionID := sanitizeSessionID(in.ClaudeCodeSessionID); sessionID != "" {
		return sessionID, nil
	}
	if sessionID := sanitizeSessionID(in.SessionHeaderValue); sessionID != "" {
		return sessionID, nil
	}
	return "", ErrStrictSessionIDRequired
}

func strictBindingKey(apiKeyID int64, protocol, sessionID, endUser string) string {
	endUserHash := "-"
	if endUser != "" {
		sum := sha256.Sum256([]byte(endUser))
		endUserHash = hex.EncodeToString(sum[:])
	}
	canonical := fmt.Sprintf("v1|protocol=%s|api_key=%d|end_user=%s|session=%s", protocol, apiKeyID, endUserHash, sessionID)
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])
}

// StrictSessionFingerprint 返回日志用的短指纹，不可反推会话 ID。
func StrictSessionFingerprint(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:strictSessionFingerprintBytes])
}

func shortBindingKey(bindingKey string) string {
	if len(bindingKey) <= 8 {
		return bindingKey
	}
	return bindingKey[:8]
}

// LogStrictSession 只记录原账号、脱敏会话、原因和最终链路。
func LogStrictSession(accountID int64, fingerprint, reason, path string) {
	slog.Info("strict_session.dispatch",
		"account_id", accountID,
		"session_fp", fingerprint,
		"reason", reason,
		"path", path,
	)
}

// ProvideGatewayService 构造网关服务并挂上永久会话绑定存储。
// 存储通过 setter 注入，避免改动 NewGatewayService 的大量测试调用点。
func ProvideGatewayService(
	accountRepo AccountRepository,
	groupRepo GroupRepository,
	usageLogRepo UsageLogRepository,
	usageBillingRepo UsageBillingRepository,
	userRepo UserRepository,
	userSubRepo UserSubscriptionRepository,
	userGroupRateRepo UserGroupRateRepository,
	cache GatewayCache,
	cfg *config.Config,
	schedulerSnapshot *SchedulerSnapshotService,
	concurrencyService *ConcurrencyService,
	billingService *BillingService,
	rateLimitService *RateLimitService,
	billingCacheService *BillingCacheService,
	identityService *IdentityService,
	httpUpstream HTTPUpstream,
	deferredService *DeferredService,
	claudeTokenProvider *ClaudeTokenProvider,
	sessionLimitCache SessionLimitCache,
	rpmCache RPMCache,
	digestStore *DigestSessionStore,
	settingService *SettingService,
	tlsFPProfileService *TLSFingerprintProfileService,
	channelService *ChannelService,
	resolver *ModelPricingResolver,
	compositeResolver *CompositeRouteResolver,
	balanceNotifyService *BalanceNotifyService,
	userPlatformQuotaRepo UserPlatformQuotaRepository,
	strictStore StrictSessionBindingStore,
) *GatewayService {
	svc := NewGatewayService(
		accountRepo,
		groupRepo,
		usageLogRepo,
		usageBillingRepo,
		userRepo,
		userSubRepo,
		userGroupRateRepo,
		cache,
		cfg,
		schedulerSnapshot,
		concurrencyService,
		billingService,
		rateLimitService,
		billingCacheService,
		identityService,
		httpUpstream,
		deferredService,
		claudeTokenProvider,
		sessionLimitCache,
		rpmCache,
		digestStore,
		settingService,
		tlsFPProfileService,
		channelService,
		resolver,
		compositeResolver,
		balanceNotifyService,
		userPlatformQuotaRepo,
	)
	svc.SetStrictSessionBindingStore(strictStore)
	return svc
}

func (s *GatewayService) SetStrictSessionBindingStore(store StrictSessionBindingStore) {
	if s == nil {
		return
	}
	s.strictSessionStore = store
}

// PrepareStrictSession 解析身份并读取已有绑定。存储读失败不会变成“新会话”。
func (s *GatewayService) PrepareStrictSession(ctx context.Context, in StrictSessionIdentityInput) (*StrictSessionPlan, error) {
	plan, err := ResolveStrictSessionPlan(s.cfg, in)
	if err != nil || plan == nil || !plan.Active {
		return plan, err
	}
	if s.strictSessionStore == nil {
		return nil, fmt.Errorf("%w: store is not configured", ErrStrictSessionStore)
	}
	binding, err := s.strictSessionStore.Get(ctx, plan.BindingKey)
	if err != nil && !errors.Is(err, ErrStrictBindingNotFound) {
		return nil, fmt.Errorf("%w: read: %w", ErrStrictSessionStore, err)
	}
	if binding != nil {
		plan.BoundAccountID = binding.AccountID
	}
	return plan, nil
}

// SelectStrictSessionAccount 在已有绑定时只检查原账号；首次分配复用官方调度并先落库。
func (s *GatewayService) SelectStrictSessionAccount(
	ctx context.Context,
	plan *StrictSessionPlan,
	groupID *int64,
	sessionHash string,
	requestedModel string,
	excludedIDs map[int64]struct{},
	metadataUserID string,
	sub2apiUserID int64,
) (*AccountSelectionResult, error) {
	if plan == nil || !plan.Active {
		return nil, fmt.Errorf("%w: strict session plan is inactive", ErrStrictSessionStore)
	}
	requestPlatform := plan.RequestPlatform
	selector := StrictSessionSelector{
		Store: s.strictSessionStore,
		OfficialSelect: func(ctx context.Context) (*AccountSelectionResult, error) {
			return s.SelectAccountWithLoadAwareness(ctx, groupID, sessionHash, requestedModel, excludedIDs, metadataUserID, sub2apiUserID)
		},
		LoadAccount: func(ctx context.Context, accountID int64) (*Account, error) {
			if s.accountRepo == nil {
				return nil, fmt.Errorf("%w: account repository unavailable", ErrStrictSessionStore)
			}
			return s.accountRepo.GetByID(ctx, accountID)
		},
		BlockReason: func(ctx context.Context, account *Account) (string, bool) {
			return s.strictAccountBlockReason(ctx, account, groupID, requestedModel, requestPlatform, sessionHash)
		},
		Acquire: func(ctx context.Context, account *Account) (*AccountSelectionResult, error) {
			return s.acquireStrictBoundAccount(ctx, account)
		},
		ReleaseSession: func(ctx context.Context, account *Account) {
			s.ReleaseAccountSession(ctx, account, sessionHash)
		},
	}
	return selector.Select(ctx, plan, groupID)
}

// Select 实现严格绑定选号。已绑定的会话不会调用 OfficialSelect。
func (s StrictSessionSelector) Select(ctx context.Context, plan *StrictSessionPlan, groupID *int64) (*AccountSelectionResult, error) {
	if s.Store == nil {
		return nil, fmt.Errorf("%w: store is not configured", ErrStrictSessionStore)
	}
	if plan == nil || plan.BindingKey == "" {
		return nil, fmt.Errorf("%w: missing binding key", ErrStrictSessionStore)
	}
	binding, err := s.Store.Get(ctx, plan.BindingKey)
	if err != nil && !errors.Is(err, ErrStrictBindingNotFound) {
		return nil, fmt.Errorf("%w: read: %w", ErrStrictSessionStore, err)
	}
	if binding != nil && binding.AccountID > 0 {
		LogStrictSession(binding.AccountID, plan.SessionFingerprint, "bound", "origin_check")
		return s.selectBound(ctx, binding.AccountID)
	}

	if s.OfficialSelect == nil {
		return nil, fmt.Errorf("%w: official selector unavailable", ErrStrictSessionStore)
	}
	selected, err := s.OfficialSelect(ctx)
	if err != nil {
		return nil, err
	}
	if selected == nil || selected.Account == nil || selected.Account.ID <= 0 {
		releaseSelection(selected)
		return nil, fmt.Errorf("%w: official selector returned no account", ErrStrictSessionStore)
	}
	created, err := s.Store.Create(ctx, &StrictSessionBinding{
		BindingKey:         plan.BindingKey,
		SessionFingerprint: plan.SessionFingerprint,
		AccountID:          selected.Account.ID,
		Protocol:           plan.Protocol,
		APIKeyID:           plan.APIKeyID,
		GroupID:            groupID,
		EndUserFingerprint: plan.EndUserFingerprint,
	})
	if err != nil {
		releaseSelection(selected)
		s.releaseSession(ctx, selected.Account)
		return nil, fmt.Errorf("%w: write: %w", ErrStrictSessionStore, err)
	}
	if created == nil || created.AccountID <= 0 {
		releaseSelection(selected)
		s.releaseSession(ctx, selected.Account)
		return nil, fmt.Errorf("%w: write returned an empty binding", ErrStrictSessionStore)
	}
	if created.AccountID != selected.Account.ID {
		releaseSelection(selected)
		s.releaseSession(ctx, selected.Account)
		LogStrictSession(created.AccountID, plan.SessionFingerprint, "concurrent_winner", "origin_check")
		return s.selectBound(ctx, created.AccountID)
	}
	LogStrictSession(created.AccountID, plan.SessionFingerprint, "assigned", "origin")
	return selected, nil
}

func (s StrictSessionSelector) selectBound(ctx context.Context, accountID int64) (*AccountSelectionResult, error) {
	if s.LoadAccount == nil {
		return nil, fmt.Errorf("%w: account loader unavailable", ErrStrictSessionStore)
	}
	account, err := s.LoadAccount(ctx, accountID)
	if err != nil {
		if errors.Is(err, ErrAccountNotFound) {
			return nil, &StrictSessionFallbackError{AccountID: accountID, Reason: "account_deleted"}
		}
		return nil, fmt.Errorf("%w: load account: %w", ErrStrictSessionStore, err)
	}
	if account == nil {
		return nil, &StrictSessionFallbackError{AccountID: accountID, Reason: "account_deleted"}
	}
	if s.BlockReason != nil {
		if reason, blocked := s.BlockReason(ctx, account); blocked {
			return nil, &StrictSessionFallbackError{AccountID: accountID, Reason: reason}
		}
	}
	if s.Acquire == nil {
		s.releaseSession(ctx, account)
		return nil, &StrictSessionFallbackError{AccountID: accountID, Reason: "concurrency_exhausted"}
	}
	selected, err := s.Acquire(ctx, account)
	if err != nil {
		s.releaseSession(ctx, account)
		var fallback *StrictSessionFallbackError
		if errors.As(err, &fallback) {
			return nil, fallback
		}
		return nil, &StrictSessionFallbackError{AccountID: accountID, Reason: "concurrency_exhausted"}
	}
	if selected == nil || selected.Account == nil {
		s.releaseSession(ctx, account)
		return nil, &StrictSessionFallbackError{AccountID: accountID, Reason: "concurrency_exhausted"}
	}
	return selected, nil
}

func (s StrictSessionSelector) releaseSession(ctx context.Context, account *Account) {
	if s.ReleaseSession != nil && account != nil {
		s.ReleaseSession(ctx, account)
	}
}

func releaseSelection(selected *AccountSelectionResult) {
	if selected != nil && selected.ReleaseFunc != nil {
		selected.ReleaseFunc()
	}
}

func (s *GatewayService) strictAccountBlockReason(ctx context.Context, account *Account, groupID *int64, requestedModel, requestPlatform, sessionHash string) (string, bool) {
	if account == nil {
		return "account_deleted", true
	}
	if !s.isAccountInGroup(account, groupID) {
		return "removed_from_pool", true
	}
	if !strictPlatformAllowed(account, requestPlatform) {
		return "platform_mismatch", true
	}
	if !account.IsActive() || !account.Schedulable {
		return "disabled", true
	}
	now := time.Now()
	if account.AutoPauseOnExpired && account.ExpiresAt != nil && !now.Before(*account.ExpiresAt) {
		return "authorization_invalid", true
	}
	if account.IsRateLimited() || account.GetRateLimitRemainingTimeWithContext(ctx, requestedModel) > 0 {
		return "rate_limited", true
	}
	if account.IsOverloaded() {
		return "overloaded", true
	}
	if account.TempUnschedulableUntil != nil && now.Before(*account.TempUnschedulableUntil) {
		return "temporarily_unschedulable", true
	}
	if !s.isAccountSchedulableForQuota(account) {
		return "quota_exceeded", true
	}
	if requestedModel != "" && !s.isModelSupportedByAccountWithContext(ctx, account, requestedModel) {
		return "model_unsupported", true
	}
	if !s.isAccountSchedulableForWindowCost(ctx, account, true) {
		return "window_cost_exhausted", true
	}
	if !s.isAccountSchedulableForRPM(ctx, account, true) {
		return "rpm_exceeded", true
	}
	if s.isAccountBlockedBySchedulingThreshold(ctx, account) {
		return "scheduling_threshold", true
	}
	if !account.IsSchedulable() {
		return "unschedulable", true
	}
	if !s.checkAndRegisterSession(ctx, account, sessionHash) {
		return "session_capacity", true
	}
	return "", false
}

func strictPlatformAllowed(account *Account, requestPlatform string) bool {
	if account == nil {
		return false
	}
	switch requestPlatform {
	case "", PlatformComposite:
		return account.Platform == PlatformAnthropic || account.Platform == PlatformAntigravity
	case PlatformAnthropic:
		return account.Platform == PlatformAnthropic || (account.Platform == PlatformAntigravity && account.IsMixedSchedulingEnabled())
	case PlatformAntigravity:
		return account.Platform == PlatformAntigravity || account.Platform == PlatformAnthropic
	default:
		return account.Platform == requestPlatform || account.Platform == PlatformAnthropic
	}
}

func (s *GatewayService) acquireStrictBoundAccount(ctx context.Context, account *Account) (*AccountSelectionResult, error) {
	if account == nil {
		return nil, &StrictSessionFallbackError{Reason: "account_deleted"}
	}
	result, err := s.tryAcquireAccountSlot(ctx, account.ID, account.Concurrency)
	acquired := err == nil && result != nil && result.Acquired
	if acquired {
		return s.newSelectionResult(ctx, account, true, result.ReleaseFunc, nil)
	}
	cfg := s.schedulingConfig()
	waiting := 0
	if s.concurrencyService != nil {
		waiting, _ = s.concurrencyService.GetAccountWaitingCount(ctx, account.ID)
	}
	if strictBoundWaitAllowed(acquired, waiting, cfg.StickySessionMaxWaiting) {
		return s.newSelectionResult(ctx, account, false, nil, &AccountWaitPlan{
			AccountID:      account.ID,
			MaxConcurrency: account.Concurrency,
			Timeout:        cfg.StickySessionWaitTimeout,
			MaxWaiting:     cfg.StickySessionMaxWaiting,
		})
	}
	return nil, &StrictSessionFallbackError{AccountID: account.ID, Reason: "concurrency_exhausted"}
}

// strictBoundWaitAllowed 只允许在原账号上有界等待，不允许换号。
func strictBoundWaitAllowed(slotAcquired bool, waiting, maxWaiting int) bool {
	if slotAcquired {
		return false
	}
	return waiting < maxWaiting
}
