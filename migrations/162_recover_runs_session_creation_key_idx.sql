-- A cancelled concurrent build leaves an INVALID index that IF NOT EXISTS
-- skips. Rebuild only this interrupted index here; reads continue while writes
-- wait for the transactional REINDEX, as for the keys in migration 142.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_index WHERE indexrelid = to_regclass('runs_session_creation_key_idx') AND NOT indisvalid) THEN
    REINDEX INDEX runs_session_creation_key_idx;
  END IF;
END
$$;
