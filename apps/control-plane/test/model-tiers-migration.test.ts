/**
 * Migration 069, model tiers: organizations there before it get Thinker,
 * Coder and Fast, each asking for the model most of its roles already
 * named; their roles name tiers instead of models; a project's override
 * keeps the model it named, on a tier that asks for it (one made for it
 * when none does); what changed is noted for the admins; and no role names
 * a model afterwards. Organizations made after it get the same three tiers,
 * naming no model yet. The database refuses a tier the API would.
 *
 * Applies the migrations before 069 to a database of its own, seeds
 * organizations and projects as they were, then applies 069.
 *
 * Requires DATABASE_URL: a role that can create databases (the owner).
 */

import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { SQL } from "bun";
import { listMigrationFiles } from "../src/db/migrate.ts";

const OWNER_URL = process.env.DATABASE_URL ?? "postgres://dude:dude@localhost:5433/dude";
const NAME = `dude_tiers_test_${Bun.randomUUIDv7("hex").slice(-12)}`;

let admin: SQL;
let db: SQL;

function databaseUrl(appRole = false): string {
  const url = new URL(OWNER_URL);
  if (appRole) {
    url.username = "dude_app";
    url.password = "dude_app";
  }
  url.pathname = `/${NAME}`;
  return url.toString();
}

const ACME = {
  investigator: { model: "llm-anthropic/claude-fable-5-1" },
  reviewer: { model: "llm-anthropic/claude-fable-5-1", effort: "high" },
  simplifier: { model: "llm-anthropic/claude-sonnet-5-5" },
  qa_browser: { model: "llm-anthropic/claude-sonnet-5-5" },
  implementer: { model: "llm-anthropic/claude-opus-5-5", effort: "high", machineSize: "msz_x" },
  fixer: { model: "llm-anthropic/claude-opus-5-5" },
};

beforeAll(async () => {
  admin = new SQL(OWNER_URL);
  await admin.unsafe(`CREATE DATABASE "${NAME}"`);
  db = new SQL(databaseUrl());
  const files = await listMigrationFiles();
  await db`CREATE TABLE schema_migrations (version text PRIMARY KEY, name text NOT NULL, checksum text NOT NULL,
           applied_at timestamptz NOT NULL DEFAULT now())`;
  for (const f of files.filter((f) => f.version < "069")) await db.unsafe(await f.contents());

  const organization = (id: string, name: string, models: unknown = {}) =>
    db`INSERT INTO organizations (id, name, slug, default_agent_models) VALUES (${id}, ${name}, ${name.toLowerCase()}, ${models}::jsonb)`;
  await organization("org_acme", "Acme", ACME);
  // A tie between two models: the first role in Thinker's order wins.
  await organization("org_tie", "Tie",
    { reviewer: { model: "llm-openai/gpt-5.6-sol" }, investigator: { model: "llm-anthropic/claude-fable-5-1" } });
  await organization("org_none", "None");
  // Most of Thinker's roles name one model; its first role names another.
  await organization("org_most", "Most",
    { investigator: { model: "llm-anthropic/claude-sonnet-5-5" }, reviewer: { model: "llm-anthropic/claude-fable-5-1" },
      simplifier: { model: "llm-anthropic/claude-fable-5-1" } });
  await organization("org_fake", "Fake", { implementer: { model: "fake/scripted" }, reviewer: { model: "fake/scripted" } });
  const project = (id: string, org: string, models: unknown) =>
    db`INSERT INTO projects (id, organization_id, name, slug, key_prefix, agent_models) VALUES (${id}, ${org}, ${id}, ${id}, 'P', ${models}::jsonb)`;
  // Matches Thinker's model; another's model no tier asks for, twice; an effort alone.
  await project("prj_docs", "org_acme", { implementer: { model: "llm-anthropic/claude-fable-5-1" }, reviewer: { effort: "low" } });
  await project("prj_abs", "org_acme", { reviewer: { model: "llm-openai/gpt-5.6-sol", effort: "max" } });
  await project("prj_abs2", "org_acme", { simplifier: { model: "llm-openai/gpt-5.6-sol" } });
  // A name too long for a tier's, and a model no tier can hold.
  await project("prj_long", "org_none", { implementer: { model: `llm-openai/${"m".repeat(30)}` }, reviewer: { model: "llm-openai/a b" } });
  // Bedrock-style ids alike in their first 24 characters, and a model named as a seeded tier is, in another case.
  await organization("org_bed", "Bed", { implementer: { model: "llm-anthropic/claude-opus-5-5" } });
  await project("prj_bed1", "org_bed", { reviewer: { model: "llm-openai/anthropic.claude-3-5-sonnet-20240620-v1:0" } });
  await project("prj_bed2", "org_bed", { reviewer: { model: "llm-openai/anthropic.claude-3-5-sonnet-20241022-v2:0" } });
  await project("prj_bed3", "org_bed", { simplifier: { model: "llm-openai/FAST" } });
  // The orchestrator's role naming a model; a project's fixer naming Coder's model.
  await organization("org_shapes", "Shapes",
    { orchestrator: { model: "llm-anthropic/claude-fable-5-1", effort: "low" }, implementer: { model: "llm-anthropic/claude-opus-5-5" } });
  await project("prj_coder", "org_shapes", { fixer: { model: "llm-anthropic/claude-opus-5-5" } });
  // A project on the scripted agent, in an organization whose tiers ask for it.
  await project("prj_fake", "org_fake", { implementer: { model: "fake/scripted" } });

  // 069 alone: what it leaves, before 072 renames the orchestrator.
  for (const f of files.filter((f) => f.version === "069")) await db.unsafe(await f.contents());
}, 120_000);

