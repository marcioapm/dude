-- 155_child_output_fence.sql
ALTER TABLE runs
  ADD COLUMN child_output_generation bigint NOT NULL DEFAULT 0,
  ADD COLUMN child_output_closed boolean NOT NULL DEFAULT false;
