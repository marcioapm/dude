-- 170_child_results_verify.sql — a verifier child's results (verify): each
-- command it ran, its exit code and the tail of its output, read from its
-- reply at settlement and given to its parent with the child's result.
-- NULL for every other child.
ALTER TABLE child_results ADD COLUMN verify jsonb;