afterAll(async () => {
  await db?.end();
  await admin?.unsafe(`DROP DATABASE IF EXISTS "${NAME}" WITH (FORCE)`);
  await admin?.end();
});

type Tier = { id: string; name: string; model: string | null; description: string; position: number };
const tiers = async (org: string) =>
  (await db`SELECT id, name, model, description, position FROM model_tiers WHERE organization_id = ${org} ORDER BY position`) as Tier[];
const byName = async (org: string) => Object.fromEntries((await tiers(org)).map((t) => [t.name, t]));
const orgModels = async (org: string) => (await db`SELECT default_agent_models AS m FROM organizations WHERE id = ${org}`)[0].m;
const projectModels = async (id: string) => (await db`SELECT agent_models AS m FROM projects WHERE id = ${id}`)[0].m;
const notes = async (org: string) =>
  await db`SELECT project_id AS "projectId", role, old_model AS "oldModel", tier_name AS "tierName", new_tier AS "newTier",
             model_changed AS "modelChanged" FROM model_tier_upgrade_notes WHERE organization_id = ${org}
           ORDER BY project_id NULLS FIRST, role`;

describe("an organization there before tiers", () => {
  test("gets Thinker, Coder and Fast, each asking for the model most of its roles named", async () => {
    const t = await tiers("org_acme");
    expect(t.slice(0, 3).map((x) => [x.name, x.model])).toEqual([
      ["Thinker", "claude-fable-5-1"],
      ["Coder", "claude-opus-5-5"],
      ["Fast", null],
    ]);
    expect(t[0]!.description).toBe("Reads, plans, judges and tidies. Slow and thorough.");
  });

  test("its roles name tiers, keep their other fields, and name no model; the fixer follows the implementer", async () => {
    const { Thinker, Coder } = await byName("org_acme");
    expect(await orgModels("org_acme")).toEqual({
      investigator: { tier: Thinker!.id },
      reviewer: { tier: Thinker!.id, effort: "high" },
      simplifier: { tier: Thinker!.id },
      qa_browser: { tier: Thinker!.id },
      implementer: { tier: Coder!.id, effort: "high", machineSize: "msz_x" },
    });
  });

  test("a project's override keeps its model: on the tier asking for it, or on one made for it once", async () => {
    const t = await byName("org_acme");
    expect(t["gpt-5.6-sol"]).toMatchObject({ model: "gpt-5.6-sol", position: 3 });
    expect(Object.keys(t)).toHaveLength(4);
    expect(await projectModels("prj_docs")).toEqual({ implementer: { tier: t.Thinker!.id }, reviewer: { effort: "low" } });
    expect(await projectModels("prj_abs")).toEqual({ reviewer: { tier: t["gpt-5.6-sol"]!.id, effort: "max" } });
    expect(await projectModels("prj_abs2")).toEqual({ simplifier: { tier: t["gpt-5.6-sol"]!.id } });
  });

  test("what changed is noted for its admins", async () => {
    expect(await notes("org_acme")).toEqual([
      { projectId: null, role: "fixer", oldModel: "llm-anthropic/claude-opus-5-5", tierName: "Coder", newTier: false, modelChanged: false },
      { projectId: null, role: "implementer", oldModel: "llm-anthropic/claude-opus-5-5", tierName: "Coder", newTier: false, modelChanged: false },
      { projectId: null, role: "investigator", oldModel: "llm-anthropic/claude-fable-5-1", tierName: "Thinker", newTier: false, modelChanged: false },
      { projectId: null, role: "qa_browser", oldModel: "llm-anthropic/claude-sonnet-5-5", tierName: "Thinker", newTier: false, modelChanged: true },
      { projectId: null, role: "reviewer", oldModel: "llm-anthropic/claude-fable-5-1", tierName: "Thinker", newTier: false, modelChanged: false },
      { projectId: null, role: "simplifier", oldModel: "llm-anthropic/claude-sonnet-5-5", tierName: "Thinker", newTier: false, modelChanged: true },
      { projectId: "prj_abs", role: "reviewer", oldModel: "llm-openai/gpt-5.6-sol", tierName: "gpt-5.6-sol", newTier: true, modelChanged: false },
      { projectId: "prj_abs2", role: "simplifier", oldModel: "llm-openai/gpt-5.6-sol", tierName: "gpt-5.6-sol", newTier: true, modelChanged: false },
      { projectId: "prj_docs", role: "implementer", oldModel: "llm-anthropic/claude-fable-5-1", tierName: "Thinker", newTier: false, modelChanged: false },
    ]);
  });

  test("a tie goes to the first role in the tier's order", async () => {
    expect((await byName("org_tie")).Thinker!.model).toBe("claude-fable-5-1");
  });

  test("the model most roles name wins over the first role's", async () => {
    expect((await byName("org_most")).Thinker!.model).toBe("claude-fable-5-1");
  });

  test("one whose roles named no model gets tiers naming none, and its roles on them", async () => {
    const t = await byName("org_none");
    expect([t.Thinker!.model, t.Coder!.model, t.Fast!.model]).toEqual([null, null, null]);
    expect((await orgModels("org_none")).reviewer).toEqual({ tier: t.Thinker!.id });
    expect((await orgModels("org_none")).implementer).toEqual({ tier: t.Coder!.id });
  });

  test("a name too long is cut to a tier's; a model no tier can hold leaves the role on the organization's tier", async () => {
    const t = await byName("org_none");
    const made = t["m".repeat(24)]!;
    expect(made.model).toBe("m".repeat(30));
    expect(await projectModels("prj_long")).toEqual({ implementer: { tier: made.id } });
    expect((await notes("org_none")).find((n: { role: string }) => n.role === "reviewer"))
      .toMatchObject({ oldModel: "llm-openai/a b", tierName: "Thinker", modelChanged: true });
  });

  test("the scripted agent's models are kept as they are", async () => {
    const t = await byName("org_fake");
    expect([t.Thinker!.model, t.Coder!.model]).toEqual(["fake/scripted", "fake/scripted"]);
  });

  test("names made alike, by a 24-character cut or by a seeded tier's name in another case, are numbered; each project keeps its own", async () => {
    const t = await byName("org_bed");
    const v1 = t["anthropic.claude-3-5-son"]!;
    const v2 = t["anthropic.claude-3-5-s 2"]!;
    const fast = t["FAST 2"]!;
    expect(Object.keys(t).sort()).toEqual(["Coder", "FAST 2", "Fast", "Thinker", "anthropic.claude-3-5-s 2", "anthropic.claude-3-5-son"].sort());
    expect([v1.model, v2.model, fast.model]).toEqual([
      "anthropic.claude-3-5-sonnet-20240620-v1:0", "anthropic.claude-3-5-sonnet-20241022-v2:0", "FAST"]);
    expect(t.Fast!.model).toBeNull();
    expect(await projectModels("prj_bed1")).toEqual({ reviewer: { tier: v1.id } });
    expect(await projectModels("prj_bed2")).toEqual({ reviewer: { tier: v2.id } });
    expect(await projectModels("prj_bed3")).toEqual({ simplifier: { tier: fast.id } });
    expect((await notes("org_bed")).filter((n: { projectId: string | null }) => n.projectId).map((n: { tierName: string; newTier: boolean }) => [n.tierName, n.newTier]))
      .toEqual([["anthropic.claude-3-5-son", true], ["anthropic.claude-3-5-s 2", true], ["FAST 2", true]]);
  });

  test("an orchestrator naming a model is on Thinker, voting for its model", async () => {
    const t = await byName("org_shapes");
    expect(t.Thinker!.model).toBe("claude-fable-5-1");
    expect((await orgModels("org_shapes")).orchestrator).toEqual({ tier: t.Thinker!.id, effort: "low" });
  });

  test("a project's override on a model a tier already asks for reuses that tier, a fixer's and the scripted agent's too", async () => {
    const shapes = await byName("org_shapes");
    expect(Object.keys(shapes)).toHaveLength(3);
    expect(await projectModels("prj_coder")).toEqual({ fixer: { tier: shapes.Coder!.id } });
    const fake = await byName("org_fake");
    expect(Object.keys(fake)).toHaveLength(3);
    expect(await projectModels("prj_fake")).toEqual({ implementer: { tier: fake.Thinker!.id } });
    const projectNotes = [...await notes("org_shapes"), ...await notes("org_fake")]
      .filter((n: { projectId: string | null }) => n.projectId);
    expect(projectNotes).toEqual([
      { projectId: "prj_coder", role: "fixer", oldModel: "llm-anthropic/claude-opus-5-5", tierName: "Coder", newTier: false, modelChanged: false },
      { projectId: "prj_fake", role: "implementer", oldModel: "fake/scripted", tierName: "Thinker", newTier: false, modelChanged: false },
    ]);
  });

  test("no organization or project names a model any more", async () => {
    const [left] = await db`
      SELECT (SELECT count(*) FROM organizations, jsonb_each(default_agent_models) r WHERE r.value ? 'model')::int
           + (SELECT count(*) FROM projects, jsonb_each(agent_models) r WHERE r.value ? 'model')::int AS n`;
    expect(left.n).toBe(0);
  });
});

