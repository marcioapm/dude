/**
 * Judgements through the public API: the organisation's judgement tier
 * (everyone reads, admins set it; Off by default; a tier of another
 * organisation refused), the calls the orchestrator recorded (members read
 * their own organisation's, newest first, filtered by asker, last 7 days),
 * one call's detail, and the test question passed on to the orchestrator.
 *
 * Against a database of its own, migrated as a release is, and a stand-in
 * for the orchestrator. The tests run in order and share its state.
 *
 * Requires DATABASE_URL: a role that can create databases (the owner).
 */

import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { SQL } from "bun";
import { join } from "node:path";
import { closePool, setPool } from "../src/db/client.ts";
import { buildRouter } from "../src/index.ts";
import { Config, useConfig } from "../src/config.ts";
import type { Router } from "../src/api/router.ts";
import { createApiKey } from "../src/api/auth.ts";

const OWNER_URL = process.env.DATABASE_URL ?? "postgres://dude:dude@localhost:5433/dude";
const ROOT = join(import.meta.dir, "../../..");
const NAME = `dude_judgements_api_test_${Bun.randomUUIDv7("hex").slice(-12)}`;
const ORG = "org_judge";
const OTHER = "org_judge_other";

function databaseUrl(appRole = false): string {
  const url = new URL(OWNER_URL);
  if (appRole) {
    url.username = "dude_app";
    url.password = "dude_app";
  }
  url.pathname = `/${NAME}`;
  return url.toString();
}

let admin: SQL;
let owner: SQL;
let app: SQL;
let router: Router;
let adminKey: string;
let memberKey: string;
let otherKey: string;
let orchestratorServer: ReturnType<typeof Bun.serve>;
/** What the stand-in orchestrator was asked to test, in order. */
const tested: unknown[] = [];

type Json = any; // eslint-disable-line @typescript-eslint/no-explicit-any
const body = async (res: Response): Promise<Json> => res.json();

function call(key: string, method: string, path: string, payload?: unknown) {
  return router.handle(new Request(`http://dude.test${path}`, {
    method,
    headers: { authorization: `Bearer ${key}`, "content-type": "application/json" },
    ...(payload === undefined ? {} : { body: JSON.stringify(payload) }),
  }));
}

const tierId = async (org: string, name: string) =>
  (await owner`SELECT id FROM model_tiers WHERE organization_id = ${org} AND name = ${name}`)[0].id as string;
const judgementTier = async () => (await owner`SELECT judgement_tier_id AS t FROM organizations WHERE id = ${ORG}`)[0].t;

/** A recorded judgement call, `minutesAgo` minutes old. */
async function recordCall(org: string, id: string, over: Partial<Record<string, unknown>> & { minutesAgo: number }) {
  const c = {
    asker: "delivery.comment", about: null, question: "Should the fixer act on this comment?\nMore lines.", status: "ok",
    result: { decision: "no", confidence: 0.93, reasoning: "It asks for nothing." }, error: null, response: "{\"decision\":\"no\"}",
    ...over,
  };
  const request = { model: "gpt-6-sol-mini", messages: [{ role: "system", content: "You decide." }, { role: "user", content: c.question }], max_tokens: 1024 };
  await owner`INSERT INTO model_calls (id, organization_id, purpose, asker, about, tier_name, model, question, request, response_text, result, status, error,
      http_status, latency_ms, input_tokens, output_tokens, created_at)
    VALUES (${id}, ${org}, 'judgement', ${c.asker}, ${c.about}, 'Fast', 'gpt-6-sol-mini', ${c.question.split("\n")[0]}, ${request}::jsonb, ${c.response},
      ${c.result}::jsonb, ${c.status}, ${c.error}, 200, 1840, 1204, 96, now() - make_interval(mins => ${c.minutesAgo}))`;
}

