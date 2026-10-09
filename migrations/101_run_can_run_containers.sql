-- 101_run_can_run_containers.sql — whether a Run asked lux to let it start
-- containers inside it (sandbox.nestedContainers), recorded with the lux Run
-- it was submitted as: a library image as its version said, an image typed
-- by hand never, DUDE_AGENT_IMAGE as agent.nested_containers said then. A
-- resume keeps lux's stored spec and so this; a preview's new generation
-- records its own. NULL for a Run submitted before this column, which its
-- page shows nothing for.

ALTER TABLE runs ADD COLUMN can_run_containers boolean;
