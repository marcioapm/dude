-- dude:no-transaction
-- 273_events_answer_causation.sql — compatibility writer for the causation
-- identifier used by the answer settlement indexes (#485).
--
-- The trigger commits before the later resumable backfill (277), so old
-- application writers cannot create new NULL causation_id rows in the gap.
-- It is narrowly scoped to the event types whose payload carries answerId.
CREATE FUNCTION events_answer_causation_compat() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS $$
BEGIN
  IF NEW.causation_id IS NULL AND NEW.event_type IN
      ('chat.message', 'pull_request.action', 'pull_request.answer_failed', 'pull_request.answer_claimed') THEN
    NEW.causation_id := COALESCE(NEW.payload->>'answerId', (NEW.payload->'github')->>'answerId');
  END IF;
  RETURN NEW;
END
$$;
CREATE TRIGGER events_answer_causation_compat
  BEFORE INSERT ON events FOR EACH ROW
  EXECUTE FUNCTION events_answer_causation_compat();
