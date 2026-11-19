-- dude:no-transaction
-- A poster's claims on a fixer's held answers (#485,
-- pull_request.answer_claimed), by Run and the answer (causation_id, 273),
-- latest first: the claim lookup before each write, the in-flight check,
-- and the delivery end's read. Apart from 274 so neither index's readers
-- filter the other's rows. Built concurrently, a file of its own, IF NOT
-- EXISTS, as 274.
CREATE INDEX CONCURRENTLY IF NOT EXISTS events_run_answer_claims_idx ON events (run_id, causation_id, cursor)
  WHERE event_type = 'pull_request.answer_claimed';
