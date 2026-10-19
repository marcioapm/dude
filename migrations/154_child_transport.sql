-- 154_child_transport.sql
ALTER TABLE child_result_outbox
  ADD COLUMN directive_id text REFERENCES directives(id),
  ADD COLUMN claimed_by text,
  ADD COLUMN claimed_until timestamptz;
CREATE UNIQUE INDEX child_result_directive_idx ON child_result_outbox (directive_id) WHERE directive_id IS NOT NULL;
CREATE INDEX child_result_pending_idx ON child_result_outbox (created_at)
  WHERE consumed_at IS NULL AND stale_at IS NULL;
GRANT SELECT ON child_results, child_result_outbox TO dude_sweeper;
