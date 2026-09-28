-- Photos live in object storage (DUDE_S3_BUCKET), never in the database:
-- a row keeps the object's key and the token its URL carries. People's
-- photos move there, and projects get one (docs/design/calmer-build.md,
-- "Project faces").
--
-- photo_url is left for an https URL someone gave instead of uploading.
-- Inline data: images never shipped (035 came with this branch); any a
-- development database holds are dropped, and none can be stored again.

ALTER TABLE people ADD COLUMN photo_key text;
UPDATE people SET photo_url = NULL, photo_token = NULL WHERE photo_url LIKE 'data:%';
ALTER TABLE people ADD CONSTRAINT people_photo_url_https CHECK (photo_url IS NULL OR photo_url LIKE 'https://%');

ALTER TABLE projects ADD COLUMN image_key text,
                     ADD COLUMN image_token text;

-- A person as every API response names one (PersonRef). A stored photo is
-- served by the backend under its token, so lists carry a short URL.
CREATE OR REPLACE FUNCTION person_ref(p people) RETURNS json LANGUAGE sql STABLE AS $$
  SELECT json_build_object('id', p.id, 'name', p.name,
    'photoUrl', CASE WHEN p.photo_key IS NOT NULL THEN '/v1/people/' || p.id || '/photo?t=' || p.photo_token
                     ELSE p.photo_url END,
    'online', p.removed_at IS NULL AND COALESCE(p.last_seen_at > now() - interval '5 minutes', false))
$$;

-- A photo for an <img>, which sends no credentials: its object's key,
-- found by the person and the token only a signed-in reader was given.
DROP FUNCTION person_photo(text, text);
CREATE FUNCTION person_photo(p_id text, p_token text) RETURNS text
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp
AS $$
  SELECT photo_key FROM people WHERE id = p_id AND photo_token = p_token AND removed_at IS NULL
$$;
REVOKE ALL ON FUNCTION person_photo(text, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION person_photo(text, text) TO dude_app;

-- The same for a project's image.
CREATE FUNCTION project_image(p_id text, p_token text) RETURNS text
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp
AS $$
  SELECT image_key FROM projects WHERE id = p_id AND image_token = p_token
$$;
REVOKE ALL ON FUNCTION project_image(text, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION project_image(text, text) TO dude_app;
