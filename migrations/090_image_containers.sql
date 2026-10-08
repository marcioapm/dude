-- 090_image_containers.sql — "Can run containers", a property of an image
-- version like its Containerfile.
--
-- A version that can run containers is checked by the builder once its
-- dude layer is on (an engine, fuse-overlayfs, newuidmap/newgidmap with
-- their file capabilities, subuid/subgid entries for agent) and fails its
-- build when it cannot; every Run of it, agent or preview, asks lux for
-- sandbox.nestedContainers. Design: docs/design/images.md.

ALTER TABLE image_versions ADD COLUMN can_run_containers boolean NOT NULL DEFAULT false;

-- The container check of a build or finish that ran one: what it found in
-- one line (what passed, or "Missing: …"), and how long it took.
ALTER TABLE image_builds
  ADD COLUMN containers_check text,
  ADD COLUMN check_seconds double precision;

-- image_publish as in 068, the rebuild it queues keeping the published
-- version's can_run_containers with its Containerfile.
CREATE OR REPLACE FUNCTION image_publish(p_version text)
RETURNS TABLE (image_id text, image_name text, version_id text, version integer, build_id text)
LANGUAGE plpgsql AS $$
DECLARE
  v image_versions;
  parent images;
  child record;
  nv text;
  nb text;
  n integer;
BEGIN
  SELECT * INTO v FROM image_versions WHERE id = p_version FOR UPDATE;
  IF NOT FOUND THEN RAISE EXCEPTION 'no image version %', p_version USING ERRCODE = 'P0002'; END IF;
  IF v.user_ref IS NULL THEN RAISE EXCEPTION 'version % has not been built', p_version USING ERRCODE = '22023'; END IF;
  SELECT * INTO parent FROM images WHERE id = v.image_id FOR UPDATE;
  UPDATE image_versions SET state = 'superseded', updated_at = now()
    WHERE image_versions.image_id = v.image_id AND state = 'published' AND id <> v.id;
  UPDATE image_versions SET state = 'published', updated_at = now(), error = NULL WHERE id = v.id;
  UPDATE images SET published_version_id = v.id WHERE id = v.image_id;
  FOR child IN
    SELECT c.id, c.name, c.organization_id, cv.containerfile, cv.build_args, cv.can_run_containers
    FROM images c
    JOIN image_versions cv ON cv.id = c.published_version_id
    WHERE c.organization_id = v.organization_id
      AND EXISTS (SELECT 1 FROM image_version_parents p WHERE p.version_id = cv.id AND p.parent_image_id = v.image_id)
      AND NOT EXISTS (SELECT 1 FROM image_versions w WHERE w.image_id = c.id AND w.state = 'queued')
    ORDER BY c.name
    FOR UPDATE OF c
  LOOP
    SELECT COALESCE(max(number), 0) + 1 INTO n FROM image_versions WHERE image_versions.image_id = child.id;
    nv := new_id('imv');
    nb := new_id('imb');
    INSERT INTO image_versions (id, organization_id, image_id, number, containerfile, build_args, can_run_containers, note, source, state)
    VALUES (nv, child.organization_id, child.id, n, child.containerfile, child.build_args, child.can_run_containers,
            'Rebuild on ' || parent.name || ' v' || v.number, 'base_rebuild', 'queued');
    INSERT INTO image_version_parents (organization_id, version_id, parent_image_id)
    SELECT child.organization_id, nv, p.parent_image_id FROM image_version_parents p
    JOIN images ci ON ci.published_version_id = p.version_id AND ci.id = child.id;
    INSERT INTO image_builds (id, organization_id, image_version_id, kind) VALUES (nb, child.organization_id, nv, 'build');
    image_id := child.id; image_name := child.name; version_id := nv; version := n; build_id := nb;
    RETURN NEXT;
  END LOOP;
END $$;
