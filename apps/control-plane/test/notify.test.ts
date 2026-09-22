/**
 * The bridge from finished phase Runs to the workflows waiting on them.
 *
 * Without this the delivery workflow parks forever: it waits on a
 * `phase.finished` signal, and nothing else produces one. The properties that
 * matter are that it fires exactly once per Run, that it covers Runs nobody
 * reported (a node that died mid-Run), and that it does not wake workflows
 * that are not waiting.
 */

import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { SQL } from "bun";
import { closePool, setPool, withOrg } from "../src/db/client.ts";
import { PostgresWorkflowRuntime } from "../src/workflow/runtime.ts";
import { notifyPhaseFinished } from "../src/workflow/notify.ts";
import { Signals } from "../src/workflow/delivery.workflow.ts";
import type { WorkflowDefinition } from "@dude/domain";

const OWNER_URL = process.env.DATABASE_URL ?? "postgres://dude:dude@localhost:5433/dude";
const APP_URL = process.env.TEST_APP_DATABASE_URL ?? "postgres://dude_app:dude_app@localhost:5433/dude";

const ORG = `org_ntf_${Bun.randomUUIDv7("hex").slice(-8)}`;

let owner: SQL;
/** This file's pool, so closing it cannot sever another file's. */
let app: SQL;
let runtime: PostgresWorkflowRuntime;
let projectId: string;

/** Records the signals it was woken with, so a test can assert on them. */
const received: Array<{ runId: string; status: string }> = [];

const waiter: WorkflowDefinition = {
  type: "test.phase-waiter",
  initialStep: "park",
  steps: {
    async park(ctx) {
      for (const signal of ctx.signals) {
        if (signal.name === Signals.PhaseFinished) {
          received.push({
            runId: String(signal.payload.runId),
            status: String(signal.payload.status),
          });
        }
      }
      return { next: "park", state: ctx.state, awaitSignals: [Signals.PhaseFinished] };
    },
  },
};

beforeAll(async () => {
  owner = new SQL(OWNER_URL);
  await owner`INSERT INTO organizations (id, name, slug) VALUES (${ORG}, ${ORG}, ${ORG})
              ON CONFLICT (id) DO NOTHING`;

  app = new SQL(APP_URL);
  setPool(app);

  projectId = `prj_${Bun.randomUUIDv7("hex").slice(-8)}`;
  await owner`INSERT INTO projects (id, organization_id, name, slug)
              VALUES (${projectId}, ${ORG}, 'Notify', ${`ntf-${Bun.randomUUIDv7("hex").slice(-6)}`})`;

  runtime = new PostgresWorkflowRuntime("notify-test");
  runtime.register(waiter);
});

afterAll(async () => {
  await closePool(app);
  await owner`DELETE FROM organizations WHERE id = ${ORG}`;
  await owner.end();
});

/** A work item with a workflow parked on it, and one phase Run. */
async function scenario(options: { status?: string; phase?: string } = {}) {
  const workItemId = `wi_${Bun.randomUUIDv7("hex").slice(-8)}`;
  const runId = `run_${Bun.randomUUIDv7("hex").slice(-8)}`;

  await owner`INSERT INTO work_items (id, organization_id, project_id, title)
              VALUES (${workItemId}, ${ORG}, ${projectId}, 'Notify me')`;

  const { workflowRunId } = await runtime.start({
    workflowType: waiter.type,
    organizationId: ORG,
    idempotencyKey: `ntf-${workItemId}`,
    workItemId,
    input: {},
  });
  // Park it, so the notifier has something waiting to wake.
  await runtime.tick();

  await owner`
    INSERT INTO runs (id, organization_id, project_id, work_item_id, attempt, status, phase, ended_at)
    VALUES (${runId}, ${ORG}, ${projectId}, ${workItemId}, 1,
            ${options.status ?? "completed"}::run_status,
            ${options.phase ?? "implement"}::run_phase, now())`;

  return { workItemId, runId, workflowRunId };
}

describe("phase notifier", () => {
  test("signals the workflow waiting on a finished Run", async () => {
    const { runId } = await scenario();
    received.length = 0;

    const { handled } = await notifyPhaseFinished(runtime);
    expect(handled).toBe(1);

    await runtime.tick();
    expect(received).toContainEqual({ runId, status: "completed" });
  });

  test("marks the Run so the next sweep does not repeat it", async () => {
    const { runId } = await scenario();

    await notifyPhaseFinished(runtime);
    const second = await notifyPhaseFinished(runtime);

    // Without the mark, every sweep would re-signal every finished Run
    // forever.
    expect(second.handled).toBe(0);

    const [row] = (await owner`
      SELECT phase_notified_at FROM runs WHERE id = ${runId}`) as Array<{
      phase_notified_at: string | null;
    }>;
    expect(row!.phase_notified_at).not.toBeNull();
  });

  test("a failed Run is reported too, with its status", async () => {
    // The workflow decides what a failure means; the notifier just delivers
    // it. Reporting only successes would park the workflow forever on a
    // phase that failed.
    const { runId } = await scenario({ status: "failed" });
    received.length = 0;

    await notifyPhaseFinished(runtime);
    await runtime.tick();

    expect(received).toContainEqual({ runId, status: "failed" });
  });

  test("an aborted Run is reported", async () => {
    const { runId } = await scenario({ status: "aborted" });
    received.length = 0;

    await notifyPhaseFinished(runtime);
    await runtime.tick();

    expect(received).toContainEqual({ runId, status: "aborted" });
  });

  test("a still-running Run is not reported", async () => {
    const { runId } = await scenario({ status: "running" });

    await notifyPhaseFinished(runtime);

    const [row] = (await owner`
      SELECT phase_notified_at FROM runs WHERE id = ${runId}`) as Array<{
      phase_notified_at: string | null;
    }>;
    expect(row!.phase_notified_at).toBeNull();
  });

  test("a phaseless Run beside a running workflow does not wake it", async () => {
    /*
     * The case the phase guard exists for: a person creates a Run directly
     * on a work item that already has a delivery workflow in flight. That
     * Run has no phase and the workflow never asked for it, so finishing it
     * must not be reported as a phase completing — the workflow would
     * advance a step it had not actually finished.
     */
    const { workItemId } = await scenario();
    const strayRunId = `run_${Bun.randomUUIDv7("hex").slice(-8)}`;
    await owner`INSERT INTO runs (id, organization_id, project_id, work_item_id, attempt, status, ended_at)
                VALUES (${strayRunId}, ${ORG}, ${projectId}, ${workItemId}, 2, 'completed', now())`;

    received.length = 0;
    await notifyPhaseFinished(runtime);
    await runtime.tick();

    expect(received.some((r) => r.runId === strayRunId)).toBe(false);

    const [row] = (await owner`
      SELECT phase_notified_at FROM runs WHERE id = ${strayRunId}`) as Array<{
      phase_notified_at: string | null;
    }>;
    expect(row!.phase_notified_at).toBeNull();
  });

  test("delivers once even when the same Run is swept twice concurrently", async () => {
    // The idempotency key is what guarantees this; a duplicate signal would
    // make a workflow think two phases finished.
    const { runId } = await scenario();
    received.length = 0;

    await Promise.all([notifyPhaseFinished(runtime), notifyPhaseFinished(runtime)]);
    await runtime.tick();

    expect(received.filter((r) => r.runId === runId)).toHaveLength(1);
  });
});
