-- The settlement anti-join probes child_run_id without the composite key's leading columns.
CREATE INDEX child_results_child_idx ON child_results (child_run_id);
