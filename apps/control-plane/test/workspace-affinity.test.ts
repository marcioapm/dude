/**
 * Workspace affinity — a Run's workspace lives on one node's disk.
 *
 * These tests exist because the bug they prevent is silent: a resumed Run
 * claimed by a different worker found no workspace, materialized a fresh
 * clone, and continued against a clean tree. Every uncommitted change was
 * gone, and the Run looked perfectly healthy throughout.
 */

import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { SQL } from "bun";
import { closePool, setPool, withOrg } from "../src/db/client.ts";
import { createApiKey } from "../src/api/auth.ts";
import { reapExpiredRunLeases } from "../src/workflow/sweepers.ts";
import { startServer } from "../src/index.ts";

const OWNER_URL = process.env.DATABASE_URL ?? "postgres://dude:dude@localhost:5433/dude";
const APP_URL = process.env.TEST_APP_DATABASE_URL ?? "postgres://dude_app:dude_app@localhost:5433/dude";
const ORG = `org_wa_${Bun.randomUUIDv7("hex").slice(0, 8)}`;

let owner: SQL;
let server: ReturnType<typeof startServer>;
let baseUrl: string;
let runnerKey: string;
let userKey: string;

let counter = 0;
const testId = (prefix: string) =>
  `${prefix}_${Bun.randomUUIDv7("hex").slice(-10)}${(counter++).toString(36)}`;

async function api(method: string, path: string, body?: unknown, key = runnerKey) {
  const res = await fetch(`${baseUrl}${path}`, {
    method,
    headers: { authorization: `Bearer ${key}`, "content-type": "application/json" },
    ...(body === undefined ? {} : { body: JSON.stringify(body) }),
  });
  const text = await res.text();
  return { status: res.status, body: text ? JSON.parse(text) : null };
}

/** Register a worker the way the runner does. */
async function registerWorker(name: string): Promise<string> {
  const res = await api("POST", "/v1/runner/workers", { name, pool: "test" });
  return res.body.id as string;
}

async function seedPendingRun(options: {
  homeWorkerId?: string | null;
  portable?: boolean;
} = {}): Promise<string> {
  const projectId = testId("prj");
  const workItemId = testId("wi");
  const runId = testId("run");

  await withOrg(ORG, async ({ sql }) => {
    await sql`INSERT INTO projects (id, organization_id, name, slug)
              VALUES (${projectId}, ${ORG}, 'Affinity', ${projectId})`;
    await sql`INSERT INTO work_items (id, organization_id, project_id, title)
              VALUES (${workItemId}, ${ORG}, ${projectId}, 'affinity')`;
    await sql`
      INSERT INTO runs (id, organization_id, project_id, work_item_id, attempt, status,
                        home_worker_id, workspace_portable, workspace_path)
      VALUES (${runId}, ${ORG}, ${projectId}, ${workItemId}, 1, 'pending',
              ${options.homeWorkerId ?? null}, ${options.portable ?? true},
              ${options.homeWorkerId ? "/tmp/workspace" : null})`;
  });
  return runId;
}

async function runRow(runId: string) {
  return withOrg(ORG, async ({ sql }) => {
    const rows = (await sql`
      SELECT status::text, worker_id AS "workerId", home_worker_id AS "homeWorkerId",
             workspace_portable AS "portable", error
      FROM runs WHERE id = ${runId}`) as Array<Record<string, unknown>>;
    return rows[0]!;
  });
}

beforeAll(async () => {
  owner = new SQL(OWNER_URL);
  await owner`INSERT INTO organizations (id, name, slug) VALUES (${ORG}, ${ORG}, ${ORG})
              ON CONFLICT (id) DO NOTHING`;
  setPool(new SQL(APP_URL));

  runnerKey = (await createApiKey({ organizationId: ORG, name: "runner", kind: "runner" })).key;
  userKey = (await createApiKey({ organizationId: ORG, name: "user" })).key;

  server = startServer(0);
  baseUrl = `http://localhost:${server.port}`;
});

afterAll(async () => {
  await server?.stop(true);
  await closePool();
  await owner`DELETE FROM workers WHERE organization_id = ${ORG}`;
  await owner`DELETE FROM organizations WHERE id = ${ORG}`;
  await owner.end();
});

describe("claiming", () => {
  test("a fresh run can be claimed by any worker", async () => {
    const workerA = await registerWorker(testId("wrk"));
    const runId = await seedPendingRun();

    const claim = await api("POST", `/v1/runner/workers/${workerA}/claim`, { limit: 5 });
    expect(claim.body.runs.map((r: { id: string }) => r.id)).toContain(runId);

    // Claiming establishes where the workspace will live.
    expect((await runRow(runId)).homeWorkerId).toBe(workerA);
  });

  test("a run with uncommitted work is not offered to another worker", async () => {
    // The bug this whole migration exists to prevent.
    const workerA = await registerWorker(testId("wrk"));
    const workerB = await registerWorker(testId("wrk"));

    const runId = await seedPendingRun({ homeWorkerId: workerA, portable: false });

    const claimB = await api("POST", `/v1/runner/workers/${workerB}/claim`, { limit: 5 });
    expect(claimB.body.runs.map((r: { id: string }) => r.id)).not.toContain(runId);

    // It is still there for the node that actually holds the work.
    const claimA = await api("POST", `/v1/runner/workers/${workerA}/claim`, { limit: 5 });
    expect(claimA.body.runs.map((r: { id: string }) => r.id)).toContain(runId);
  });

  test("a run whose work is committed may move to another worker", async () => {
    // Everything is in the mirror, so a fresh clone loses nothing.
    const workerA = await registerWorker(testId("wrk"));
    const workerB = await registerWorker(testId("wrk"));

    const runId = await seedPendingRun({ homeWorkerId: workerA, portable: true });

    const claimB = await api("POST", `/v1/runner/workers/${workerB}/claim`, { limit: 5 });
    expect(claimB.body.runs.map((r: { id: string }) => r.id)).toContain(runId);

    // The workspace's home moves with it.
    expect((await runRow(runId)).homeWorkerId).toBe(workerB);
  });
});

