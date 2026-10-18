-- A cancelled concurrent build leaves an INVALID index that IF NOT EXISTS
-- skips. Rebuild only interrupted indexes here; reads continue while writes
-- wait for the transactional REINDEX, as for the keys in migration 142.
DO $$
DECLARE
  name text;
BEGIN
  FOREACH name IN ARRAY ARRAY['events_session_github_reads_idx', 'events_task_github_reads_idx'] LOOP
    IF EXISTS (SELECT 1 FROM pg_index WHERE indexrelid = to_regclass(name) AND NOT indisvalid) THEN
      EXECUTE format('REINDEX INDEX %I', name);
    END IF;
  END LOOP;
END
$$;
