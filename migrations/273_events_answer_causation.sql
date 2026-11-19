-- dude:no-transaction
-- 273_events_answer_causation.sql — compatibility writer for the causation
-- identifier used by the answer settlement indexes (#485).
--
-- The trigger commits before the later resumable backfill (277), so old
-- application writers cannot create new NULL causation_id rows in the gap.
-- It is narrowly scoped to the event types whose payload carries answerId.
--
-- The runner sends this file as one simple-Query message, which Postgres
-- runs as one implicit transaction, so even dropping the trigger and then
-- creating it would leave no moment without it under the runner.
-- CREATE OR REPLACE TRIGGER (PG14+) is still the better form: replacing it
-- takes a lock that does not block reads (SHARE ROW EXCLUSIVE), where a
-- drop takes one that does (ACCESS EXCLUSIVE), and with the function
-- replaced in place too, there is no gap even when the statements run one
-- at a time, outside the runner.
CREATE OR REPLACE FUNCTION events_answer_causation_compat() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS $$
BEGIN
  IF NEW.causation_id IS NULL AND NEW.event_type IN
      ('chat.message', 'pull_request.action', 'pull_request.answer_failed', 'pull_request.answer_claimed') THEN
    NEW.causation_id := COALESCE(NEW.payload->>'answerId', (NEW.payload->'github')->>'answerId');
  END IF;
  RETURN NEW;
END
$$;
CREATE OR REPLACE TRIGGER events_answer_causation_compat
  BEFORE INSERT ON events FOR EACH ROW
  EXECUTE FUNCTION events_answer_causation_compat();
