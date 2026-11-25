CREATE TABLE IF NOT EXISTS local_search_jobs (
  id VARCHAR(96) NOT NULL,
  thread_id CHAR(36) NOT NULL,
  run_id CHAR(36) NOT NULL,
  tool_call_id VARCHAR(128) NULL,
  status VARCHAR(32) NOT NULL,
  query TEXT NOT NULL,
  grant_id VARCHAR(128) NULL,
  local_root TEXT NULL,
  local_kind VARCHAR(32) NULL,
  created_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL,
  completed_at DATETIME(3) NULL,
  files_count INT NOT NULL DEFAULT 0,
  error TEXT NULL,
  result JSON NULL,

  PRIMARY KEY (id),
  FOREIGN KEY (thread_id) REFERENCES threads(id) ON DELETE CASCADE,
  FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE,

  INDEX idx_local_search_jobs_thread_updated (thread_id, updated_at),
  INDEX idx_local_search_jobs_run_updated (run_id, updated_at),
  INDEX idx_local_search_jobs_status_updated (status, updated_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
