package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/anthropicaudit"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

type AnthropicSessionFilter struct {
	AccountID    int64
	StartTime    time.Time
	EndTime      time.Time
	Page         int
	PageSize     int
	OnlyMulti    bool
	OnlyFailed   bool
	OnlyMismatch bool
	Model        string
	Query        string
	QueryLike    string
	QueryID      int64
}

type AnthropicSessionSummary struct {
	InboundRequests  int64 `json:"inbound_requests"`
	OutboundAttempts int64 `json:"outbound_attempts"`
	MultiAttempts    int64 `json:"multi_attempts"`
	FailedRequests   int64 `json:"failed_requests"`
}

type AnthropicAccountOption struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type AnthropicSessionRow struct {
	ClientRequestID string    `json:"client_request_id"`
	RequestID       string    `json:"request_id"`
	CreatedAt       time.Time `json:"created_at"`
	APIKeyID        int64     `json:"api_key_id"`
	APIKeyName      string    `json:"api_key_name"`
	Username        string    `json:"username"`
	UserID          int64     `json:"user_id"`
	Endpoint        string    `json:"endpoint"`
	InboundModel    string    `json:"inbound_model"`
	OutboundModel   string    `json:"outbound_model"`
	Stream          bool      `json:"stream"`
	Status          int       `json:"status"`
	AccountID       int64     `json:"account_id"`
	AccountName     string    `json:"account_name"`
	AttemptCount    int       `json:"attempt_count"`
	DurationMS      int64     `json:"duration_ms"`
	HasInbound      bool      `json:"has_inbound"`
	HasOutbound     bool      `json:"has_outbound"`
	ErrorClass      string    `json:"error_class"`
	Consistency     string    `json:"consistency"`
	Truncated       bool      `json:"truncated"`
}

type AnthropicSessionList struct {
	Summary        AnthropicSessionSummary     `json:"summary"`
	Records        []AnthropicSessionRow       `json:"records"`
	Total          int64                       `json:"total"`
	Page           int                         `json:"page"`
	PageSize       int                         `json:"page_size"`
	Accounts       []AnthropicAccountOption    `json:"accounts"`
	Models         []string                    `json:"models"`
	StartTime      time.Time                   `json:"start_time"`
	EndTime        time.Time                   `json:"end_time"`
	InboundEnabled bool                        `json:"inbound_enabled"`
	CaptureHealth  anthropicaudit.CaptureStats `json:"capture_health"`
}

type AnthropicCapture struct {
	Direction          string          `json:"direction"`
	ClientRequestID    string          `json:"client_request_id"`
	RequestID          string          `json:"request_id"`
	AccountID          int64           `json:"account_id"`
	AccountName        string          `json:"account_name"`
	UserID             int64           `json:"user_id"`
	APIKeyID           int64           `json:"api_key_id"`
	APIKeyName         string          `json:"api_key_name"`
	Username           string          `json:"username"`
	Endpoint           string          `json:"endpoint"`
	ClientPath         string          `json:"client_path"`
	Model              string          `json:"model"`
	Stream             *bool           `json:"stream,omitempty"`
	AttemptSeq         int             `json:"attempt_seq"`
	RetryReason        string          `json:"retry_reason"`
	AccountSwitchCount int             `json:"account_switch_count"`
	AttemptCount       int             `json:"attempt_count"`
	Status             int             `json:"status"`
	ErrorClass         string          `json:"error_class"`
	UpstreamRequestID  string          `json:"upstream_request_id"`
	DurationMS         int64           `json:"duration_ms"`
	HeadersMS          int64           `json:"headers_ms"`
	Headers            json.RawMessage `json:"headers,omitempty"`
	Body               json.RawMessage `json:"body,omitempty"`
	BodyState          string          `json:"body_state"`
	Summary            json.RawMessage `json:"summary,omitempty"`
	Consistency        string          `json:"consistency"`
	Truncated          bool            `json:"truncated"`
	OriginalBytes      int             `json:"original_bytes"`
	CreatedAt          time.Time       `json:"created_at"`
}

