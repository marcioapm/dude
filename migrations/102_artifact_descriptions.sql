-- 102_artifact_descriptions.sql — what a published file is for.
--
-- The one line `dude publish --description` gave, as lux reports it; ''
-- when there was none or lux is older than artifact-publish (lux#77).
ALTER TABLE artifacts ADD COLUMN description text NOT NULL DEFAULT '';
