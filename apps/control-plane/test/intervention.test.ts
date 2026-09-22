/**
 * Human intervention tests — plan §24.
 *
 * Steer, pause, resume and abort are the ways a person redirects autonomous
 * work, so the properties that matter are: the request is durable, it is
 * auditable, and it cannot be applied to a Run that has already finished.
 */

import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { SQL } from "bun";
import { closePool, setPool, withOrg } from "../src/db/client.ts";
import { createApiKey } from "../src/api/auth.ts";
import { startServer } from "../src/index.ts";

const OWNER_URL = process.env.DATABASE_URL ?? "postgres://dude:dude@localhost:5433/dude";
const APP_URL = process.env.TEST_APP_DATABASE_URL ?? "postgres://dude_app:dude_app@localhost:5433/dude";
const ORG = `org_iv_${Bun.randomUUIDv7("hex").slice(0, 8)}`;

let owner: SQL;
let server: ReturnType<typeof startServer>;
let baseUrl: string;
let userKey: string;
let runnerKey: string;

let idCounter = 0;
const testId = (prefix: string) =>
  `${prefix}_${Bun.randomUUIDv7("hex").slice(-10)}${(idCounter++).toString(36)}`;

async function api(
  method: string,
  path: string,
  body?: unknown,
  key = userKey,
): Promise<{ status: number; body: any }> {
  const res = await fetch(`${baseUrl}${path}`, {
    method,
    headers: { authorization: `Bearer ${key}`, "content-type": "application/json" },
    ...(body === undefined ? {} : { body: JSON.stringify(body) }),
  });
  const text = await res.text();
  return { status: res.status, body: text ? JSON.parse(text) : null };
}

/** Seed a Run directly, in whatever state the test needs. */
async function seedRun(status = "running"): Promise<{ runId: string; workItemId: string }> {
  const projectId = testId("prj");
  const workItemId = testId("wi");
  const runId = testId("run");

  await withOrg(ORG, async ({ sql }) => {
    await sql`INSERT INTO projects (id, organization_id, name, slug)
              VALUES (${projectId}, ${ORG}, 'Intervention', ${projectId})`;
    await sql`INSERT INTO work_items (id, organization_id, project_id, title, status)
              VALUES (${workItemId}, ${ORG}, ${projectId}, 'steer me', 'running')`;
    await sql`INSERT INTO runs (id, organization_id, project_id, work_item_id, attempt, status,
                                lease_expires_at)
              VALUES (${runId}, ${ORG}, ${projectId}, ${workItemId}, 1, ${status}::run_status,
                      now() + interval '5 minutes')`;
  });
  return { runId, workItemId };
}

async function runRow(runId: string) {
  return withOrg(ORG, async ({ sql }) => {
    const rows = (await sql`
      SELECT status::text, control::text, control_reason AS "controlReason",
             worker_id AS "workerId", ended_at AS "endedAt"
      FROM runs WHERE id = ${runId}`) as Array<Record<string, unknown>>;
    return rows[0]!;
  });
}

async function eventsFor(runId: string): Promise<string[]> {
  return withOrg(ORG, async ({ sql }) => {
    const rows = (await sql`
      SELECT event_type FROM events WHERE run_id = ${runId} ORDER BY cursor`) as Array<{
      event_type: string;
    }>;
    return rows.map((r) => r.event_type);
  });
}

beforeAll(async () => {
  owner = new SQL(OWNER_URL);
  await owner`INSERT INTO organizations (id, name, slug) VALUES (${ORG}, ${ORG}, ${ORG})
              ON CONFLICT (id) DO NOTHING`;
  setPool(new SQL(APP_URL));

  userKey = (await createApiKey({ organizationId: ORG, name: "test user" })).key;
  runnerKey = (await createApiKey({ organizationId: ORG, name: "test runner", kind: "runner" })).key;

  server = startServer(0);
  baseUrl = `http://localhost:${server.port}`;
});

afterAll(async () => {
  await server?.stop(true);
  await closePool();
  await owner`DELETE FROM organizations WHERE id = ${ORG}`;
  await owner.end();
});