type AnthropicSessionDetail struct {
	Session  AnthropicSessionRow `json:"session"`
	Inbound  *AnthropicCapture   `json:"inbound"`
	Attempts []AnthropicCapture  `json:"attempts"`
}

type AnthropicSessionRepository interface {
	ListAnthropicSessions(context.Context, *AnthropicSessionFilter) (*AnthropicSessionList, error)
	GetAnthropicSession(context.Context, string, time.Time) (*AnthropicSessionDetail, error)
}

func (s *OpsService) ListAnthropicSessions(ctx context.Context, filter *AnthropicSessionFilter) (*AnthropicSessionList, error) {
	if err := s.RequireMonitoringEnabled(ctx); err != nil {
		return nil, err
	}
	normalized, err := normalizeSessionFilter(filter)
	if err != nil {
		return nil, err
	}
	repo, ok := s.opsRepo.(AnthropicSessionRepository)
	if !ok {
		return nil, infraerrors.ServiceUnavailable("ANTHROPIC_AUDIT_UNAVAILABLE", "Request monitoring repository unavailable")
	}
	queryCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	result, err := repo.ListAnthropicSessions(queryCtx, normalized)
	if err != nil {
		return nil, infraerrors.InternalServer("ANTHROPIC_AUDIT_QUERY_FAILED", "Failed to query Anthropic requests").WithCause(err)
	}
	result.Page = normalized.Page
	result.PageSize = normalized.PageSize
	result.StartTime = normalized.StartTime
	result.EndTime = normalized.EndTime
	result.InboundEnabled = anthropicaudit.InboundEnabled()
	result.CaptureHealth = anthropicaudit.Stats()
	return result, nil
}

func (s *OpsService) GetAnthropicSession(ctx context.Context, clientRequestID string) (*AnthropicSessionDetail, error) {
	if err := s.RequireMonitoringEnabled(ctx); err != nil {
		return nil, err
	}
	clientRequestID = strings.TrimSpace(clientRequestID)
	if clientRequestID == "" {
		return nil, infraerrors.BadRequest("ANTHROPIC_AUDIT_INVALID_FILTER", "client_request_id is required")
	}
	repo, ok := s.opsRepo.(AnthropicSessionRepository)
	if !ok {
		return nil, infraerrors.ServiceUnavailable("ANTHROPIC_AUDIT_UNAVAILABLE", "Request monitoring repository unavailable")
	}
	queryCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	since := time.Now().UTC().Add(-anthropicaudit.RetentionDays * 24 * time.Hour)
	detail, err := repo.GetAnthropicSession(queryCtx, clientRequestID, since)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, infraerrors.NotFound("ANTHROPIC_AUDIT_NOT_FOUND", "Request not found")
		}
		return nil, infraerrors.InternalServer("ANTHROPIC_AUDIT_QUERY_FAILED", "Failed to query Anthropic request").WithCause(err)
	}
	if detail.Attempts == nil {
		detail.Attempts = []AnthropicCapture{}
	}
	detail.Session = sessionRowFromDetail(detail)
	return detail, nil
}

func normalizeSessionFilter(filter *AnthropicSessionFilter) (*AnthropicSessionFilter, error) {
	if filter == nil || filter.StartTime.IsZero() || !filter.StartTime.Before(filter.EndTime) || filter.EndTime.Sub(filter.StartTime) > anthropicaudit.RetentionDays*24*time.Hour {
		return nil, infraerrors.BadRequest("ANTHROPIC_AUDIT_INVALID_FILTER", "Choose a time window of up to 30 days")
	}
	next := *filter
	next.Model = strings.TrimSpace(next.Model)
	next.Query = strings.TrimSpace(next.Query)
	next.QueryLike = likeContains(next.Query)
	if id, err := strconv.ParseInt(next.Query, 10, 64); err == nil && id > 0 {
		next.QueryID = id
	}
	if next.Page < 1 {
		next.Page = 1
	}
	if next.Page > 100000 {
		return nil, infraerrors.BadRequest("ANTHROPIC_AUDIT_INVALID_PAGE", "Page is too large")
	}
	if next.PageSize < 1 {
		next.PageSize = 50
	}
	if next.PageSize > 100 {
		next.PageSize = 100
	}
	return &next, nil
}

