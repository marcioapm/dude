-- 262_pinned_memories.sql — a project's pinned facts.
--
-- A pinned memory is in front of every agent on its project: each
-- conductor briefing and each Run's first prompt carries the project's
-- pinned facts ("Project facts", delivery.ProjectFacts), bounded in code.
-- Only a project's own fact is pinned: never the organization's (it has
-- no project to brief), never a brainstorm session's (private to its
-- members), and never a note or procedure (task detail and long steps,
-- which search finds when the work needs them). A fact about a task is
-- refused too, in the orchestrator (memory.Pin), as memory_refs is
-- another table.
--
-- pinned_at: when it was pinned, NULL for not; pinned_by_*: who pinned it,
-- a person or an agent's Run, as the briefing names them. Every existing
-- row is unpinned, so the CHECK is added NOT VALID, as 235's are: no scan
-- of memories under ACCESS EXCLUSIVE, and it holds for every row written.
ALTER TABLE memories
  ADD COLUMN pinned_at timestamptz,
  ADD COLUMN pinned_by_person_id text REFERENCES people(id) ON DELETE SET NULL,
  ADD COLUMN pinned_by_run_id text REFERENCES runs(id) ON DELETE SET NULL,
  ADD CONSTRAINT memories_pinned_project_fact CHECK (
    pinned_at IS NULL OR (project_id IS NOT NULL AND session_id IS NULL AND kind = 'fact')) NOT VALID;

-- What a briefing reads: a project's live pinned facts.
CREATE INDEX memories_pinned_idx ON memories (project_id, pinned_at)
  WHERE pinned_at IS NOT NULL AND archived_at IS NULL;
