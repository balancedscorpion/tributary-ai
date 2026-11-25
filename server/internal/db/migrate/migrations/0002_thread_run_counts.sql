-- Track run count per thread and when the rolling summary was last refreshed.

ALTER TABLE threads
  ADD COLUMN run_count BIGINT NOT NULL DEFAULT 0,
  ADD COLUMN summary_run_count BIGINT NOT NULL DEFAULT 0;

