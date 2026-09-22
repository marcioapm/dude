/**
 * Background sweeper tests.
 *
 * These exist because the sweepers are what make "waiting is free" true: they
 * wake parked workflows, dispatch the outbox, and reclaim work from components
 * that disappeared. A sweeper that silently stops working looks exactly like a
 * system with nothing to do.
 */

import { afterAll, afterEach, beforeAll, describe, expect, test } from "bun:test";
import { SQL } from "bun";
import { closePool, setPool, withOrg, withSystemScope } from "../src/db/client.ts";
import {
  Sweeper,
  dispatchOutbox,
  reapExpiredRunLeases,
  reapLostWorkers,
} from "../src/workflow/sweepers.ts";

const OWNER_URL = process.env.DATABASE_URL ?? "postgres://dude:dude@localhost:5433/dude";
const APP_URL = process.env.TEST_APP_DATABASE_URL ?? "postgres://dude_app:dude_app@localhost:5433/dude";

/**
 * Unique test ids.
 *
 * `randomUUIDv7` is time-ordered, so truncating it makes successive calls
 * collide — a counter guarantees distinct rows within a run.
 */
let idCounter = 0;
const testId = (prefix: string) =>
  `${prefix}_${Bun.randomUUIDv7("hex").slice(-10)}${(idCounter++).toString(36)}`;

const ORG_A = `org_sw_a_${Bun.randomUUIDv7("hex").slice(0, 8)}`;
const ORG_B = `org_sw_b_${Bun.randomUUIDv7("hex").slice(0, 8)}`;

let owner: SQL;

/** Seed a project and work item so runs have something to belong to. */
async function seedProject(organizationId: string): Promise<{ projectId: string; workItemId: string }> {
  const projectId = testId("prj");
  const workItemId = testId("wi");

  await withOrg(organizationId, async ({ sql }) => {
    await sql`INSERT INTO projects (id, organization_id, name, slug)
              VALUES (${projectId}, ${organizationId}, 'Sweeper', ${projectId})`;
    await sql`INSERT INTO work_items (id, organization_id, project_id, title)
              VALUES (${workItemId}, ${organizationId}, ${projectId}, 'sweep me')`;
  });
  return { projectId, workItemId };
}

/** Create a run with a lease that expired `secondsAgo` seconds ago. */
async function seedExpiredRun(
  organizationId: string,
  secondsAgo: number,
  status = "running",
): Promise<string> {
  const { projectId, workItemId } = await seedProject(organizationId);
  const runId = testId("run");

  await withOrg(organizationId, async ({ sql }) => {
    await sql`
      INSERT INTO runs (id, organization_id, project_id, work_item_id, attempt, status,
                        lease_expires_at, started_at)
      VALUES (${runId}, ${organizationId}, ${projectId}, ${workItemId}, 1,
              ${status}::run_status,
              now() - ${`${secondsAgo} seconds`}::interval, now())`;
  });
  return runId;
}

beforeAll(async () => {
  owner = new SQL(OWNER_URL);
  for (const id of [ORG_A, ORG_B]) {
    await owner`INSERT INTO organizations (id, name, slug) VALUES (${id}, ${id}, ${id})
                ON CONFLICT (id) DO NOTHING`;
  }
  setPool(new SQL(APP_URL));
});

afterAll(async () => {
  await closePool();
  await owner`DELETE FROM organizations WHERE id IN (${ORG_A}, ${ORG_B})`;
  await owner.end();
});

afterEach(async () => {
  // Workers are not tenant-scoped, so they outlive an organization delete.
  await owner`DELETE FROM workers WHERE organization_id IN (${ORG_A}, ${ORG_B})`;
});

