/**
 * The delivery workflow's transitions.
 *
 * Driven against a real PostgreSQL and the real runtime, because the thing
 * under test is whether a *durable* state machine loops correctly — and the
 * durability is the part that breaks. Phase Runs are completed by hand here
 * rather than by a runner: the question is what the workflow does with an
 * outcome, not whether an agent can produce one.
 *
 * The properties that matter:
 *   - review fans out and waits for all of its Runs, not the first;
 *   - blocking findings send it back through fix, and clear ones do not;
 *   - the loop ends at its bound instead of spending forever;
 *   - PR feedback that is not actionable never wakes a fixer.
 */

import { afterAll, afterEach, beforeAll, describe, expect, test } from "bun:test";
import { SQL } from "bun";
import { EventTypes, newId } from "@dude/domain";
import { closePool, setPool, withOrg } from "../src/db/client.ts";
import { PostgresWorkflowRuntime } from "../src/workflow/runtime.ts";
import { appendInScope } from "../src/events/ledger.ts";
import {
  DEFAULT_DELIVERY_POLICY,
  Signals,
  deliveryWorkflow,
  DELIVERY_WORKFLOW_TYPE,
} from "../src/workflow/delivery.workflow.ts";
import { pathsFromDiffstat } from "../src/workflow/delivery.ts";

const OWNER_URL = process.env.DATABASE_URL ?? "postgres://dude:dude@localhost:5433/dude";
const APP_URL = process.env.TEST_APP_DATABASE_URL ?? "postgres://dude_app:dude_app@localhost:5433/dude";

const ORG = `org_dlv_${Bun.randomUUIDv7("hex").slice(-8)}`;

let owner: SQL;
/** This file's pool, so closing it cannot sever another file's. */
let app: SQL;
let runtime: PostgresWorkflowRuntime;
let projectId: string;
let repositoryId: string;

beforeAll(async () => {
  owner = new SQL(OWNER_URL);
  await owner`INSERT INTO organizations (id, name, slug) VALUES (${ORG}, ${ORG}, ${ORG})
              ON CONFLICT (id) DO NOTHING`;

  app = new SQL(APP_URL);
  setPool(app);

  projectId = `prj_${Bun.randomUUIDv7("hex").slice(-8)}`;
  repositoryId = `repo_${Bun.randomUUIDv7("hex").slice(-8)}`;
  await owner`INSERT INTO projects (id, organization_id, name, slug)
              VALUES (${projectId}, ${ORG}, 'Delivery', ${`dlv-${Bun.randomUUIDv7("hex").slice(-6)}`})`;
  await owner`INSERT INTO repositories (id, organization_id, project_id, name, url)
              VALUES (${repositoryId}, ${ORG}, ${projectId}, 'target',
                      'https://github.com/acme/target.git')`;

  runtime = new PostgresWorkflowRuntime("delivery-test");
  runtime.register(deliveryWorkflow);
});

afterAll(async () => {
  await closePool(app);
  await owner`DELETE FROM organizations WHERE id = ${ORG}`;
  await owner.end();
});

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

async function newWorkItem(title = "Deliver me"): Promise<string> {
  const id = `wi_${Bun.randomUUIDv7("hex").slice(-8)}`;
  await owner`INSERT INTO work_items (id, organization_id, project_id, title, goal)
              VALUES (${id}, ${ORG}, ${projectId}, ${title}, 'Make it work')`;
  return id;
}

/** Advance until the workflow parks or completes. */
async function drain(maxTicks = 50): Promise<void> {
  for (let i = 0; i < maxTicks; i++) {
    if ((await runtime.tick()) === 0) return;
  }
}

async function stateOf(workflowRunId: string) {
  const run = await runtime.get(ORG, workflowRunId);
  return {
    status: run?.status,
    step: run?.step,
    state: (run?.state ?? {}) as Record<string, unknown>,
  };
}

/** The phase Runs this workflow is currently waiting on. */
async function pendingRuns(workflowRunId: string): Promise<string[]> {
  const { state } = await stateOf(workflowRunId);
  return (state.pendingRunIds as string[] | undefined) ?? [];
}

/**
 * Finish a phase Run the way a runner would, then signal the workflow.
 *
 * `diffstat` is what selects the conditional reviewers downstream, so a test
 * that wants a security reviewer supplies a path under `auth/`.
 */
