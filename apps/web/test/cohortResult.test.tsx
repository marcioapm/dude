import { expect, test } from "bun:test";
import { renderToStaticMarkup } from "react-dom/server";
import type { PersistedEvent } from "@dude/domain";
import { apply, emptyProjection, project } from "../src/api/conversation.ts";
import { renderTurn } from "../src/screens/RunScreen.tsx";
import type { People } from "../src/people.tsx";

const noPeople: People = { you: null, me: null, all: [], byId: new Map(), names: new Map(), refresh: async () => noPeople, seen: () => false };

let cursor = 0;
function event(eventType: string, payload: Record<string, unknown>, runId = "run_parent"): PersistedEvent {
  return { eventId: `evt_${++cursor}`, cursor, eventType, payload, runId, taskId: "task_cohort",
    organizationId: "org_test", projectId: "prj_test", sessionId: null, workflowRunId: null,
    actor: { type: "system", id: "dude" }, source: "orchestrator", correlationId: null, causationId: null,
    occurredAt: "2026-10-29T10:00:00Z" };
}
const metadata = { cohortId: "cohort_1", parentRunId: "run_parent", directiveId: "dir_cohort", completed: 1, total: 3,
  members: [{ runId: "run_one", status: "completed" }, { runId: "run_two", status: "failed" }, { runId: "run_three", status: "aborted" }] };

test("one queued cohort row becomes delivered in place, including reconnect and duplicate events", () => {
  const queued = event("run.cohort.queued", metadata);
  const state = apply(emptyProjection(), [queued]);
  const row = state.turns[0]!;
  expect(row.kind).toBe("cohortResult");
  let html = renderToStaticMarkup(renderTurn(row, "conductor", 0, false, noPeople, "dude"));
  expect(html).toContain("Completed 1/3");
  expect(html).toContain("Queued");
  for (const m of metadata.members) {
    expect(html).toContain(m.runId);
    expect(html).toContain(`href="#/session/${m.runId}"`);
    expect(html).toContain(m.status);
  }
  const receipt = event("run.directive.delivered", { directiveId: "dir_cohort", read: true });
  apply(state, [queued, receipt, receipt, event("run.cohort.queued", metadata)]);
  expect(state.turns).toHaveLength(1);
  expect(state.turns[0]).toBe(row);
  html = renderToStaticMarkup(renderTurn(row, "conductor", 0, false, noPeople, "dude"));
  expect(html).toContain("Delivered");
  expect(project([queued, receipt], "paused").turns).toEqual(state.turns);
});

test("receipt before queued event survives unrelated checks and only matches its parent", () => {
  const receipt = event("run.directive.delivered", { directiveId: "dir_cohort" });
  const checks = event("run.checks.judged", { verified: true, turn: 1 });
  const queued = event("run.cohort.queued", metadata);
  const rows = project([receipt, checks, queued], "paused").turns;
  expect(rows).toHaveLength(1);
  expect(rows[0]).toMatchObject({ kind: "cohortResult", deliveredAt: receipt.occurredAt });
  expect(project([event("run.cohort.queued", metadata, "run_other")], "running").turns).toEqual([]);
});
