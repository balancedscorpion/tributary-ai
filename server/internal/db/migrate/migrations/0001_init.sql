-- Initial chat schema for Option C.

CREATE TABLE IF NOT EXISTS threads (
  id CHAR(36) NOT NULL PRIMARY KEY,
  title VARCHAR(200) NOT NULL,
  summary MEDIUMTEXT NULL,

  project_id CHAR(36) NULL,

  status ENUM('active','archived') NOT NULL DEFAULT 'active',

  created_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL,
  last_message_at DATETIME(3) NULL,
  last_run_id CHAR(36) NULL,

  next_seq BIGINT NOT NULL DEFAULT 1,

  INDEX idx_threads_status_updated (status, updated_at),
  INDEX idx_threads_last_message (last_message_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS runs (
  id CHAR(36) NOT NULL PRIMARY KEY,
  thread_id CHAR(36) NOT NULL,

  status ENUM('queued','running','completed','failed','cancelled') NOT NULL,

  provider_id VARCHAR(32) NOT NULL,
  model VARCHAR(200) NOT NULL,
  profile_id VARCHAR(200) NULL,

  system_prompt MEDIUMTEXT NOT NULL,
  max_iterations INT NOT NULL,

  request_id VARCHAR(128) NULL,
  input_tokens INT NULL,
  output_tokens INT NULL,
  total_tokens INT NULL,

  error_code VARCHAR(64) NULL,
  error_message TEXT NULL,

  created_at DATETIME(3) NOT NULL,
  started_at DATETIME(3) NULL,
  ended_at DATETIME(3) NULL,
  updated_at DATETIME(3) NOT NULL,

  FOREIGN KEY (thread_id) REFERENCES threads(id) ON DELETE CASCADE,
  INDEX idx_runs_thread_created (thread_id, created_at),
  INDEX idx_runs_status_created (status, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS messages (
  id CHAR(36) NOT NULL PRIMARY KEY,
  thread_id CHAR(36) NOT NULL,
  run_id CHAR(36) NULL,

  seq BIGINT NOT NULL,
  role ENUM('system','user','assistant','tool') NOT NULL,

  content LONGTEXT NULL,

  thinking LONGTEXT NULL,
  thinking_complete TINYINT(1) NOT NULL DEFAULT 0,

  tool_calls JSON NULL,
  tool_call_id VARCHAR(128) NULL,
  name VARCHAR(200) NULL,

  ui_hidden TINYINT(1) NOT NULL DEFAULT 0,

  created_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL,

  FOREIGN KEY (thread_id) REFERENCES threads(id) ON DELETE CASCADE,
  FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE SET NULL,

  UNIQUE KEY uniq_messages_thread_seq (thread_id, seq),
  INDEX idx_messages_thread_created (thread_id, created_at),
  INDEX idx_messages_run (run_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS artifacts (
  id CHAR(36) NOT NULL PRIMARY KEY,
  thread_id CHAR(36) NOT NULL,
  message_id CHAR(36) NULL,

  type ENUM('data','code','image','file') NOT NULL,
  title VARCHAR(255) NOT NULL,
  language VARCHAR(64) NULL,

  tool_name VARCHAR(200) NULL,
  tool_call_id VARCHAR(128) NULL,
  ok TINYINT(1) NULL,

  content_storage ENUM('mysql','file') NOT NULL,
  content_text LONGTEXT NULL,
  content_path TEXT NULL,

  byte_size BIGINT NOT NULL,
  metadata JSON NULL,

  created_at DATETIME(3) NOT NULL,

  FOREIGN KEY (thread_id) REFERENCES threads(id) ON DELETE CASCADE,
  FOREIGN KEY (message_id) REFERENCES messages(id) ON DELETE SET NULL,

  INDEX idx_artifacts_thread_created (thread_id, created_at),
  INDEX idx_artifacts_message (message_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS projects (
  id CHAR(36) NOT NULL PRIMARY KEY,
  name VARCHAR(255) NOT NULL,
  root_rel_path TEXT NOT NULL,

  file_count INT NOT NULL DEFAULT 0,
  total_bytes BIGINT NOT NULL DEFAULT 0,

  created_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL,

  INDEX idx_projects_updated (updated_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS project_files (
  project_id CHAR(36) NOT NULL,
  path VARCHAR(512) NOT NULL,
  byte_size BIGINT NOT NULL,
  sha256 BINARY(32) NULL,
  updated_at DATETIME(3) NOT NULL,

  PRIMARY KEY (project_id, path),
  FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS jobs (
  id CHAR(36) NOT NULL PRIMARY KEY,
  kind ENUM('thread_summarize','thread_title') NOT NULL,
  thread_id CHAR(36) NOT NULL,
  run_id CHAR(36) NULL,

  status ENUM('queued','running','completed','failed') NOT NULL,
  attempts INT NOT NULL DEFAULT 0,
  not_before DATETIME(3) NOT NULL,
  last_error TEXT NULL,

  created_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL,

  FOREIGN KEY (thread_id) REFERENCES threads(id) ON DELETE CASCADE,
  INDEX idx_jobs_status_not_before (status, not_before)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
