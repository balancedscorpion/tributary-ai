CREATE TABLE IF NOT EXISTS run_events (
  run_id CHAR(36) NOT NULL,
  thread_id CHAR(36) NOT NULL,
  seq BIGINT NOT NULL,
  type VARCHAR(128) NOT NULL,
  data JSON NOT NULL,
  created_at DATETIME(3) NOT NULL,

  PRIMARY KEY (run_id, seq),
  FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE,
  FOREIGN KEY (thread_id) REFERENCES threads(id) ON DELETE CASCADE,

  INDEX idx_run_events_thread_created (thread_id, created_at),
  INDEX idx_run_events_run_created (run_id, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
