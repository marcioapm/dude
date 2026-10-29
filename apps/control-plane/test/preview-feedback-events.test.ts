/**
 * Preview feedback reaches the project's live streams: a submit, resolve or
 * reopen on a branch preview's server is a project-scoped ledger event
 * written with the change, so a second person's `/v1/events/stream?projectId=`
 * sees the first person's submit, and another organisation's stream does not.
 *
 * Requires DATABASE_URL: a role that can create databases (the owner).
 */

import { afterAll, beforeAll, expect, test } from "bun:test";
import { SQL } from "bun";
import { closePool, setPool } from "../src/db/client.ts";
import { buildRouter, startServer } from "../src/index.ts";
import { createApiKey } from "../src/api/auth.ts";
import { listenForEvents } from "../src/events/listen.ts";

const OWNER_URL = process.env.DATABASE_URL ?? "postgres://dude:dude@localhost:5433/dude";
const NAME = `dude_pf_events_${Bun.randomUUIDv7("hex").slice(-12)}`;
const dbUrl = (app = false) => {
  const u = new URL(OWNER_URL);
  u.pathname = `/${NAME}`;
  if (app) { u.username = "dude_app"; u.password = "dude_app"; }
  return u.toString();
};

type Person = { key: string; personId: string };
type Frame = { eventType: string; projectId: string | null; organizationId: string; actor: { id: string }; payload: Record<string, unknown> };
let admin: SQL, owner: SQL, app: SQL;
let server: ReturnType<typeof startServer>;
let stopListening: () => Promise<void>;
let base: string;
let ana: Person, bo: Person, cy: Person;

beforeAll(async () => {
  admin = new SQL(OWNER_URL);
  await admin.unsafe(`CREATE DATABASE "${NAME}"`);
  const migrate = Bun.spawnSync(["bun", "run", "apps/control-plane/src/db/migrate.ts"], { cwd: `${import.meta.dir}/../../..`,
    env: { ...process.env, DATABASE_URL: dbUrl() } });
  if (migrate.exitCode) throw new Error(migrate.stderr.toString());
  owner = new SQL(dbUrl()); app = new SQL(dbUrl(true)); setPool(app);
  for (const org of ["org_pfe", "org_pfe_other"]) await owner`INSERT INTO organizations (id, name, slug) VALUES (${org}, ${org}, ${org})`;
  await owner`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES
    ('prj_pfe', 'org_pfe', 'Web', 'web', 'PFE'), ('prj_pfe_other', 'org_pfe_other', 'Other', 'other', 'OTH')`;
  ana = await createApiKey({ organizationId: "org_pfe", name: "Ana" });
  bo = await createApiKey({ organizationId: "org_pfe", name: "Bo" });
  cy = await createApiKey({ organizationId: "org_pfe_other", name: "Cy" });
  await owner`INSERT INTO runs (id, organization_id, project_id, preview_branch, attempt, status, kind, wakeable)
    VALUES ('run_pfe', 'org_pfe', 'prj_pfe', 'feat/x', 1, 'running', 'preview', true)`;
  await owner`INSERT INTO preview_servers (run_id, organization_id, name, lux_server_id, hostname, url)
    VALUES ('run_pfe', 'org_pfe', 'web', 'lux_web', 'web', 'https://web-pfe.preview.test')`;
  server = startServer(0, buildRouter(""));
  stopListening = await listenForEvents(dbUrl(true));
  base = `http://localhost:${server.port}`;
});

afterAll(async () => {
  await stopListening?.();
  await server?.stop(true);
  await closePool(app);
  await owner?.end();
  await admin?.unsafe(`DROP DATABASE IF EXISTS "${NAME}" WITH (FORCE)`);
  await admin?.end();
});

/** Frames on `key`'s live stream until `until` holds or `ms` pass. */
function watch(key: string, path: string, until: (seen: Frame[]) => boolean, ms = 4000) {
  const ctl = new AbortController();
  const seen: Frame[] = [];
  const open = fetch(`${base}${path}`, { headers: { authorization: `Bearer ${key}` }, signal: ctl.signal });
  const done = (async () => {
    const res = await open;
    const reader = res.body!.getReader();
    const decoder = new TextDecoder();
    const timer = setTimeout(() => ctl.abort(), ms);
    let buffer = "";
    try {
      while (!until(seen)) {
        const { done, value } = await reader.read();
        if (done) break;
        buffer += decoder.decode(value, { stream: true });
        const frames = buffer.split("\n\n");
        buffer = frames.pop() ?? "";
        for (const f of frames) {
          const data = f.match(/^data: (.*)$/m)?.[1];
          if (data) seen.push(JSON.parse(data) as Frame);
        }
      }
    } catch {
      // Aborted at the deadline: what was seen is the answer.
    } finally {
      clearTimeout(timer);
      ctl.abort();
    }
    return seen;
  })();
  return { opened: open.then(() => Bun.sleep(150)), done };
}

const post = (key: string, path: string, body: unknown) => fetch(`${base}${path}`, { method: "POST",
  headers: { authorization: `Bearer ${key}`, "content-type": "application/json" }, body: JSON.stringify(body) });

test("a second person's project stream hears the first person's submit, resolve and reopen; another organisation's does not", async () => {
  const types = (s: Frame[]) => s.filter((f) => f.eventType.startsWith("preview_feedback.")).map((f) => f.eventType);
  const boStream = watch(bo.key, "/v1/events/stream?live=1&projectId=prj_pfe", (s) => types(s).length >= 3);
  const cyStream = watch(cy.key, "/v1/events/stream?live=1", () => false, 2500);
  await Promise.all([boStream.opened, cyStream.opened]);

  const res = await post(ana.key, "/v1/branch-previews/run_pfe/servers/web/feedback", { body: "Too close", submissionKey: "k1",
    anchor: { kind: "preview", url: "https://web-pfe.preview.test/cart", selector: "#pay" } });
  expect(res.status).toBe(201);
  const item = (await res.json()) as { id: string };
  // A retried submit is the same item: no second event.
  expect((await post(ana.key, "/v1/branch-previews/run_pfe/servers/web/feedback", { body: "Too close", submissionKey: "k1",
    anchor: { kind: "preview", url: "https://web-pfe.preview.test/cart", selector: "#pay" } })).status).toBe(201);
  expect((await post(bo.key, `/v1/preview-feedback/${item.id}/resolve`, { note: "done" })).status).toBe(200);
  expect((await post(ana.key, `/v1/preview-feedback/${item.id}/reopen`, {})).status).toBe(200);

  const seen = await boStream.done;
  expect(types(seen)).toEqual(["preview_feedback.submitted", "preview_feedback.resolved", "preview_feedback.reopened"]);
  const submitted = seen.find((f) => f.eventType === "preview_feedback.submitted")!;
  expect(submitted.projectId).toBe("prj_pfe");
  expect(submitted.actor.id).toBe(ana.personId);
  expect(submitted.payload).toEqual({ feedbackId: item.id, previewRunId: "run_pfe", server: "web", status: "open" });
  expect(types(await cyStream.done)).toEqual([]);

  // Written with the change: one row each in the ledger.
  const rows = await owner`SELECT event_type FROM events WHERE project_id = 'prj_pfe' AND event_type LIKE 'preview_feedback.%' ORDER BY cursor`;
  expect(rows.map((r: { event_type: string }) => r.event_type)).toEqual(types(seen));
});
