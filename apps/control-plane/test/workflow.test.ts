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

    const state = await runtime.get(ORG, workflowRunId);
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
    let state = await runtime.get(ORG, workflowRunId);
    expect(state?.status).toBe("waiting");
    expect(state?.step).toBe("receive");
    expect(trace).toContain("ask");

    await runtime.signal(ORG, workflowRunId, "approved", { by: "marcio" });
    await drain();

    state = await runtime.get(ORG, workflowRunId);
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
    await runtime.signal(ORG, workflowRunId, "approved", { early: true });
    await drain();

    const state = await runtime.get(ORG, workflowRunId);
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
    await runtime.signal(ORG, workflowRunId, "approved", { n: 1 }, "delivery-1");
    await runtime.signal(ORG, workflowRunId, "approved", { n: 1 }, "delivery-1");

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
    let state = await runtime.get(ORG, workflowRunId);
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

    state = await runtime.get(ORG, workflowRunId);
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

    const state = await runtime.get(ORG, workflowRunId);
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

    const state = await runtime.get(ORG, workflowRunId);
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

    await runtime.abort(ORG, workflowRunId, "requirement changed");

    const state = await runtime.get(ORG, workflowRunId);
    expect(state?.status).toBe("aborted");
    expect(state?.lastError).toBe("requirement changed");
    // An aborted run does not resume even if its signal arrives later.
    await runtime.signal(ORG, workflowRunId, "approved", {});
    expect(await runtime.tick(ORG)).toBe(0);
  });
});

// ---------------------------------------------------------------------------
// Regressions found by code review. Each of these passed silently before the
// corresponding fix, which is why they are asserted explicitly.
// ---------------------------------------------------------------------------

describe("signal durability", () => {
  test("a signal survives a step that throws", async () => {
    // Consuming signals before the step committed meant a transient failure
    // silently discarded a human approval or a CI webhook, and the retry saw
    // an empty signal list.
    let attempts = 0;
    const seen: string[][] = [];

    const runtimeLocal = new PostgresWorkflowRuntime("signal-durability");
    runtimeLocal.register({
      type: "test.signal_retry",
      initialStep: "ask",
      maxAttempts: 3,
      steps: {
        ask: async () => ({ next: "receive", awaitSignals: ["approved"] }),
        receive: async ({ signals }) => {
          seen.push(signals.map((s) => s.name));
          if (attempts++ === 0) throw new Error("transient failure");
          return { next: null, state: { answer: signals[0]?.payload ?? "NONE" } };
        },
      },
    });

    const { workflowRunId } = await runtimeLocal.start({
      workflowType: "test.signal_retry",
      organizationId: ORG,
      idempotencyKey: `sigretry-${Bun.randomUUIDv7("hex").slice(0, 8)}`,
      input: {},
    });

    await runtimeLocal.tick(ORG);
    await runtimeLocal.signal(ORG, workflowRunId, "approved", { by: "human" });

    // First execution throws; the signal must remain for the retry.
    await runtimeLocal.tick(ORG);
    await withOrg(ORG, async ({ sql }) => {
      await sql`UPDATE workflow_runs SET wake_at = now() WHERE id = ${workflowRunId}`;
    });
    await runtimeLocal.tick(ORG);

    const state = await runtimeLocal.get(ORG, workflowRunId);
    expect(state?.status).toBe("completed");
    expect(state?.state).toMatchObject({ answer: { by: "human" } });
    // Both executions saw the signal, rather than the retry seeing nothing.
    expect(seen).toEqual([["approved"], ["approved"]]);
  });

  test("a failing step keeps the set of signals it was waiting for", async () => {
    const runtimeLocal = new PostgresWorkflowRuntime("awaiting-preserved");
    runtimeLocal.register({
      type: "test.awaiting_preserved",
      initialStep: "park",
      maxAttempts: 5,
      steps: {
        park: async () => ({ next: "work", awaitSignals: ["pr_approved", "pr_rejected"] }),
        work: async () => {
          throw new Error("transient failure");
        },
      },
    });

    const { workflowRunId } = await runtimeLocal.start({
      workflowType: "test.awaiting_preserved",
      organizationId: ORG,
      idempotencyKey: `awaiting-${Bun.randomUUIDv7("hex").slice(0, 8)}`,
      input: {},
    });

    await runtimeLocal.tick(ORG);
    await runtimeLocal.signal(ORG, workflowRunId, "pr_approved", {});
    await runtimeLocal.tick(ORG); // fails, parks on backoff

    const state = await runtimeLocal.get(ORG, workflowRunId);
    // Clearing this would make the run unwakeable by either signal.
    expect(state?.awaitingSignals.sort()).toEqual(["pr_approved", "pr_rejected"]);
  });
});

