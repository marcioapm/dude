-- 099_artifact_descriptions.sql — what a published file is for.
--
-- lux#77's `lux-shim publish --description TEXT` (dude publish
-- --description) carries one short line saying what the file is for. lux
-- keeps it with the artifact and reports it in artifact.published and its
-- listing; dude records it with the row and shows it under the name in a
-- task's and a session's Files. A file published without one, or through
-- an older lux, has ''.
ALTER TABLE artifacts ADD COLUMN description text NOT NULL DEFAULT '';
