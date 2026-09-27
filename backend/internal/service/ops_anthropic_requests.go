package service

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/anthropicaudit"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

type AnthropicRequestFilter struct {
	AccountID          int64
	StartTime, EndTime time.Time
	Page, PageSize     int
	OnlyMismatch       bool
}
type AnthropicRequestSummary struct {
	Attempts             int64 `json:"attempts"`
	CorrelatedRequests   int64 `json:"correlated_requests"`
	UncorrelatedAttempts int64 `json:"uncorrelated_attempts"`
	Matched              int64 `json:"matched"`
	Mismatched           int64 `json:"mismatched"`
	Unknown              int64 `json:"unknown"`
	HTTPFailures         int64 `json:"http_failures"`
	TransportErrors      int64 `json:"transport_errors"`
	IdentityVariants     int64 `json:"identity_variants"`
	ParameterVariants    int64 `json:"parameter_variants"`
}
type AnthropicRequestRecord struct {
	ID              int64                   `json:"id"`
	CreatedAt       time.Time               `json:"created_at"`
	RequestID       string                  `json:"request_id"`
	ClientRequestID string                  `json:"client_request_id"`
	Audit           anthropicaudit.Snapshot `json:"audit"`
}
type AnthropicIdentityVariant struct {
	Signature  string    `json:"signature"`
	Count      int64     `json:"count"`
	UserAgent  string    `json:"user_agent"`
	Entrypoint string    `json:"entrypoint"`
	Version    string    `json:"version"`
	DeviceHash string    `json:"device_hash"`
	FirstSeen  time.Time `json:"first_seen"`
	LastSeen   time.Time `json:"last_seen"`
}
type AnthropicRequestList struct {
	Summary           AnthropicRequestSummary    `json:"summary"`
	Variants          []AnthropicIdentityVariant `json:"variants"`
	Records           []AnthropicRequestRecord   `json:"records"`
	Total             int64                      `json:"total"`
	Page              int                        `json:"page"`
	PageSize          int                        `json:"page_size"`
	AccountID         int64                      `json:"account_id"`
	StartTime         time.Time                  `json:"start_time"`
	EndTime           time.Time                  `json:"end_time"`
	CollectionEnabled bool                       `json:"collection_enabled"`
	SinkHealth        OpsSystemLogSinkHealth     `json:"sink_health"`
}

// Kept separate from OpsRepository so existing repository adapters can opt in.
type AnthropicRequestRepository interface {
	ListAnthropicRequests(context.Context, *AnthropicRequestFilter) (*AnthropicRequestList, error)
}

func (s *OpsService) ListAnthropicRequests(ctx context.Context, filter *AnthropicRequestFilter) (*AnthropicRequestList, error) {
	if err := s.RequireMonitoringEnabled(ctx); err != nil {
		return nil, err
	}
	if filter == nil || filter.AccountID <= 0 || filter.StartTime.IsZero() || !filter.StartTime.Before(filter.EndTime) || filter.EndTime.Sub(filter.StartTime) > 7*24*time.Hour {
		return nil, infraerrors.BadRequest("ANTHROPIC_AUDIT_INVALID_FILTER", "Choose an account and a time window of up to 7 days")
	}
	f := *filter
	if f.Page < 1 {
		f.Page = 1
	}
	if f.Page > 100000 {
		return nil, infraerrors.BadRequest("ANTHROPIC_AUDIT_INVALID_PAGE", "Page is too large")
	}
	if f.PageSize < 1 {
		f.PageSize = 50
	}
	if f.PageSize > 100 {
		f.PageSize = 100
	}
	repo, ok := s.opsRepo.(AnthropicRequestRepository)
	if !ok {
		return nil, infraerrors.ServiceUnavailable("ANTHROPIC_AUDIT_UNAVAILABLE", "Request monitoring repository unavailable")
	}
	// Limit database work independently of the HTTP server timeout.
	queryCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	result, err := repo.ListAnthropicRequests(queryCtx, &f)
	if err != nil {
		return nil, infraerrors.InternalServer("ANTHROPIC_AUDIT_QUERY_FAILED", "Failed to query outbound requests").WithCause(err)
	}
	result.Page = f.Page
	result.PageSize = f.PageSize
	result.AccountID = f.AccountID
	result.StartTime = f.StartTime
	result.EndTime = f.EndTime
	result.CollectionEnabled = anthropicaudit.EnabledForAccount(f.AccountID)
	result.SinkHealth = s.GetSystemLogSinkHealth()
	return result, nil
}
