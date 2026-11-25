ALTER TABLE runs
  ADD COLUMN purpose ENUM('analysis', 'search') NOT NULL DEFAULT 'analysis';

ALTER TABLE runs
  ADD COLUMN context_tokens_before INT NULL,
  ADD COLUMN context_tokens_after INT NULL,
  ADD COLUMN context_summary_buckets INT NULL,
  ADD COLUMN context_summary_rounds INT NULL,
  ADD COLUMN context_artifact_refs_loaded INT NULL,
  ADD COLUMN context_need_retrieval TINYINT(1) NULL,
  ADD COLUMN context_need_retrieval_tooling TINYINT(1) NULL,
  ADD COLUMN context_hard_capped TINYINT(1) NULL,
  ADD COLUMN context_plan JSON NULL;

