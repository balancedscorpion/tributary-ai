-- Stable run-scoped query obligations over run_query_dependencies. Each
-- additive statement is isolated so partially applied MySQL DDL can be
-- replayed safely. Legacy rows retain NULL until an idempotent bind/advance
-- hydrates them. MySQL permits multiple NULL unique keys during rollout.

ALTER TABLE run_query_dependencies ADD COLUMN query_obligation_id VARCHAR(96) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL AFTER validation_state;
ALTER TABLE run_query_dependencies ADD COLUMN active_query_obligation_id VARCHAR(96) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin GENERATED ALWAYS AS (CASE WHEN detached=0 AND required_for_completion=1 THEN query_obligation_id ELSE NULL END) STORED AFTER replacement_job_id;
ALTER TABLE run_query_dependencies ADD UNIQUE INDEX uniq_run_query_active_obligation (run_id, active_query_obligation_id);
ALTER TABLE run_query_dependencies ADD INDEX idx_run_query_obligation_attempts (run_id, query_obligation_id, created_at, job_id);
