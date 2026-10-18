-- Retain each commit lux delivered, including imports before a later unmerged fetch.
ALTER TABLE runs ADD COLUMN sync_history jsonb NOT NULL DEFAULT '{}';
