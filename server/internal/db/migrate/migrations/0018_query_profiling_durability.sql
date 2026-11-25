-- Durable pre-submit profiling and provider pacing fields. Every additive
-- operation is isolated so a partially applied MySQL DDL migration can be
-- replayed safely by the migrator.

ALTER TABLE query_jobs ADD COLUMN query_profile JSON NULL AFTER submit_dispatched;
ALTER TABLE query_jobs ADD COLUMN profile_outcome VARCHAR(32) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL AFTER query_profile;
ALTER TABLE query_jobs ADD COLUMN profile_handle JSON NULL AFTER profile_outcome;
ALTER TABLE query_jobs ADD COLUMN profile_phase VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL AFTER profile_handle;
ALTER TABLE query_jobs ADD COLUMN profile_dispatched TINYINT(1) NOT NULL DEFAULT 0 AFTER profile_phase;
ALTER TABLE query_jobs ADD COLUMN cancel_started_at DATETIME(3) NULL AFTER cancellation_outcome;
ALTER TABLE query_jobs ADD COLUMN remote_submitted_at DATETIME(3) NULL AFTER submitted_at;
ALTER TABLE query_jobs ADD COLUMN next_poll_at DATETIME(3) NULL AFTER remote_submitted_at;
ALTER TABLE query_jobs ADD COLUMN next_poll_action VARCHAR(32) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL AFTER next_poll_at;
ALTER TABLE query_jobs ADD COLUMN drain_rows_committed BIGINT UNSIGNED NOT NULL DEFAULT 0 AFTER drain_seq;
ALTER TABLE query_jobs ADD COLUMN drain_bytes_committed BIGINT UNSIGNED NOT NULL DEFAULT 0 AFTER drain_rows_committed;
ALTER TABLE query_jobs ADD COLUMN drain_truncated TINYINT(1) NOT NULL DEFAULT 0 AFTER drain_bytes_committed;
ALTER TABLE query_jobs ADD INDEX idx_query_jobs_next_poll (execution_state, next_poll_at);
