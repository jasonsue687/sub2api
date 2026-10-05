package service

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/anthropicaudit"
	"github.com/stretchr/testify/assert"
)

func TestNormalizeSessionFilterAndChooseAttempt(t *testing.T) {
	start := time.Now().Add(-time.Hour)
	end := time.Now()
	filter, err := normalizeSessionFilter(&AnthropicSessionFilter{StartTime: start, EndTime: end, Query: `50%_id`, Page: 0, PageSize: 500})
	if err != nil {
		t.Fatal(err)
	}
	if filter.Page != 1 || filter.PageSize != 100 || filter.QueryLike != `%50\%\_id%` || filter.QueryID != 0 {
		t.Fatalf("%+v", filter)
	}
	filter, err = normalizeSessionFilter(&AnthropicSessionFilter{StartTime: start, EndTime: end, Query: "12"})
	if err != nil || filter.QueryID != 12 {
		t.Fatalf("%v %+v", err, filter)
	}
	if _, err = normalizeSessionFilter(&AnthropicSessionFilter{StartTime: start, EndTime: start.Add(31 * 24 * time.Hour)}); err == nil {
		t.Fatal("expected window rejection")
	}
	attempts := []AnthropicCapture{{AttemptSeq: 1, Status: 429, AccountID: 1}, {AttemptSeq: 2, Status: 529, AccountID: 2}, {AttemptSeq: 3, Status: 200, AccountID: 5}}
	if got := chooseAttempt(attempts); got == nil || got.AccountID != 5 {
		t.Fatalf("default success %+v", got)
	}
	failed := []AnthropicCapture{{AttemptSeq: 1, Status: 500, AccountID: 1}, {AttemptSeq: 2, Status: 400, AccountID: 2}}
	if got := chooseAttempt(failed); got == nil || got.AttemptSeq != 2 {
		t.Fatalf("last failure %+v", got)
	}
	detail := &AnthropicSessionDetail{Inbound: &AnthropicCapture{Direction: anthropicaudit.DirectionInbound, ClientRequestID: "c", Model: "in", Status: 200, AttemptCount: 0, Endpoint: "/v1/messages"}, Attempts: nil}
	row := sessionRowFromDetail(detail)
	if row.HasOutbound || row.HasInbound != true || row.Endpoint != "/v1/messages" {
		t.Fatalf("no outbound %+v", row)
	}
	detail = &AnthropicSessionDetail{Attempts: []AnthropicCapture{{Direction: anthropicaudit.DirectionOutbound, ClientRequestID: "c2", ClientPath: "/v1/chat/completions", Endpoint: "/v1/messages", Model: "mapped", Status: 200, AttemptSeq: 1}}}
	row = sessionRowFromDetail(detail)
	if row.HasInbound || row.Endpoint != "/v1/chat/completions" || row.OutboundModel != "mapped" {
		t.Fatalf("no inbound %+v", row)
	}
}

type sessionRepoStub struct{ *opsRepoMock }

func (s *sessionRepoStub) ListAnthropicSessions(context.Context, *AnthropicSessionFilter) (*AnthropicSessionList, error) {
	return &AnthropicSessionList{Records: []AnthropicSessionRow{{ClientRequestID: "c"}}, Total: 1}, nil
}

func (s *sessionRepoStub) GetAnthropicSession(context.Context, string, time.Time) (*AnthropicSessionDetail, error) {
	return nil, sql.ErrNoRows
}

func TestListAnthropicSessionsKeepsOldShapeSeparate(t *testing.T) {
	svc := &OpsService{opsRepo: &sessionRepoStub{opsRepoMock: &opsRepoMock{}}}
	start := time.Now().Add(-time.Hour)
	result, err := svc.ListAnthropicSessions(context.Background(), &AnthropicSessionFilter{StartTime: start, EndTime: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 1 || !result.InboundEnabled || result.CaptureHealth.QueueCapacity == 0 {
		t.Fatalf("%+v", result)
	}
	if _, err = svc.GetAnthropicSession(context.Background(), "missing"); err == nil {
		t.Fatal("expected not found")
	}
}

func TestSessionOutboundConsistencyExcludesInbound(t *testing.T) {
	for _, tc := range []struct {
		name, inbound string
		attempts      []string
		count         int
		want          string
	}{
		{"corrected outbound", "mismatch", []string{"matched"}, 1, "matched"},
		{"unrecognized inbound", "unknown", []string{"matched"}, 1, "matched"},
		{"earlier retry mismatched", "matched", []string{"mismatch", "matched"}, 2, "mismatch"},
		{"one unknown retry", "matched", []string{"unknown", "matched"}, 2, "unknown"},
		{"empty state", "matched", []string{"", "matched"}, 2, "unknown"},
		{"no outbound", "mismatch", nil, 0, "unknown"},
		{"missing capture", "matched", []string{"matched"}, 2, "unknown"},
		{"mismatch with missing capture", "matched", []string{"mismatch"}, 2, "mismatch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			detail := &AnthropicSessionDetail{Inbound: &AnthropicCapture{Direction: "inbound", Consistency: tc.inbound, AttemptCount: tc.count}}
			for i, state := range tc.attempts {
				detail.Attempts = append(detail.Attempts, AnthropicCapture{Direction: "outbound", Consistency: state, AttemptSeq: i + 1, Status: 200})
			}
			row := sessionRowFromDetail(detail)
			assert.Equal(t, tc.want, row.Consistency)
			assert.Equal(t, len(tc.attempts) > 0, row.HasOutbound)
			assert.Equal(t, tc.inbound, detail.Inbound.Consistency)
		})
	}
}
