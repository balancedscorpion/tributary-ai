CREATE TABLE IF NOT EXISTS model_invocations (
  id VARCHAR(96) NOT NULL,

  run_id CHAR(36) NULL,
  thread_id CHAR(36) NULL,
  job_id VARCHAR(96) NULL,
  operation VARCHAR(64) NOT NULL,

  provider_id VARCHAR(64) NOT NULL,
  provider_config_id VARCHAR(200) NULL,
  profile_id VARCHAR(200) NULL,
  api_type VARCHAR(64) NULL,
  requested_model VARCHAR(255) NOT NULL,
  resolved_model VARCHAR(255) NULL,

  status VARCHAR(32) NOT NULL,
  started_at DATETIME(3) NOT NULL,
  ended_at DATETIME(3) NULL,
  updated_at DATETIME(3) NOT NULL,

  input_tokens BIGINT NULL,
  output_tokens BIGINT NULL,
  total_tokens BIGINT NULL,
  cached_input_tokens BIGINT NULL,
  cache_write_tokens BIGINT NULL,
  reasoning_tokens BIGINT NULL,
  usage_quality VARCHAR(32) NOT NULL,

  upstream_request_count INT UNSIGNED NOT NULL DEFAULT 0,
  provider_request_id VARCHAR(255) NULL,
  sanitized_details JSON NULL,
  error_code VARCHAR(64) NULL,
  error_message TEXT NULL,

  PRIMARY KEY (id),
  INDEX idx_model_invocations_ended_provider_model (ended_at, provider_id, resolved_model),
  INDEX idx_model_invocations_started_provider_model (started_at, provider_id, resolved_model),
  INDEX idx_model_invocations_run (run_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- Run fields remain a compatibility roll-up of the invocation ledger. Keep
-- their capacity aligned with the ledger so long tool-driven runs cannot
-- overflow while being summarized.
-- Keep the modern provider-config identity separate from profile_id, which
-- remains the legacy compatible-provider profile reference.
ALTER TABLE runs
  ADD COLUMN provider_config_id VARCHAR(200) NULL AFTER profile_id,

  MODIFY COLUMN input_tokens BIGINT NULL,
  MODIFY COLUMN output_tokens BIGINT NULL,
  MODIFY COLUMN total_tokens BIGINT NULL;