async function completeRun(
  workflowRunId: string,
  runId: string,
  options: { status?: string; diffstat?: string; headSha?: string } = {},
): Promise<void> {
  const status = options.status ?? "completed";
  const headSha = options.headSha ?? `sha_${Bun.randomUUIDv7("hex").slice(-8)}`;

  await withOrg(ORG, async (scope) => {
    await scope.sql`UPDATE runs SET status = ${status}::run_status, head_sha = ${headSha},
                    branch = 'dude/test', ended_at = now() WHERE id = ${runId}`;

    if (options.diffstat !== undefined) {
      const rows = (await scope.sql`
        SELECT work_item_id FROM runs WHERE id = ${runId}`) as Array<{ work_item_id: string }>;
      await appendInScope(scope, {
        eventType: EventTypes.GitCommitCreated,
        organizationId: ORG,
        projectId,
        workItemId: rows[0]!.work_item_id,
        runId,
        actor: { type: "agent", id: "test" },
        source: "runner",
        payload: { headSha, diffstat: options.diffstat, repo: "target" },
      });
    }
  });

  await runtime.signal(ORG, workflowRunId, Signals.PhaseFinished, { runId, status });
}

async function addFinding(
  workItemId: string,
  runId: string,
  severity: string,
  options: { file?: string; category?: string; title?: string } = {},
): Promise<string> {
  const id = newId("event").replace("evt", "find");
  await withOrg(ORG, async (scope) => {
    await scope.sql`
      INSERT INTO review_findings (
        id, organization_id, work_item_id, run_id, category, severity, file, title)
      VALUES (${id}, ${ORG}, ${workItemId}, ${runId}, ${options.category ?? "correctness"},
              ${severity}::finding_severity, ${options.file ?? "src/thing.ts"},
              ${options.title ?? "Something is wrong"})`;
  });
  return id;
}

/** Start the workflow and run it up to its first park. */
async function startDelivery(
  workItemId: string,
  policy = DEFAULT_DELIVERY_POLICY,
): Promise<string> {
  const { workflowRunId } = await runtime.start({
    workflowType: DELIVERY_WORKFLOW_TYPE,
    organizationId: ORG,
    idempotencyKey: `dlv-${workItemId}`,
    workItemId,
    input: { workItemId, projectId, repositoryId, policy },
  });
  await drain();
  return workflowRunId;
}

// ---------------------------------------------------------------------------

describe("implement", () => {
  test("creates one implementer Run and parks on it", async () => {
    const workItemId = await newWorkItem();
    const workflowRunId = await startDelivery(workItemId);

    const { status, step } = await stateOf(workflowRunId);
    expect(status).toBe("waiting");
    expect(step).toBe("awaitImplement");

    const runs = (await owner`
      SELECT phase, role, status FROM runs WHERE work_item_id = ${workItemId}`) as Array<{
      phase: string;
      role: string;
      status: string;
    }>;
    expect(runs).toHaveLength(1);
    expect(runs[0]).toMatchObject({ phase: "implement", role: "implementer", status: "pending" });
  });

  test("an implementer that changed nothing is escalated, not reviewed", async () => {
    // There is nothing for a reviewer to look at, and pretending otherwise
    // would spend a reviewer Run to discover that.
    const workItemId = await newWorkItem();
    const workflowRunId = await startDelivery(workItemId);
    const [runId] = await pendingRuns(workflowRunId);

    await completeRun(workflowRunId, runId!, { diffstat: "" });
    await drain();

    const { status, state } = await stateOf(workflowRunId);
    expect(status).toBe("completed");
    expect(state.escalation).toMatchObject({ reason: "no_changes" });
  });

  test("a failed implementer is escalated", async () => {
    const workItemId = await newWorkItem();
    const workflowRunId = await startDelivery(workItemId);
    const [runId] = await pendingRuns(workflowRunId);

    await completeRun(workflowRunId, runId!, { status: "failed" });
    await drain();

    const { state } = await stateOf(workflowRunId);
    expect(state.escalation).toMatchObject({ reason: "implement_failed" });
  });
});

