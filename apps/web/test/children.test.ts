/**
 * A tool call's child sessions in the conversation projection: each
 * agent.child.* event lands on the child nested under the parent's call,
 * never among the Run's own turns.
 */

import { describe, expect, test } from "bun:test";
import { EventTypes } from "@dude/domain";
import type { PersistedEvent } from "@dude/domain";
import { apply, emptyProjection, project, snapshot, type ChildTurn, type ToolTurn } from "../src/api/conversation.ts";

let cursor = 0;

function ev(eventType: string, payload: Record<string, unknown> = {}): PersistedEvent {
  cursor += 1;
  return {
    eventId: `evt_${cursor}`, cursor, eventType, organizationId: "org_test", projectId: null, taskId: null, runId: "run_test",
    sessionId: null, workflowRunId: null, actor: { type: "agent", id: "run_test" }, source: "runner", correlationId: null,
    causationId: null, payload, occurredAt: new Date(Date.UTC(2026, 9, 13, 1, 44, cursor)).toISOString(),
  } as unknown as PersistedEvent;
}

const kid = { childId: "ses_kid", callId: "c1" };
const started = () => ev(EventTypes.ChildStarted, { ...kid, kind: "task", title: "Inspect test capture setup", agentType: "explore", model: "claude-haiku-4.5" });
const parentCall = () => ev(EventTypes.ToolCalled, { tool: "task", callId: "c1", input: { description: "Inspect test capture setup", subagent_type: "explore" } });

function only<T>(xs: readonly T[]): T {
  expect(xs).toHaveLength(1);
  return xs[0]!;
}

function childOf(turns: readonly unknown[]): ChildTurn {
  const call = only(turns) as ToolTurn;
  return only(call.children);
}

describe("agent.child.started", () => {
  test("hangs a child, with its title, type and model, under the parent's call", () => {
    const { turns } = project([parentCall(), started()]);
    const call = only(turns) as ToolTurn;
    expect(call).toMatchObject({ kind: "tool", tool: "task", id: "c1" });
    expect(only(call.children)).toMatchObject({
      kind: "child", id: "ses_kid", callId: "c1", childKind: "task", title: "Inspect test capture setup", agentType: "explore",
      model: "claude-haiku-4.5", status: "running", endedAt: null, turns: [], waitingOn: null,
    });
  });

  test("a child whose call is unknown hangs under a call made for it", () => {
    const { turns } = project([started()]);
    const call = only(turns) as ToolTurn;
    expect(call).toMatchObject({ kind: "tool", id: "c1", tool: "task", status: "running" });
    expect(only(call.children).id).toBe("ses_kid");
  });

  test("several children of one turn each hang under their own call", () => {
    const { turns } = project([
      ev(EventTypes.ToolCalled, { tool: "task", callId: "s1" }), ev(EventTypes.ToolCalled, { tool: "task", callId: "s2" }),
      ev(EventTypes.ChildStarted, { childId: "a", callId: "s1", kind: "spawn", title: "A" }),
      ev(EventTypes.ChildStarted, { childId: "b", callId: "s2", kind: "spawn", title: "B" }),
      ev(EventTypes.ChildToolCalled, { childId: "b", parentCallId: "s2", callId: "k1", tool: "bash", input: { command: "ls" } }),
    ]);
    const [s1, s2] = turns as ToolTurn[];
    expect(s1!.children.map((c) => c.id)).toEqual(["a"]);
    expect(s2!.children.map((c) => [c.id, c.turns.length])).toEqual([["b", 1]]);
  });
});

describe("a child's own turns", () => {
  test("its tool call and completion are one turn inside it, not the Run's", () => {
    const { turns, toolCounts } = project([parentCall(), started(),
      ev(EventTypes.ChildToolCalled, { ...kid, parentCallId: "c1", callId: "k1", tool: "grep", input: { pattern: "viewport" } }),
      ev(EventTypes.ChildToolCompleted, { ...kid, parentCallId: "c1", callId: "k1", tool: "grep", status: "completed", output: { head: "tests/conftest.py:12" } }),
    ]);
    const child = childOf(turns);
    const call = only(child.turns) as ToolTurn;
    expect(call).toMatchObject({ kind: "tool", id: "k1", tool: "grep", args: { pattern: "viewport" }, status: "completed", result: { output: { head: "tests/conftest.py:12" } } });
    expect(toolCounts.get("grep")).toBeUndefined();
  });

  test("an error completion is failed", () => {
    const { turns } = project([parentCall(), started(),
      ev(EventTypes.ChildToolCompleted, { ...kid, parentCallId: "c1", callId: "k1", tool: "bash", status: "error" })]);
    expect(only(childOf(turns).turns)).toMatchObject({ status: "failed" });
  });

  test("its message is a message turn inside it; an empty one is none", () => {
    const { turns } = project([parentCall(), started(),
      ev(EventTypes.ChildMessage, { ...kid, text: "No viewport is set in the repo." }), ev(EventTypes.ChildMessage, { ...kid, text: "  " })]);
    expect(only(childOf(turns).turns)).toMatchObject({ kind: "message", text: "No viewport is set in the repo." });
  });

  test("a child of a child hangs under its parent child's call", () => {
    const { turns } = project([parentCall(), started(),
      ev(EventTypes.ChildToolCalled, { ...kid, parentCallId: "c1", callId: "k2", tool: "task" }),
      ev(EventTypes.ChildStarted, { childId: "ses_grand", callId: "k2", parentChildId: "ses_kid", kind: "task", title: "Deeper" }),
    ]);
    const inner = only(childOf(turns).turns) as ToolTurn;
    expect(only(inner.children)).toMatchObject({ id: "ses_grand", title: "Deeper" });
  });
});

