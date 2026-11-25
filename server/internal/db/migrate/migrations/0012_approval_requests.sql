CREATE TABLE IF NOT EXISTS approval_requests (
  id VARCHAR(96) NOT NULL,
  thread_id CHAR(36) NOT NULL,
  run_id CHAR(36) NOT NULL,
  tool_call_id VARCHAR(128) NULL,
  tool VARCHAR(160) NOT NULL,
  category VARCHAR(32) NOT NULL,
  operation VARCHAR(160) NULL,
  subject JSON NOT NULL,
  summary TEXT NOT NULL,
  preview JSON NULL,
  options_json JSON NULL,
  status VARCHAR(32) NOT NULL,
  decision JSON NULL,
  created_at DATETIME(3) NOT NULL,
  expires_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL,

  PRIMARY KEY (id),
  FOREIGN KEY (thread_id) REFERENCES threads(id) ON DELETE CASCADE,
  FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE,

  INDEX idx_approval_requests_thread_status_created (thread_id, status, created_at),
  INDEX idx_approval_requests_run_status_created (run_id, status, created_at),
  INDEX idx_approval_requests_status_expires (status, expires_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