describe("review fan-out", () => {
  test("the diff decides how many reviewers run", async () => {
    const workItemId = await newWorkItem();
    const workflowRunId = await startDelivery(workItemId);
    const [implementRun] = await pendingRuns(workflowRunId);

    // Touching auth summons the security reviewer alongside correctness.
    await completeRun(workflowRunId, implementRun!, {
      diffstat: " src/auth/session.ts | 12 ++++++\n 1 file changed",
    });
    await drain();

    const reviews = (await owner`
      SELECT id, phase, role FROM runs
      WHERE work_item_id = ${workItemId} AND phase = 'review'`) as Array<{ role: string }>;
    expect(reviews.length).toBeGreaterThanOrEqual(2);
    expect(reviews.every((r) => r.role === "reviewer")).toBe(true);
  });

  test("waits for every reviewer, not the first", async () => {
    /*
     * Acting on a partial finding set would send the fixer back for the rest
     * immediately — one wasted Run per reviewer that had not reported yet.
     */
    const workItemId = await newWorkItem();
    const workflowRunId = await startDelivery(workItemId);
    const [implementRun] = await pendingRuns(workflowRunId);
    await completeRun(workflowRunId, implementRun!, {
      diffstat: " src/auth/session.ts | 3 +\n migrations/9.sql | 2 +",
    });
    await drain();

    const reviewRuns = await pendingRuns(workflowRunId);
    expect(reviewRuns.length).toBeGreaterThan(1);

    // Finish all but one: the workflow must still be parked.
    for (const runId of reviewRuns.slice(0, -1)) {
      await completeRun(workflowRunId, runId);
    }
    await drain();

    const { step } = await stateOf(workflowRunId);
    expect(step).toBe("awaitReview");
    expect(await pendingRuns(workflowRunId)).toHaveLength(1);
  });
});

describe("review → fix loop", () => {
  /** Drive implement, then review, leaving the workflow parked on review. */
  async function throughReview(workItemId: string, policy = DEFAULT_DELIVERY_POLICY) {
    const workflowRunId = await startDelivery(workItemId, policy);
    const [implementRun] = await pendingRuns(workflowRunId);
    await completeRun(workflowRunId, implementRun!, { diffstat: " src/thing.ts | 4 ++\n" });
    await drain();
    return workflowRunId;
  }

  test("a clean review skips the fixer entirely", async () => {
    const workItemId = await newWorkItem();
    const workflowRunId = await throughReview(workItemId);

    for (const runId of await pendingRuns(workflowRunId)) {
      await completeRun(workflowRunId, runId);
    }
    await drain();

    // Straight to simplify: no fix Run was created.
    const fixes = (await owner`
      SELECT id FROM runs WHERE work_item_id = ${workItemId} AND phase = 'fix'`) as unknown[];
    expect(fixes).toHaveLength(0);

    const simplifies = (await owner`
      SELECT id FROM runs WHERE work_item_id = ${workItemId} AND phase = 'simplify'`) as unknown[];
    expect(simplifies).toHaveLength(1);
  });

  test("a blocking finding sends it to a fixer carrying that finding", async () => {
    const workItemId = await newWorkItem();
    const workflowRunId = await throughReview(workItemId);

    const reviewRuns = await pendingRuns(workflowRunId);
    const findingId = await addFinding(workItemId, reviewRuns[0]!, "blocking");
    for (const runId of reviewRuns) await completeRun(workflowRunId, runId);
    await drain();

    const fixes = (await owner`
      SELECT id, phase, role FROM runs
      WHERE work_item_id = ${workItemId} AND phase = 'fix'`) as Array<{ role: string }>;
    expect(fixes).toHaveLength(1);
    expect(fixes[0]!.role).toBe("implementer");

    // The attempt is counted before the fixer runs, so a crash still spends
    // one and the bound remains reachable.
    const [finding] = (await owner`
      SELECT fix_attempts FROM review_findings WHERE id = ${findingId}`) as Array<{
      fix_attempts: number;
    }>;
    expect(finding!.fix_attempts).toBe(1);
  });

  test("a non-blocking finding does not stop the work item", async () => {
    // Policy decides what blocks — a `note` is recorded, not acted on.
    const workItemId = await newWorkItem();
    const workflowRunId = await throughReview(workItemId);

    const reviewRuns = await pendingRuns(workflowRunId);
    await addFinding(workItemId, reviewRuns[0]!, "note");
    for (const runId of reviewRuns) await completeRun(workflowRunId, runId);
    await drain();

    const fixes = (await owner`
      SELECT id FROM runs WHERE work_item_id = ${workItemId} AND phase = 'fix'`) as unknown[];
    expect(fixes).toHaveLength(0);
  });

  test("the loop ends at its bound rather than spending forever", async () => {
    /*
     * The property the whole policy exists for. A fixer that never resolves
     * anything must be stopped by the workflow, not by a budget alert.
     */
    const policy = { ...DEFAULT_DELIVERY_POLICY, maxReviewIterations: 2, maxAttemptsPerFinding: 99 };
    const workItemId = await newWorkItem();
    const workflowRunId = await throughReview(workItemId, policy);

    for (let cycle = 0; cycle < 10; cycle++) {
      const { status, step } = await stateOf(workflowRunId);
      if (status !== "waiting") break;

      const pending = await pendingRuns(workflowRunId);
      if (step === "awaitReview") {
        // Every review reports the same unfixed problem.
        await addFinding(workItemId, pending[0]!, "blocking", { file: `src/cycle${cycle}.ts` });
      }
      for (const runId of pending) await completeRun(workflowRunId, runId);
      await drain();
    }

    const { status, state } = await stateOf(workflowRunId);
    expect(status).toBe("completed");
    expect(state.escalation).toMatchObject({ reason: "exhausted" });
  });

  test("a finding the fixer keeps failing escalates before the loop's budget", async () => {
    const policy = { ...DEFAULT_DELIVERY_POLICY, maxReviewIterations: 9, maxAttemptsPerFinding: 1 };
    const workItemId = await newWorkItem();
    const workflowRunId = await throughReview(workItemId, policy);

    const reviewRuns = await pendingRuns(workflowRunId);
    await addFinding(workItemId, reviewRuns[0]!, "blocking", { file: "src/stubborn.ts" });
    for (const runId of reviewRuns) await completeRun(workflowRunId, runId);
    await drain();

    // First fix attempt: it changes an unrelated file, so the finding stands.
    const [fixRun] = await pendingRuns(workflowRunId);
    await completeRun(workflowRunId, fixRun!, { diffstat: " src/other.ts | 1 +\n" });
    await drain();

    for (const runId of await pendingRuns(workflowRunId)) {
      await completeRun(workflowRunId, runId);
    }
    await drain();

    const { state } = await stateOf(workflowRunId);
    expect(state.escalation).toMatchObject({ reason: "stuck" });
  });

  test("a fix that rewrites the file supersedes the finding about it", async () => {
    const workItemId = await newWorkItem();
    const workflowRunId = await throughReview(workItemId);

    const reviewRuns = await pendingRuns(workflowRunId);
    const findingId = await addFinding(workItemId, reviewRuns[0]!, "blocking", {
      file: "src/broken.ts",
    });
    for (const runId of reviewRuns) await completeRun(workflowRunId, runId);
    await drain();

    const [fixRun] = await pendingRuns(workflowRunId);
    await completeRun(workflowRunId, fixRun!, { diffstat: " src/broken.ts | 20 +++---\n" });
    await drain();

    const [finding] = (await owner`
      SELECT status FROM review_findings WHERE id = ${findingId}`) as Array<{ status: string }>;
    expect(finding!.status).toBe("superseded");
  });
});

