package service

import (
	"context"
	"errors"
	"maps"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type strictSettingsRepo struct {
	SettingRepository
	mu                sync.Mutex
	values            map[string]string
	readErr, writeErr error
	reads             int
	readStarted       chan struct{}
	readRelease       chan struct{}
}

func (r *strictSettingsRepo) GetAll(context.Context) (map[string]string, error) {
	r.mu.Lock()
	r.reads++
	values, err, started, release := maps.Clone(r.values), r.readErr, r.readStarted, r.readRelease
	r.readStarted, r.readRelease = nil, nil
	r.mu.Unlock()
	if started != nil {
		close(started)
		<-release
	}
	return values, err
}
func (r *strictSettingsRepo) SetMultiple(_ context.Context, values map[string]string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.writeErr != nil {
		return r.writeErr
	}
	if r.values == nil {
		r.values = map[string]string{}
	}
	maps.Copy(r.values, values)
	return nil
}
func strictSettingsValues(cfg config.GatewayStrictSessionBindingConfig) map[string]string {
	settings := &SystemSettings{}
	ApplyResolvedStrictSessionBinding(settings, cfg)
	return strictSessionBindingUpdateMap(settings)
}
func expireStrictSettings(s *SettingService) {
	cached := *s.strictSessionBindingCache.Load().(*cachedStrictSessionBinding)
	cached.expiresAt = 0
	s.strictSessionBindingCache.Store(&cached)
}

func TestStrictSettingsDefaultsAndExplicitDisableSurviveRestart(t *testing.T) {
	ctx := context.Background()
	repo := &strictSettingsRepo{}
	s := NewSettingService(repo, nil)
	cfg, err := s.StrictSessionBindingConfig(ctx)
	require.NoError(t, err)
	require.Equal(t, config.DefaultStrictSessionBindingConfig(), cfg)
	require.Empty(t, repo.values, "a read must not persist defaults")
	_, err = s.StrictSessionBindingConfig(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, repo.reads, "requests within TTL reuse the cached policy")
	cfg.Enabled = false
	require.NoError(t, s.persistSystemSettings(ctx, strictSettingsValues(cfg)))
	restarted := NewSettingService(repo, nil)
	actual, err := restarted.StrictSessionBindingConfig(ctx)
	require.NoError(t, err)
	require.Equal(t, cfg, actual)
	require.False(t, actual.Enabled)
	require.NotContains(t, repo.values, "strict_session_binding_override")
}

func TestStrictSettingsRefreshFailureRetainsWholePolicy(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run(map[bool]string{true: "enabled", false: "disabled"}[enabled], func(t *testing.T) {
			cfg := config.DefaultStrictSessionBindingConfig()
			cfg.Enabled, cfg.SessionHeader, cfg.SameAccountRetryLimit = enabled, "X-Custom-Session", 3
			cfg.FallbackOrder, cfg.FallbackGroupID = config.StrictFallbackOrderThirdPartyFirst, 91
			cfg.ThirdParty = config.GatewayStrictThirdPartyConfig{Enabled: true, BaseURL: "https://relay.example", APIKey: "test-secret", TimeoutSeconds: 17}
			repo := &strictSettingsRepo{values: strictSettingsValues(cfg)}
			s := NewSettingService(repo, nil)
			before, err := s.StrictSessionBindingConfig(context.Background())
			require.NoError(t, err)
			expireStrictSettings(s)
			repo.readErr = errors.New("database unavailable")
			// Even if a partial result accompanies an error, none of it is trusted.
			repo.values = map[string]string{SettingKeyStrictSessionBindingEnabled: "false"}
			after, err := s.StrictSessionBindingConfig(context.Background())
			require.NoError(t, err)
			require.Equal(t, before, after)
			require.Equal(t, cfg, after)
			_, err = s.StrictSessionBindingConfig(context.Background())
			require.NoError(t, err)
			require.Equal(t, 2, repo.reads, "failed refreshes back off instead of hitting storage for every request")
			repo.readErr = nil
			expireStrictSettings(s)
			after, err = s.StrictSessionBindingConfig(context.Background())
			require.NoError(t, err)
			require.False(t, after.Enabled)
			require.Empty(t, after.ThirdParty.APIKey)
		})
	}
}

func TestStrictSettingsColdReadFailureAndRecovery(t *testing.T) {
	repo := &strictSettingsRepo{readErr: errors.New("database unavailable")}
	s := NewSettingService(repo, nil)
	_, err := s.StrictSessionBindingConfig(context.Background())
	require.ErrorIs(t, err, ErrStrictSessionConfigUnavailable)
	gw := &GatewayService{settingService: s}
	_, err = gw.PrepareStrictSession(context.Background(), StrictSessionIdentityInput{SessionHeaderValue: "session"})
	require.ErrorIs(t, err, ErrStrictSessionConfigUnavailable)
	require.Equal(t, 1, repo.reads)
	repo.readErr = nil
	expireStrictSettings(s)
	cfg, err := s.StrictSessionBindingConfig(context.Background())
	require.NoError(t, err)
	require.True(t, cfg.Enabled)
}

