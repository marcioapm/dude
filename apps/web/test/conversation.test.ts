/**
 * The event-to-conversation projection.
 *
 * This is the only place that turns the ledger's record of *what happened*
 * into a narrative of *what the agent is doing*, so the properties worth
 * pinning down are: a tool call and its completion become one turn, the plan
 * never becomes a wall of near-identical tool cards, and folding events one
 * at a time gives exactly the same answer as folding them all at once —
 * which is what lets the UI stream without rebuilding its history.
 */

import { describe, expect, test } from "bun:test";
import { EventTypes } from "@dude/domain";
import type { PersistedEvent } from "@dude/domain";
import { apply, emptyProjection, project, snapshot } from "../src/api/conversation.ts";

let cursor = 0;

/** A ledger event with only the fields the projection reads. */
function ev(eventType: string, payload: Record<string, unknown> = {}): PersistedEvent {
  cursor += 1;
  return {
    eventId: `evt_${cursor}`,
    cursor,
    eventType,
    organizationId: "org_test",
    projectId: null,
    workItemId: null,
    runId: "run_test",
    sessionId: "ses_test",
    workflowRunId: null,
    actor: { type: "agent", id: "orchestrator" },
    source: "runner",
    correlationId: null,
    causationId: null,
    payload,
    occurredAt: new Date(Date.UTC(2026, 0, 1, 0, 0, cursor)).toISOString(),
  } as unknown as PersistedEvent;
}

describe("turns", () => {
  test("an agent message becomes a message turn", () => {
    const { turns } = project([ev(EventTypes.AgentMessage, { text: "Reading the tests" })]);

    expect(turns).toHaveLength(1);
    expect(turns[0]).toMatchObject({ kind: "message", text: "Reading the tests" });
  });

  test("an empty message is not a turn", () => {
    // The harness emits partial messages while streaming; a blank one would
    // render as an empty bubble.
    const { turns } = project([
      ev(EventTypes.AgentMessage, { text: "   " }),
      ev(EventTypes.AgentMessage, {}),
    ]);

    expect(turns).toEqual([]);
  });

  test("a tool call and its completion are one turn, not two", () => {
    const { turns } = project([
      ev(EventTypes.ToolCalled, { tool: "bash", callId: "c1", input: { command: "ls" } }),
      ev(EventTypes.ToolCompleted, { tool: "bash", callId: "c1" }),
    ]);

    expect(turns).toHaveLength(1);
    expect(turns[0]).toMatchObject({ kind: "tool", tool: "bash", status: "completed" });
  });

  test("a completion with no matching call still produces a turn", () => {
    // Some tools are only reported once finished, so this is normal rather
    // than a gap in the record.
    const { turns } = project([ev(EventTypes.ToolCompleted, { tool: "read", callId: "c9" })]);

    expect(turns).toHaveLength(1);
    expect(turns[0]).toMatchObject({ kind: "tool", tool: "read", status: "completed" });
  });

  test("an errored tool reads as failed", () => {
    const { turns } = project([
      ev(EventTypes.ToolCalled, { tool: "bash", callId: "c1" }),
      ev(EventTypes.ToolCompleted, { tool: "bash", callId: "c1", status: "error" }),
    ]);

    expect(turns[0]).toMatchObject({ status: "failed" });
  });

  test("steering and answers are human turns, distinguished by intent", () => {
    const { turns } = project([
      ev(EventTypes.RunSteered, { text: "Use the existing helper" }),
      ev(EventTypes.QuestionAnswered, { answer: "Yes, merge it" }),
    ]);

    expect(turns).toMatchObject([
      { kind: "human", intent: "steer", text: "Use the existing helper" },
      { kind: "human", intent: "answer", text: "Yes, merge it" },
    ]);
  });
});

describe("plan", () => {
  test("todowrite becomes the plan rather than a tool turn", () => {
    const todos = [
      { content: "Read the failing test", status: "completed" },
      { content: "Fix the assertion", status: "in_progress" },
    ];
    const { turns, plan } = project([
      ev(EventTypes.ToolCalled, { tool: "todowrite", callId: "t1", input: { todos } }),
      ev(EventTypes.ToolCompleted, { tool: "todowrite", callId: "t1", input: { todos } }),
    ]);

    // Neither the call nor its completion may become a card: the agent
    // rewrites the whole list each time.
    expect(turns).toEqual([]);
    expect(plan).toMatchObject([
      { content: "Read the failing test", status: "completed" },
      { content: "Fix the assertion", status: "in_progress" },
    ]);
  });

  test("the latest todowrite replaces the previous plan", () => {
    const { plan } = project([
      ev(EventTypes.ToolCalled, { tool: "todowrite", input: { todos: [{ content: "One" }] } }),
      ev(EventTypes.ToolCalled, {
        tool: "todowrite",
        input: { todos: [{ content: "One" }, { content: "Two" }] },
      }),
    ]);

    expect(plan.map((p) => p.content)).toEqual(["One", "Two"]);
  });

  test("an unknown todo status falls back to pending rather than rendering blank", () => {
    const { plan } = project([
      ev(EventTypes.ToolCalled, {
        tool: "todowrite",
        input: { todos: [{ content: "Odd", status: "something_new" }] },
      }),
    ]);

    expect(plan[0]).toMatchObject({ status: "pending" });
  });

  test("a todowrite without a todos array leaves the plan alone", () => {
    const { plan } = project([
      ev(EventTypes.ToolCalled, { tool: "todowrite", input: { todos: [{ content: "Kept" }] } }),
      ev(EventTypes.ToolCompleted, { tool: "todowrite", input: {} }),
    ]);

    expect(plan.map((p) => p.content)).toEqual(["Kept"]);
  });
});