describe("steer", () => {
  test("records a durable, auditable directive", async () => {
    const { runId } = await seedRun();

    const res = await api("POST", `/v1/runs/${runId}/steer`, {
      text: "Do not change the public API of GithubClient.",
    });

    expect(res.status).toBe(201);
    expect(res.body.text).toBe("Do not change the public API of GithubClient.");
    expect(res.body.scope).toBe("run");
    // Undelivered until a runner picks it up.
    expect(res.body.deliveredAt).toBeNull();

    // The ledger must explain why the agent changed course.
    expect(await eventsFor(runId)).toContain("run.steered");
  });

  test("supports turn-scoped directives", async () => {
    // The distinction between "stop doing X" and "for this step, do Y".
    const { runId } = await seedRun();
    const res = await api("POST", `/v1/runs/${runId}/steer`, {
      text: "Just run the tests, do not fix anything yet.",
      scope: "turn",
    });
    expect(res.body.scope).toBe("turn");
  });

  test("a superseding directive keeps the earlier one", async () => {
    // History is the point: a later correction must not erase why the agent
    // behaved the way it did twenty minutes ago.
    const { runId } = await seedRun();
    const first = await api("POST", `/v1/runs/${runId}/steer`, { text: "Use fetch." });
    const second = await api("POST", `/v1/runs/${runId}/steer`, {
      text: "Actually, use the existing http client.",
      supersedes: first.body.id,
    });

    expect(second.body.supersedes).toBe(first.body.id);

    const listed = await api("GET", `/v1/runs/${runId}/directives`);
    expect(listed.body.directives).toHaveLength(2);
  });

  test("cannot steer a finished run", async () => {
    const { runId } = await seedRun("completed");
    const res = await api("POST", `/v1/runs/${runId}/steer`, { text: "too late" });
    expect(res.status).toBe(409);
    expect(res.body.error.message).toMatch(/completed/);
  });

  test("rejects an empty directive", async () => {
    const { runId } = await seedRun();
    expect((await api("POST", `/v1/runs/${runId}/steer`, { text: "" })).status).toBe(400);
  });
});

describe("pause and resume", () => {
  test("records the request without pretending it has taken effect", async () => {
    // The runner owns the agent process, so a pause is requested here and
    // confirmed there. Conflating the two would make the gap unrepresentable.
    const { runId } = await seedRun();

    const res = await api("POST", `/v1/runs/${runId}/pause`, { reason: "need to check something" });
    expect(res.status).toBe(200);
    expect(res.body.pending).toBe(true);

    const run = await runRow(runId);
    expect(run.control).toBe("pause_graceful");
    expect(run.controlReason).toBe("need to check something");
    // Still running: only the runner can actually stop the agent.
    expect(run.status).toBe("running");

    expect(await eventsFor(runId)).toContain("run.paused");
  });

  test("distinguishes graceful from hard", async () => {
    const { runId } = await seedRun();
    await api("POST", `/v1/runs/${runId}/pause`, { mode: "hard" });
    expect((await runRow(runId)).control).toBe("pause_hard");
  });

  test("resume releases the worker so the run is re-claimable", async () => {
    // Plan §24: resume works from durable state, not from an assumption that
    // the prior process survived.
    const { runId } = await seedRun("paused");
    await withOrg(ORG, async ({ sql }) => {
      await sql`UPDATE runs SET worker_id = NULL, control = 'pause_graceful' WHERE id = ${runId}`;
    });

    const res = await api("POST", `/v1/runs/${runId}/resume`);
    expect(res.status).toBe(200);

    const run = await runRow(runId);
    expect(run.status).toBe("pending");
    expect(run.control).toBe("none");
    expect(run.workerId).toBeNull();

    expect(await eventsFor(runId)).toContain("run.resumed");
  });

  test("cannot resume a run that is not paused", async () => {
    const { runId } = await seedRun("running");
    const res = await api("POST", `/v1/runs/${runId}/resume`);
    expect(res.status).toBe(409);
    expect(res.body.error.message).toMatch(/not paused/);
  });

  test("cannot pause a finished run", async () => {
    const { runId } = await seedRun("completed");
    expect((await api("POST", `/v1/runs/${runId}/pause`)).status).toBe(409);
  });
});