func TestStrictSettingsInvalidStoredValuesDoNotDisableBinding(t *testing.T) {
	for _, values := range []map[string]string{
		{SettingKeyStrictSessionBindingEnabled: "broken"},
		{SettingKeyStrictSessionSameAccountRetryLimit: "not-an-integer"},
		{SettingKeyStrictSessionFallbackGroupID: "-1"},
		{SettingKeyStrictSessionThirdPartyTimeoutSeconds: "-1"},
		{SettingKeyStrictSessionFallbackOrder: "unknown"},
		{SettingKeyStrictSessionThirdPartyEnabled: "true"},
	} {
		repo := &strictSettingsRepo{values: values}
		s := NewSettingService(repo, nil)
		_, err := s.StrictSessionBindingConfig(context.Background())
		require.ErrorIs(t, err, ErrStrictSessionConfigUnavailable)
	}
}

func TestStrictSettingsSaveFailureAndUnrelatedSaveKeepCache(t *testing.T) {
	ctx := context.Background()
	repo := &strictSettingsRepo{}
	s := NewSettingService(repo, nil)
	before, err := s.StrictSessionBindingConfig(ctx)
	require.NoError(t, err)
	disabled := before
	disabled.Enabled = false
	repo.writeErr = errors.New("write failed")
	require.Error(t, s.persistSystemSettings(ctx, strictSettingsValues(disabled)))
	after, err := s.StrictSessionBindingConfig(ctx)
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.Empty(t, repo.values)
	repo.writeErr = nil
	require.NoError(t, s.persistSystemSettings(ctx, map[string]string{"registration_enabled": "false"}))
	after, err = s.StrictSessionBindingConfig(ctx)
	require.NoError(t, err)
	require.Equal(t, before, after)
	// A successful write publishes immediately even if later reads would fail.
	require.NoError(t, s.persistSystemSettings(ctx, strictSettingsValues(disabled)))
	repo.readErr = errors.New("post-save read failed")
	after, err = s.StrictSessionBindingConfig(ctx)
	require.NoError(t, err)
	require.Equal(t, disabled, after)
}

func TestStrictSettingsRefreshCannotOverwriteSavedPolicy(t *testing.T) {
	ctx := context.Background()
	started, release := make(chan struct{}), make(chan struct{})
	repo := &strictSettingsRepo{readStarted: started, readRelease: release}
	s := NewSettingService(repo, nil)
	refreshed := make(chan error, 1)
	go func() { _, err := s.StrictSessionBindingConfig(ctx); refreshed <- err }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("refresh did not start")
	}
	next := config.DefaultStrictSessionBindingConfig()
	next.Enabled = false
	saved := make(chan error, 1)
	go func() { saved <- s.persistSystemSettings(ctx, strictSettingsValues(next)) }()
	close(release)
	require.NoError(t, <-refreshed)
	require.NoError(t, <-saved)
	actual, err := s.StrictSessionBindingConfig(ctx)
	require.NoError(t, err)
	require.Equal(t, next, actual)
}

func TestStrictSettingsRequestSnapshotSurvivesAdminSave(t *testing.T) {
	repo := &strictSettingsRepo{}
	s := NewSettingService(repo, nil)
	gw := &GatewayService{settingService: s, strictSessionStore: NewMemoryStrictSessionBindingStore()}
	before, err := gw.EffectiveStrictSessionBinding(context.Background())
	require.NoError(t, err)
	requestCtx := WithStrictSessionBindingConfig(context.Background(), before)
	next := before
	next.Enabled = false
	next.ThirdParty = config.GatewayStrictThirdPartyConfig{Enabled: true, BaseURL: "https://new.example", APIKey: "new-secret"}
	require.NoError(t, s.persistSystemSettings(context.Background(), strictSettingsValues(next)))
	actual, err := gw.EffectiveStrictSessionBinding(requestCtx)
	require.NoError(t, err)
	require.Equal(t, before, actual)
	input := StrictSessionIdentityInput{SessionHeaderValue: "stable-session"}
	current, err := gw.PrepareStrictSession(requestCtx, input)
	require.NoError(t, err)
	require.True(t, current.Active)
	later, err := gw.PrepareStrictSession(context.Background(), input)
	require.NoError(t, err)
	require.False(t, later.Active)
}
