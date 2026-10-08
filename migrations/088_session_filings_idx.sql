-- 088_session_filings_idx.sql — a session's filings, by session: the
-- sessions list counts each session's filed items, which without this
-- index scans every filing of the organisation once per session listed.

CREATE INDEX session_filings_session_idx ON session_filings (session_id);
