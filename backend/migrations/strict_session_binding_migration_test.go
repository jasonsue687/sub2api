package migrations

import (
	"strings"
	"testing"
)

func TestStrictSessionBindingMigrationKeepsRowsWithoutCascade(t *testing.T) {
	body, err := FS.ReadFile("242_strict_session_bindings.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sqlText := strings.ToLower(string(body))
	if strings.Contains(sqlText, "drop table") {
		t.Fatal("migration must not drop strict_session_bindings")
	}
	if strings.Contains(sqlText, "on delete cascade") || strings.Contains(sqlText, "references accounts") {
		t.Fatal("account deletion must not cascade to session bindings")
	}
	for _, needle := range []string{
		"create table if not exists strict_session_bindings",
		"binding_key",
		"account_id",
		"unique",
		"session_fingerprint",
	} {
		if !strings.Contains(sqlText, needle) {
			t.Fatalf("migration missing %q", needle)
		}
	}
}
