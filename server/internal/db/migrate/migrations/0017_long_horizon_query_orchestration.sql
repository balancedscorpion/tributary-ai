-- Durable long-horizon query orchestration. Query execution is intentionally
-- independent from a model run: a stopped/completed run does not delete or
-- cancel its query jobs, and a later continuation reuses the same run id.

ALTER TABLE runs
  MODIFY COLUMN status ENUM('queued','running','waiting_external','resume_pending','resuming','waiting_user','completed','failed','cancelled') NOT NULL;

-- Keep every additive DDL operation in its own statement. MySQL commits DDL
-- independently; the migrator can therefore recognize a duplicate
-- column/index after a crash and continue with the still-missing statements.
ALTER TABLE runs ADD COLUMN revision BIGINT UNSIGNED NOT NULL DEFAULT 0 AFTER status;
ALTER TABLE runs ADD COLUMN continuation_count INT UNSIGNED NOT NULL DEFAULT 0 AFTER max_iterations;
ALTER TABLE runs ADD COLUMN max_auto_continuations INT UNSIGNED NOT NULL DEFAULT 3 AFTER continuation_count;
ALTER TABLE runs ADD COLUMN last_checkpoint_sequence BIGINT UNSIGNED NOT NULL DEFAULT 0 AFTER max_auto_continuations;
ALTER TABLE runs ADD COLUMN wake_at DATETIME(3) NULL AFTER last_checkpoint_sequence;
ALTER TABLE runs ADD COLUMN resume_lease_holder VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL AFTER wake_at;
ALTER TABLE runs ADD COLUMN resume_lease_expires_at DATETIME(3) NULL AFTER resume_lease_holder;
ALTER TABLE runs ADD INDEX idx_runs_continuation_wake (status, wake_at);
ALTER TABLE runs ADD INDEX idx_runs_resume_lease (resume_lease_expires_at);