describe("run lease reaper", () => {
  test("reclaims a run whose worker stopped renewing", async () => {
    const runId = await seedExpiredRun(ORG_A, 120);

    const { handled } = await reapExpiredRunLeases();
    expect(handled).toBeGreaterThanOrEqual(1);

    const run = await withOrg(ORG_A, async ({ sql }) => {
      const rows = (await sql`
        SELECT status, error, ended_at, lease_expires_at FROM runs WHERE id = ${runId}`) as Array<{
        status: string;
        error: string;
        ended_at: string | null;
        lease_expires_at: string | null;
      }>;
      return rows[0]!;
    });

    expect(run.status).toBe("failed");
    expect(run.error).toMatch(/lease expired/);
    expect(run.ended_at).not.toBeNull();
    // A finished run must stop holding capacity.
    expect(run.lease_expires_at).toBeNull();
  });

  test("records the reclamation in the owning tenant's ledger", async () => {
    // A run that failed for infrastructure reasons must be as inspectable as
    // one that failed on its own merits.
    const runId = await seedExpiredRun(ORG_A, 120);
    await reapExpiredRunLeases();

    const events = await withOrg(ORG_A, async ({ sql }) => {
      return (await sql`
        SELECT event_type, payload FROM events WHERE run_id = ${runId}`) as Array<{
        event_type: string;
        payload: Record<string, unknown>;
      }>;
    });

    const failure = events.find((e) => e.event_type === "run.failed");
    expect(failure).toBeDefined();
    expect(failure!.payload.reason).toBe("lease_expired");
  });

  test("leaves runs whose lease is still valid alone", async () => {
    const { projectId, workItemId } = await seedProject(ORG_A);
    const runId = testId("run");

    await withOrg(ORG_A, async ({ sql }) => {
      await sql`
        INSERT INTO runs (id, organization_id, project_id, work_item_id, attempt, status,
                          lease_expires_at)
        VALUES (${runId}, ${ORG_A}, ${projectId}, ${workItemId}, 1, 'running',
                now() + interval '5 minutes')`;
    });

    await reapExpiredRunLeases();

    const status = await withOrg(ORG_A, async ({ sql }) => {
      const rows = (await sql`SELECT status FROM runs WHERE id = ${runId}`) as Array<{
        status: string;
      }>;
      return rows[0]!.status;
    });
    expect(status).toBe("running");
  });

  test("does not touch runs that already finished", async () => {
    // A completed run keeps its status even if it left a stale lease behind.
    const { projectId, workItemId } = await seedProject(ORG_A);
    const runId = testId("run");

    await withOrg(ORG_A, async ({ sql }) => {
      await sql`
        INSERT INTO runs (id, organization_id, project_id, work_item_id, attempt, status,
                          lease_expires_at)
        VALUES (${runId}, ${ORG_A}, ${projectId}, ${workItemId}, 1, 'completed',
                now() - interval '5 minutes')`;
    });

    await reapExpiredRunLeases();

    const status = await withOrg(ORG_A, async ({ sql }) => {
      const rows = (await sql`SELECT status FROM runs WHERE id = ${runId}`) as Array<{
        status: string;
      }>;
      return rows[0]!.status;
    });
    expect(status).toBe("completed");
  });

  test("reclaims across tenants in one pass", async () => {
    // The point of a system-scoped sweep: one query, every tenant.
    const runA = await seedExpiredRun(ORG_A, 120);
    const runB = await seedExpiredRun(ORG_B, 120);

    const { handled } = await reapExpiredRunLeases();
    expect(handled).toBeGreaterThanOrEqual(2);

    for (const [org, runId] of [
      [ORG_A, runA],
      [ORG_B, runB],
    ] as const) {
      const status = await withOrg(org, async ({ sql }) => {
        const rows = (await sql`SELECT status FROM runs WHERE id = ${runId}`) as Array<{
          status: string;
        }>;
        return rows[0]!.status;
      });
      expect(status).toBe("failed");
    }
  });
});

describe("worker liveness reaper", () => {
  test("marks a silent worker lost and frees its capacity", async () => {
    const workerId = testId("wrk");
    await owner`
      INSERT INTO workers (id, organization_id, name, status, active_runs, last_heartbeat_at)
      VALUES (${workerId}, ${ORG_A}, ${workerId}, 'ready', 3, now() - interval '10 minutes')`;

    const { handled } = await reapLostWorkers(60);
    expect(handled).toBeGreaterThanOrEqual(1);

    const rows = (await owner`
      SELECT status, active_runs FROM workers WHERE id = ${workerId}`) as Array<{
      status: string;
      active_runs: number;
    }>;
    expect(rows[0]!.status).toBe("lost");
    // Capacity accounting must stop counting a worker that is gone.
    expect(rows[0]!.active_runs).toBe(0);
  });

  test("leaves a recently heartbeating worker alone", async () => {
    const workerId = testId("wrk");
    await owner`
      INSERT INTO workers (id, organization_id, name, status, last_heartbeat_at)
      VALUES (${workerId}, ${ORG_A}, ${workerId}, 'ready', now())`;

    await reapLostWorkers(60);

    const rows = (await owner`SELECT status FROM workers WHERE id = ${workerId}`) as Array<{
      status: string;
    }>;
    expect(rows[0]!.status).toBe("ready");
  });
});

