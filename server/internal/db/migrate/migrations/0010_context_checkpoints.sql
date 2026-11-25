CREATE TABLE IF NOT EXISTS chat_context_checkpoints (
  id CHAR(36) NOT NULL PRIMARY KEY,
  thread_id CHAR(36) NOT NULL,
  run_id CHAR(36) NULL,

  source_message_seq BIGINT NOT NULL,
  source_message_id VARCHAR(128) NOT NULL,

  replacement_history JSON NOT NULL,
  summary MEDIUMTEXT NULL,
  summary_message_ids JSON NULL,
  referenced_artifact_ids JSON NULL,

  token_estimate INT NOT NULL DEFAULT 0,
  chars_per_token INT NOT NULL DEFAULT 0,
  model VARCHAR(200) NOT NULL DEFAULT '',
  provider_id VARCHAR(32) NOT NULL DEFAULT '',
  plan JSON NULL,

  is_active TINYINT(1) NOT NULL DEFAULT 1,
  created_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL,

  FOREIGN KEY (thread_id) REFERENCES threads(id) ON DELETE CASCADE,
  FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE SET NULL,

  INDEX idx_context_checkpoints_thread_created (thread_id, created_at),
  INDEX idx_context_checkpoints_thread_active (thread_id, is_active, updated_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
