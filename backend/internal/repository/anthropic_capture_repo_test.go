package repository

import (
	"context"
	"database/sql"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestListAnthropicSessionsUsesWindowWithoutBodies(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	start := time.Now().Add(-time.Hour)
	end := time.Now()
	mock.ExpectQuery(regexp.QuoteMeta(listAnthropicSessionsSQL)).
		WithArgs(start, end, int64(11), true, false, false, "claude", "%c7f3%", int64(0), 50, 0).
		WillReturnRows(sqlmock.NewRows([]string{"result"}).AddRow(`{"summary":{"inbound_requests":2,"outbound_attempts":3,"multi_attempts":1,"failed_requests":0},"total":2,"records":[{"client_request_id":"c7f3","has_inbound":true,"attempt_count":1}],"accounts":[],"models":["claude"]}`))
	repo := &opsRepository{db: db}
	result, err := repo.ListAnthropicSessions(context.Background(), &service.AnthropicSessionFilter{
		AccountID: 11, StartTime: start, EndTime: end, Page: 1, PageSize: 50, OnlyMulti: true, Model: "claude", QueryLike: "%c7f3%",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Summary.InboundRequests != 2 || result.Total != 2 || result.Records[0].ClientRequestID != "c7f3" {
		t.Fatalf("%+v", result)
	}
	if stringsContainBodyColumns(listAnthropicSessionsSQL) {
		t.Fatal("list SQL must not read request bodies")
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func stringsContainBodyColumns(query string) bool {
	// The list projection names headers/body only if it selects those columns.
	// Summary JSON stays; raw documents must not.
	return regexp.MustCompile(`(?i)\bc\.(headers|body)\b`).MatchString(query)
}

func TestCaptureCleanupDoesNotBroadenLogFilters(t *testing.T) {
	for _, field := range []string{"platform", "level", "host", "query", "supported"} {
		t.Run(field, func(t *testing.T) {
			db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "captures.db"))
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			_, err = db.Exec(`CREATE TABLE anthropic_request_captures (client_request_id TEXT); INSERT INTO anthropic_request_captures VALUES ('target'), ('other')`)
			require.NoError(t, err)
			filter := &service.OpsSystemLogCleanupFilter{ClientRequestID: "target"}
			switch field {
			case "platform":
				filter.Platform = "openai"
			case "level":
				filter.Level = "error"
			case "host":
				filter.Host = "another-host"
			case "query":
				filter.Query = "unrelated message"
			}
			repo := &opsRepository{db: db}
			repo.deleteMatchingCaptures(context.Background(), filter)
			var count int
			require.NoError(t, db.QueryRow("SELECT count(*) FROM anthropic_request_captures").Scan(&count))
			want := 2
			if field == "supported" {
				want = 1
			}
			assert.Equal(t, want, count)
			require.NoError(t, db.QueryRow("SELECT count(*) FROM anthropic_request_captures WHERE client_request_id='other'").Scan(&count))
			assert.Equal(t, 1, count)
		})
	}
}
