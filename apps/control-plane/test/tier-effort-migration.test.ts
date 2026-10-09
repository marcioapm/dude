/**
 * Migration 099, effort moves onto the model tier: a role's effort is
 * deleted from every organisation and project role config (a role left
 * with nothing is dropped), tiers there before keep a NULL effort, options
 * and headers, and an organisation made after gets Thinker at high and
 * Coder at medium. The table refuses an effort, options or headers the API
 * would.
 *
 * Applies the migrations before 099 to a database of its own, seeds role
 * efforts as production has them, then applies 099.
 *
 * Requires DATABASE_URL: a role that can create databases (the owner).
 */

import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { SQL } from "bun";
import { listMigrationFiles } from "../src/db/migrate.ts";

const OWNER_URL = process.env.DATABASE_URL ?? "postgres://dude:dude@localhost:5433/dude";
const NAME = `dude_tier_effort_test_${Bun.randomUUIDv7("hex").slice(-12)}`;

let admin: SQL;
let db: SQL;

beforeAll(async () => {
  admin = new SQL(OWNER_URL);
  await admin.unsafe(`CREATE DATABASE "${NAME}"`);
  const url = new URL(OWNER_URL);
  url.pathname = `/${NAME}`;
  db = new SQL(url.toString());
  const files = await listMigrationFiles();
  await db`CREATE TABLE schema_migrations (version text PRIMARY KEY, name text NOT NULL, checksum text NOT NULL,
           applied_at timestamptz NOT NULL DEFAULT now())`;
  for (const f of files.filter((f) => f.version < "099")) await db.unsafe(await f.contents());

  await db`INSERT INTO organizations (id, name, slug) VALUES ('org_prod', 'Prod', 'prod'), ('org_plain', 'Plain', 'plain')`;
  // As production has it: one organisation role at high. And a role whose
  // only setting is its effort, and a project's role with an effort beside a tier.
  await db`UPDATE organizations SET default_agent_models = default_agent_models
    || jsonb_build_object('implementer', default_agent_models->'implementer' || '{"effort": "high"}'::jsonb,
                          'fixer', '{"effort": "medium"}'::jsonb)
    WHERE id = 'org_prod'`;
  await db`INSERT INTO projects (id, organization_id, name, slug, key_prefix, agent_models) VALUES
    ('prj_a', 'org_prod', 'A', 'a', 'A', '{"reviewer": {"effort": "low", "timeLimitMinutes": 45}, "simplifier": {"effort": "max"}}'::jsonb),
    ('prj_b', 'org_plain', 'B', 'b', 'B', '{"reviewer": {"timeLimitMinutes": 60}}'::jsonb)`;

  for (const f of files.filter((f) => f.version === "099")) await db.unsafe(await f.contents());
}, 120_000);

afterAll(async () => {
  await db?.end();
  await admin?.unsafe(`DROP DATABASE IF EXISTS "${NAME}" WITH (FORCE)`);
  await admin?.end();
});

const orgModels = async (org: string) => (await db`SELECT default_agent_models AS m FROM organizations WHERE id = ${org}`)[0].m;
const projectModels = async (id: string) => (await db`SELECT agent_models AS m FROM projects WHERE id = ${id}`)[0].m;
const tiers = async (org: string) =>
  (await db`SELECT name, effort, options, headers FROM model_tiers WHERE organization_id = ${org} ORDER BY position`) as
    Array<{ name: string; effort: string | null; options: unknown; headers: unknown }>;

describe("a role's effort", () => {
  test("is gone from organisation and project roles; their other fields stay, and a role left with nothing goes", async () => {
    const org = await orgModels("org_prod");
    expect(org.implementer).toEqual({ tier: expect.any(String) });
    expect(org.fixer).toBeUndefined();
    expect(await projectModels("prj_a")).toEqual({ reviewer: { timeLimitMinutes: 45 } });
    expect(await projectModels("prj_b")).toEqual({ reviewer: { timeLimitMinutes: 60 } });
    const [left] = await db`
      SELECT (SELECT count(*) FROM organizations, jsonb_each(default_agent_models) r WHERE r.value ? 'effort')::int
           + (SELECT count(*) FROM projects, jsonb_each(agent_models) r WHERE r.value ? 'effort')::int AS n`;
    expect(left.n).toBe(0);
  });
});

describe("tiers", () => {
  test("there before keep the model's default and no options or headers", async () => {
    for (const org of ["org_prod", "org_plain"]) {
      expect(await tiers(org)).toEqual([
        { name: "Thinker", effort: null, options: null, headers: null },
        { name: "Coder", effort: null, options: null, headers: null },
        { name: "Fast", effort: null, options: null, headers: null },
      ]);
    }
  });

  test("of an organisation made after: Thinker at high, Coder at medium, Fast at the model's default", async () => {
    await db`INSERT INTO organizations (id, name, slug) VALUES ('org_new', 'New', 'new')`;
    expect((await tiers("org_new")).map((t) => [t.name, t.effort])).toEqual([["Thinker", "high"], ["Coder", "medium"], ["Fast", null]]);
  });

  test("show their effort, options and headers", async () => {
    const [t] = await db`SELECT model_tier(t) AS j FROM model_tiers t WHERE organization_id = 'org_new' AND name = 'Thinker'`;
    expect(t.j).toMatchObject({ name: "Thinker", effort: "high", options: null, headers: null });
  });

  test("refuse an effort, options or headers the API would", async () => {
    const set = async (sql: string) => {
      try {
        await db.unsafe(`UPDATE model_tiers SET ${sql} WHERE organization_id = 'org_new' AND name = 'Fast'`);
        return "";
      } catch (err) {
        return String(err);
      }
    };
    expect(await set(`effort = 'max', options = '{"effort": "xhigh"}', headers = '{"X-Team": "dude"}'`)).toBe("");
    for (const bad of [`effort = 'xhigh'`, `effort = ''`, `options = '"x"'`, `options = '[]'`,
      `options = jsonb_build_object('k', repeat('x', 4090))`, `headers = '{"X Team": "a"}'`, `headers = '{"a": 1}'`,
      `headers = jsonb_build_object('a', E'b\\nc')`, `headers = '[]'`]) {
      expect(await set(bad)).toContain("check constraint");
    }
  });
});
