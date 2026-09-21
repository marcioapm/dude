/**
 * Durable workflow runtime tests.
 *
 * The interesting properties are all about crash- and concurrency-safety, so
 * these run against real PostgreSQL: idempotent starts, signals that arrive
 * before the wait, retry with backoff, dead-lettering, and two pollers never
 * advancing the same run.
 */

import { afterAll, beforeAll, beforeEach, describe, expect, test } from "bun:test";
import { SQL } from "bun";
import type { WorkflowDefinition } from "@dude/domain";
import { closePool, setPool, withOrg } from "../src/db/client.ts";
import { PostgresWorkflowRuntime, backoffMs } from "../src/workflow/runtime.ts";

const OWNER_URL = process.env.DATABASE_URL ?? "postgres://dude:dude@localhost:5433/dude";
const APP_URL = process.env.TEST_APP_DATABASE_URL ?? "postgres://dude_app:dude_app@localhost:5433/dude";
const ORG = `org_wf_${Bun.randomUUIDv7("hex").slice(0, 8)}`;

let owner: SQL;
let runtime: PostgresWorkflowRuntime;

/** Records which steps ran, so tests can assert on the path taken. */
let trace: string[] = [];

/** linear: a -> b -> done */
const linear: WorkflowDefinition = {
  type: "test.linear",
  initialStep: "a",
  steps: {
    a: async () => {
      trace.push("a");
      return { next: "b", state: { visited: ["a"] } };
    },
    b: async ({ state }) => {
      trace.push("b");
      return { next: null, state: { ...state, done: true } };
    },
  },
};

/** waiter: parks until an "approved" signal arrives. */
const waiter: WorkflowDefinition = {
  type: "test.waiter",
  initialStep: "ask",
  steps: {
    ask: async () => {
      trace.push("ask");
      return { next: "receive", awaitSignals: ["approved"] };
    },
    receive: async ({ signals }) => {
      trace.push(`receive:${signals.map((s) => s.name).join(",")}`);
      return { next: null, state: { answer: signals[0]?.payload } };
    },
  },
};

/** flaky: fails `failures` times, then succeeds. */
let failures = 0;
const flaky: WorkflowDefinition = {
  type: "test.flaky",
  initialStep: "try",
  maxAttempts: 3,
  steps: {
    try: async ({ attempt }) => {
      trace.push(`try:${attempt}`);
      if (failures-- > 0) throw new Error("transient failure");
      return { next: null };
    },
  },
};

/** doomed: always fails, to exercise dead-lettering. */
const doomed: WorkflowDefinition = {
  type: "test.doomed",
  initialStep: "fail",
  maxAttempts: 2,
  steps: {
    fail: async () => {
      trace.push("fail");
      throw new Error("permanent failure");
    },
  },
};

/** Advance until no workflow is runnable, or `maxTicks` is reached. */
async function drain(maxTicks = 20): Promise<void> {
  for (let i = 0; i < maxTicks; i++) {
    if ((await runtime.tick(ORG)) === 0) return;
  }
}

beforeAll(async () => {
  owner = new SQL(OWNER_URL);
  await owner`INSERT INTO organizations (id, name, slug) VALUES (${ORG}, ${ORG}, ${ORG})
              ON CONFLICT (id) DO NOTHING`;
  setPool(new SQL(APP_URL));

  runtime = new PostgresWorkflowRuntime("test-poller");
  for (const def of [linear, waiter, flaky, doomed]) runtime.register(def);
});

afterAll(async () => {
  await closePool();
  await owner`DELETE FROM organizations WHERE id = ${ORG}`;
  await owner.end();
});

beforeEach(() => {
  trace = [];
});

describe("start", () => {
  test("runs a linear workflow to completion", async () => {
    const { workflowRunId } = await runtime.start({
      workflowType: "test.linear",
      organizationId: ORG,
      idempotencyKey: `linear-${Bun.randomUUIDv7("hex").slice(0, 8)}`,
      input: {},
    });

    await drain();

    const state = await runtime.get(workflowRunId);
    expect(state?.status).toBe("completed");
    expect(state?.state).toMatchObject({ done: true });
    expect(trace).toEqual(["a", "b"]);
  });

  test("deduplicates by idempotency key", async () => {
    const key = `dedupe-${Bun.randomUUIDv7("hex").slice(0, 8)}`;
    const first = await runtime.start({
      workflowType: "test.linear", organizationId: ORG, idempotencyKey: key, input: {},
    });
    const second = await runtime.start({
      workflowType: "test.linear", organizationId: ORG, idempotencyKey: key, input: {},
    });

    expect(second.workflowRunId).toBe(first.workflowRunId);
    expect(first.deduplicated).toBe(false);
    expect(second.deduplicated).toBe(true);
  });

  test("rejects an unregistered workflow type", async () => {
    await expect(
      runtime.start({
        workflowType: "test.nonexistent", organizationId: ORG, idempotencyKey: "x", input: {},
      }),
    ).rejects.toThrow(/unknown workflow type/);
  });
});

