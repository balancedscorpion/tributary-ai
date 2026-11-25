ALTER TABLE messages
  ADD COLUMN referenced_artifact_ids JSON NULL,
  ADD COLUMN context_summary LONGTEXT NULL,
  ADD COLUMN context_plan JSON NULL,
  ADD COLUMN hidden_context LONGTEXT NULL;
