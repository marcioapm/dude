-- dude:no-transaction
-- A review-only verdict's claims: whether dude's review under a claim was
-- made, submitted, refused or downgraded (delivery.claimState, read on
-- every post attempt of the verdict, and again after an ambiguous answer).
-- events_type_idx finds them only among every claim of the organization,
-- and the task's indexes among every other event of the task; with many
-- of either the plan is a sequential scan. This finds the task's claim
-- events by their claim id at once.
-- Built concurrently, so events are written throughout; a file of its own,
-- outside a transaction. IF NOT EXISTS makes a retry after a crash before it
-- was recorded a no-op.
CREATE INDEX CONCURRENTLY IF NOT EXISTS events_task_review_claims_idx ON events (task_id, (payload->>'claimId'))
  WHERE event_type IN ('pull_request.review_claimed', 'pull_request.review_submitted', 'pull_request.review_failed',
    'pull_request.review_downgraded');
