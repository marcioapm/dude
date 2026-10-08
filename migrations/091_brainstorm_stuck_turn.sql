ALTER TABLE runs
  ADD COLUMN stuck_interrupted_at timestamptz,
  ADD COLUMN stuck_fingerprint text;
