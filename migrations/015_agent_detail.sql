-- What a phase Run's agent is, and what it has used.
--
-- The chat shows which agent ran a phase (OpenCode, Claude Code, …) and its
-- model; aggregates (tokens over an epic) read the totals here rather than
-- summing events. dude's translation of each agent's output is the one place
-- that knows the agent's own format; these columns are already normalized.

ALTER TABLE runs
  -- Which coding agent ran it ('opencode', 'claude-code', 'codex', 'scripted'),
  -- and the model it was configured with. Set when the Run is handed to lux.
  ADD COLUMN harness            text,
  ADD COLUMN model              text,
  -- Token totals as the agent reported them, summed over its turns. Context
  -- is the latest size of the conversation, not a sum.
  ADD COLUMN input_tokens       bigint NOT NULL DEFAULT 0,
  ADD COLUMN output_tokens      bigint NOT NULL DEFAULT 0,
  ADD COLUMN cache_read_tokens  bigint NOT NULL DEFAULT 0,
  ADD COLUMN cache_write_tokens bigint NOT NULL DEFAULT 0,
  ADD COLUMN context_tokens     bigint NOT NULL DEFAULT 0,
  -- Thinking streamed since the last complete thought, saved with the
  -- cursor like the reply buffer.
  ADD COLUMN agent_thought_buffer text NOT NULL DEFAULT '',
  -- What the workflow decided this phase is about, carried on the Run so
  -- the prompt is built from the same decision rather than re-deriving it:
  -- the findings a fix addresses, and for a review, the severities the
  -- delivery's policy blocks on.
  ADD COLUMN finding_ids          text[] NOT NULL DEFAULT '{}',
  ADD COLUMN blocking_severities  text[] NOT NULL DEFAULT '{}';