describe("signals", () => {
  test("parks without consuming resources, then resumes on signal", async () => {
    const { workflowRunId } = await runtime.start({
      workflowType: "test.waiter",
      organizationId: ORG,
      idempotencyKey: `wait-${Bun.randomUUIDv7("hex").slice(0, 8)}`,
      input: {},
    });

    await drain();

    // Parked: the step ran, and no further work is possible for this run.
    // `trace` is shared, and drain() advances every runnable workflow in the
    // org, so assert on this workflow's own steps rather than the whole trace.
    let state = await runtime.get(workflowRunId);
    expect(state?.status).toBe("waiting");
    expect(state?.step).toBe("receive");
    expect(trace).toContain("ask");

    await runtime.signal(workflowRunId, "approved", { by: "marcio" });
    await drain();

    state = await runtime.get(workflowRunId);
    expect(state?.status).toBe("completed");
    expect(state?.state).toMatchObject({ answer: { by: "marcio" } });
  });

  test("delivers a signal that arrives before the workflow waits for it", async () => {
    const { workflowRunId } = await runtime.start({
      workflowType: "test.waiter",
      organizationId: ORG,
      idempotencyKey: `early-${Bun.randomUUIDv7("hex").slice(0, 8)}`,
      input: {},
    });

    // Signal first — the inbox must hold it rather than drop it.
    await runtime.signal(workflowRunId, "approved", { early: true });
    await drain();

    const state = await runtime.get(workflowRunId);
    expect(state?.status).toBe("completed");
    expect(state?.state).toMatchObject({ answer: { early: true } });
  });

  test("deduplicates signals by idempotency key", async () => {
    const { workflowRunId } = await runtime.start({
      workflowType: "test.waiter",
      organizationId: ORG,
      idempotencyKey: `sigdedupe-${Bun.randomUUIDv7("hex").slice(0, 8)}`,
      input: {},
    });

    // A redelivered webhook must not enqueue the signal twice.
    await runtime.signal(workflowRunId, "approved", { n: 1 }, "delivery-1");
    await runtime.signal(workflowRunId, "approved", { n: 1 }, "delivery-1");

    const pending = await withOrg(ORG, async ({ sql }) => {
      return (await sql`
        SELECT count(*)::int AS n FROM workflow_signals
        WHERE workflow_run_id = ${workflowRunId}`) as Array<{ n: number }>;
    });
    expect(pending[0]!.n).toBe(1);
  });
});

describe("failure handling", () => {
  test("retries a failing step, then succeeds", async () => {
    failures = 2;
    const { workflowRunId } = await runtime.start({
      workflowType: "test.flaky",
      organizationId: ORG,
      idempotencyKey: `flaky-${Bun.randomUUIDv7("hex").slice(0, 8)}`,
      input: {},
    });

    // First attempt fails and parks on a backoff timer.
    await runtime.tick(ORG);
    let state = await runtime.get(workflowRunId);
    expect(state?.attempt).toBe(1);
    expect(state?.lastError).toMatch(/transient failure/);

    // Clear the backoff rather than sleeping through it.
    await withOrg(ORG, async ({ sql }) => {
      await sql`UPDATE workflow_runs SET wake_at = now() WHERE id = ${workflowRunId}`;
    });
    await runtime.tick(ORG);

    await withOrg(ORG, async ({ sql }) => {
      await sql`UPDATE workflow_runs SET wake_at = now() WHERE id = ${workflowRunId}`;
    });
    await runtime.tick(ORG);

    state = await runtime.get(workflowRunId);
    expect(state?.status).toBe("completed");
    // The attempt counter resets once a step succeeds.
    expect(state?.attempt).toBe(0);
  });

  test("dead-letters after exhausting attempts, staying inspectable", async () => {
    const { workflowRunId } = await runtime.start({
      workflowType: "test.doomed",
      organizationId: ORG,
      idempotencyKey: `doomed-${Bun.randomUUIDv7("hex").slice(0, 8)}`,
      input: {},
    });

    for (let i = 0; i < 3; i++) {
      await runtime.tick(ORG);
      await withOrg(ORG, async ({ sql }) => {
        await sql`UPDATE workflow_runs SET wake_at = now() WHERE id = ${workflowRunId}`;
      });
    }

    const state = await runtime.get(workflowRunId);
    expect(state?.status).toBe("dead_lettered");
    expect(state?.lastError).toMatch(/permanent failure/);
    // A dead-lettered run is never silently retried.
    expect(await runtime.tick(ORG)).toBe(0);
  });

  test("backs off exponentially with a ceiling", () => {
    expect(backoffMs(1)).toBe(1000);
    expect(backoffMs(2)).toBe(2000);
    expect(backoffMs(3)).toBe(4000);
    expect(backoffMs(99)).toBe(60_000);
  });
});

describe("concurrency", () => {
  test("two pollers never advance the same run", async () => {
    const other = new PostgresWorkflowRuntime("test-poller-2");
    other.register(linear);

    // Drain anything left runnable by earlier tests, so the counts below
    // reflect only this workflow.
    await drain();
    trace = [];

    const { workflowRunId } = await runtime.start({
      workflowType: "test.linear",
      organizationId: ORG,
      idempotencyKey: `race-${Bun.randomUUIDv7("hex").slice(0, 8)}`,
      input: {},
    });

    // Both poll simultaneously; SKIP LOCKED must give the run to exactly one.
    const [a, b] = await Promise.all([runtime.tick(ORG), other.tick(ORG)]);
    expect(a + b).toBe(1);

    // Step "a" ran once, not twice.
    expect(trace.filter((t) => t === "a")).toHaveLength(1);

    const state = await runtime.get(workflowRunId);
    expect(state?.step).toBe("b");
  });
});

describe("abort", () => {
  test("stops a parked workflow", async () => {
    const { workflowRunId } = await runtime.start({
      workflowType: "test.waiter",
      organizationId: ORG,
      idempotencyKey: `abort-${Bun.randomUUIDv7("hex").slice(0, 8)}`,
      input: {},
    });
    await drain();

    await runtime.abort(workflowRunId, "requirement changed");

    const state = await runtime.get(workflowRunId);
    expect(state?.status).toBe("aborted");
    expect(state?.lastError).toBe("requirement changed");
    // An aborted run does not resume even if its signal arrives later.
    await runtime.signal(workflowRunId, "approved", {});
    expect(await runtime.tick(ORG)).toBe(0);
  });
});
