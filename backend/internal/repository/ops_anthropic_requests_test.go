package repository

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/pkg/anthropicaudit"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func TestAnthropicQueryAccountWindowAndPage(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	start := time.Now().Add(-time.Hour)
	end := time.Now()
	mock.ExpectQuery(regexp.QuoteMeta(anthropicRequestsSQL)).WithArgs(anthropicaudit.Component, int64(11), start, end, true, 50, 50).
		WillReturnRows(sqlmock.NewRows([]string{"result"}).AddRow(`{"summary":{"attempts":145,"mismatched":4},"total":4,"records":[],"variants":[]}`))
	r := &opsRepository{db: db}
	result, err := r.ListAnthropicRequests(context.Background(), &service.AnthropicRequestFilter{AccountID: 11, StartTime: start, EndTime: end, Page: 2, PageSize: 50, OnlyMismatch: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Summary.Attempts != 145 || result.Total != 4 {
		t.Fatal("summary confused with page size")
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
