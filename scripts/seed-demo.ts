/**
 * Seed an organization for the local demo: several people in one
 * organization, a project and tasks owned by them.
 *
 * Organizations and the first API key are provisioned, not self-served, so
 * this is a deliberate out-of-band step rather than an API call. The tasks
 * are written as they would be mid-flight (some waiting on a person, one
 * shared by two), so the sidebar, the board and the inbox have something to
 * show: who is online, what waits on you, what waits on others.
 *
 * Prints JSON on stdout: the organization, a user key to sign in with (the
 * admin's), and every person's key.
 */

import { SQL } from "bun";
import { newId } from "@dude/domain";
import { insertPerson } from "../apps/control-plane/src/api/auth.ts";
import { closePool, setPool, withOrg } from "../apps/control-plane/src/db/client.ts";

const ownerDsn = process.env.OWNER_DSN;
const appDsn = process.env.DATABASE_URL;

if (!ownerDsn || !appDsn) {
  console.error("OWNER_DSN and DATABASE_URL are required");
  process.exit(1);
}

/** The demo's people; the first is you, the organization's admin. Those somewhere are online. */
const PEOPLE = [
  { name: "Ana Costa", email: "ana@demo.test", role: "admin", lastSeenWhere: "Board" },
  { name: "Ben Okafor", email: "ben@demo.test", role: "member", lastSeenWhere: "WEB-2" },
  { name: "Chloé Martin", email: "chloe@demo.test", role: "member", lastSeenWhere: "Settings" },
  { name: "Dev Patel", email: "dev@demo.test", role: "member", lastSeenWhere: null },
] as const;

/** Tasks, each with its people as indexes into PEOPLE, the owner first. */
const TASKS: Array<{ title: string; status: string; people: number[]; epic: number | null }> = [
  { title: "Sign in with Google", status: "awaiting_confirmation", people: [0], epic: 0 },
  { title: "Members can be invited by email", status: "running", people: [0, 1], epic: 0 },
  { title: "Rate-limit the public API", status: "awaiting_confirmation", people: [1], epic: 1 },
  { title: "Profile photos on S3", status: "review", people: [2, 0], epic: 0 },
  { title: "Paginate the audit log", status: "awaiting_confirmation", people: [3], epic: 1 },
  { title: "Dark mode for the settings pages", status: "queued", people: [2], epic: null },
  { title: "Retry webhooks with backoff", status: "done", people: [3], epic: 1 },
];

const EPICS = ["Team", "Platform"];

const owner = new SQL(ownerDsn);
const organizationId = newId("organization");
const slug = `demo-${Date.now().toString(36)}`;

try {
  // The trigger seeds Thinker, Coder and Fast with no model (migration 066):
  // the reviewer's Thinker asks for a real one, the implementer's Coder plays
  // the scripted agent.
  await owner`INSERT INTO organizations (id, name, slug) VALUES (${organizationId}, 'Demo', ${slug})`;
  await owner`UPDATE model_tiers SET model = CASE name WHEN 'Thinker' THEN 'claude-sonnet-5' WHEN 'Coder' THEN 'fake/scripted' END
              WHERE organization_id = ${organizationId} AND name IN ('Thinker', 'Coder')`;

  // Written through the app role, so the same row-level security that
  // protects production applies to the demo's data too.
  setPool(new SQL(appDsn));

  const people = await withOrg(organizationId, async (scope) => {
    const { sql } = scope;
    const made: Array<{ personId: string; keyId: string; key: string }> = [];
    for (const p of PEOPLE) made.push((await insertPerson(scope, p))!);

    const projectId = newId("project");
    await sql`
      INSERT INTO projects (id, organization_id, name, slug, key_prefix, description, agent_models, next_task_number)
      VALUES (${projectId}, ${organizationId}, 'Website', 'web', 'WEB', 'The public website and its API',
              '{}'::jsonb, ${TASKS.length + 1})`;
    const epicIds: string[] = [];
    for (const [position, title] of EPICS.entries()) {
      const id = newId("epic");
      epicIds.push(id);
      await sql`
        INSERT INTO epics (id, organization_id, project_id, title, position)
        VALUES (${id}, ${organizationId}, ${projectId}, ${title}, ${position})`;
    }

    for (const [i, t] of TASKS.entries()) {
      const taskId = newId("task");
      // Ownership is task_people position 0; owner_key_id is only the legacy mirror.
      await sql`
        INSERT INTO tasks (id, organization_id, project_id, number, epic_id, title, status, owner_key_id)
        VALUES (${taskId}, ${organizationId}, ${projectId}, ${i + 1},
                ${t.epic === null ? null : epicIds[t.epic]!}, ${t.title}, ${t.status}::task_status,
                ${made[t.people[0]!]!.keyId})`;
      for (const [position, person] of t.people.entries()) {
        await sql`
          INSERT INTO task_people (task_id, person_id, organization_id, position)
          VALUES (${taskId}, ${made[person]!.personId}, ${organizationId}, ${position})`;
      }
    }
    return made;
  });

  console.log(JSON.stringify({
    organizationId,
    userKey: people[0]!.key,
    people: PEOPLE.map((p, i) => ({ name: p.name, email: p.email, role: p.role, key: people[i]!.key })),
  }));
} finally {
  await owner.end();
  await closePool();
}
