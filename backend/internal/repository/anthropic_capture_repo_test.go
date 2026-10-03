package repository

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
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
