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
  test("a plan update is the plan, not a turn", () => {
    const { turns, plan } = project([
      ev(EventTypes.PlanUpdated, {
        todos: [
          { content: "Read the failing test", status: "completed" },
          { content: "Fix the assertion", status: "in_progress" },
        ],
      }),
    ]);

    // It must not also become a card: the agent rewrites the whole list
    // each time, so every update would look like near-identical work.
    expect(turns).toEqual([]);
    expect(plan).toMatchObject([
      { content: "Read the failing test", status: "completed" },
      { content: "Fix the assertion", status: "in_progress" },
    ]);
  });

  test("the latest update replaces the previous plan", () => {
    const { plan } = project([
      ev(EventTypes.PlanUpdated, { todos: [{ content: "One" }] }),
      ev(EventTypes.PlanUpdated, { todos: [{ content: "One" }, { content: "Two" }] }),
    ]);

    expect(plan.map((p) => p.content)).toEqual(["One", "Two"]);
  });

  test("an unknown todo status falls back to pending rather than rendering blank", () => {
    const { plan } = project([
      ev(EventTypes.PlanUpdated, { todos: [{ content: "Odd", status: "something_new" }] }),
    ]);

    expect(plan[0]).toMatchObject({ status: "pending" });
  });

  test("an update without a todos array leaves the plan alone", () => {
    const { plan } = project([
      ev(EventTypes.PlanUpdated, { todos: [{ content: "Kept" }] }),
      ev(EventTypes.PlanUpdated, {}),
    ]);

    expect(plan.map((p) => p.content)).toEqual(["Kept"]);
  });

  test("a tool the harness did not classify as a plan is still a turn", () => {
    // Naming the plan tool is the adapter's job; the projection must not
    // second-guess it and swallow an ordinary tool call.
    const { turns } = project([
      ev(EventTypes.ToolCalled, { tool: "todowrite", callId: "t1", input: { todos: [] } }),
    ]);

    expect(turns).toHaveLength(1);
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
    const { activity, activeTool } = project(
      [ev(EventTypes.ToolCalled, { tool: "bash", callId: "c1" })],
      "completed",
    );

    expect(activity).toBeNull();
    expect(activeTool).toBeNull();
  });

  test("a tool still running when the Run fails did not succeed", () => {
    const { turns } = project([ev(EventTypes.ToolCalled, { tool: "bash", callId: "c1" })], "failed");

    expect(turns[0]).toMatchObject({ status: "failed", endedAt: expect.any(String) });
  });

  test("an aborted Run leaves its open tool aborted, not failed", () => {
    // The design system reads `aborted` as deliberate and `failed` as an
    // error; painting a stopped tool red would misreport what happened.
    const { turns } = project([ev(EventTypes.ToolCalled, { tool: "bash", callId: "c1" })], "aborted");

    expect(turns[0]).toMatchObject({ status: "aborted" });
  });

  test("a requested pause does not settle the conversation", () => {
    /*
     * `run.paused` is appended when a pause is *requested*; the Run keeps
     * running until the runner confirms. Treating the event as termination
     * would blank the activity indicator the moment an operator clicks it.
     */
    const { activity, turns } = project(
      [
        ev(EventTypes.ToolCalled, { tool: "bash", callId: "c1" }),
        ev(EventTypes.RunPaused, { requested: true, mode: "graceful" }),
      ],
      "running",
    );

    expect(activity).toBe("tool");
    expect(turns[0]).toMatchObject({ status: "running" });
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

describe("what the agent received, thought and got back", () => {
  test("the prompt, thoughts and a tool's output become turns in order", () => {
    const { turns } = project([
      ev(EventTypes.PromptDelivered, { text: "Implement this task." }),
      ev(EventTypes.AgentThought, { text: "Read the tests first." }),
      ev(EventTypes.ToolCalled, { tool: "bash", callId: "c1", input: { command: "pytest" } }),
      ev(EventTypes.ToolCompleted, {
        tool: "bash", callId: "c1", status: "error", exitCode: 3,
        output: { head: "F..", tail: "1 failed", omittedBytes: 9000 },
      }),
      ev(EventTypes.AgentMessage, { text: "One test fails.", contextTokens: 15258 }),
    ]);

    expect(turns.map((t) => t.kind)).toEqual(["prompt", "thought", "tool", "message"]);
    expect(turns[2]).toMatchObject({
      status: "failed",
      result: { exitCode: 3, output: { head: "F..", tail: "1 failed", omittedBytes: 9000 } },
    });
    expect(turns[3]).toMatchObject({ contextTokens: 15258 });
  });

  test("a steer stays queued until the agent takes it", () => {
    const events = [ev(EventTypes.RunSteered, { text: "Stop", directiveId: "dir_1" })];
    expect(project(events).turns[0]).toMatchObject({ deliveredAt: null });

    events.push(ev(EventTypes.DirectiveDelivered, { directiveId: "dir_1" }));
    expect(project(events).turns[0]).toMatchObject({ deliveredAt: events[1]!.occurredAt });
  });

  test("tokens are counted when cost is unknown, and a turn's totals are shown where it ended", () => {
    const conversation = project([
      ev(EventTypes.ModelRequestCompleted, { contextTokens: 12000, contextWindow: 744000 }),
      ev(EventTypes.ModelRequestCompleted, {
        turn: true, contextTokens: 12000,
        tokens: { input: 2, output: 10, cacheRead: 11889, cacheWrite: 71 },
      }),
    ]);

    expect(conversation.costUsd).toBe(0);
    expect(conversation.tokens).toBe(12);
    expect(conversation).toMatchObject({ contextTokens: 12000, contextWindow: 744000 });
    expect(conversation.turns).toEqual([
      expect.objectContaining({ kind: "usage", outputTokens: 10, contextTokens: 12000 }),
    ]);
  });

  test("a turn that ends on a message carries its totals on that message", () => {
    const { turns } = project([
      ev(EventTypes.AgentMessage, { text: "Done." }),
      ev(EventTypes.ModelRequestCompleted, { turn: true, contextTokens: 15500, tokens: { input: 2, output: 251 } }),
    ]);

    expect(turns).toEqual([expect.objectContaining({ kind: "message", contextTokens: 15500, outputTokens: 251 })]);
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
    ev(EventTypes.PlanUpdated, { todos: [{ content: "Plan" }] }),
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

    expect(snapshot(incremental, "completed")).toEqual(project(events, "completed"));
  });

  test("a replayed event is not folded twice", () => {
    // A reconnect can re-deliver an event that was already applied; the
    // cursor is what makes that harmless.
    const state = apply(emptyProjection(), events);
    const before = snapshot(state, "completed");

    apply(state, events);

    expect(snapshot(state, "completed")).toEqual(before);
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
