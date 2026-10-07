package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fingerprintObservationStore struct {
	AccountFingerprintStore
	observations []*FingerprintRecord
	err          error
}

func (s *fingerprintObservationStore) Observe(_ context.Context, record *FingerprintRecord) error {
	s.observations = append(s.observations, record)
	return s.err
}

func TestFingerprintRegistryReusesCreationWithoutChangingCachedIdentity(t *testing.T) {
	cache := &stubIdentityCache{fingerprint: &Fingerprint{
		ClientID: "existing-device", UserAgent: claude.DefaultUserAgent(), StainlessOS: "Linux", UpdatedAt: time.Now().Unix(),
	}}
	store := &fingerprintObservationStore{}
	s := NewIdentityService(cache)
	s.registry = store
	headers := http.Header{"User-Agent": {"claude-cli/2.0.1 (external, local-agent)"}, "X-Stainless-Os": {"Windows"}, "Authorization": {"not-to-be-recorded"}}
	metadata := FormatMetadataUserID(strings.Repeat("a", 64), "not-to-be-recorded-account", "11111111-1111-4111-8111-111111111111", "2.1.100")
	s.RecordIncomingFingerprint(context.Background(), headers, metadata)
	require.Len(t, store.observations, 1)
	got := store.observations[0]
	want := s.createFingerprintFromHeaders(headers)
	want.ClientID = strings.Repeat("a", 64)
	assert.Equal(t, *want, got.Fingerprint)
	assert.Equal(t, "client", got.ClientIDOrigin)
	assert.Equal(t, headers.Get("User-Agent"), got.IncomingHeaders["user-agent"])
	assert.NotContains(t, got.IncomingHeaders, "authorization")
	assert.Zero(t, cache.setCalls)
	assert.Equal(t, "existing-device", cache.fingerprint.ClientID)
	assert.Equal(t, "Linux", cache.fingerprint.StainlessOS)
	// Changing session/account metadata cannot create another fingerprint.
	otherSession := FormatMetadataUserID(strings.Repeat("a", 64), "other-account", "22222222-2222-4222-8222-222222222222", "2.1.100")
	s.RecordIncomingFingerprint(context.Background(), headers, otherSession)
	assert.Equal(t, got.Key, store.observations[1].Key)
	// A real change in incoming headers must be registered even if the cache
	// refuses to downgrade its existing higher-version identity.
	headers.Set("X-Stainless-OS", "macOS")
	s.RecordIncomingFingerprint(context.Background(), headers, metadata)
	assert.NotEqual(t, got.Key, store.observations[2].Key)
}

func TestFingerprintRegistryGeneratedDeviceDoesNotSplitIdenticalRequests(t *testing.T) {
	s := NewIdentityService(&stubIdentityCache{})
	store := &fingerprintObservationStore{}
	s.registry = store
	for i := 0; i < 2; i++ {
		s.RecordIncomingFingerprint(context.Background(), http.Header{}, "")
	}
	require.Len(t, store.observations, 2)
	assert.Equal(t, store.observations[0].Key, store.observations[1].Key)
	assert.Len(t, store.observations[0].Fingerprint.ClientID, 64)
	assert.Equal(t, "generated", store.observations[0].ClientIDOrigin)
}

func TestFingerprintRegistryFailureDoesNotChangeLegacySelectionOrUpgrade(t *testing.T) {
	headers := http.Header{"User-Agent": {"claude-cli/3.0.0 (external, local-agent)"}, "X-Stainless-Os": {"Windows"}}
	original := Fingerprint{ClientID: "cached-device", UserAgent: claude.DefaultUserAgent(), StainlessOS: "Linux", UpdatedAt: time.Now().Unix()}
	withRegistry, withoutRegistry := &stubIdentityCache{fingerprint: &original}, &stubIdentityCache{fingerprint: &original}
	s := NewIdentityService(withRegistry)
	s.registry = &fingerprintObservationStore{err: errors.New("database unavailable")}
	s.RecordIncomingFingerprint(context.Background(), headers, "")
	got, err := s.GetOrCreateFingerprint(context.Background(), 14, headers)
	require.NoError(t, err)
	want, err := NewIdentityService(withoutRegistry).GetOrCreateFingerprint(context.Background(), 14, headers)
	require.NoError(t, err)
	got.UpdatedAt, want.UpdatedAt = 0, 0
	assert.Equal(t, want, got)
	assert.Equal(t, "cached-device", got.ClientID)
	assert.Equal(t, withoutRegistry.setCalls, withRegistry.setCalls)
}

func TestFingerprintRegistryDoesNotParticipateInOutboundIdentitySelection(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, endpoint := range []string{"messages", "count_tokens"} {
		for _, mimic := range []bool{false, true} {
			t.Run(endpoint+map[bool]string{false: "/normal", true: "/mimic"}[mimic], func(t *testing.T) {
				resetGatewayForwardingSettingsCacheForTest(t)
				original := Fingerprint{ClientID: strings.Repeat("b", 64), UserAgent: claude.DefaultUserAgent(), StainlessOS: "Linux", StainlessArch: "x64", UpdatedAt: time.Now().Unix()}
				var bodies [][]byte
				var headers []http.Header
				for _, registered := range []bool{false, true} {
					identity := NewIdentityService(&stubIdentityCache{fingerprint: &original})
					if registered {
						// The embedded store interface is nil: any attempt to look up
						// a binding from the forwarding path would panic this test.
						identity.registry = &fingerprintObservationStore{}
					}
					svc := &GatewayService{cfg: &config.Config{}, identityService: identity}
					c, _ := gin.CreateTestContext(httptest.NewRecorder())
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/"+endpoint, nil)
					c.Request.Header.Set("User-Agent", "claude-cli/2.0.0 (external, local-agent)")
					c.Request.Header.Set("X-Stainless-OS", "Windows")
					identity.RecordIncomingFingerprint(context.Background(), c.Request.Header, "")
					account := &Account{ID: 14, Platform: PlatformAnthropic, Type: AccountTypeOAuth}
					body := []byte(`{"model":"claude-haiku-4-5","messages":[{"role":"user","content":"test"}]}`)
					var req *http.Request
					var wireBody []byte
					var err error
					if endpoint == "messages" {
						req, wireBody, err = svc.buildUpstreamRequest(context.Background(), c, account, body, "test-token", "oauth", "claude-haiku-4-5", false, mimic)
					} else {
						req, wireBody, err = svc.buildCountTokensRequest(context.Background(), c, account, body, "test-token", "oauth", "claude-haiku-4-5", mimic)
					}
					require.NoError(t, err)
					bodies = append(bodies, wireBody)
					headers = append(headers, req.Header)
				}
				assert.Equal(t, bodies[0], bodies[1])
				for _, key := range []string{"User-Agent", "X-Stainless-Lang", "X-Stainless-Package-Version", "X-Stainless-OS", "X-Stainless-Arch", "X-Stainless-Runtime", "X-Stainless-Runtime-Version", "anthropic-beta"} {
					assert.Equal(t, getHeaderRaw(headers[0], key), getHeaderRaw(headers[1], key), key)
				}
			})
		}
	}
}
