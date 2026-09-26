-- Delivery stopping for a person now waits for their decision, and says
-- which decisions fit (delivery.Escalation.Actions): try again, accept the
-- findings a review got stuck on, take what was merged, wait on pull
-- requests still open, or stop. Escalations recorded before say so too —
-- as a person can take them now: a pull request fix from then kept none of
-- its feedback, so trying it again is not offered.
UPDATE events SET payload = payload || jsonb_build_object('actions',
  CASE
    WHEN payload->>'reason' IN ('stuck', 'exhausted') THEN '["retry", "accept", "stop"]'::jsonb
    WHEN payload->>'reason' = 'pull_request_closed' AND COALESCE((payload->'detail'->>'open')::int, 0) > 0
      THEN '["done", "wait", "stop"]'::jsonb
    WHEN payload->>'reason' = 'pull_request_closed' THEN '["done", "stop"]'::jsonb
    WHEN payload->>'reason' IN ('pr_loop_exhausted', 'pr_fix_failed') THEN '["stop"]'::jsonb
    ELSE '["retry", "stop"]'::jsonb
  END)
WHERE event_type = 'question.asked' AND payload->>'kind' = 'escalation';