describe("resume", () => {
  test("a paused run returns to its own node, not to whoever asks first", async () => {
    const workerA = await registerWorker(testId("wrk"));
    const workerB = await registerWorker(testId("wrk"));

    // Pause leaves the workspace where it is, with uncommitted work in it.
    const runId = await seedPendingRun({ homeWorkerId: workerA, portable: false });
    await withOrg(ORG, async ({ sql }) => {
      await sql`UPDATE runs SET status = 'paused' WHERE id = ${runId}`;
    });

    const resumed = await api("POST", `/v1/runs/${runId}/resume`, {}, userKey);
    expect(resumed.status).toBe(200);

    // Resume must not hand the Run to a node that cannot see its workspace.
    const claimB = await api("POST", `/v1/runner/workers/${workerB}/claim`, { limit: 5 });
    expect(claimB.body.runs.map((r: { id: string }) => r.id)).not.toContain(runId);

    const claimA = await api("POST", `/v1/runner/workers/${workerA}/claim`, { limit: 5 });
    expect(claimA.body.runs.map((r: { id: string }) => r.id)).toContain(runId);
  });

  test("resume preserves the workspace home", async () => {
    const workerA = await registerWorker(testId("wrk"));
    const runId = await seedPendingRun({ homeWorkerId: workerA, portable: false });
    await withOrg(ORG, async ({ sql }) => {
      await sql`UPDATE runs SET status = 'paused' WHERE id = ${runId}`;
    });

    await api("POST", `/v1/runs/${runId}/resume`, {}, userKey);

    const run = await runRow(runId);
    // `worker_id` says who is executing; `home_worker_id` says where the work
    // lives. Resume clears the first and must keep the second.
    expect(run.workerId).toBeNull();
    expect(run.homeWorkerId).toBe(workerA);
  });
});

describe("a node that disappears", () => {
  test("a rebuildable run is requeued rather than failed", async () => {
    const workerA = await registerWorker(testId("wrk"));
    const runId = await seedPendingRun({ homeWorkerId: workerA, portable: true });

    await withOrg(ORG, async ({ sql }) => {
      await sql`UPDATE runs SET status = 'running', worker_id = ${workerA},
                                lease_expires_at = now() - interval '5 minutes'
                WHERE id = ${runId}`;
    });

    await reapExpiredRunLeases();

    const run = await runRow(runId);
    expect(run.status).toBe("pending");
    expect(run.error).toBeNull();
    // Released, so another node can pick it up.
    expect(run.workerId).toBeNull();
  });

  test("a run holding uncommitted work fails loudly instead of silently restarting", async () => {
    // Plan §32: retry elsewhere if reconstructable, otherwise escalate with
    // clear state. Quietly re-running this on a clean clone would discard the
    // agent's work with no indication that anything was lost.
    const workerA = await registerWorker(testId("wrk"));
    const runId = await seedPendingRun({ homeWorkerId: workerA, portable: false });

    await withOrg(ORG, async ({ sql }) => {
      await sql`UPDATE runs SET status = 'running', worker_id = ${workerA},
                                lease_expires_at = now() - interval '5 minutes'
                WHERE id = ${runId}`;
    });

    await reapExpiredRunLeases();

    const run = await runRow(runId);
    expect(run.status).toBe("failed");
    expect(String(run.error)).toMatch(/uncommitted work/);
    // The home is retained, so an operator can still find the workspace.
    expect(run.homeWorkerId).toBe(workerA);
  });
});

describe("portability reporting", () => {
  test("the runner can pin its own run by reporting a dirty tree", async () => {
    const workerA = await registerWorker(testId("wrk"));
    const runId = await seedPendingRun({ homeWorkerId: workerA, portable: true });

    await api("POST", `/v1/runner/runs/${runId}/status`, {
      status: "running",
      workspacePortable: false,
    });

    expect((await runRow(runId)).portable).toBe(false);
  });

  test("omitting the field leaves portability unchanged", async () => {
    // Most status updates say nothing about the workspace; they must not
    // reset it to the default and quietly unpin the Run.
    const workerA = await registerWorker(testId("wrk"));
    const runId = await seedPendingRun({ homeWorkerId: workerA, portable: false });

    await api("POST", `/v1/runner/runs/${runId}/status`, { status: "running" });

    expect((await runRow(runId)).portable).toBe(false);
  });
});
