-- 164_recover_github_read_v2_indexes.sql — as 151 for 160's and 161's
-- indexes: a cancelled concurrent build leaves an INVALID index that IF NOT
-- EXISTS skips; rebuild only an interrupted one.
DO $$
DECLARE
  name text;
BEGIN
  FOREACH name IN ARRAY ARRAY['events_session_github_reads_v2_idx', 'events_task_github_reads_v2_idx'] LOOP
    IF EXISTS (SELECT 1 FROM pg_index WHERE indexrelid = to_regclass(name) AND NOT indisvalid) THEN
      EXECUTE format('REINDEX INDEX %I', name);
    END IF;
  END LOOP;
END
$$;
