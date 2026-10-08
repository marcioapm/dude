ALTER TABLE runs
  ADD COLUMN stuck_interrupted_at timestamptz,
  ADD COLUMN stuck_fingerprint text;

-- A brainstorm already hung in a call may write no more frames to date it.
-- Keep the first ledger sighting, falling back to the Run's start clocks.
UPDATE runs r SET
  open_tool_calls_at = COALESCE((SELECT jsonb_object_agg(c, to_jsonb(COALESCE(
      (SELECT min(e.occurred_at) FROM events e WHERE e.run_id = r.id AND e.event_type = 'agent.tool.called'
         AND e.payload->>'callId' = c),
      r.active_since, r.started_at, r.created_at)))
    FROM unnest(r.open_tool_calls) c), '{}')
WHERE r.role = 'brainstorm' AND r.phase IS NULL
  AND r.status IN ('scheduled', 'starting', 'running', 'paused')
  AND r.open_tool_calls_at = '{}';
