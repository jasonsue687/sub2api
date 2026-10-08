-- Pause newly created accounts until an administrator enables scheduling.
-- Existing accounts retain their current scheduling state.
ALTER TABLE accounts ALTER COLUMN schedulable SET DEFAULT FALSE;