beforeAll(async () => {
  admin = new SQL(OWNER_URL);
  await admin.unsafe(`CREATE DATABASE "${NAME}"`);
  const migrate = Bun.spawnSync(["bun", "run", "apps/control-plane/src/db/migrate.ts"], {
    cwd: ROOT, env: { ...process.env, DATABASE_URL: databaseUrl() },
  });
  if (migrate.exitCode !== 0) throw new Error(`migrate: ${migrate.stderr.toString()}`);
  owner = new SQL(databaseUrl());
  for (const id of [ORG, OTHER]) await owner`INSERT INTO organizations (id, name, slug) VALUES (${id}, ${id === ORG ? "Acme" : id}, ${id})`;
  app = new SQL(databaseUrl(true));
  setPool(app);
  // The first key of an organisation is its admin's.
  adminKey = (await createApiKey({ organizationId: ORG, name: "Ana" })).key;
  memberKey = (await createApiKey({ organizationId: ORG, name: "Bo" })).key;
  otherKey = (await createApiKey({ organizationId: OTHER, name: "Cy" })).key;
  orchestratorServer = Bun.serve({
    port: 0,
    async fetch(req) {
      const path = new URL(req.url).pathname;
      if (path === "/internal/judgements/test") {
        const asked = await req.json() as { tierId: string };
        tested.push(asked);
        if (asked.tierId === "mtr_missing") return Response.json({ error: { code: "not_found", message: "no model tier mtr_missing" } }, { status: 404 });
        return Response.json({ ok: true, decision: "yes", confidence: 0.99, reasoning: "Rayleigh scattering.", model: "gpt-6-sol-mini",
          latencyMs: 1200, inputTokens: 380, outputTokens: 32, error: null });
      }
      return Response.json({});
    },
  });
  useConfig(Config.load({ env: { ...process.env,
    DUDE_ORCHESTRATOR_URL: `http://localhost:${orchestratorServer.port}`, DUDE_ORCHESTRATOR_TOKEN: "svc" } }));
  router = buildRouter("");
});

afterAll(async () => {
  useConfig(null);
  await orchestratorServer?.stop(true);
  if (app) await closePool(app);
  await owner?.end();
  await admin?.unsafe(`DROP DATABASE IF EXISTS "${NAME}" WITH (FORCE)`);
  await admin?.end();
});

describe("the judgement tier", () => {
  test("is Off by default; everyone reads it, only admins may change it", async () => {
    expect(await body(await call(memberKey, "GET", "/v1/judgements/settings"))).toEqual({ tierId: null, canEdit: false });
    expect(await body(await call(adminKey, "GET", "/v1/judgements/settings"))).toEqual({ tierId: null, canEdit: true });
    const fast = await tierId(ORG, "Fast");
    expect((await call(memberKey, "PUT", "/v1/judgements/settings", { tierId: fast })).status).toBe(403);
    expect(await judgementTier()).toBeNull();
  });

  test("an admin picks a tier, and Off again", async () => {
    const fast = await tierId(ORG, "Fast");
    const res = await call(adminKey, "PUT", "/v1/judgements/settings", { tierId: fast });
    expect(res.status).toBe(200);
    expect(await body(res)).toEqual({ tierId: fast, canEdit: true });
    expect(await judgementTier()).toBe(fast);
    expect(await body(await call(memberKey, "GET", "/v1/judgements/settings"))).toEqual({ tierId: fast, canEdit: false });
    expect((await body(await call(adminKey, "PUT", "/v1/judgements/settings", { tierId: null }))).tierId).toBeNull();
    expect(await judgementTier()).toBeNull();
    const [event] = await owner`SELECT payload FROM events WHERE event_type = 'settings.updated' AND organization_id = ${ORG} ORDER BY cursor DESC LIMIT 1`;
    expect(event.payload).toEqual({ scope: "organization", changed: { judgementTier: null } });
  });

  test("a tier of another organisation, or none, is refused; a body that is not a setting too", async () => {
    const theirs = await tierId(OTHER, "Fast");
    for (const tier of [theirs, "mtr_nope"]) {
      const res = await call(adminKey, "PUT", "/v1/judgements/settings", { tierId: tier });
      expect(res.status).toBe(400);
      expect((await body(res)).error.message).toBe(`there is no model tier ${tier}`);
    }
    for (const bad of [{}, { tierId: "" }, { tierId: 3 }, { tierId: null, extra: 1 }]) {
      expect((await call(adminKey, "PUT", "/v1/judgements/settings", bad)).status).toBe(400);
    }
    expect(await judgementTier()).toBeNull();
  });

  test("another organisation's setting is its own", async () => {
    const fast = await tierId(ORG, "Fast");
    await call(adminKey, "PUT", "/v1/judgements/settings", { tierId: fast });
    expect((await body(await call(otherKey, "GET", "/v1/judgements/settings"))).tierId).toBeNull();
    await call(adminKey, "PUT", "/v1/judgements/settings", { tierId: null });
  });

  test("removing the judgement tier moves the setting to its replacement", async () => {
    const added = await body(await call(adminKey, "POST", "/v1/models/tiers", { name: "Judge", model: "gpt-6-sol-mini" }));
    const judge = added.tiers.find((t: Json) => t.name === "Judge").id;
    const thinker = await tierId(ORG, "Thinker");
    await call(adminKey, "PUT", "/v1/judgements/settings", { tierId: judge });
    expect((await call(adminKey, "DELETE", `/v1/models/tiers/${judge}`, { replacement: null })).status).toBe(409);
    expect((await call(adminKey, "DELETE", `/v1/models/tiers/${judge}`, { replacement: thinker })).status).toBe(200);
    expect((await body(await call(memberKey, "GET", "/v1/judgements/settings"))).tierId).toBe(thinker);
    await call(adminKey, "PUT", "/v1/judgements/settings", { tierId: null });
  });
});