describe("outbox dispatcher", () => {
  /** Queue an outbox entry the way a workflow step would. */
  async function enqueue(organizationId: string, kind: string, payload: Record<string, unknown>) {
    return withOrg(organizationId, async ({ sql }) => {
      const rows = (await sql`
        INSERT INTO workflow_outbox (organization_id, kind, payload)
        VALUES (${organizationId}, ${kind}, ${payload}::jsonb)
        RETURNING id`) as Array<{ id: number }>;
      return rows[0]!.id;
    });
  }

  test("dispatches a pending entry exactly once", async () => {
    const id = await enqueue(ORG_A, "test.notify", { hello: "world" });

    const seen: Array<Record<string, unknown>> = [];
    const { handled } = await dispatchOutbox({
      "test.notify": async (entry) => {
        seen.push(entry.payload);
      },
    });

    expect(handled).toBeGreaterThanOrEqual(1);
    expect(seen).toContainEqual({ hello: "world" });

    // A dispatched entry must not be picked up again.
    const again = await dispatchOutbox({ "test.notify": async () => {} });
    const row = await withSystemScope("outbox-dispatcher", async ({ sql }) => {
      const rows = (await sql`
        SELECT dispatched_at FROM workflow_outbox WHERE id = ${id}`) as Array<{
        dispatched_at: string | null;
      }>;
      return rows[0]!;
    });
    expect(row.dispatched_at).not.toBeNull();
    expect(again.handled).toBe(0);
  });

  test("retries a failing handler with backoff rather than dropping it", async () => {
    // A failing integration must not lose the side effect.
    const id = await enqueue(ORG_A, "test.flaky", {});

    await dispatchOutbox({
      "test.flaky": async () => {
        throw new Error("downstream unavailable");
      },
    });

    const row = await withSystemScope("outbox-dispatcher", async ({ sql }) => {
      const rows = (await sql`
        SELECT attempts, last_error, dispatched_at, next_attempt_at > now() AS deferred
        FROM workflow_outbox WHERE id = ${id}`) as Array<{
        attempts: number;
        last_error: string;
        dispatched_at: string | null;
        deferred: boolean;
      }>;
      return rows[0]!;
    });

    expect(row.attempts).toBe(1);
    expect(row.last_error).toMatch(/downstream unavailable/);
    expect(row.dispatched_at).toBeNull();
    expect(row.deferred).toBe(true);
  });

  test("an unknown kind is retried, not silently discarded", async () => {
    const id = await enqueue(ORG_A, "test.unhandled", {});
    await dispatchOutbox({});

    const row = await withSystemScope("outbox-dispatcher", async ({ sql }) => {
      const rows = (await sql`
        SELECT last_error, dispatched_at FROM workflow_outbox WHERE id = ${id}`) as Array<{
        last_error: string;
        dispatched_at: string | null;
      }>;
      return rows[0]!;
    });

    expect(row.last_error).toMatch(/no handler/);
    expect(row.dispatched_at).toBeNull();
  });
});

describe("Sweeper loop", () => {
  test("keeps running after a sweep throws", async () => {
    // A failing sweep must not kill the loop, or the system silently stops
    // reclaiming work with no indication that it has.
    let calls = 0;
    const sweeper = new Sweeper(
      "run-lease-reaper",
      async () => {
        calls++;
        if (calls === 1) throw new Error("transient");
        return { handled: 0 };
      },
      { intervalMs: 10 },
    );

    sweeper.start();
    await Bun.sleep(120);
    await sweeper.stop();

    expect(calls).toBeGreaterThan(1);
  });

  test("stops cleanly", async () => {
    let calls = 0;
    const sweeper = new Sweeper(
      "run-lease-reaper",
      async () => {
        calls++;
        return { handled: 0 };
      },
      { intervalMs: 10 },
    );

    sweeper.start();
    await Bun.sleep(60);
    await sweeper.stop();

    const afterStop = calls;
    await Bun.sleep(60);
    expect(calls).toBe(afterStop);
  });

  test("drains a backlog without sleeping between batches", async () => {
    // While there is work, the loop should continue immediately; a long
    // interval must not throttle a burst.
    let remaining = 3;
    const sweeper = new Sweeper(
      "run-lease-reaper",
      async () => (remaining-- > 0 ? { handled: 1 } : { handled: 0 }),
      { intervalMs: 10_000 },
    );

    sweeper.start();
    await Bun.sleep(100);
    await sweeper.stop();

    expect(remaining).toBeLessThan(0);
  });
});
