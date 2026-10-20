-- 169_recover_events_run_chat_idx.sql — as 167 for 168's index: a cancelled
-- concurrent build leaves an INVALID index that IF NOT EXISTS skips; rebuild
-- only an interrupted one. Transactional: the rebuild is conditional, which
-- needs a DO block, where REINDEX CONCURRENTLY cannot run.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_index WHERE indexrelid = to_regclass('events_run_chat_idx') AND NOT indisvalid) THEN
    REINDEX INDEX events_run_chat_idx;
  END IF;
END
$$;
