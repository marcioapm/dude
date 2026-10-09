/**
 * A project's key is unique in its organisation (migration 095). Creating a
 * project takes an optional key: one another project has is refused with
 * 409 and a free suggestion; none given, dude derives the first free one of
 * projectKeyCandidates (@dude/domain). Two creates racing for one key get a
 * 409 or another derived key, never a 500.
 *
 * Requires DATABASE_URL: the owner of a database migrated to the release.
 */

import { afterAll, beforeAll, expect, test } from "bun:test";
import { SQL } from "bun";
import { personPrincipal } from "../src/api/auth.ts";
import { Router } from "../src/api/router.ts";
import { registerProjectRoutes } from "../src/api/routes/projects.ts";
import { registerWorkRoutes } from "../src/api/routes/work.ts";
import { closePool, setPool } from "../src/db/client.ts";

const org = `org_keys_${Bun.randomUUIDv7("hex").slice(-12)}`;
const person = `${org}_person`;
let owner: SQL;
let app: SQL;
let router: Router;

beforeAll(async () => {
  owner = new SQL(process.env.DATABASE_URL!);
  const url = new URL(process.env.DATABASE_URL!);
  url.username = "dude_app"; url.password = "dude_app";
  app = new SQL(url.toString()); setPool(app);
  await owner`INSERT INTO organizations (id, name, slug) VALUES (${org}, ${org}, ${org})`;
  await owner`INSERT INTO people (id, organization_id, name, role) VALUES (${person}, ${org}, 'Person', 'admin')`;
  const principal = await personPrincipal(org, person);
  router = new Router(async () => principal);
  registerProjectRoutes(router);
  registerWorkRoutes(router);
});
afterAll(async () => {
  await closePool(app);
  await owner`DELETE FROM organizations WHERE id = ${org}`;
  await owner.end();
});

const call = (method: string, path: string, body: Record<string, unknown>) => router.handle(new Request(`http://dude.test${path}`, {
  method, headers: { "content-type": "application/json" }, body: JSON.stringify(body),
}));
const create = (name: string, slug: string, key?: string) => call("POST", "/v1/projects", { name, slug, ...(key === undefined ? {} : { key }) });
type Refusal = { error: { code: string; message: string; details?: { suggestion?: string } } };
const keys = async () => (await owner`SELECT slug, key_prefix FROM projects WHERE organization_id = ${org} ORDER BY created_at, slug`)
  .map((r: { slug: string; key_prefix: string }) => [r.slug, r.key_prefix]);

test("no key given: billing-api is BILL, billing-worker the next free one, BWOR, and the API says so", async () => {
  const api = await create("Billing API", "billing-api");
  expect(api.status).toBe(201);
  expect(((await api.json()) as { key: string }).key).toBe("BILL");
  const worker = await create("Billing Worker", "billing-worker");
  expect(worker.status).toBe(201);
  expect(((await worker.json()) as { key: string }).key).toBe("BWOR");
  expect(await keys()).toEqual([["billing-api", "BILL"], ["billing-worker", "BWOR"]]);
});

test("a key another project has, in any case, is refused with 409 naming it and a free suggestion; nothing is made", async () => {
  for (const key of ["BILL", "bill", "Bill"]) {
    const res = await create("Billing Ledger", "billing-ledger", key);
    expect(res.status).toBe(409);
    const body = (await res.json()) as Refusal;
    expect(body.error.message).toBe("BILL is already the key of Billing API; pick another");
    expect(body.error.details).toEqual({ suggestion: "BLED" });
  }
  expect((await owner`SELECT 1 FROM projects WHERE slug = 'billing-ledger'`).length).toBe(0);
  // The suggestion is free: taking it works.
  const taken = await create("Billing Ledger", "billing-ledger", "BLED");
  expect(taken.status).toBe(201);
  expect(((await taken.json()) as { key: string }).key).toBe("BLED");
});

test("a chosen key is upper-cased; one that is not 2 to 6 letters or digits starting with a letter is refused with 400", async () => {
  const chosen = await create("Payments", "payments", "pay2");
  expect(chosen.status).toBe(201);
  expect(((await chosen.json()) as { key: string }).key).toBe("PAY2");
  for (const key of ["P", "2PAY", "PAYMENT", "PA-Y", "PA Y", "", "PÁY"]) {
    const res = await create("Bad", `bad-${Bun.randomUUIDv7("hex").slice(-8)}`, key);
    expect(res.status).toBe(400);
    expect(((await res.json()) as Refusal).error.message)
      .toBe("request body failed validation: key: key must be 2 to 6 letters or digits, starting with a letter");
  }
  expect((await owner`SELECT 1 FROM projects WHERE organization_id = ${org} AND name = 'Bad'`).length).toBe(0);
});

test("a project with a digit in its key numbers its tasks under it: PAY2-1", async () => {
  const [project] = await owner`SELECT id FROM projects WHERE organization_id = ${org} AND key_prefix = 'PAY2'` as Array<{ id: string }>;
  const res = await call("POST", "/v1/tasks", { projectId: project!.id, title: "First", goal: "A goal long enough to be a goal." });
  expect(res.status).toBe(201);
  expect(((await res.json()) as { key: string }).key).toBe("PAY2-1");
});

