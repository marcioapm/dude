-- 085_brainstorm_role.sql — the agent people brainstorm with in a session.
--
-- Its own file: a new enum value cannot be used in the transaction that
-- adds it, and 086 uses it in indexes and checks.
ALTER TYPE agent_role ADD VALUE IF NOT EXISTS 'brainstorm';
