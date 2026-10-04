package migrations

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// This data-only migration uses portable SQL. SQLite executes its predicates and
// repeat-run behavior locally; it does not replace a full Postgres upgrade test.
func TestStrictSessionSettingsMigrationPreservesExplicitChoices(t *testing.T) {
	migration, err := FS.ReadFile("243_strict_session_settings_database_only.sql")
	require.NoError(t, err)
	for _, tc := range []struct {
		name, override, enabled string
		kept                    bool
	}{
		{"fresh", "", "", false},
		{"legacy_seed", "false", "false", false},
		{"saved_disabled", "true", "false", true},
		{"saved_enabled", "true", "true", true},
		{"already_migrated", "", "false", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, err := sql.Open("sqlite", ":memory:")
			require.NoError(t, err)
			db.SetMaxOpenConns(1)
			t.Cleanup(func() { require.NoError(t, db.Close()) })
			_, err = db.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT);
CREATE TABLE strict_session_bindings (binding_key TEXT, account_id INTEGER);
INSERT INTO strict_session_bindings VALUES ('session', 8);
INSERT INTO settings VALUES ('unrelated', 'keep');`)
			require.NoError(t, err)
			for key, value := range map[string]string{"strict_session_binding_override": tc.override, "strict_session_binding_enabled": tc.enabled} {
				if value != "" {
					_, err = db.Exec("INSERT INTO settings VALUES (?, ?)", key, value)
					require.NoError(t, err)
				}
			}
			if tc.enabled != "" {
				_, err = db.Exec("INSERT INTO settings VALUES ('strict_session_third_party_api_key', 'test-secret')")
				require.NoError(t, err)
			}
			for range 2 {
				_, err = db.Exec(string(migration))
				require.NoError(t, err)
			}
			var enabled string
			err = db.QueryRow("SELECT value FROM settings WHERE key='strict_session_binding_enabled'").Scan(&enabled)
			if tc.kept {
				require.NoError(t, err)
				require.Equal(t, tc.enabled, enabled)
			} else {
				require.ErrorIs(t, err, sql.ErrNoRows)
			}
			var secret string
			err = db.QueryRow("SELECT value FROM settings WHERE key='strict_session_third_party_api_key'").Scan(&secret)
			if tc.kept {
				require.NoError(t, err)
				require.Equal(t, "test-secret", secret)
			} else {
				require.ErrorIs(t, err, sql.ErrNoRows)
			}
			var count, account int
			require.NoError(t, db.QueryRow("SELECT count(*) FROM settings WHERE key='strict_session_binding_override'").Scan(&count))
			require.Zero(t, count)
			require.NoError(t, db.QueryRow("SELECT count(*) FROM settings WHERE key='unrelated' AND value='keep'").Scan(&count))
			require.Equal(t, 1, count)
			require.NoError(t, db.QueryRow("SELECT account_id FROM strict_session_bindings WHERE binding_key='session'").Scan(&account))
			require.Equal(t, 8, account)
		})
	}
}