func likeContains(value string) string {
	if value == "" {
		return ""
	}
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + replacer.Replace(value) + "%"
}

func sessionRowFromDetail(detail *AnthropicSessionDetail) AnthropicSessionRow {
	row := AnthropicSessionRow{ClientRequestID: "", HasOutbound: len(detail.Attempts) > 0, AttemptCount: len(detail.Attempts)}
	if detail.Inbound != nil {
		row = rowFromCapture(detail.Inbound)
		row.HasInbound = true
		row.InboundModel = detail.Inbound.Model
		row.AttemptCount = detail.Inbound.AttemptCount
		if len(detail.Attempts) > row.AttemptCount {
			row.AttemptCount = len(detail.Attempts)
		}
		row.HasOutbound = len(detail.Attempts) > 0
	}
	chosen := chooseAttempt(detail.Attempts)
	if chosen != nil {
		row.OutboundModel = chosen.Model
		row.AccountID = chosen.AccountID
		row.AccountName = chosen.AccountName
		if detail.Inbound == nil {
			row = rowFromCapture(chosen)
			row.HasInbound = false
			row.HasOutbound = true
			row.InboundModel = ""
			row.OutboundModel = chosen.Model
			row.AttemptCount = len(detail.Attempts)
			if chosen.ClientPath != "" {
				row.Endpoint = chosen.ClientPath
			}
		}
	}
	if row.ClientRequestID == "" && len(detail.Attempts) > 0 {
		row.ClientRequestID = detail.Attempts[0].ClientRequestID
	}
	// The session check describes all recorded outbound attempts, never inbound.
	// Missing captures cannot establish that every outbound attempt matched.
	row.Consistency = "matched"
	if len(detail.Attempts) == 0 || row.AttemptCount > len(detail.Attempts) {
		row.Consistency = "unknown"
	}
	for _, attempt := range detail.Attempts {
		if attempt.Consistency == "mismatch" {
			row.Consistency = "mismatch"
			break
		}
		if attempt.Consistency != "matched" {
			row.Consistency = "unknown"
		}
	}
	return row
}

func chooseAttempt(attempts []AnthropicCapture) *AnthropicCapture {
	var chosen *AnthropicCapture
	for i := range attempts {
		item := &attempts[i]
		if chosen == nil {
			chosen = item
			continue
		}
		itemOK := item.Status >= 200 && item.Status < 300
		chosenOK := chosen.Status >= 200 && chosen.Status < 300
		if itemOK && !chosenOK {
			chosen = item
			continue
		}
		if itemOK == chosenOK && item.AttemptSeq >= chosen.AttemptSeq {
			chosen = item
		}
	}
	return chosen
}

func rowFromCapture(item *AnthropicCapture) AnthropicSessionRow {
	stream := false
	if item.Stream != nil {
		stream = *item.Stream
	}
	endpoint := item.Endpoint
	if item.Direction == anthropicaudit.DirectionInbound && item.ClientPath != "" {
		endpoint = item.ClientPath
	}
	return AnthropicSessionRow{
		ClientRequestID: item.ClientRequestID,
		RequestID:       item.RequestID,
		CreatedAt:       item.CreatedAt,
		APIKeyID:        item.APIKeyID,
		APIKeyName:      item.APIKeyName,
		Username:        item.Username,
		UserID:          item.UserID,
		Endpoint:        endpoint,
		Stream:          stream,
		Status:          item.Status,
		AccountID:       item.AccountID,
		AccountName:     item.AccountName,
		DurationMS:      item.DurationMS,
		ErrorClass:      item.ErrorClass,
		Consistency:     item.Consistency,
		Truncated:       item.Truncated,
	}
}