CREATE TABLE IF NOT EXISTS run_continuation_checkpoints (
  id VARCHAR(96) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  run_id CHAR(36) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  thread_id CHAR(36) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  sequence BIGINT UNSIGNED NOT NULL,
  tool_batch_sequence BIGINT UNSIGNED NOT NULL,
  history_json JSON NOT NULL,
  provider_state_json JSON NULL,
  execution_snapshot_json LONGTEXT NULL,
  dependency_ids JSON NOT NULL,
  created_at DATETIME(3) NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uniq_run_continuation_sequence (run_id, sequence),
  FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE,
  FOREIGN KEY (thread_id) REFERENCES threads(id) ON DELETE CASCADE,
  INDEX idx_run_continuation_latest (run_id, sequence)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS query_jobs (
  id VARCHAR(96) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  provider VARCHAR(32) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  connection_id VARCHAR(200) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL,
  connection_key VARCHAR(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  thread_id CHAR(36) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  run_id CHAR(36) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL,
  owner_user_token VARCHAR(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL,
  owner_handle VARCHAR(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL,
  sql_text LONGTEXT NULL,
  sql_preview TEXT NULL,
  execution_state VARCHAR(32) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  monitor_health VARCHAR(32) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL DEFAULT 'ok',
  policy_health VARCHAR(40) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL DEFAULT 'ok',
  materialization_state VARCHAR(32) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL DEFAULT 'none',
  cancellation_reason VARCHAR(32) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL,
  cancellation_outcome VARCHAR(40) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL,
  validation_state VARCHAR(32) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL DEFAULT 'unvalidated',
  validation_detail TEXT NULL,
  native_handle JSON NULL,
  idempotency_key VARCHAR(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL,
  replaces_job_id VARCHAR(96) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL,
  keep_on_run_completion TINYINT(1) NOT NULL DEFAULT 1,
  submit_dispatched TINYINT(1) NOT NULL DEFAULT 0,
  budgets JSON NULL,
  telemetry JSON NULL,
  metadata JSON NULL,
  artifact_id VARCHAR(96) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL,
  error_message TEXT NULL,
  submitted_at DATETIME(3) NOT NULL,
  started_at DATETIME(3) NULL,
  completed_at DATETIME(3) NULL,
  last_access_at DATETIME(3) NOT NULL,
  revision BIGINT UNSIGNED NOT NULL DEFAULT 1,
  drain_seq BIGINT UNSIGNED NOT NULL DEFAULT 0,
  monitor_lease_holder VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL,
  monitor_lease_expires_at DATETIME(3) NULL,
  created_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL,
  PRIMARY KEY (id),
  FOREIGN KEY (thread_id) REFERENCES threads(id) ON DELETE CASCADE,
  FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE SET NULL,
  UNIQUE KEY uniq_query_job_idempotency (provider, connection_key, idempotency_key),
  INDEX idx_query_jobs_active (execution_state, updated_at),
  INDEX idx_query_jobs_thread (thread_id, submitted_at),
  INDEX idx_query_jobs_connection (connection_id, execution_state),
  INDEX idx_query_jobs_monitor_lease (monitor_lease_expires_at),
  INDEX idx_query_jobs_replacement (replaces_job_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- Immutable observations, including durable exactly-once wake notifications.
CREATE TABLE IF NOT EXISTS query_job_events (
  job_id VARCHAR(96) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  sequence BIGINT UNSIGNED NOT NULL,
  event_type VARCHAR(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  wake_kind VARCHAR(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL,
  detail TEXT NULL,
  payload JSON NULL,
  observation_key VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  created_at DATETIME(3) NOT NULL,
  claimed_by VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL,
  claimed_at DATETIME(3) NULL,
  PRIMARY KEY (job_id, sequence),
  UNIQUE KEY uniq_query_job_observation (job_id, observation_key),
  FOREIGN KEY (job_id) REFERENCES query_jobs(id) ON DELETE CASCADE,
  INDEX idx_query_job_wakes (job_id, wake_kind, claimed_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- Idempotent bounded result chunks. page_json contains only the admitted
-- rows; cursor_after_json is the native cursor positioned after those rows.
CREATE TABLE IF NOT EXISTS query_result_chunks (
  job_id VARCHAR(96) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  sequence BIGINT UNSIGNED NOT NULL,
  page_json JSON NOT NULL,
  cursor_after_json JSON NULL,
  native_page_token TEXT NULL,
  row_count INT UNSIGNED NOT NULL DEFAULT 0,
  byte_count BIGINT UNSIGNED NOT NULL DEFAULT 0,
  truncated TINYINT(1) NOT NULL DEFAULT 0,
  created_at DATETIME(3) NOT NULL,
  PRIMARY KEY (job_id, sequence),
  FOREIGN KEY (job_id) REFERENCES query_jobs(id) ON DELETE CASCADE,
  INDEX idx_query_result_chunks_created (job_id, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS run_query_dependencies (
  run_id CHAR(36) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  job_id VARCHAR(96) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  required_for_completion TINYINT(1) NOT NULL DEFAULT 1,
  detached TINYINT(1) NOT NULL DEFAULT 0,
  validation_state VARCHAR(32) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL DEFAULT 'unvalidated',
  replacement_job_id VARCHAR(96) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL,
  created_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL,
  PRIMARY KEY (run_id, job_id),
  FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE,
  FOREIGN KEY (job_id) REFERENCES query_jobs(id) ON DELETE CASCADE,
  INDEX idx_run_query_dependencies_job (job_id),
  INDEX idx_run_query_dependencies_barrier (run_id, required_for_completion, detached, validation_state)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- Cross-process admission reservations are separate from monitor leases so a
-- monitor restart cannot accidentally free an executing provider query.
CREATE TABLE IF NOT EXISTS query_capacity_reservations (
  job_id VARCHAR(96) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  thread_id CHAR(36) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  connection_key VARCHAR(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  reservation_state ENUM('active','released') NOT NULL DEFAULT 'active',
  reserved_at DATETIME(3) NOT NULL,
  released_at DATETIME(3) NULL,
  PRIMARY KEY (job_id),
  FOREIGN KEY (job_id) REFERENCES query_jobs(id) ON DELETE CASCADE,
  FOREIGN KEY (thread_id) REFERENCES threads(id) ON DELETE CASCADE,
  INDEX idx_query_capacity_global (reservation_state, reserved_at),
  INDEX idx_query_capacity_thread (thread_id, reservation_state),
  INDEX idx_query_capacity_connection (connection_key, reservation_state)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- A permanent singleton row serializes capacity decisions even when the
-- caller's transaction isolation is READ COMMITTED and the reservation table
-- is initially empty (where range/count locks alone cannot prevent phantoms).
CREATE TABLE IF NOT EXISTS query_capacity_gate (
  id TINYINT UNSIGNED NOT NULL,
  touched_at DATETIME(3) NOT NULL,
  PRIMARY KEY (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

INSERT INTO query_capacity_gate (id, touched_at)
VALUES (1, UTC_TIMESTAMP(3))
ON DUPLICATE KEY UPDATE touched_at=touched_at;