describe("simplify", () => {
  test("runs only after blocking findings are clear, and may commit", async () => {
    const workItemId = await newWorkItem();
    const workflowRunId = await startDelivery(workItemId);
    const [implementRun] = await pendingRuns(workflowRunId);
    await completeRun(workflowRunId, implementRun!, { diffstat: " src/thing.ts | 4 ++\n" });
    await drain();
    for (const runId of await pendingRuns(workflowRunId)) await completeRun(workflowRunId, runId);
    await drain();

    const [simplify] = (await owner`
      SELECT id, role FROM runs WHERE work_item_id = ${workItemId} AND phase = 'simplify'`) as Array<{
      id: string;
      role: string;
    }>;
    expect(simplify!.role).toBe("simplifier");
  });

  test("a simplifier that changed nothing is not a failure", async () => {
    // The code was already simple enough. Carrying on with what we had beats
    // escalating a work item that is finished.
    const workItemId = await newWorkItem();
    const workflowRunId = await startDelivery(workItemId);
    const [implementRun] = await pendingRuns(workflowRunId);
    await completeRun(workflowRunId, implementRun!, { diffstat: " src/thing.ts | 4 ++\n" });
    await drain();
    for (const runId of await pendingRuns(workflowRunId)) await completeRun(workflowRunId, runId);
    await drain();

    const [simplifyRun] = await pendingRuns(workflowRunId);
    await completeRun(workflowRunId, simplifyRun!, { diffstat: "" });
    await drain();

    const { state } = await stateOf(workflowRunId);
    expect(state.escalation).toBeUndefined();
  });
});

describe("diffstat parsing", () => {
  test("reads the paths that select conditional reviewers", () => {
    const paths = pathsFromDiffstat(
      [
        " src/auth/session.ts        | 12 ++++++++----",
        " migrations/011_pr.sql      |  4 ++++",
        " 2 files changed, 16 insertions(+)",
      ].join("\n"),
    );
    expect(paths).toEqual(["src/auth/session.ts", "migrations/011_pr.sql"]);
  });

  test("a rename reports the new path, which is what a reviewer reads", () => {
    const paths = pathsFromDiffstat(" src/{old => new}/thing.ts | 2 +-");
    expect(paths[0]).toContain("new");
  });

  test("empty output is no paths, not one empty path", () => {
    expect(pathsFromDiffstat("")).toEqual([]);
    expect(pathsFromDiffstat(" 0 files changed")).toEqual([]);
  });
});
