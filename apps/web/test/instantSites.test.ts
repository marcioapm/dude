/**
 * Every place the web app orders times from two sources compares instants:
 * a Run's createdAt may come in the orchestrator's offset (+01:00) and a
 * ledger event's in UTC (Z). Each pair below orders one way as text and
 * the other way in time.
 */

import { describe, expect, test } from "bun:test";
import type { PersistedEvent, Run } from "@dude/domain";
import { eventAttempts } from "../src/attempts.ts";
import { byInstant } from "../src/instant.ts";
import { prFeedbackReason, whyItRan } from "../src/screens/TaskScreen.tsx";

// 10:00:02Z, stamped in a +01:00 offset: as text it sorts after 10:00:05Z, in time before it.
const RUN_AT = "2026-10-03T11:00:02+01:00";
const AFTER_RUN = "2026-10-03T10:00:05Z";

const event = (eventType: string, occurredAt: string, payload: Record<string, unknown> = {}): PersistedEvent => ({
  eventId: `e_${eventType}_${occurredAt}`, eventType, occurredAt, organizationId: "o", projectId: null, taskId: "t", runId: null,
  sessionId: null, workflowRunId: null, actor: { type: "system", id: "workflow" }, source: "control-plane",
  correlationId: null, causationId: null, payload: { number: 41, repo: "web", ...payload }, cursor: 1,
}) as PersistedEvent;

const run = (id: string, attempt: number, phase: string, createdAt: string): Run =>
  ({ id, attempt, phase, role: null, kind: "agent", createdAt, status: "completed" }) as unknown as Run;

describe("two sources, ordered by instant", () => {
  test("eventAttempts: an event on no Run, after a +01:00 attempt start in time, is that attempt's", () => {
    const of = eventAttempts([run("r1", 1, "implement", "2026-10-03T09:00:00Z"), run("r2", 2, "implement", RUN_AT)], [], 2);
    expect(of(event("task.updated", AFTER_RUN))).toBe(2);
    expect(of(event("task.updated", "2026-10-03T10:00:00Z"))).toBe(1);
  });

  test("prFeedbackReason: feedback after the fix began, in another zone, is not what it answers", () => {
    const events = [event("pull_request.reviewed", "2026-10-03T09:59:00Z", { author: "ana" }),
      event("pull_request.commented", AFTER_RUN, { author: "cy" })];
    expect(prFeedbackReason(events, RUN_AT)).toBe("for ana's review");
  });

  test("whyItRan: a pull request opened after the fix began, in another zone, did not wake it", () => {
    const fix = run("r_fix", 1, "fix", RUN_AT);
    expect(whyItRan(fix, 0, [fix], [], [event("pull_request.opened", AFTER_RUN)])).toBe("for the review");
    expect(whyItRan(fix, 0, [fix], [], [event("pull_request.opened", "2026-10-03T09:59:00Z")])).toBe("for the pull request's feedback");
  });

  test("byInstant: a cross-zone pair by instant, and an unparseable time last", () => {
    const lines = [
      { id: "garbage", at: "not a time" },
      { id: "z-later", at: AFTER_RUN },
      { id: "offset-earlier", at: RUN_AT },
      { id: "first", at: "2026-10-03T09:00:00Z" },
    ];
    expect([...lines].sort(byInstant((l) => l.at)).map((l) => l.id)).toEqual(["first", "offset-earlier", "z-later", "garbage"]);
    expect([...lines].reverse().sort(byInstant((l) => l.at)).map((l) => l.id)).toEqual(["first", "offset-earlier", "z-later", "garbage"]);
  });
});
