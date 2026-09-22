-- What a phase Run needs to know about itself, and what the PR poller needs
-- to remember between polls.

-- Which reviewer flavour a review Run is. Recorded on the Run rather than
-- only in its creation event, because the claim route builds the prompt from
-- it and the findings route scopes re-review resolution by it.
ALTER TABLE runs ADD COLUMN category text;

-- Pull request feedback a fix Run must address: the comments and check
-- failures that woke it. On the Run so the prompt shows exactly what the
-- fixer was sent to fix, even after more feedback arrives.
ALTER TABLE runs ADD COLUMN pr_feedback jsonb NOT NULL DEFAULT '[]'::jsonb;

-- When the poller last asked the forge about this PR, and the newest comment
-- it has already handed to the workflow. Comments at or before the cursor
-- have been seen; a restart must not resend them as fresh feedback.
ALTER TABLE pull_requests
  ADD COLUMN last_polled_at     timestamptz,
  ADD COLUMN feedback_cursor    timestamptz;

-- The poller's hot path: open PRs due a look.
CREATE INDEX pull_requests_poll_idx ON pull_requests (last_polled_at NULLS FIRST)
  WHERE state IN ('draft', 'open');

GRANT SELECT, UPDATE ON pull_requests TO dude_sweeper;