describe("abort safety", () => {
  test("an abort during a step is not overwritten by that step", async () => {
    // The transition used to key on id alone, so a slow step's commit
    // resurrected a run the user had already cancelled.
    const runtimeLocal = new PostgresWorkflowRuntime("abort-race");
    runtimeLocal.register({
      type: "test.slow",
      initialStep: "slow",
      steps: {
        slow: async () => {
          await Bun.sleep(400);
          return { next: "more" };
        },
        more: async () => ({ next: null }),
      },
    });

    const { workflowRunId } = await runtimeLocal.start({
      workflowType: "test.slow",
      organizationId: ORG,
      idempotencyKey: `abortrace-${Bun.randomUUIDv7("hex").slice(0, 8)}`,
      input: {},
    });

    const ticking = runtimeLocal.tick(ORG);
    await Bun.sleep(100);
    await runtimeLocal.abort(ORG, workflowRunId, "user cancelled");
    await ticking;

    const state = await runtimeLocal.get(ORG, workflowRunId);
    expect(state?.status).toBe("aborted");
    expect(state?.lastError).toBe("user cancelled");
    // And it must stay stopped rather than being picked up again.
    expect(await runtimeLocal.tick(ORG)).toBe(0);
  });

  test("sleepUntil does not resurrect a terminal run", async () => {
    const { workflowRunId } = await runtime.start({
      workflowType: "test.linear",
      organizationId: ORG,
      idempotencyKey: `sleepterm-${Bun.randomUUIDv7("hex").slice(0, 8)}`,
      input: {},
    });
    await drain();
    expect((await runtime.get(ORG, workflowRunId))?.status).toBe("completed");

    await runtime.sleepUntil(ORG, workflowRunId, new Date(Date.now() - 1000));

    const state = await runtime.get(ORG, workflowRunId);
    expect(state?.status).toBe("completed");
  });
});

describe("tenant isolation", () => {
  test("one organization cannot abort, signal or read another's workflow", async () => {
    // Run ids travel in webhook URLs and events, so holding one must not
    // confer authority over the run.
    const otherOrg = `org_wf_other_${Bun.randomUUIDv7("hex").slice(0, 8)}`;
    await owner`INSERT INTO organizations (id, name, slug) VALUES (${otherOrg}, ${otherOrg}, ${otherOrg})`;

    try {
      const { workflowRunId } = await runtime.start({
        workflowType: "test.waiter",
        organizationId: ORG,
        idempotencyKey: `victim-${Bun.randomUUIDv7("hex").slice(0, 8)}`,
        input: {},
      });
      await drain();

      // Reads disclose nothing.
      expect(await runtime.get(otherOrg, workflowRunId)).toBeNull();

      // Signals are refused rather than injected.
      await expect(
        runtime.signal(otherOrg, workflowRunId, "approved", { by: "attacker" }),
      ).rejects.toThrow(/not found/);

      // Aborts are a no-op for a foreign tenant.
      await runtime.abort(otherOrg, workflowRunId, "cross-tenant abort");
      const state = await runtime.get(ORG, workflowRunId);
      expect(state?.status).toBe("waiting");
      expect(state?.lastError).toBeNull();
    } finally {
      await owner`DELETE FROM organizations WHERE id = ${otherOrg}`;
    }
  });
});
