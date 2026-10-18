-- 145_repository_setup_script.sql — a repository's setup script: bash a
-- Run executes once in each fresh checkout of it, before the agent starts
-- (phases.SetupInit), so a project can install its git hooks or anything
-- else a clone lacks. NULL: none. Bounded in bytes as the API bounds it.
ALTER TABLE repositories
  ADD COLUMN setup_script text,
  ADD CONSTRAINT repositories_setup_script_size CHECK (octet_length(setup_script) <= 16384);
