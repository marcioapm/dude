-- Ordered person membership is authoritative; legacy owner keys no longer
-- reorder membership or rotate when a credential is revoked.
DROP TRIGGER tasks_owner_person ON tasks;
DROP FUNCTION tasks_owner_person();
DROP TRIGGER api_keys_revoked ON api_keys;
DROP FUNCTION api_keys_revoked();

-- Fill only tasks without membership, never overwrite an existing assignment.
INSERT INTO task_people (task_id, person_id, organization_id, position, added_at)
SELECT t.id, p.id, t.organization_id, 0, t.created_at
FROM tasks t
JOIN api_keys k ON k.id = t.owner_key_id AND k.organization_id = t.organization_id
JOIN people p ON p.id = k.person_id AND p.organization_id = t.organization_id AND p.removed_at IS NULL
WHERE NOT EXISTS (SELECT 1 FROM task_people tp WHERE tp.task_id = t.id);

CREATE FUNCTION task_people_membership() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM tasks t WHERE t.id = NEW.task_id AND t.organization_id = NEW.organization_id)
     OR NOT EXISTS (SELECT 1 FROM people p WHERE p.id = NEW.person_id AND p.organization_id = NEW.organization_id) THEN
    RAISE EXCEPTION 'task membership must belong to the task and person organization' USING ERRCODE = '23514';
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER task_people_membership BEFORE INSERT OR UPDATE ON task_people
  FOR EACH ROW EXECUTE FUNCTION task_people_membership();

-- Historical key actors remain 'human'; keyless actors name people directly.
ALTER TABLE events DROP CONSTRAINT events_actor_type_check;
ALTER TABLE events ADD CONSTRAINT events_actor_type_check
  CHECK (actor_type IN ('system', 'human', 'person', 'agent', 'integration'));
