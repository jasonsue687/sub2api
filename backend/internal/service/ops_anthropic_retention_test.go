package service

import (
	"context"
	"database/sql"
	"reflect"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/anthropicaudit"
	_ "modernc.org/sqlite"
)

// Execute the actual batched deletion against mixed records, rather than
// mocking the SQL: shorter/longer runtime retention must not affect audits.
func TestAnthropicRetentionIndependentOfRuntimeLogs(t *testing.T) {
	for _, tc := range []struct {
		name string
		days int
		want []int
	}{
		{"runtime 7 days", 7, []int{1, 2, 3, 8, 10}},
		{"runtime 90 days", 90, []int{1, 2, 3, 5, 6, 7, 8, 9, 10}},
		{"runtime disabled", -1, []int{1, 2, 3, 5, 6, 7, 8, 9, 10}},
		{"runtime clear all", 0, []int{1, 2, 3}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, err := sql.Open("sqlite", ":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := db.Close(); err != nil {
					t.Error(err)
				}
			}()
			db.SetMaxOpenConns(1)
			if _, err = db.Exec(`CREATE TABLE ops_system_logs (id INTEGER PRIMARY KEY, component TEXT, created_at TIMESTAMP)`); err != nil {
				t.Fatal(err)
			}
			now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
			for _, row := range []struct {
				id        int
				component any
				age       time.Duration
			}{
				{1, anthropicaudit.Component, 0},
				{2, anthropicaudit.Component, 29 * 24 * time.Hour},
				{3, anthropicaudit.Component, 30 * 24 * time.Hour},
				{4, anthropicaudit.Component, 30*24*time.Hour + time.Second},
				{5, "http.access", 31 * 24 * time.Hour},
				{6, "http.access", 8 * 24 * time.Hour},
				{7, nil, 8 * 24 * time.Hour},
				{8, "http.access", time.Hour},
				{9, "other.audit", 40 * 24 * time.Hour},
				{10, nil, time.Hour},
			} {
				if _, err = db.Exec(`INSERT INTO ops_system_logs VALUES ($1,$2,$3)`, row.id, row.component, now.Add(-row.age)); err != nil {
					t.Fatal(err)
				}
			}
			// Small batches exercise continuation and final empty-batch handling.
			deleted, err := cleanupSystemLogs(context.Background(), db, now, tc.days, 2)
			if err != nil {
				t.Fatal(err)
			}
			if deleted != int64(10-len(tc.want)) {
				t.Fatalf("deleted %d", deleted)
			}
			rows, err := db.Query(`SELECT id FROM ops_system_logs ORDER BY id`)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := rows.Close(); err != nil {
					t.Error(err)
				}
			}()
			var got []int
			for rows.Next() {
				var id int
				if err := rows.Scan(&id); err != nil {
					t.Fatal(err)
				}
				got = append(got, id)
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("remaining = %v, want %v", got, tc.want)
			}
		})
	}
}
