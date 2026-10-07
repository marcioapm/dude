import { afterAll, beforeAll, expect, test } from "bun:test";
import { SQL } from "bun";
import { migrate } from "../src/db/migrate.ts";
import { closePool, setPool, withOrg } from "../src/db/client.ts";
import { authenticate, createApiKey } from "../src/api/auth.ts";
import { Router, type RequestContext } from "../src/api/router.ts";
import { personOf, registerPeopleRoutes, setTaskPeople } from "../src/api/routes/people.ts";

// Removing a person while a task they follow has its people replaced: the
// removal route as served, against putTaskPeople's statements held open
// after its task lock (structure.ts), so the two overlap where a removal
// that locked its tasks under the person's row deadlocked.

const ownerUrl = process.env.DATABASE_URL!;
const name = `dude_removal_locks_${Bun.randomUUIDv7("hex").slice(-12)}`;
const org = "org_removal_locks";
const project = "prj_removal_locks";
let admin: SQL;
let owner: SQL;
let app: SQL;
let adminKey: string;
let adminPersonId: string;
const router = new Router(credential => authenticate(credential));
registerPeopleRoutes(router);

function dbUrl(appRole = false) {
  const url = new URL(ownerUrl);
  url.pathname = `/${name}`;
  if (appRole) { url.username = "dude_app"; url.password = "dude_app"; }
  return url.toString();
}

function ctx(): RequestContext {
  const url = new URL("http://dude.test/v1/tasks/tsk_locks/people");
  return { principal: { credentialKind: "person", organizationId: org, personId: adminPersonId, kind: "user", name: "Admin",
    role: "admin" } as RequestContext["principal"], url, params: {}, request: new Request(url.toString()) };
}

// Resolves once a backend of this database waits on a lock.
async function lockWait(): Promise<"waiting"> {
  const deadline = Date.now() + 10_000;
  while (Date.now() < deadline) {
    const [row] = await admin`SELECT count(*)::int AS n FROM pg_stat_activity
      WHERE datname = ${name} AND wait_event_type = 'Lock'`;
    if (row.n > 0) return "waiting";
    await Bun.sleep(10);
  }
  throw new Error("timed out waiting for a lock waiter");
}

beforeAll(async () => {
  admin = new SQL(ownerUrl);
  await admin.unsafe(`CREATE DATABASE "${name}"`);
  await migrate(dbUrl(), { log() {} });
  owner = new SQL(dbUrl());
  await owner`INSERT INTO organizations (id, name, slug) VALUES (${org}, ${org}, ${org})`;
  await owner`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES (${project}, ${org}, 'Locks', 'locks', 'LK')`;
  app = new SQL(dbUrl(true));
  setPool(app);
  const made = await createApiKey({ organizationId: org, name: "Admin" });
  adminKey = made.key;
  adminPersonId = made.personId;
});
afterAll(async () => {
  await closePool(app);
  await owner?.end();
  await admin?.unsafe(`DROP DATABASE IF EXISTS "${name}" WITH (FORCE)`);
  await admin?.end();
});

test("removing a person while their task's people are replaced does not deadlock", async () => {
  const leaving = "per_locks_leaving";
  const first = "per_locks_first";
  const added = "per_locks_added";
  await owner`INSERT INTO people (id, organization_id, name)
    VALUES (${leaving}, ${org}, 'Leaving'), (${first}, ${org}, 'First'), (${added}, ${org}, 'Added')`;
  await owner`INSERT INTO tasks (id, organization_id, project_id, number, title) VALUES ('tsk_locks', ${org}, ${project}, 1, 'Locks')`;
  await owner`INSERT INTO task_people (task_id, person_id, organization_id, position)
    VALUES ('tsk_locks', ${first}, ${org}, 0), ('tsk_locks', ${leaving}, ${org}, 1)`;

  let lockedTask!: () => void;
  const taskLocked = new Promise<void>(resolve => { lockedTask = resolve; });
  let proceed!: () => void;
  const released = new Promise<void>(resolve => { proceed = resolve; });
  // putTaskPeople: the task's row, its people checked while the departing
  // one is still active, then the list replaced, keeping them on it.
  const replaced = withOrg(org, async scope => {
    await scope.sql`SELECT project_id FROM tasks WHERE id = 'tsk_locks' FOR UPDATE`;
    const ids = [await personOf(scope, first), await personOf(scope, leaving), await personOf(scope, added)] as string[];
    lockedTask();
    await released;
    await setTaskPeople(scope, ctx(), "tsk_locks", project, ids, false);
    return "replaced";
  }).catch((e: unknown) => e);
  await taskLocked;
  const removal = router.handle(new Request(`http://dude.test/v1/people/${leaving}`, {
    method: "DELETE", headers: { authorization: `Bearer ${adminKey}` } }));
  // The removal either finishes or waits on the task's row; the
  // replacement goes on from there.
  await Promise.race([removal, lockWait()]);
  proceed();
  const [removed, replacement] = await Promise.all([removal, replaced]);
  // A deadlock victim is either side: the replacement's error (SQLSTATE
  // 40P01 in errno), or the removal's 500.
  expect((replacement as { errno?: string })?.errno).not.toBe("40P01");
  expect(removed.status).toBe(204);
  expect(replacement).toBe("replaced");
  const [gone] = await owner`SELECT removed_at IS NOT NULL AS removed FROM people WHERE id = ${leaving}`;
  expect(gone.removed).toBe(true);
});