describe("agent.child.waiting", () => {
  test("sets what it waits on; its next event clears it", () => {
    const before = [parentCall(), started()];
    const waiting = ev(EventTypes.ChildWaiting, { ...kid, on: "permission", permission: "external_directory", patterns: ["/opt/*"], answered: "none" });
    const state = apply(emptyProjection(), [...before, waiting]);
    expect(childOf(snapshot(state).turns).waitingOn).toEqual({ permission: "external_directory", patterns: ["/opt/*"], since: waiting.occurredAt });
    apply(state, [ev(EventTypes.ChildToolCalled, { ...kid, parentCallId: "c1", callId: "k9", tool: "read" })]);
    expect(childOf(snapshot(state).turns).waitingOn).toBeNull();
  });

  test("one its harness answered holds nothing", () => {
    const { turns } = project([parentCall(), started(),
      ev(EventTypes.ChildWaiting, { ...kid, on: "permission", permission: "read", patterns: [], answered: "allow" })]);
    expect(childOf(turns).waitingOn).toBeNull();
  });
});

describe("agent.child.ended", () => {
  test("sets its status, end, cost and summary, and ends its open calls with it", () => {
    const before = [parentCall(), started(), ev(EventTypes.ChildToolCalled, { ...kid, parentCallId: "c1", callId: "k1", tool: "read" })];
    const ended = ev(EventTypes.ChildEnded, { ...kid, status: "completed", summary: "Playwright's 1280×720 default applies.", costUsd: 0.37, durationSecs: 154 });
    const { turns } = project([...before, ended]);
    const child = childOf(turns);
    expect(child).toMatchObject({ status: "completed", endedAt: ended.occurredAt, costUsd: 0.37, summary: "Playwright's 1280×720 default applies." });
    expect(only(child.turns)).toMatchObject({ status: "completed", endedAt: ended.occurredAt });
  });

  test("failed is failed and interrupted is aborted", () => {
    for (const [status, want] of [["failed", "failed"], ["interrupted", "aborted"]] as const) {
      const { turns } = project([parentCall(), started(), ev(EventTypes.ChildEnded, { ...kid, status })]);
      expect(childOf(turns).status, status).toBe(want);
    }
  });

  test("its cost is its own: the Run's cost is never the sum with it", () => {
    const conversation = project([parentCall(), started(), ev(EventTypes.ChildEnded, { ...kid, status: "completed", costUsd: 0.37 })]);
    expect(conversation.costUsd).toBe(0);
  });
});

test("a Run that ended settles a child still running and its open calls, and what it waited on", () => {
  const state = apply(emptyProjection(), [parentCall(), started(),
    ev(EventTypes.ChildToolCalled, { ...kid, parentCallId: "c1", callId: "k1", tool: "grep" }),
    ev(EventTypes.ChildWaiting, { ...kid, on: "permission", permission: "external_directory", patterns: [], answered: "none" })]);
  const child = childOf(snapshot(state, "aborted").turns);
  expect(child).toMatchObject({ status: "aborted", waitingOn: null });
  expect(only(child.turns)).toMatchObject({ status: "aborted" });
  // The live projection is untouched.
  expect(childOf(snapshot(state).turns).status).toBe("running");
});

test("folding one event at a time is folding them all at once", () => {
  const events = [parentCall(), started(),
    ev(EventTypes.ChildToolCalled, { ...kid, parentCallId: "c1", callId: "k1", tool: "read" }),
    ev(EventTypes.ChildMessage, { ...kid, text: "Read it." }),
    ev(EventTypes.ChildEnded, { ...kid, status: "completed", summary: "Done." }),
    ev(EventTypes.ToolCompleted, { tool: "task", callId: "c1", status: "completed" })];
  const state = emptyProjection();
  for (const e of events) apply(state, [e]);
  expect(JSON.stringify(snapshot(state).turns)).toBe(JSON.stringify(project(events).turns));
});
