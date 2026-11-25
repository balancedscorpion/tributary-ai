-- Forward-only upgrade of the immutable 0015 usage ledger to the canonical v2
-- attribution and cumulative-journal schema. Keeping 0015 intact ensures both
-- fresh installs and installations that already applied the feature branch
-- traverse the same deterministic transformation.

ALTER TABLE model_invocations
  ADD COLUMN plan_id CHAR(36) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL AFTER job_id,
  ADD COLUMN config_fingerprint VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL AFTER profile_id,
  ADD COLUMN effective_api_surface VARCHAR(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL AFTER api_type,
  ADD COLUMN snapshot_sequence BIGINT UNSIGNED NOT NULL DEFAULT 0 AFTER updated_at,
  ADD COLUMN provider_total_tokens BIGINT NULL AFTER total_tokens,
  ADD COLUMN derived_total_tokens BIGINT NULL AFTER provider_total_tokens,
  ADD COLUMN lower_bound_mask BIGINT UNSIGNED NOT NULL DEFAULT 0 AFTER reasoning_tokens,
  ADD COLUMN token_semantics_profile VARCHAR(32) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL DEFAULT 'raw_counters_v1' AFTER usage_quality;

-- Legacy compatible profiles sometimes carried the synthetic
-- provider_config_id "legacy-default" as well as the authoritative profile.
-- Canonical v2 identity permits exactly one of these dimensions.
UPDATE model_invocations
SET provider_config_id = NULL
WHERE profile_id IS NOT NULL AND TRIM(profile_id) <> '';

UPDATE model_invocations
SET provider_total_tokens = CASE WHEN usage_quality = 'provider_reported' THEN total_tokens ELSE NULL END,
    derived_total_tokens = CASE WHEN usage_quality <> 'provider_reported' THEN total_tokens ELSE NULL END,
    -- The legacy schema stored only one surface. It is safe to treat that as
    -- the effective dispatch surface for completed calls; failed/running rows
    -- may have ended before dispatch, so retain only the planned value there.
    effective_api_surface = CASE WHEN status = 'completed' THEN CASE
      WHEN provider_id = 'openai' THEN 'responses'
      WHEN provider_id = 'anthropic' THEN 'messages'
      WHEN provider_id = 'gemini' THEN 'generate_content'
      WHEN provider_id = 'bedrock' AND LOWER(COALESCE(api_type, '')) IN ('responses', 'response') THEN 'responses'
      WHEN provider_id = 'bedrock' AND LOWER(COALESCE(api_type, '')) IN ('chat_completions', 'chat_completion') THEN 'chat_completions'
      WHEN provider_id = 'bedrock' AND LOWER(COALESCE(api_type, '')) LIKE '%converse%' THEN 'converse_stream'
      WHEN provider_id = 'bedrock' THEN 'invoke_stream'
      WHEN provider_id = 'compatible' AND LOWER(COALESCE(api_type, '')) = 'responses' THEN 'responses'
      WHEN provider_id = 'compatible' THEN 'chat_completions'
      ELSE NULL
    END ELSE NULL END,
    api_type = CASE
      WHEN provider_id = 'openai' THEN 'responses'
      WHEN provider_id = 'anthropic' THEN 'messages'
      WHEN provider_id = 'gemini' THEN 'generate_content'
      WHEN provider_id = 'bedrock' AND LOWER(COALESCE(api_type, '')) IN ('responses', 'response') THEN 'responses'
      WHEN provider_id = 'bedrock' AND LOWER(COALESCE(api_type, '')) IN ('chat_completions', 'chat_completion') THEN 'chat_completions'
      WHEN provider_id = 'bedrock' AND LOWER(COALESCE(api_type, '')) LIKE '%converse%' THEN 'converse_stream'
      WHEN provider_id = 'bedrock' THEN 'invoke_stream'
      WHEN provider_id = 'compatible' AND LOWER(COALESCE(api_type, '')) = 'responses' THEN 'responses'
      WHEN provider_id = 'compatible' THEN 'chat_completions'
      ELSE NULL
    END;

UPDATE model_invocations
SET lower_bound_mask =
      (CASE WHEN input_tokens IS NOT NULL THEN 1 ELSE 0 END) |
      (CASE WHEN output_tokens IS NOT NULL THEN 2 ELSE 0 END) |
      (CASE WHEN provider_total_tokens IS NOT NULL THEN 4 ELSE 0 END) |
      (CASE WHEN derived_total_tokens IS NOT NULL THEN 8 ELSE 0 END) |
      (CASE WHEN cached_input_tokens IS NOT NULL THEN 16 ELSE 0 END) |
      (CASE WHEN cache_write_tokens IS NOT NULL THEN 32 ELSE 0 END) |
      (CASE WHEN reasoning_tokens IS NOT NULL THEN 64 ELSE 0 END),
    usage_quality = CASE
      WHEN input_tokens IS NOT NULL OR output_tokens IS NOT NULL OR provider_total_tokens IS NOT NULL OR derived_total_tokens IS NOT NULL OR
           cached_input_tokens IS NOT NULL OR cache_write_tokens IS NOT NULL OR reasoning_tokens IS NOT NULL
      THEN 'legacy_partial' ELSE usage_quality END
WHERE status <> 'completed' OR usage_quality = 'legacy_partial';

UPDATE model_invocations
SET error_code = CASE
  WHEN error_code IS NULL THEN NULL
  WHEN UPPER(TRIM(error_code)) IN ('PROVIDER_ERROR','AUTH_ERROR','RATE_LIMIT','INVALID_REQUEST','TIMEOUT','STREAM_TIMEOUT','ABORTED','STREAM_ENDED','PROCESS_RESTART','PERSISTENCE_ERROR','UNKNOWN')
    THEN UPPER(TRIM(error_code))
  WHEN UPPER(error_code) LIKE '%AUTH%' OR UPPER(error_code) LIKE '%UNAUTHORIZED%' OR UPPER(error_code) LIKE '%FORBIDDEN%' THEN 'AUTH_ERROR'
  WHEN UPPER(error_code) LIKE '%RATE%' OR UPPER(error_code) LIKE '%QUOTA%' OR error_code LIKE '%429%' THEN 'RATE_LIMIT'
  WHEN UPPER(error_code) LIKE '%TIMEOUT%' OR UPPER(error_code) LIKE '%DEADLINE%' THEN 'TIMEOUT'
  WHEN UPPER(error_code) LIKE '%CANCEL%' OR UPPER(error_code) LIKE '%ABORT%' THEN 'ABORTED'
  WHEN UPPER(error_code) LIKE '%INVALID%' OR UPPER(error_code) LIKE '%BAD_REQUEST%' THEN 'INVALID_REQUEST'
  WHEN UPPER(error_code) LIKE '%EOF%' OR UPPER(error_code) LIKE '%STREAM_ENDED%' THEN 'STREAM_ENDED'
  ELSE 'PROVIDER_ERROR'
END;

ALTER TABLE model_invocations
  CHANGE COLUMN provider_config_id connection_id VARCHAR(200) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL,
  CHANGE COLUMN api_type planned_api_surface VARCHAR(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL,
  MODIFY COLUMN id VARCHAR(96) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  MODIFY COLUMN run_id CHAR(36) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL,
  MODIFY COLUMN thread_id CHAR(36) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL,
  MODIFY COLUMN job_id VARCHAR(96) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL,
  MODIFY COLUMN operation VARCHAR(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  MODIFY COLUMN provider_id VARCHAR(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  MODIFY COLUMN profile_id VARCHAR(200) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL,
  MODIFY COLUMN requested_model VARCHAR(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  MODIFY COLUMN resolved_model VARCHAR(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL,
  MODIFY COLUMN status VARCHAR(32) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  MODIFY COLUMN usage_quality VARCHAR(32) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  MODIFY COLUMN upstream_request_count BIGINT UNSIGNED NOT NULL DEFAULT 0,
  MODIFY COLUMN provider_request_id VARCHAR(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL,
  MODIFY COLUMN error_code VARCHAR(32) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL,
  DROP COLUMN total_tokens,
  DROP COLUMN error_message,
  ADD INDEX idx_model_invocations_connection (connection_id, ended_at),
  ADD INDEX idx_model_invocations_plan (plan_id),
  ADD INDEX idx_model_invocations_running_updated (status, updated_at);

UPDATE runs
SET connection_id = provider_config_id
WHERE connection_id IS NULL AND provider_config_id IS NOT NULL;

ALTER TABLE runs
  DROP COLUMN provider_config_id;
