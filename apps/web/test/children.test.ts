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

  test("that call's own event, arriving later, fills in the same turn, the child still under it", () => {
    const state = apply(emptyProjection(), [started()]);
    const made = only(state.turns) as ToolTurn;
    const called = parentCall();
    apply(state, [called, ev(EventTypes.ToolCompleted, { tool: "task", callId: "c1", status: "completed" })]);
    const call = only(snapshot(state).turns) as ToolTurn;
    expect(call).toBe(made);
    expect(call).toMatchObject({ id: "c1", tool: "task", args: { description: "Inspect test capture setup", subagent_type: "explore" },
      startedAt: called.occurredAt, status: "completed" });
    expect(only(call.children).id).toBe("ses_kid");
  });

  test("a grandchild's call heard after the grandchild fills in the same turn", () => {
    const state = apply(emptyProjection(), [parentCall(), started(),
      ev(EventTypes.ChildStarted, { childId: "ses_grand", callId: "k2", parentChildId: "ses_kid", kind: "task", title: "Deeper" })]);
    const made = only(childOf(state.turns).turns) as ToolTurn;
    apply(state, [ev(EventTypes.ChildToolCalled, { ...kid, parentCallId: "c1", callId: "k2", tool: "task", input: { description: "Deeper" } })]);
    const call = only(childOf(snapshot(state).turns).turns) as ToolTurn;
    expect(call).toBe(made);
    expect(call).toMatchObject({ tool: "task", args: { description: "Deeper" } });
    expect(only(call.children).id).toBe("ses_grand");
  });

  // A harness that reports a call only once finished never sends its call event.
  test("that call heard only as its completion fills in the same turn", () => {
    const state = apply(emptyProjection(), [started()]);
    const made = only(state.turns) as ToolTurn;
    apply(state, [ev(EventTypes.ToolCompleted, { tool: "task", callId: "c1", status: "completed", input: { description: "Actual" } })]);
    const call = only(snapshot(state).turns) as ToolTurn;
    expect(call).toBe(made);
    expect(call).toMatchObject({ id: "c1", tool: "task", args: { description: "Actual" }, status: "completed" });
    expect(only(call.children).id).toBe("ses_kid");
    expect(state.placeholderCalls.size).toBe(0);
  });

  test("a grandchild's call heard only as its completion fills in the same turn", () => {
    const state = apply(emptyProjection(), [parentCall(), started(),
      ev(EventTypes.ChildStarted, { childId: "ses_grand", callId: "k2", parentChildId: "ses_kid", kind: "task", title: "Deeper" })]);
    const made = only(childOf(state.turns).turns) as ToolTurn;
    apply(state, [ev(EventTypes.ChildToolCompleted, { ...kid, parentCallId: "c1", callId: "k2", tool: "task", status: "completed",
      input: { description: "Actual" } })]);
    const call = only(childOf(snapshot(state).turns).turns) as ToolTurn;
    expect(call).toBe(made);
    expect(call).toMatchObject({ tool: "task", args: { description: "Actual" }, status: "completed" });
    expect(only(call.children).id).toBe("ses_grand");
    expect(state.placeholderCalls.size).toBe(0);
  });

  test("a completion with no input keeps the args its call gave", () => {
    const state = apply(emptyProjection(), [started(), parentCall(),
      ev(EventTypes.ToolCompleted, { tool: "task", callId: "c1", status: "completed" })]);
    expect(only(state.turns)).toMatchObject({ args: { description: "Inspect test capture setup", subagent_type: "explore" }, status: "completed" });
    expect(state.placeholderCalls.size).toBe(0);
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
  const held = () => ev(EventTypes.ChildWaiting, { ...kid, on: "permission", permission: "external_directory", patterns: ["/opt/*"], answered: "none" });

  test("sets what it waits on", () => {
    const before = [parentCall(), started()];
    const waiting = held();
    const { turns } = project([...before, waiting]);
    expect(childOf(turns).waitingOn).toEqual({ permission: "external_directory", patterns: ["/opt/*"], since: waiting.occurredAt });
  });

  // The Run is still running: only the child's own event may clear the wait.
  const next: Array<[string, () => PersistedEvent]> = [
    ["a call", () => ev(EventTypes.ChildToolCalled, { ...kid, parentCallId: "c1", callId: "k9", tool: "read" })],
    ["a call's completion", () => ev(EventTypes.ChildToolCompleted, { ...kid, parentCallId: "c1", callId: "k9", tool: "read", status: "completed" })],
    ["a message", () => ev(EventTypes.ChildMessage, { ...kid, text: "Resumed." })],
    ["its end", () => ev(EventTypes.ChildEnded, { ...kid, status: "completed" })],
  ];
  for (const [what, event] of next) {
    test(`its next event, ${what}, clears it`, () => {
      const state = apply(emptyProjection(), [parentCall(), started(), held()]);
      expect(childOf(snapshot(state).turns).waitingOn).not.toBeNull();
      apply(state, [event()]);
      expect(childOf(snapshot(state).turns).waitingOn).toBeNull();
    });
  }

  test("another child's event leaves it waiting", () => {
    const state = apply(emptyProjection(), [parentCall(), started(), held(),
      ev(EventTypes.ToolCalled, { tool: "task", callId: "c2" }),
      ev(EventTypes.ChildStarted, { childId: "ses_other", callId: "c2", kind: "task", title: "Other" }),
      ev(EventTypes.ChildMessage, { childId: "ses_other", callId: "c2", text: "Other speaks." })]);
    expect(state.childrenById.get("ses_kid")!.waitingOn).toMatchObject({ permission: "external_directory" });
  });

  test("one for a call already over holds nothing", () => {
    const { turns } = project([parentCall(), started(),
      ev(EventTypes.ChildWaiting, { ...kid, on: "permission", permission: "bash", patterns: ["cat /etc/hostname"], answered: "none", held: false })]);
    expect(childOf(turns).waitingOn).toBeNull();
  });

  test("one for a call already over leaves an earlier wait as it was", () => {
    const before = [parentCall(), started()];
    const first = held();
    const { turns } = project([...before, first,
      ev(EventTypes.ChildWaiting, { ...kid, on: "permission", permission: "bash", patterns: ["cat /etc/hostname"], answered: "none", held: false })]);
    expect(childOf(turns).waitingOn).toEqual({ permission: "external_directory", patterns: ["/opt/*"], since: first.occurredAt });
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

// One call id reused for another tool (the fixture's "c1": bash, then task) is two calls, each with its own row.
describe("a call id reused for another tool", () => {
  const bashCall = () => ev(EventTypes.ToolCalled, { tool: "bash", callId: "c1", input: { command: "npm test" } });
  const bashDone = () => ev(EventTypes.ToolCompleted, { tool: "bash", callId: "c1", status: "completed", exitCode: 0, output: { head: "9 passed" } });
  const taskDone = () => ev(EventTypes.ToolCompleted, { tool: "task", callId: "c1", status: "completed", output: { head: "No viewport set." } });
  const childEnded = () => ev(EventTypes.ChildEnded, { ...kid, status: "completed", summary: "Done." });

  function rows(turns: readonly unknown[]): [ToolTurn, ToolTurn] {
    const tools = (turns as Array<{ kind: string }>).filter((t): t is ToolTurn => t.kind === "tool");
    expect(tools.map((t) => [t.id, t.tool])).toEqual([["c1", "bash"], ["c1", "task"]]);
    return [tools[0]!, tools[1]!];
  }

  // Events are made in fold order: one made earlier has a lower cursor, and a lower cursor is skipped as already folded.
  test("bash then task: two rows, each with its own input, end, result and children", () => {
    const events = [bashCall(), bashDone(), parentCall(), started(), childEnded(), taskDone()];
    const [, bashEnd, , , , taskEnd] = events;
    const { turns, toolCounts } = project(events);
    const [bash, task] = rows(turns);
    expect(bash).toMatchObject({ args: { command: "npm test" }, status: "completed", endedAt: bashEnd.occurredAt,
      result: { exitCode: 0, output: { head: "9 passed" } }, children: [] });
    expect(task).toMatchObject({ args: { description: "Inspect test capture setup", subagent_type: "explore" }, status: "completed",
      endedAt: taskEnd.occurredAt, result: { output: { head: "No viewport set." } } });
    expect(only(task.children)).toMatchObject({ id: "ses_kid", status: "completed", summary: "Done." });
    expect([toolCounts.get("bash"), toolCounts.get("task")]).toEqual([1, 1]);
  });

  test("the first call's completion repeated while the second runs leaves the second running, and its own completion ends it", () => {
    const events = [bashCall(), bashDone(), parentCall(), bashDone()];
    const [, bashEnd, task] = events;
    const state = apply(emptyProjection(), events);
    const [, running] = rows(snapshot(state).turns);
    expect(running).toMatchObject({ status: "running", endedAt: null, result: null });
    expect(snapshot(state).activeTool).toEqual({ name: "task", since: task.occurredAt });

    const taskEnd = taskDone();
    apply(state, [taskEnd]);
    const [bash, ended] = rows(snapshot(state).turns);
    expect(bash).toMatchObject({ status: "completed", endedAt: bashEnd.occurredAt, result: { exitCode: 0, output: { head: "9 passed" } } });
    expect(ended).toMatchObject({ status: "completed", endedAt: taskEnd.occurredAt, result: { output: { head: "No viewport set." } } });
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

test("a Run that ended settles a grandchild still waiting under a child that already ended", () => {
  const events = [parentCall(), started(),
    ev(EventTypes.ChildToolCalled, { ...kid, parentCallId: "c1", callId: "inner", tool: "task" }),
    ev(EventTypes.ChildStarted, { childId: "grand", callId: "inner", parentChildId: "ses_kid", kind: "task", title: "Deeper" }),
    ev(EventTypes.ChildWaiting, { childId: "grand", callId: "inner", parentChildId: "ses_kid", on: "permission", permission: "bash",
      patterns: [], answered: "none", held: true }),
    ev(EventTypes.ChildEnded, { ...kid, status: "completed" }),
    ev(EventTypes.ToolCompleted, { tool: "task", callId: "c1", status: "completed" })];
  const state = apply(emptyProjection(), events);
  const grandOf = (turns: readonly unknown[]) => {
    const child = childOf(turns);
    return { child, grand: only((only(child.turns) as ToolTurn).children) };
  };
  const live = snapshot(state);
  const { child, grand } = grandOf(project(events, "aborted").turns);
  expect(child.status).toBe("completed");
  expect(grand).toMatchObject({ id: "grand", status: "aborted", waitingOn: null });
  // The live projection is untouched.
  expect(grandOf(live.turns).grand).toMatchObject({ status: "running", waitingOn: { permission: "bash" } });
  const settledAgain = snapshot(state, "aborted");
  expect(settledAgain.turns[0]).not.toBe(live.turns[0]);
  const done = apply(emptyProjection(), [parentCall(), started(), ev(EventTypes.ChildEnded, { ...kid, status: "completed" }),
    ev(EventTypes.ToolCompleted, { tool: "task", callId: "c1", status: "completed" })]);
  expect(only(snapshot(done, "aborted").turns)).toMatchObject({ status: "completed" });
  expect(childOf(snapshot(done, "aborted").turns).status).toBe("completed");
});

test("a settled transcript keeps what it showed while the live one goes on", () => {
  const state = apply(emptyProjection(), [parentCall(), started(),
    ev(EventTypes.ChildMessage, { ...kid, text: "before" }),
    ev(EventTypes.ChildToolCalled, { ...kid, parentCallId: "c1", callId: "inner", tool: "task" }),
    ev(EventTypes.ChildStarted, { childId: "grand", callId: "inner", parentChildId: "ses_kid", kind: "task", title: "Deeper" }),
    ev(EventTypes.ChildMessage, { childId: "grand", text: "grand before" }),
    ev(EventTypes.ChildEnded, { ...kid, status: "interrupted" })]);
  const settled = snapshot(state, "aborted");
  const shown = JSON.stringify(settled.turns);
  const said = (child: ChildTurn) => child.turns.map((t) => (t.kind === "message" ? t.text : `${t.tool}:${t.status}`));

  apply(state, [
    ev(EventTypes.ChildMessage, { ...kid, text: "after" }),
    ev(EventTypes.ChildToolCalled, { ...kid, parentCallId: "c1", callId: "late", tool: "grep" }),
    ev(EventTypes.ChildToolCompleted, { ...kid, parentCallId: "c1", callId: "inner", tool: "task", status: "completed" }),
    ev(EventTypes.ChildMessage, { childId: "grand", text: "grand after" }),
    ev(EventTypes.ChildToolCalled, { childId: "grand", parentCallId: "inner", callId: "g1", tool: "read" }),
    ev(EventTypes.ChildStarted, { childId: "late-grand", callId: "inner", parentChildId: "ses_kid", kind: "task", title: "Late" }),
  ]);

  expect(JSON.stringify(settled.turns)).toBe(shown);
  const child = childOf(settled.turns);
  expect(said(child)).toEqual(["before", "task:aborted"]);
  const inner = child.turns[1] as ToolTurn;
  expect(inner.children.map((c) => c.id)).toEqual(["grand"]);
  expect(said(only(inner.children))).toEqual(["grand before"]);

  const liveChild = childOf(snapshot(state).turns);
  expect(said(liveChild)).toEqual(["before", "task:completed", "after", "grep:running"]);
  const liveInner = liveChild.turns[1] as ToolTurn;
  expect(liveInner.children.map((c) => c.id)).toEqual(["grand", "late-grand"]);
  expect(said(liveInner.children[0]!)).toEqual(["grand before", "grand after", "read:running"]);
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