describe("abort", () => {
  test("stops the run and its work item", async () => {
    const { runId, workItemId } = await seedRun();

    const res = await api("POST", `/v1/runs/${runId}/abort`, { reason: "requirement changed" });
    expect(res.status).toBe(200);

    const run = await runRow(runId);
    expect(run.status).toBe("aborted");
    expect(run.endedAt).not.toBeNull();
    expect(run.controlReason).toBe("requirement changed");

    // A work item whose run was aborted must not look like it is still
    // progressing.
    const workItem = await withOrg(ORG, async ({ sql }) => {
      const rows = (await sql`
        SELECT status::text FROM work_items WHERE id = ${workItemId}`) as Array<{ status: string }>;
      return rows[0]!;
    });
    expect(workItem.status).toBe("aborted");

    expect(await eventsFor(runId)).toContain("run.aborted");
  });

  test("preserves the ledger", async () => {
    // Plan §24: abort stops the work, it does not erase it.
    const { runId } = await seedRun();
    await api("POST", `/v1/runs/${runId}/steer`, { text: "before the abort" });
    await api("POST", `/v1/runs/${runId}/abort`);

    const events = await eventsFor(runId);
    expect(events).toContain("run.steered");
    expect(events).toContain("run.aborted");
  });

  test("is idempotent enough to be safe", async () => {
    const { runId } = await seedRun();
    expect((await api("POST", `/v1/runs/${runId}/abort`)).status).toBe(200);
    // A second abort is a conflict, not a crash or a silent double-abort.
    expect((await api("POST", `/v1/runs/${runId}/abort`)).status).toBe(409);
  });
});

describe("the runner control channel", () => {
  test("lease renewal carries pending control and directives", async () => {
    // Renewal already runs on a timer, so interventions ride back on it
    // rather than needing a second poll.
    const { runId } = await seedRun();
    await api("POST", `/v1/runs/${runId}/steer`, { text: "prefer the simpler fix" });
    await api("POST", `/v1/runs/${runId}/pause`, { mode: "hard", reason: "stop now" });

    const lease = await api("POST", `/v1/runner/runs/${runId}/lease`, undefined, runnerKey);
    expect(lease.status).toBe(200);
    expect(lease.body.control).toBe("pause_hard");
    expect(lease.body.controlReason).toBe("stop now");
    expect(lease.body.directives).toHaveLength(1);
    expect(lease.body.directives[0].text).toBe("prefer the simpler fix");
  });

  test("a directive is delivered once", async () => {
    // Handing the same instruction to two consecutive turns would make the
    // agent repeat work it already did.
    const { runId } = await seedRun();
    await api("POST", `/v1/runs/${runId}/steer`, { text: "only once" });

    const first = await api("POST", `/v1/runner/runs/${runId}/lease`, undefined, runnerKey);
    const second = await api("POST", `/v1/runner/runs/${runId}/lease`, undefined, runnerKey);

    expect(first.body.directives).toHaveLength(1);
    expect(second.body.directives).toHaveLength(0);
  });

  test("a user key cannot use the runner channel", async () => {
    const { runId } = await seedRun();
    const res = await api("POST", `/v1/runner/runs/${runId}/lease`, undefined, userKey);
    expect(res.status).toBe(401);
  });
});

describe("tenant isolation", () => {
  test("one organization cannot steer or abort another's run", async () => {
    // Run ids appear in URLs and events; holding one must not confer control.
    const otherOrg = `org_iv_other_${Bun.randomUUIDv7("hex").slice(0, 8)}`;
    await owner`INSERT INTO organizations (id, name, slug) VALUES (${otherOrg}, ${otherOrg}, ${otherOrg})`;

    try {
      const otherKey = (await createApiKey({ organizationId: otherOrg, name: "attacker" })).key;
      const { runId } = await seedRun();

      expect((await api("POST", `/v1/runs/${runId}/steer`, { text: "hi" }, otherKey)).status).toBe(404);
      expect((await api("POST", `/v1/runs/${runId}/abort`, undefined, otherKey)).status).toBe(404);
      expect((await api("POST", `/v1/runs/${runId}/pause`, undefined, otherKey)).status).toBe(404);

      // Untouched.
      expect((await runRow(runId)).status).toBe("running");
    } finally {
      await owner`DELETE FROM organizations WHERE id = ${otherOrg}`;
    }
  });
});
