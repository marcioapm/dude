-- A Run lux lost while it waited on a person keeps that person's
-- question: dude moves it to the Run that replaces it, in the same
-- transaction (delivery.ReplaceLostConductorTx). The replacement can only
-- be inserted once the lost Run has ended (runs_live_conductor_idx), and a
-- question can only name it once it exists (questions.run_id's key), so the
-- question is left open on the lost Run until the move, and cancelled at
-- commit only if nothing moved it. (Migration 250; 248 and 249 are taken
-- by other branches.)
ALTER TABLE runs ADD COLUMN lux_lost_at timestamptz;

CREATE OR REPLACE FUNCTION cancel_open_questions() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  -- Lost by lux: settled at commit (cancel_lost_questions), after the move.
  IF NEW.lux_lost_at IS NOT NULL THEN
    RETURN NEW;
  END IF;
  UPDATE questions SET status = 'cancelled' WHERE run_id = NEW.id AND status = 'open';
  RETURN NEW;
END $$;

-- What is still open on a lost Run at commit was not moved: it dies with
-- it, as 015 has every question die with its Run.
CREATE FUNCTION cancel_lost_questions() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  UPDATE questions SET status = 'cancelled' WHERE run_id = NEW.id AND status = 'open';
  RETURN NULL;
END $$;

CREATE CONSTRAINT TRIGGER runs_cancel_lost_questions
  AFTER UPDATE OF status ON runs
  DEFERRABLE INITIALLY DEFERRED
  FOR EACH ROW WHEN (NEW.status IN ('completed', 'failed', 'aborted') AND OLD.status IS DISTINCT FROM NEW.status
                     AND NEW.lux_lost_at IS NOT NULL)
  EXECUTE FUNCTION cancel_lost_questions();
