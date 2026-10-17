/**
 * Lifecycle in the web app (docs/design/tracking-work.md, Epic 1): the
 * Activity lines Close, Reopen, Archive and Unarchive write, and the one
 * line Close and archive's batch makes.
 */

import { describe, expect, test } from "bun:test";
import { renderToStaticMarkup } from "react-dom/server";
import { EventTypes, type PersistedEvent } from "@dude/domain";
import { activityLines } from "../src/screens/TaskScreen.tsx";

const ANA = "per_ana";
let cursor = 0;
function ev(eventType: string, payload: Record<string, unknown>, extra: Partial<PersistedEvent> = {}): PersistedEvent {
  cursor++;
  return { eventId: `evt_l${cursor}`, cursor, eventType, occurredAt: new Date(Date.UTC(2026, 9, 17, 10, 0, cursor)).toISOString(),
    organizationId: "org", projectId: "prj", taskId: "wi_63", runId: null, sessionId: null, workflowRunId: null,
    actor: { type: "human", id: ANA }, source: "orchestrator", correlationId: null, causationId: null, payload, ...extra };
}
const people = { names: new Map([[ANA, "Ana Ribeiro"]]), byId: new Map(), you: ANA } as unknown as Parameters<typeof activityLines>[1];
const said = (lines: ReturnType<typeof activityLines>) =>
  lines.map((l) => renderToStaticMarkup(<>{l.text}</>).replace(/<[^>]+>/g, "").replace(/&#x27;/g, "'")).reverse();

describe("lifecycle in Activity", () => {
  test("each event has its line, both ways, with the handover said", () => {
    const lines = activityLines([
      ev(EventTypes.TaskClosed, { resolution: "duplicate", duplicateOf: "CP-62", handedOver: ["CP-47"] }),
      ev(EventTypes.TaskReopened, { was: "duplicate" }),
      ev(EventTypes.TaskClosed, { resolution: "wont_do", removedRelations: ["CP-47"], note: "Superseded by the new export." }),
      ev(EventTypes.TaskArchived, {}),
      ev(EventTypes.TaskUnarchived, {}),
      ev(EventTypes.TaskReopened, { was: "cant_reproduce" }),
    ], people, [], [], 1, "CP-63");
    expect(said(lines)).toEqual([
      "Ana Ribeiro closed CP-63 as a duplicate of CP-62 — CP-47, which waited on CP-63, now waits on CP-62",
      "Ana Ribeiro reopened CP-63 (was Duplicate)",
      "Ana Ribeiro closed CP-63 as Won't do · CP-47 no longer waits on it",
      "Ana Ribeiro archived CP-63",
      "Ana Ribeiro unarchived CP-63",
      "Ana Ribeiro reopened CP-63 (was Can't reproduce)",
    ]);
    expect(lines.find((l) => l.quote === "Superseded by the new export.")).toBeDefined();
  });

  test("Close and archive is one line, the note under it", () => {
    const closed = ev(EventTypes.TaskClosed, { resolution: "wont_do", andArchived: true, note: "Not this quarter." });
    const lines = activityLines([closed, ev(EventTypes.TaskArchived, { viaClose: true }, { causationId: closed.eventId })], people, [], [], 1, "CP-44");
    expect(said(lines)).toEqual(["Ana Ribeiro closed CP-44 as Won't do and archived it"]);
    expect(lines[0]!.quote).toBe("Not this quarter.");
  });

  test("an archive with its epic says so; a closed start when unblocked says why it ended", () => {
    const lines = activityLines([
      ev(EventTypes.TaskArchived, { viaEpic: "epc_1" }),
      ev(EventTypes.TaskAutoStartCancelled, { how: "deliver", why: "closed" }),
    ], people, [], [], 1, "CP-46");
    expect(said(lines)).toEqual(["Ana Ribeiro archived CP-46 with its epic", "Ana Ribeiro closed it, which ends start when unblocked"]);
  });
});
