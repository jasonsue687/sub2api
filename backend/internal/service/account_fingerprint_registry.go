package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

var (
	ErrFingerprintRecordNotFound = infraerrors.NotFound("FINGERPRINT_NOT_FOUND", "Fingerprint record not found")
	ErrFingerprintAccountInvalid = infraerrors.BadRequest("FINGERPRINT_ACCOUNT_INVALID", "Select an existing Anthropic OAuth subscription account")
)

type FingerprintAccount struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	FingerprintID *int64 `json:"fingerprint_id"`
}

type FingerprintRecord struct {
	ID              int64                `json:"id"`
	Key             string               `json:"-"`
	Source          string               `json:"source"`
	Fingerprint     Fingerprint          `json:"fingerprint"`
	IncomingHeaders map[string]string    `json:"incoming_headers"`
	ClientIDOrigin  string               `json:"client_id_origin"`
	SourceAccountID *int64               `json:"source_account_id"`
	RequestCount    int64                `json:"request_count"`
	FirstSeenAt     time.Time            `json:"first_seen_at"`
	LastSeenAt      time.Time            `json:"last_seen_at"`
	BoundAccounts   []FingerprintAccount `json:"bound_accounts"`
}

type FingerprintImportResult struct {
	Imported int `json:"imported"`
	Missing  int `json:"missing"`
	Failed   int `json:"failed"`
}

// This registry is deliberately separate from IdentityCache. No outbound
// identity selection method can read administrative bindings through it.
type AccountFingerprintStore interface {
	Observe(context.Context, *FingerprintRecord) error
	List(context.Context, int, int, string, string) ([]FingerprintRecord, int64, error)
	Get(context.Context, int64) (*FingerprintRecord, error)
	Accounts(context.Context, string, int, int) ([]FingerprintAccount, int64, error)
	Binding(context.Context, int64) (*FingerprintRecord, error)
	Bind(context.Context, int64, *int64) error
	ImportCache(context.Context) (*FingerprintImportResult, error)
}

// FingerprintRecordKey excludes observation timestamps and never includes a
// session, request ID, beta capabilities, credentials or message content.
func FingerprintRecordKey(value any) string {
	data, _ := json.Marshal(value)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// RecordIncomingFingerprint reuses the existing identity creation and metadata
// parsing logic. It records a snapshot before cache merging or mimic. Generated
// IDs are excluded from the dedup key; ON CONFLICT preserves the first ID.
func (s *IdentityService) RecordIncomingFingerprint(ctx context.Context, headers http.Header, metadataUserID string) {
	if s == nil || s.registry == nil {
		return
	}
	incoming := make(map[string]string)
	boundedHeaders := make(http.Header)
	for _, key := range []string{"User-Agent", "X-Stainless-Lang", "X-Stainless-Package-Version", "X-Stainless-OS", "X-Stainless-Arch", "X-Stainless-Runtime", "X-Stainless-Runtime-Version"} {
		value := headers.Get(key)
		// Keep the side channel bounded even for malformed authenticated clients.
		if len(value) > 1024 {
			value = value[:1024]
		}
		if value != "" {
			incoming[strings.ToLower(key)] = value
			boundedHeaders.Set(key, value)
		}
	}
	deviceID := ""
	if len(metadataUserID) <= 2048 {
		if parsed := ParseMetadataUserID(metadataUserID); parsed != nil && len(parsed.DeviceID) <= 256 {
			deviceID = parsed.DeviceID
		}
	}
	fp := s.createNewFingerprintFromHeaders(boundedHeaders)
	fp.UpdatedAt = 0
	origin := "generated"
	if deviceID != "" {
		fp.ClientID = deviceID
		origin = "client"
	}
	keyFingerprint := *fp
	keyFingerprint.ClientID = deviceID
	record := &FingerprintRecord{
		Source: "request", Fingerprint: *fp, IncomingHeaders: incoming,
		ClientIDOrigin: origin, RequestCount: 1,
		Key: FingerprintRecordKey(struct {
			Source      string
			Headers     map[string]string
			Fingerprint Fingerprint
		}{"request", incoming, keyFingerprint}),
	}
	// Registration must never change admission, cache updates or forwarding.
	observeCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	if err := s.registry.Observe(observeCtx, record); err != nil {
		logger.LegacyPrintf("service.identity", "Fingerprint registration failed: %v", err)
	}
}

func (s *GatewayService) RecordIncomingFingerprint(ctx context.Context, headers http.Header, metadataUserID string) {
	if s != nil {
		s.identityService.RecordIncomingFingerprint(ctx, headers, metadataUserID)
	}
}