describe("recent judgements", () => {
  test("none yet", async () => {
    const res = await body(await call(memberKey, "GET", "/v1/judgements"));
    expect(res.calls).toEqual([]);
    expect(res.summary.calls).toBe(0);
    expect(res.askers).toEqual([]);
    expect(res.next).toBeNull();
  });

  test("members read the last 7 days, newest first, each with its question's first line and answer", async () => {
    await recordCall(ORG, "mcl_a", { minutesAgo: 2, about: "ACME-28 · acme#5477" });
    await recordCall(ORG, "mcl_b", { minutesAgo: 14, asker: "delivery.escalation", result: { decision: "yes", confidence: 0.81, reasoning: "A person decides." } });
    await recordCall(ORG, "mcl_c", { minutesAgo: 63, status: "error", result: null, response: null, error: "no answer from the LLM proxy in 1m0s" });
    await recordCall(ORG, "mcl_old", { minutesAgo: 8 * 24 * 60 });
    await recordCall(OTHER, "mcl_theirs", { minutesAgo: 1 });
    const res = await body(await call(memberKey, "GET", "/v1/judgements"));
    expect(res.calls.map((c: Json) => c.id)).toEqual(["mcl_a", "mcl_b", "mcl_c"]);
    expect(res.summary.calls).toBe(3);
    expect(Date.now() - Date.parse(res.summary.since)).toBeGreaterThan(7 * 24 * 3600_000 - 60_000);
    expect(res.askers).toEqual(["delivery.comment", "delivery.escalation"]);
    const { createdAt, ...first } = res.calls[0];
    expect(typeof createdAt).toBe("string");
    expect(first).toEqual({
      id: "mcl_a", asker: "delivery.comment", about: "ACME-28 · acme#5477", question: "Should the fixer act on this comment?",
      decision: "no", confidence: 0.93, reasoning: "It asks for nothing.", status: "ok", error: null,
      tierName: "Fast", model: "gpt-6-sol-mini", latencyMs: 1840, inputTokens: 1204, outputTokens: 96,
    });
    expect(res.calls[2]).toMatchObject({ status: "error", decision: null, confidence: null, reasoning: null, error: "no answer from the LLM proxy in 1m0s" });
  });

  test("filtered by asker; paged with before", async () => {
    const filtered = await body(await call(memberKey, "GET", "/v1/judgements?asker=delivery.escalation"));
    expect(filtered.calls.map((c: Json) => c.id)).toEqual(["mcl_b"]);
    expect(filtered.summary.calls).toBe(3);
    expect(filtered.askers).toEqual(["delivery.comment", "delivery.escalation"]);
    const page1 = await body(await call(memberKey, "GET", "/v1/judgements?limit=2"));
    expect(page1.calls.map((c: Json) => c.id)).toEqual(["mcl_a", "mcl_b"]);
    expect(page1.next).toBe("mcl_b");
    const page2 = await body(await call(memberKey, "GET", `/v1/judgements?limit=2&before=${page1.next}`));
    expect(page2.calls.map((c: Json) => c.id)).toEqual(["mcl_c"]);
    expect(page2.next).toBeNull();
    expect((await call(memberKey, "GET", "/v1/judgements?limit=0")).status).toBe(400);
    expect((await call(memberKey, "GET", "/v1/judgements?limit=101")).status).toBe(400);
  });

  test("another organisation sees only its own", async () => {
    const res = await body(await call(otherKey, "GET", "/v1/judgements"));
    expect(res.calls.map((c: Json) => c.id)).toEqual(["mcl_theirs"]);
    expect((await call(otherKey, "GET", "/v1/judgements/mcl_a")).status).toBe(404);
  });

  test("one call's detail carries what was sent and what came back", async () => {
    const res = await call(memberKey, "GET", "/v1/judgements/mcl_a");
    expect(res.status).toBe(200);
    const detail = await body(res);
    expect(detail.id).toBe("mcl_a");
    expect(detail.request.messages).toEqual([{ role: "system", content: "You decide." }, { role: "user", content: "Should the fixer act on this comment?\nMore lines." }]);
    expect(detail.responseText).toBe("{\"decision\":\"no\"}");
    expect((await call(memberKey, "GET", "/v1/judgements/mcl_nope")).status).toBe(404);
  });

  test("the list shows the question stored with the call, not one read from what was sent", async () => {
    await recordCall(ORG, "mcl_q", { minutesAgo: 1, question: "Stored question?" });
    await owner`UPDATE model_calls SET request = '{"messages": []}'::jsonb WHERE id = 'mcl_q'`;
    try {
      const res = await body(await call(memberKey, "GET", "/v1/judgements?limit=1"));
      expect(res.calls.map((c: Json) => [c.id, c.question])).toEqual([["mcl_q", "Stored question?"]]);
    } finally {
      await owner`DELETE FROM model_calls WHERE id = 'mcl_q'`;
    }
  });
});

