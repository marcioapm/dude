-- 237_sweeper_conductor_tier.sql — the sweep's wake probe
-- (delivery.Wakeable) passes over a quiet delivery whose conductor has no
-- model (delivery.quietNoModel): it resolves the conductor's tier from the
-- task's project and organization (already granted) to model_tiers. Only
-- the columns that needs: a task's project, a tier's organization and model.
GRANT SELECT (id, project_id) ON tasks TO dude_sweeper;
GRANT SELECT (id, organization_id, model) ON model_tiers TO dude_sweeper;