describe("an organization made after", () => {
  test("gets the three tiers, naming no model, and its roles on them; the orchestrator's too", async () => {
    await db`INSERT INTO organizations (id, name, slug, default_agent_models) VALUES ('org_after', 'After', 'after',
      ${{ orchestrator: { effort: "low" } }}::jsonb)`;
    const t = await byName("org_after");
    expect(Object.values(t).map((x) => [x.name, x.model])).toEqual([["Thinker", null], ["Coder", null], ["Fast", null]]);
    expect(await orgModels("org_after")).toEqual({
      orchestrator: { effort: "low", tier: t.Thinker!.id },
      investigator: { tier: t.Thinker!.id },
      reviewer: { tier: t.Thinker!.id },
      simplifier: { tier: t.Thinker!.id },
      qa_browser: { tier: t.Thinker!.id },
      implementer: { tier: t.Coder!.id },
    });
    expect(await notes("org_after")).toEqual([]);
  });
});

describe("the database refuses what the API would", () => {
  const asApp = async <T>(org: string, fn: (sql: SQL) => Promise<T>): Promise<string> => {
    const app = new SQL(databaseUrl(true));
    try {
      return await app.begin(async (tx) => {
        await tx`SELECT set_config('app.organization_id', ${org}, true)`;
        await fn(tx);
        return "";
      });
    } catch (err) {
      return String(err);
    } finally {
      await app.end();
    }
  };

  test("the app cannot seed tiers into an organization, its own or another's", async () => {
    // An organization with no tiers, so a call that ran would succeed and add three.
    await db`INSERT INTO organizations (id, name, slug) VALUES ('org_seedless', 'Seedless', 'seedless')`;
    await db`UPDATE organizations SET default_agent_models = '{}' WHERE id = 'org_seedless'`;
    await db`DELETE FROM model_tiers WHERE organization_id = 'org_seedless'`;
    for (const as of ["org_acme", "org_seedless"]) {
      expect(await asApp(as, (tx) => tx`SELECT seed_model_tiers_for('org_seedless', 'evil', 'evil')`))
        .toContain("permission denied for function seed_model_tiers_for");
    }
    expect(await tiers("org_seedless")).toEqual([]);
    expect(await orgModels("org_seedless")).toEqual({});
  });

  test("the app reads and dismisses the upgrade's notes, but cannot add or delete one", async () => {
    const count = async () => (await db`SELECT count(*)::int AS n FROM model_tier_upgrade_notes WHERE organization_id = 'org_acme'`)[0].n as number;
    const n = await count();
    expect(await asApp("org_acme", (tx) => tx`INSERT INTO model_tier_upgrade_notes (organization_id, role, old_model, tier_name)
      VALUES ('org_acme', 'reviewer', 'x', 'Thinker')`)).toContain("permission denied for table model_tier_upgrade_notes");
    expect(await asApp("org_acme", (tx) => tx`DELETE FROM model_tier_upgrade_notes`))
      .toContain("permission denied for table model_tier_upgrade_notes");
    expect(await count()).toBe(n);
    expect(await asApp("org_acme", (tx) => tx`SELECT 1 FROM model_tier_upgrade_notes`)).toBe("");
  });
  const insert = async (id: string, over: Record<string, unknown> = {}): Promise<string> => {
    // The name is short and unique, so a refusal is the field under test's.
    const t = { name: id.slice(-20), description: "", model: "claude-opus-5-5", ...over };
    try {
      await db`INSERT INTO model_tiers (id, organization_id, name, description, model) VALUES (${id}, 'org_none', ${t.name}, ${t.description}, ${t.model})`;
      return "";
    } catch (err) {
      return String(err);
    }
  };

  test("the most of each is a tier", async () => {
    expect(await insert("most", { name: "x".repeat(24), description: "d".repeat(80), model: "m".repeat(200) })).toBe("");
    expect(await insert("scripted", { model: "fake/hang" })).toBe("");
  });

  for (const [what, over, constraint] of [
    ["an empty name", { name: "" }, "name"],
    ["a name over 24", { name: "x".repeat(25) }, "name"],
    ["a description over 80", { description: "d".repeat(81) }, "description"],
    ["a model with its provider", { model: "llm-anthropic/claude-opus-5-5" }, "model"],
    ["a model with a space", { model: "a b" }, "model"],
    ["an empty model", { model: "" }, "model"],
    ["a model over 200", { model: "m".repeat(201) }, "model"],
    ["a test model it does not have", { model: "fake/other" }, "model"],
  ] as const) {
    test(what, async () => {
      expect(await insert(`bad ${what}`, over)).toContain(`model_tiers_${constraint}_check`);
    });
  }

  test("a name used twice, whatever its case", async () => {
    expect(await insert("dup", { name: "thinker" })).toMatch(/model_tiers_name_idx/);
  });
});