describe("activity", () => {
  test("an outstanding tool call is what the agent is doing", () => {
    const { activity, activeTool } = project([
      ev(EventTypes.ToolCalled, { tool: "bash", callId: "c1" }),
    ]);

    expect(activity).toBe("tool");
    expect(activeTool).toMatchObject({ name: "bash" });
  });

  test("a finished Run is not doing anything", () => {
    // Without this a completed run shows a thinking indicator forever.
    const { activity, activeTool } = project([
      ev(EventTypes.ToolCalled, { tool: "bash", callId: "c1" }),
      ev(EventTypes.RunCompleted, { status: "completed" }),
    ]);

    expect(activity).toBeNull();
    expect(activeTool).toBeNull();
  });

  test("a tool still running when the Run ends did not succeed", () => {
    const { turns } = project([
      ev(EventTypes.ToolCalled, { tool: "bash", callId: "c1" }),
      ev(EventTypes.RunAborted, { reason: "operator" }),
    ]);

    expect(turns[0]).toMatchObject({ status: "failed", endedAt: expect.any(String) });
  });
});

describe("usage", () => {
  test("cost and tokens accumulate across model requests", () => {
    const { costUsd, tokens } = project([
      ev(EventTypes.ModelRequestCompleted, { costUsd: 0.02, tokens: { input: 100, output: 50 } }),
      ev(EventTypes.ModelRequestCompleted, { costUsd: 0.03, tokens: { input: 10, output: 5 } }),
    ]);

    expect(costUsd).toBeCloseTo(0.05, 5);
    expect(tokens).toBe(165);
  });

  test("a malformed usage payload contributes nothing rather than NaN", () => {
    const { costUsd, tokens } = project([
      ev(EventTypes.ModelRequestCompleted, { costUsd: "free", tokens: { input: null } }),
    ]);

    expect(costUsd).toBe(0);
    expect(tokens).toBe(0);
  });
});

describe("incremental folding", () => {
  /**
   * The property the streaming UI depends on: applying events one at a time
   * to a retained projection must equal folding them all at once. If these
   * ever diverge, a reconnect would show a different conversation than a
   * fresh load of the same ledger.
   */
  const events = [
    ev(EventTypes.AgentMessage, { text: "Starting" }),
    ev(EventTypes.ToolCalled, { tool: "todowrite", input: { todos: [{ content: "Plan" }] } }),
    ev(EventTypes.ToolCalled, { tool: "bash", callId: "c1", input: { command: "ls" } }),
    ev(EventTypes.ModelRequestCompleted, { costUsd: 0.01, tokens: { input: 7, output: 3 } }),
    ev(EventTypes.ToolCompleted, { tool: "bash", callId: "c1" }),
    ev(EventTypes.RunSteered, { text: "Also update the README" }),
    ev(EventTypes.AgentMessage, { text: "Done" }),
    ev(EventTypes.RunCompleted, { status: "completed" }),
  ];

  test("event-at-a-time equals all-at-once", () => {
    const incremental = emptyProjection();
    for (const event of events) apply(incremental, [event]);

    expect(snapshot(incremental)).toEqual(project(events));
  });

  test("a replayed event is not folded twice", () => {
    // A reconnect can re-deliver an event that was already applied; the
    // cursor is what makes that harmless.
    const state = apply(emptyProjection(), events);
    const before = snapshot(state);

    apply(state, events);

    expect(snapshot(state)).toEqual(before);
  });

  test("settled turns keep their identity across frames", () => {
    // This is what lets React leave a finished turn alone when the next
    // event arrives; a fresh object every frame would re-render everything.
    const state = emptyProjection();
    apply(state, [events[0]!]);
    const first = state.turns[0];

    apply(state, [events[6]!]);

    expect(state.turns[0]).toBe(first);
  });
});
