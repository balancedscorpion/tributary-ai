ALTER TABLE runs
  ADD COLUMN context_evidence_budget INT NULL,
  ADD COLUMN context_evidence_used INT NULL,
  ADD COLUMN context_evidence_exhausted TINYINT(1) NULL;
