//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/anthropicaudit"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAnthropicSessionConsistencyIncludesAllAttempts(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2036, 1, 1, 0, 0, 0, 0, time.UTC)
	prefix := "integration-capture-consistency-"
	t.Cleanup(func() {
		_, err := integrationDB.ExecContext(ctx, "DELETE FROM anthropic_request_captures WHERE client_request_id LIKE $1", prefix+"%")
		assert.NoError(t, err)
	})
	for _, item := range []struct{ id, direction, consistency string }{
		{"matched-inbound", "inbound", "matched"}, {"matched-inbound", "outbound", "mismatch"},
		{"unknown-inbound", "inbound", "unknown"}, {"unknown-inbound", "outbound", "mismatch"},
		{"no-inbound", "outbound", "mismatch"}, {"no-inbound", "outbound", "unknown"},
		{"partly-unknown", "inbound", "matched"}, {"partly-unknown", "outbound", "unknown"},
		{"all-matched", "inbound", "matched"}, {"all-matched", "outbound", "matched"},
	} {
		require.NoError(t, insertAnthropicCapture(ctx, integrationDB, anthropicaudit.Record{
			ClientRequestID: prefix + item.id, Direction: item.direction, Consistency: item.consistency,
			Status: 200, CreatedAt: start.Add(time.Minute), AttemptSeq: 1,
		}))
	}
	repo := &opsRepository{db: integrationDB}
	filter := &service.AnthropicSessionFilter{StartTime: start, EndTime: start.Add(time.Hour), Page: 1, PageSize: 50, QueryLike: prefix + "%"}
	result, err := repo.ListAnthropicSessions(ctx, filter)
	require.NoError(t, err)
	require.EqualValues(t, 5, result.Total)
	states := map[string]string{}
	for _, row := range result.Records {
		states[row.ClientRequestID] = row.Consistency
	}
	assert.Equal(t, "unknown", states[prefix+"partly-unknown"])
	assert.Equal(t, "matched", states[prefix+"all-matched"])
	filter.OnlyMismatch = true
	result, err = repo.ListAnthropicSessions(ctx, filter)
	require.NoError(t, err)
	require.EqualValues(t, 3, result.Total)
	for _, row := range result.Records {
		assert.Equal(t, "mismatch", row.Consistency)
	}
}
