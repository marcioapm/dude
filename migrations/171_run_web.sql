-- 171_run_web.sql — what a brainstorm Run's harness reaches of the web
-- ({search, fetch, anyHost, hosts}), as its spec configured it at submit:
-- the session's next agent is told when its own differs. NULL for every
-- other Run, and for a brainstorm submitted before this.
ALTER TABLE runs ADD COLUMN web jsonb;