describe("the test question", () => {
  test("a member is refused before the orchestrator is asked; an admin's reaches it with the tier named", async () => {
    tested.length = 0;
    const fast = await tierId(ORG, "Fast");
    expect((await call(memberKey, "POST", "/v1/judgements/test", { tierId: fast })).status).toBe(403);
    expect(tested).toEqual([]);
    const res = await call(adminKey, "POST", "/v1/judgements/test", { tierId: fast });
    expect(res.status).toBe(200);
    expect(await body(res)).toEqual({ ok: true, decision: "yes", confidence: 0.99, reasoning: "Rayleigh scattering.", model: "gpt-6-sol-mini",
      latencyMs: 1200, inputTokens: 380, outputTokens: 32, error: null });
    expect(tested).toEqual([{ tierId: fast }]);
  });

  test("a body naming no tier is refused unasked; the orchestrator's refusal is passed on", async () => {
    tested.length = 0;
    for (const bad of [{}, { tierId: "" }, { tierId: "x", model: "y" }]) {
      expect((await call(adminKey, "POST", "/v1/judgements/test", bad)).status).toBe(400);
    }
    expect(tested).toEqual([]);
    const res = await call(adminKey, "POST", "/v1/judgements/test", { tierId: "mtr_missing" });
    expect(res.status).toBe(404);
    expect((await body(res)).error.message).toBe("no model tier mtr_missing");
  });
});