test("a project's key is fixed: PATCH with key or key_prefix answers 200 and leaves it as it was", async () => {
  const [project] = await owner`SELECT id FROM projects WHERE organization_id = ${org} AND key_prefix = 'PAY2'` as Array<{ id: string }>;
  for (const body of [{ key: "NEW2" }, { key: "NEW2", name: "Payments Renamed" }, { key_prefix: "NEW2" }]) {
    const res = await call("PATCH", `/v1/projects/${project!.id}`, body);
    expect(res.status).toBe(200);
    expect(((await res.json()) as { key: string }).key).toBe("PAY2");
  }
  expect([...await owner`SELECT name, slug, key_prefix FROM projects WHERE id = ${project!.id}`])
    .toEqual([{ name: "Payments Renamed", slug: "payments", key_prefix: "PAY2" }]);
});

const BARRIER_LOCK = 920_092;

/**
 * Runs two creates so that both read the taken keys before either inserts:
 * a BEFORE INSERT trigger on this organisation's projects waits for an
 * advisory lock this test holds, and the lock is released only once both
 * inserts are blocked on it.
 */
async function bothReadThenInsert(requests: () => Array<Promise<Response>>): Promise<Response[]> {
  const fn = `hold_${org}`;
  await owner.unsafe(`CREATE FUNCTION ${fn}() RETURNS trigger LANGUAGE plpgsql AS $$
    BEGIN IF NEW.organization_id = '${org}' THEN PERFORM pg_advisory_xact_lock(${BARRIER_LOCK}); END IF; RETURN NEW; END $$`);
  await owner.unsafe(`CREATE TRIGGER ${fn} BEFORE INSERT ON projects FOR EACH ROW EXECUTE FUNCTION ${fn}()`);
  const holder = await owner.reserve();
  let held = false;
  try {
    await holder`SELECT pg_advisory_lock(${BARRIER_LOCK})`;
    held = true;
    const pending = Promise.all(requests());
    const deadline = Date.now() + 15_000;
    for (;;) {
      const [row] = (await owner`
        SELECT count(*)::int AS waiting FROM pg_stat_activity
        WHERE datname = current_database() AND wait_event_type = 'Lock' AND wait_event = 'advisory'
          AND query LIKE '%INSERT INTO projects%'`) as Array<{ waiting: number }>;
      const waiting = row?.waiting ?? 0;
      if (waiting >= 2) break;
      if (Date.now() > deadline) throw new Error(`only ${waiting} of 2 project inserts reached the barrier in 15 s`);
      await Bun.sleep(20);
    }
    await holder`SELECT pg_advisory_unlock(${BARRIER_LOCK})`;
    held = false;
    return await pending;
  } finally {
    if (held) await holder`SELECT pg_advisory_unlock(${BARRIER_LOCK})`;
    holder.release();
    await owner.unsafe(`DROP TRIGGER ${fn} ON projects`);
    await owner.unsafe(`DROP FUNCTION ${fn}()`);
  }
}

test("two creates at once choosing one key: one is made, the other refused with the key's 409, never 500", async () => {
  const both = await bothReadThenInsert(() => [create("Search A", "search-a", "SRCH"), create("Search B", "search-b", "SRCH")]);
  const got = await Promise.all(both.map(async (r) => [r.status, await r.json()] as [number, Refusal | { key: string; name: string }]));
  expect(got.map(([s]) => s).sort()).toEqual([201, 409]);
  const made = got.find(([s]) => s === 201)![1] as { name: string };
  const refused = got.find(([s]) => s === 409)![1] as Refusal;
  expect(refused.error.message).toBe(`SRCH is already the key of ${made.name}; pick another`);
  expect(refused.error.details?.suggestion).toMatch(/^[A-Z][A-Z0-9]{1,5}$/);
  expect(refused.error.details?.suggestion).not.toBe("SRCH");
  expect((await owner`SELECT 1 FROM projects WHERE organization_id = ${org} AND key_prefix = 'SRCH'`).length).toBe(1);
}, 20_000);

test("two creates at once deriving one key: both are made, under distinct keys", async () => {
  const both = await bothReadThenInsert(() => [create("Orders API", "orders-api"), create("Orders Worker", "orders-worker")]);
  const got = await Promise.all(both.map(async (r) => [r.status, await r.json()] as [number, { key: string; slug: string }]));
  expect(got.map(([s]) => s)).toEqual([201, 201]);
  const bySlug = Object.fromEntries(got.map(([, p]) => [p.slug, p.key]));
  // Whichever lands first is ORDE; the other derives again and takes its next.
  expect([["ORDE", "OWOR"], ["OAPI", "ORDE"]]).toContainEqual([bySlug["orders-api"]!, bySlug["orders-worker"]!]);
}, 20_000);

test("a project holding a harness that is not one any more (aider) can still be saved, which drops it", async () => {
  const made = await create("Legacy", "legacy");
  const { id } = (await made.json()) as { id: string };
  await owner`UPDATE projects SET agent_models = '{"reviewer": {"harness": "aider", "context": "old"}}' WHERE id = ${id}`;
  // The editor sends agentModels back whole, as it read it, with its change.
  const res = await call("PATCH", `/v1/projects/${id}`, { description: "edited", agentModels: { reviewer: { harness: "aider", context: "old" } } });
  expect(res.status).toBe(200);
  const [row] = await owner`SELECT description, agent_models FROM projects WHERE id = ${id}`;
  expect(row.description).toBe("edited");
  expect(row.agent_models).toEqual({ reviewer: { context: "old" } });
});
