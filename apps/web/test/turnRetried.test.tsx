/**
 * A turn dude retries after a temporary model error (`run.turn.retried`)
 * reads as one system line, in the Run's conversation and in the task's
 * Activity: what failed, how long until the retry, which attempt of how
 * many.
 */

import { describe, expect, test } from "bun:test";
import { renderToStaticMarkup } from "react-dom/server";
import { EventTypes, type PersistedEvent, type Run } from "@dude/domain";
import { TASK_ID } from "../src/fixtures/data.ts";
import { project, turnRetriedNotice } from "../src/api/conversation.ts";
import { activityLines } from "../src/screens/TaskScreen.tsx";

const at = (s: number) => new Date(Date.UTC(2026, 10, 4, 10, 0, s)).toISOString();

let cursor = 0;
function ev(eventType: string, payload: Record<string, unknown>, runId = "run_tr"): PersistedEvent {
  cursor++;
  return { eventId: `evt_tr${cursor}`, cursor: 40_000 + cursor, eventType, occurredAt: at(cursor), organizationId: "org",
    projectId: "prj", taskId: TASK_ID, runId, sessionId: null, workflowRunId: null,
    actor: { type: "system", id: "dude" }, source: "orchestrator",
    correlationId: null, causationId: null, payload } as unknown as PersistedEvent;
}

const limited = { attempt: 2, maxAttempts: 4, after: 143, retryAfter: true,
  reason: `the agent's turn failed: API Error: Request rejected (429) · {"retry_after":143}` };

describe("a retried turn", () => {
  test("says the status, the wait and the attempt", () => {
    expect(turnRetriedNotice(limited)).toBe("Model busy (429). Retrying in 2m 23s (attempt 2 of 4).");
    expect(turnRetriedNotice({ attempt: 1, maxAttempts: 4, after: 15, reason: "API Error: Connection closed mid-response" }))
      .toBe("Temporary model error. Retrying in 15s (attempt 1 of 4).");
  });

  test("is a notice in the Run's conversation", () => {
    const { turns } = project([ev(EventTypes.AgentMessage, { text: "Working." }), ev(EventTypes.RunTurnRetried, limited)]);
    const notices = turns.filter((t) => t.kind === "notice");
    expect(notices.map((t) => t.text)).toEqual(["Model busy (429). Retrying in 2m 23s (attempt 2 of 4)."]);
  });

  test("is a line in the task's Activity", () => {
    const impl = { id: "run_tr", organizationId: "org", projectId: "prj", taskId: TASK_ID, attempt: 1, status: "running",
      kind: "agent", phase: "implement", role: "implementer", createdAt: at(0) } as unknown as Run;
    const people = { names: new Map(), byId: new Map(), you: null } as unknown as Parameters<typeof activityLines>[1];
    const lines = activityLines([ev(EventTypes.RunTurnRetried, limited)], people, [impl], [], 1);
    const div = document.createElement("div");
    expect(lines.length).toBe(1);
    div.innerHTML = renderToStaticMarkup(<>{lines[0]!.text}</>);
    expect(div.textContent).toBe("The implementer's turn failed: Model busy (429). Retrying in 2m 23s (attempt 2 of 4).");
  });
});
