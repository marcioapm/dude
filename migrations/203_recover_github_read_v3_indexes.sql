-- 203_recover_github_read_v3_indexes.sql — as 167 for 197's to 199's
-- indexes: a cancelled concurrent build leaves an INVALID index that IF NOT
-- EXISTS skips; rebuild only an interrupted one.
DO $$
DECLARE
  name text;
BEGIN
  FOREACH name IN ARRAY ARRAY['events_session_github_reads_v3_idx', 'events_task_github_reads_v3_idx',
      'events_automation_github_reads_v2_idx'] LOOP
    IF EXISTS (SELECT 1 FROM pg_index WHERE indexrelid = to_regclass(name) AND NOT indisvalid) THEN
      EXECUTE format('REINDEX INDEX %I', name);
    END IF;
  END LOOP;
END
$$;
