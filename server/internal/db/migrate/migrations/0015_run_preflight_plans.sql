-- Durable capability preflight plans and per-thread model targets.

ALTER TABLE threads
  ADD COLUMN last_target_connection_id VARCHAR(200) NULL AFTER last_run_id,
  ADD COLUMN last_target_model VARCHAR(200) NULL AFTER last_target_connection_id,
  ADD COLUMN attachment_policies JSON NULL AFTER last_target_model;

ALTER TABLE runs
  ADD COLUMN connection_id VARCHAR(200) NULL AFTER provider_id,
  ADD COLUMN plan_id CHAR(36) NULL AFTER connection_id,
  ADD UNIQUE KEY uniq_runs_plan (plan_id);

CREATE TABLE IF NOT EXISTS run_plans (
  plan_id CHAR(36) NOT NULL PRIMARY KEY,
  thread_id CHAR(36) NOT NULL,
  run_id CHAR(36) NULL,
  target_connection_id VARCHAR(200) NOT NULL,
  target_model VARCHAR(200) NOT NULL,
  config_fingerprint VARCHAR(128) NULL,
  requirements_hash VARCHAR(128) NOT NULL,
  result_status VARCHAR(32) NOT NULL,
  status ENUM('draft','finalized') NOT NULL DEFAULT 'draft',
  blockers JSON NOT NULL,
  choices JSON NOT NULL,
  notices JSON NOT NULL,
  stale_evidence TINYINT(1) NOT NULL DEFAULT 0,
  capability_report JSON NOT NULL,
  choice_selections JSON NULL,
  created_at DATETIME(3) NOT NULL,
  expires_at DATETIME(3) NOT NULL,
  finalized_at DATETIME(3) NULL,

  FOREIGN KEY (thread_id) REFERENCES threads(id) ON DELETE CASCADE,
  FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE SET NULL,
  UNIQUE KEY uniq_run_plans_run (run_id),
  INDEX idx_run_plans_thread_created (thread_id, created_at),
  INDEX idx_run_plans_draft_expiry (status, expires_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS provider_capability_evidence (
  connection_id VARCHAR(200) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL,
  model VARCHAR(200) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL,
  fingerprint VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL,
  probe VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL,
  verdict ENUM('verified','unsupported','failed','skipped') NOT NULL,
  reason VARCHAR(200) NULL,
  message TEXT NOT NULL,
  request_id VARCHAR(200) NULL,
  duration_ms BIGINT NOT NULL DEFAULT 0,
  verified_at DATETIME(3) NOT NULL,

  PRIMARY KEY (connection_id, model, fingerprint, probe),
  INDEX idx_provider_capability_evidence_recent (connection_id, verified_at),
  INDEX idx_provider_capability_evidence_fingerprint (connection_id, model, fingerprint)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
