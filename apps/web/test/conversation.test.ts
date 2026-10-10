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
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { EventRow } from "@dude/design-system/components";
import { renderTurn, summarize } from "../src/screens/RunScreen.tsx";
import { EventTypes } from "@dude/domain";
import type { PersistedEvent } from "@dude/domain";
import { actorName, apply, emptyProjection, humanActor, landsHint, project, snapshot, steerWait, type HumanTurn } from "../src/api/conversation.ts";
import { modelCostShown } from "../src/api/client.ts";

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
    taskId: null,
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

describe("git event labels", () => {
  test("clone and checkout render truthful outcomes and checkout coordinates", () => {
    const labels = [
      [EventTypes.GitClone, { repo: "target", ref: "main", status: "cloned" }, "Cloned · target · at main"],
      [EventTypes.GitClone, { repo: "web", ref: "missing", status: "failed", error: "ref not found" }, "Clone · web · at missing · failed · ref not found"],
      [EventTypes.GitCheckout, { repo: "target", ref: "main", branch: "dude/task", base: "abc123" }, "Checked out · target · at main · on dude/task · from abc123"],
    ] as const;
    for (const [type, payload, label] of labels) {
      const event = ev(type, payload);
      const summary = summarize(event);
      expect(summary).toBe(label);
      const html = renderToStaticMarkup(createElement(EventRow, {
        occurredAt: event.occurredAt, actor: { type: "system" }, eventType: type, summary,
      }));
      expect(html).toContain(label);
      expect(html).toContain(type);
    }
  });
});

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

  // A real turn, in the order the orchestrator records it (a brainstorm's
  // or a conductor's): the turn's totals arrive after its last message,
  // then the turn ends. Whatever the order, nothing is thinking after it.
  const realTurn = () => [
    ev(EventTypes.PromptDelivered, { text: "where does metering go?" }),
    ev(EventTypes.ToolCalled, { tool: "list_tasks", callId: "c1" }),
    ev(EventTypes.ToolCompleted, { tool: "list_tasks", callId: "c1", status: "completed" }),
    ev(EventTypes.ModelRequestCompleted, { tokens: { input: 10, output: 5 } }),
    ev(EventTypes.AgentMessage, { text: "In the meter's rollup." }),
    ev(EventTypes.ModelRequestCompleted, { turn: true, tokens: { input: 12, output: 7 } }),
    ev(EventTypes.SessionStopped, { reason: "turn_complete" }),
  ];

  test("a turn's end leaves nothing thinking, one event at a time or all at once", () => {
    const events = realTurn();
    expect(snapshot(apply(emptyProjection(), events), "running")).toMatchObject({ activity: null, activeTool: null });
    const live = emptyProjection();
    for (const e of events) apply(live, [e]);
    expect(snapshot(live, "running")).toMatchObject({ activity: null, activeTool: null });
  });

  test("the turn's closing totals alone end it, before the session stops", () => {
    const events = realTurn().slice(0, -1);
    expect(snapshot(apply(emptyProjection(), events), "running").activity).toBeNull();
  });

  test("a stopped session ends the turn when no totals closed it", () => {
    const events = [
      ev(EventTypes.PromptDelivered, { text: "go" }),
      ev(EventTypes.ModelRequestCompleted, { tokens: { input: 1, output: 1 } }),
      ev(EventTypes.SessionStopped, { reason: "turn_complete" }),
    ];
    expect(snapshot(apply(emptyProjection(), events), "running").activity).toBeNull();
  });

  test("between model requests of a turn, the agent is thinking", () => {
    const events = realTurn();
    const live = emptyProjection();
    // After a tool's result and its model request, and again after the
    // message before the closing totals: thinking, not idle.
    apply(live, events.slice(0, 4));
    expect(snapshot(live, "running").activity).toBe("thinking");
    const next = emptyProjection();
    apply(next, [...events.slice(0, 4), ev(EventTypes.ModelRequestCompleted, { tokens: { input: 1, output: 1 } })]);
    expect(snapshot(next, "running").activity).toBe("thinking");
  });

  test("the next turn thinks again", () => {
    const state = apply(emptyProjection(), realTurn());
    apply(state, [
      ev(EventTypes.PromptDelivered, { text: "and retries?" }),
      ev(EventTypes.ModelRequestCompleted, { tokens: { input: 1, output: 1 } }),
    ]);
    expect(snapshot(state, "running").activity).toBe("thinking");
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

  test("lux's reported AI cost replaces the harness's sum, latest wins, and is settled only when final", () => {
    const events = [
      ev(EventTypes.ModelRequestCompleted, { costUsd: 0.02 }),
      ev(EventTypes.ModelRequestCompleted, { costUsd: 0.03 }),
    ];
    expect(project(events)).toMatchObject({ costSource: { from: "agent", settled: false } });

    events.push(ev(EventTypes.RunCostReported, { aiUsd: 1.2, computeUsd: 0.004, status: "incomplete" }));
    let c = project(events);
    expect(c.costUsd).toBe(1.2);
    expect(c.costSource).toEqual({ from: "lux", settled: false });

    // Harness deltas after lux reported are the same tokens: not added on.
    events.push(ev(EventTypes.ModelRequestCompleted, { costUsd: 0.5 }));
    events.push(ev(EventTypes.RunCostReported, { aiUsd: 1.810247, computeUsd: 0.007659225, status: "final" }));
    c = project(events);
    expect(c.costUsd).toBe(1.810247);
    expect(c.costSource).toEqual({ from: "lux", settled: true });

    // The same answer folded one event at a time.
    let state = emptyProjection();
    for (const e of events) state = apply(state, [e]);
    expect(snapshot(state, "completed").costUsd).toBe(1.810247);
  });

  test("a report with no AI amount leaves the harness's cost standing", () => {
    const c = project([
      ev(EventTypes.ModelRequestCompleted, { costUsd: 0.04 }),
      ev(EventTypes.RunCostReported, { aiUsd: null, computeUsd: 0.001, status: "pending" }),
    ]);
    expect(c.costUsd).toBeCloseTo(0.04, 9);
    expect(c.costSource.from).toBe("agent");
  });

  test("lux's zero is a price; the agent's zero is not reported", () => {
    expect(modelCostShown(0, "lux")).toBe(0);
    expect(modelCostShown(0, "agent")).toBeNull();
    expect(modelCostShown(0.3, "agent")).toBe(0.3);
    expect(modelCostShown(1.81, "lux")).toBe(1.81);
  });

  test("a later report with no AI amount clears lux's figure, as the Run's row does", () => {
    const events = [
      ev(EventTypes.ModelRequestCompleted, { costUsd: 0.04 }),
      ev(EventTypes.RunCostReported, { aiUsd: 1.2, computeUsd: 0.001, status: "incomplete" }),
    ];
    expect(project(events).costUsd).toBe(1.2);
    events.push(ev(EventTypes.RunCostReported, { aiUsd: null, computeUsd: 0.001, status: "incomplete" }));
    const c = project(events);
    expect(c.costUsd).toBeCloseTo(0.04, 9);
    expect(c.costSource).toEqual({ from: "agent", settled: false });
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
    expect(project(events).turns[0]).toMatchObject({ deliveredAt: events[1]!.occurredAt, read: false });
  });
});

describe("a steer read at the agent's next step", () => {
  // The agent is running bash; a person steers; the harness takes it; bash
  // finishes; the next step reads it; the agent calls the next tool.
  const script = () => [
    ev(EventTypes.PromptDelivered, { text: "Do it", lands: "next_step" }),
    ev(EventTypes.ToolCalled, { tool: "bash", callId: "c1", input: { command: "sleep 20" } }),
    ev(EventTypes.RunSteered, { text: "Check the migration too", directiveId: "dir_1" }),
    ev(EventTypes.DirectiveAccepted, { directiveId: "dir_1", lands: "next_step", receipt: true }),
    ev(EventTypes.ToolCompleted, { tool: "bash", callId: "c1", status: "completed" }),
    ev(EventTypes.DirectiveDelivered, { directiveId: "dir_1", read: true }),
    ev(EventTypes.ToolCalled, { tool: "read", callId: "c2", input: { path: "migrations/060.sql" } }),
  ];

  test("while the tool runs it is accepted, queued where it was typed, and the running tool is known", () => {
    const events = script().slice(0, 4);
    const conversation = project(events);
    expect(conversation.turns.map((t) => t.kind)).toEqual(["prompt", "tool", "human"]);
    expect(conversation.turns[2]).toMatchObject({
      deliveredAt: null, acceptedAt: events[3]!.occurredAt, lands: "next_step", read: false,
    });
    expect(conversation.activeTool).toMatchObject({ name: "bash" });
    expect(conversation.lands).toBe("next_step");
  });

  test("once read, it sits between the tool it waited for and the next one, saying which", () => {
    const events = script();
    const { turns } = project(events);
    expect(turns.map((t) => (t.kind === "tool" ? t.tool : t.kind))).toEqual(["prompt", "bash", "human", "read"]);
    expect(turns[2]).toMatchObject({ deliveredAt: events[5]!.occurredAt, read: true, after: "bash", at: events[2]!.occurredAt });
  });

  test("the move keeps the turn's identity, and folding one event at a time gives the same transcript", () => {
    const events = script();
    const state = emptyProjection();
    for (const e of events.slice(0, 3)) apply(state, [e]);
    const steer = state.turns[2];
    const bash = state.turns[1];
    for (const e of events.slice(3)) apply(state, [e]);
    expect(state.turns[2]).toBe(steer!);
    expect(state.turns[1]).toBe(bash!);
    expect(snapshot(state).turns).toEqual(project(events).turns);
  });

  test("a steer typed before the tool's completion, read after a later tool, moves past both", () => {
    const { turns } = project([
      ev(EventTypes.RunSteered, { text: "S", directiveId: "dir_1" }),
      ev(EventTypes.ToolCalled, { tool: "bash", callId: "c1" }),
      ev(EventTypes.ToolCompleted, { tool: "bash", callId: "c1", status: "completed" }),
      ev(EventTypes.AgentMessage, { text: "Done with bash." }),
      ev(EventTypes.DirectiveDelivered, { directiveId: "dir_1", read: true }),
    ]);
    expect(turns.map((t) => t.kind)).toEqual(["tool", "message", "human"]);
    // A message came between: it was not read right after a tool.
    expect(turns[2]).toMatchObject({ after: null, read: true });
  });

  test("a delivery from an older lux leaves it where it was typed, with no read time", () => {
    const { turns } = project([
      ev(EventTypes.ToolCalled, { tool: "bash", callId: "c1" }),
      ev(EventTypes.RunSteered, { text: "S", directiveId: "dir_1" }),
      ev(EventTypes.ToolCompleted, { tool: "bash", callId: "c1", status: "completed" }),
      ev(EventTypes.DirectiveDelivered, { directiveId: "dir_1" }),
    ]);
    expect(turns.map((t) => t.kind)).toEqual(["tool", "human"]);
    expect(turns[1]).toMatchObject({ read: false, after: null });
    expect((turns[1] as { deliveredAt: string | null }).deliveredAt).not.toBeNull();
  });

  test("a harness that reads between turns says so, and a repeated delivery changes nothing", () => {
    const events = [
      ev(EventTypes.RunSteered, { text: "S", directiveId: "dir_1" }),
      ev(EventTypes.DirectiveAccepted, { directiveId: "dir_1", lands: "next_turn", receipt: true }),
      ev(EventTypes.AgentMessage, { text: "Turn over." }),
      ev(EventTypes.DirectiveDelivered, { directiveId: "dir_1", read: true }),
      ev(EventTypes.AgentMessage, { text: "Next turn." }),
      ev(EventTypes.DirectiveDelivered, { directiveId: "dir_1", read: true }),
    ];
    const conversation = project(events);
    expect(conversation.lands).toBe("next_turn");
    expect(conversation.turns.map((t) => t.kind)).toEqual(["message", "human", "message"]);
    expect(conversation.turns[1]).toMatchObject({ lands: "next_turn", deliveredAt: events[3]!.occurredAt });
  });

  test("a failed steer says why, and interrupting it re-sends the same turn rather than adding one", () => {
    const events = [
      ev(EventTypes.RunSteered, { text: "S", directiveId: "dir_1" }),
      ev(EventTypes.DirectiveFailed, { directiveId: "dir_1", error: "the agent exited" }),
    ];
    expect(project(events).turns[0]).toMatchObject({ failed: "the agent exited", deliveredAt: null });
    events.push(
      ev(EventTypes.RunSteered, { text: "S", directiveId: "dir_2", supersedes: "dir_1", interrupt: true }),
      ev(EventTypes.DirectiveDelivered, { directiveId: "dir_2" }),
    );
    const { turns } = project(events);
    expect(turns).toHaveLength(1);
    expect(turns[0]).toMatchObject({ failed: null, interrupting: true, deliveredAt: events[3]!.occurredAt });
  });

  test("a new steer that supersedes with other words is a turn of its own", () => {
    const { turns } = project([
      ev(EventTypes.RunSteered, { text: "S", directiveId: "dir_1" }),
      ev(EventTypes.RunSteered, { text: "Something else", directiveId: "dir_2", supersedes: "dir_1", interrupt: true }),
    ]);
    expect(turns).toHaveLength(2);
  });

  const interruptNow = [
    ev(EventTypes.RunSteered, { text: "S", directiveId: "dir_a" }),
    ev(EventTypes.ToolCalled, { tool: "bash", callId: "c1" }),
    ev(EventTypes.ToolCompleted, { tool: "bash", callId: "c1", status: "completed" }),
    ev(EventTypes.DirectiveDelivered, { directiveId: "dir_a", read: true }),
    ev(EventTypes.RunSteered, { text: "S", directiveId: "dir_b", supersedes: "dir_a", interrupt: true }),
    ev(EventTypes.DirectiveDelivered, { directiveId: "dir_b", interruptOnly: true }),
  ];

  test("Interrupt now clicked after the steer was read stays its one turn, read where it was read", () => {
    const { turns } = project(interruptNow);
    expect(turns.map((t) => t.kind)).toEqual(["tool", "human"]);
    expect(turns[1]).toMatchObject({ read: true, after: "bash", deliveredAt: interruptNow[3]!.occurredAt, failed: null });
  });

  test("Interrupt now before the read, then the read and the interrupt's delivery: the same one turn", () => {
    const live = [
      ev(EventTypes.RunSteered, { text: "S", directiveId: "dir_a" }),
      ev(EventTypes.ToolCalled, { tool: "bash", callId: "c1" }),
      ev(EventTypes.RunSteered, { text: "S", directiveId: "dir_b", supersedes: "dir_a", interrupt: true }),
      ev(EventTypes.ToolCompleted, { tool: "bash", callId: "c1", status: "cancelled" }),
      ev(EventTypes.DirectiveDelivered, { directiveId: "dir_a", read: true }),
      ev(EventTypes.DirectiveDelivered, { directiveId: "dir_b", interruptOnly: true }),
    ];
    const { turns } = project(live);
    expect(turns.map((t) => t.kind)).toEqual(["tool", "human"]);
    expect(turns[1]).toMatchObject({ read: true, after: "bash", deliveredAt: live[4]!.occurredAt, interrupting: true });
  });

  test("an older lux fails the steer the interrupt cancelled, and the interrupt with it: one failed turn to retry", () => {
    const { turns } = project([
      ev(EventTypes.RunSteered, { text: "S", directiveId: "dir_a" }),
      ev(EventTypes.RunSteered, { text: "S", directiveId: "dir_b", supersedes: "dir_a", interrupt: true }),
      ev(EventTypes.DirectiveFailed, { directiveId: "dir_a", error: "the turn was cancelled before the agent read it" }),
      ev(EventTypes.DirectiveFailed, { directiveId: "dir_b", error: "the turn was cancelled before the agent read it" }),
    ]);
    expect(turns).toHaveLength(1);
    expect(turns[0]).toMatchObject({ failed: "the turn was cancelled before the agent read it", deliveredAt: null, directiveId: "dir_b" });
  });

  test("the steer fails while the interrupt carrying its words is on its way: not failed, then read once", () => {
    const events = [
      ev(EventTypes.RunSteered, { text: "S", directiveId: "dir_a" }),
      ev(EventTypes.RunSteered, { text: "S", directiveId: "dir_b", supersedes: "dir_a", interrupt: true }),
      ev(EventTypes.DirectiveFailed, { directiveId: "dir_a", error: "the agent exited" }),
    ];
    expect(project(events).turns[0]).toMatchObject({ failed: null, deliveredAt: null, interrupting: true });
    events.push(ev(EventTypes.DirectiveDelivered, { directiveId: "dir_b", read: true }));
    const { turns } = project(events);
    expect(turns).toHaveLength(1);
    expect(turns[0]).toMatchObject({ failed: null, read: true, deliveredAt: events[3]!.occurredAt });
  });

  test("the progress row stays found when a steer moves past it", () => {
    const events = [
      ev(EventTypes.RunSteered, { text: "S", directiveId: "dir_1" }),
      ev("agent.custom.progress", { data: { done: 1, of: 3 } }),
      ev(EventTypes.DirectiveDelivered, { directiveId: "dir_1", read: true }),
      ev("agent.custom.progress", { data: { done: 2, of: 3 } }),
    ];
    const { turns } = project(events);
    expect(turns.map((t) => t.kind)).toEqual(["progress", "human"]);
    expect(turns[0]).toMatchObject({ done: 2 });
  });
});

describe("what a queued steer waits for, and what the composer promises", () => {
  const queued = (lands: "next_step" | "next_turn" | null = null) =>
    ({ ...(project([ev(EventTypes.RunSteered, { text: "S", directiveId: "d" })]).turns[0] as HumanTurn), lands });

  test("the run's own state comes first", () => {
    expect(steerWait(queued("next_step"), "paused", "bash", "next_step")).toEqual({ kind: "paused" });
    expect(steerWait(queued(), "starting", null, null)).toEqual({ kind: "starting" });
    expect(steerWait(queued(), "scheduled", null, null)).toEqual({ kind: "starting" });
  });

  test("next step: after the running tool, named, or at the next step", () => {
    expect(steerWait(queued("next_step"), "running", "bash", null)).toEqual({ kind: "tool", tool: "bash" });
    expect(steerWait(queued(), "running", null, "next_step")).toEqual({ kind: "next_step" });
  });

  test("a harness that reads between turns, or a lux that has not said, waits for the turn", () => {
    expect(steerWait(queued("next_turn"), "running", "bash", "next_step")).toEqual({ kind: "next_turn" });
    expect(steerWait(queued(), "running", "bash", null)).toEqual({ kind: "next_turn" });
  });

  test("the composer's hint follows the same capability, and says nothing when not running", () => {
    expect(landsHint("running", "bash", "next_step")).toBe("Lands after the current tool");
    expect(landsHint("running", null, "next_step")).toBe("Lands at the agent's next step");
    expect(landsHint("running", "bash", "next_turn")).toBe("Lands when the turn ends");
    expect(landsHint("running", null, null)).toBe("Lands when the turn ends");
    expect(landsHint("paused", null, "next_step")).toBeNull();
  });

  test("retrying a failed steer keeps it one turn, queued again", () => {
    const { turns } = project([
      ev(EventTypes.RunSteered, { text: "S", directiveId: "dir_1" }),
      ev(EventTypes.DirectiveFailed, { directiveId: "dir_1", error: "gone" }),
      ev(EventTypes.RunSteered, { text: "S", directiveId: "dir_2", supersedes: "dir_1" }),
    ]);
    expect(turns).toHaveLength(1);
    expect(turns[0]).toMatchObject({ failed: null, deliveredAt: null, interrupting: false, directiveId: "dir_2" });
  });

  test("retrying a failed interrupt queues a plain steer, which can be interrupted again", () => {
    const events = [
      ev(EventTypes.RunSteered, { text: "S", directiveId: "dir_1" }),
      ev(EventTypes.RunSteered, { text: "S", directiveId: "dir_2", supersedes: "dir_1", interrupt: true }),
      ev(EventTypes.DirectiveFailed, { directiveId: "dir_2", error: "workload not reachable" }),
      ev(EventTypes.RunSteered, { text: "S", directiveId: "dir_3", supersedes: "dir_2" }),
    ];
    let { turns } = project(events);
    expect(turns).toHaveLength(1);
    expect(turns[0]).toMatchObject({ failed: null, deliveredAt: null, interrupting: false, directiveId: "dir_3" });
    events.push(ev(EventTypes.RunSteered, { text: "S", directiveId: "dir_4", supersedes: "dir_3", interrupt: true }));
    ({ turns } = project(events));
    expect(turns).toHaveLength(1);
    expect(turns[0]).toMatchObject({ interrupting: true, directiveId: "dir_4" });
  });
});

describe("tokens", () => {
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

describe("an agent that asks", () => {
  test("the question waits for an answer, and once answered its turn is the record: no second turn", () => {
    const events = [
      ev(EventTypes.QuestionAsked, { kind: "agent", questionId: "qst_1", prompt: "Sort the table?", options: ["yes", "no"] }),
    ];
    let conversation = project(events);
    expect(conversation.openQuestion).toMatchObject({ questionId: "qst_1", text: "Sort the table?",
      items: [{ header: "", question: "Sort the table?", multiple: false,
        choices: [{ label: "yes", description: "", recommended: false }, { label: "no", description: "", recommended: false }] }] });
    expect(conversation.activity).toBeNull();

    // An answer from before answers were kept per question: the choice its text names.
    events.push(ev(EventTypes.QuestionAnswered, { questionId: "qst_1", answer: "Yes ", directiveId: "dir_1" }));
    conversation = project(events);
    expect(conversation.openQuestion).toBeNull();
    expect(conversation.turns.map((t) => t.kind)).toEqual(["question"]);
    expect(conversation.turns[0]).toMatchObject({ answers: [{ choices: [0], text: "" }] });
  });

  test("several questions answered through the form: each answer on its question, the note as the person's own turn", () => {
    const items = [
      { header: "Retry scope", question: "Which failures?", multiple: false,
        choices: [{ label: "5xx only", description: "A 4xx is our bug.", recommended: true }, { label: "Everything" }] },
      { header: "Button", question: "What should it say?", multiple: false, choices: [] },
    ];
    const events = [
      ev(EventTypes.QuestionAsked, { kind: "agent", questionId: "qst_m", prompt: "2 questions: Retry scope, Button", options: [], items }),
      { ...ev(EventTypes.QuestionAnswered, { questionId: "qst_m", answer: "1. Retry scope …", directiveId: "dir_m",
        answers: [{ choices: [0], text: "" }, { choices: [], text: "Pay with two cards" }], note: "Keep it under 10s.",
        attachments: [{ id: "att_1", name: "a.png", width: 10, height: 10, original: {} }] }), actor: { type: "person", id: "per_ana" } } as PersistedEvent,
    ];
    const conversation = project(events);
    expect(conversation.turns.map((t) => t.kind)).toEqual(["question", "human"]);
    expect(conversation.turns[0]).toMatchObject({
      items: [{ header: "Retry scope", choices: [{ label: "5xx only", recommended: true }, { label: "Everything", description: "", recommended: false }] },
        { header: "Button" }],
      answers: [{ choices: [0], text: "" }, { choices: [], text: "Pay with two cards" }],
      answeredBy: { id: "per_ana" },
    });
    expect(conversation.turns[1]).toMatchObject({ kind: "human", intent: "message", text: "Keep it under 10s.", deliveredAt: null });
    expect((conversation.turns[1] as { attachments: unknown[] }).attachments).toHaveLength(1);
    // Read when the agent takes the directive, as any message is.
    events.push(ev(EventTypes.DirectiveDelivered, { directiveId: "dir_m" }));
    expect(project(events).turns[1]).toMatchObject({ deliveredAt: events[2]!.occurredAt });
  });

  test("the question block leaves the message, which the question turn says instead", () => {
    const { turns } = project([
      ev(EventTypes.AgentMessage, { text: "I need a decision.\n\n```question\nSort?\n- yes\n```\n" }),
    ]);
    expect(turns[0]).toMatchObject({ kind: "message", text: "I need a decision." });
  });

  test("a question closed because the escalation it asked about was decided on the banner waits no more", () => {
    const events = [
      ev(EventTypes.QuestionAsked, { kind: "agent", questionId: "qst_e", prompt: "Retry?", options: ["Retry", "Stop"] }),
      ev(EventTypes.QuestionClosed, { questionId: "qst_e", by: "decision", action: "retry" }),
    ];
    const conversation = project(events);
    expect(conversation.openQuestion).toBeNull();
    expect(conversation.turns).toEqual([expect.objectContaining({ kind: "question", answeredAt: null, closedAt: events[1]!.occurredAt })]);
  });

  test("a workflow escalation is not a question this agent asked", () => {
    const conversation = project([ev(EventTypes.QuestionAsked, { kind: "escalation", reason: "review_loop_exhausted" })]);
    expect(conversation.turns).toEqual([]);
    expect(conversation.openQuestion).toBeNull();
  });

  test("a run that ended asks nothing", () => {
    const events = [ev(EventTypes.QuestionAsked, { kind: "agent", questionId: "qst_1", prompt: "Sort?" })];
    expect(project(events, "aborted").openQuestion).toBeNull();
  });
});

describe("how long a resume took", () => {
  const timed = (payload: Record<string, unknown>) => ev(EventTypes.RunResumeTimed, payload);
  const phases = { react: 120, schedule: 300, image: 1000, restore: 1200, start: 800, reload: 2100, take: 400, firstOutput: 1600 };
  type Turns = ReturnType<typeof project>["turns"];
  const noticeTexts = (turns: Turns) => turns.map((t) => t.kind === "notice" && t.text);
  const unparkedNotices = (turns: Turns) => turns.filter((t) => t.kind === "notice" && t.notice === "unparked");
  // The turns of events folded one at a time, as the UI streams them.
  const folded = (events: PersistedEvent[]) => {
    const state = emptyProjection();
    for (const e of events) apply(state, [e]);
    return snapshot(state).turns;
  };

  test("a park's return says how long it took, and its phases in order on hover", () => {
    const { turns } = project([
      ev("run.parked", { reason: "person" }),
      ev("run.unparked", { reason: "person" }),
      ev(EventTypes.AgentMessage, { text: "On it." }),
      timed({ epoch: 2, cause: "answer", moved: false, totalMs: 6400, untilBusyMs: 4800, phases }),
    ]);
    const notices = turns.filter((t) => t.kind === "notice");
    expect(notices.map((t) => t.text)).toEqual([expect.stringContaining("Parked"), "Taken back up in 6.4s."]);
    expect(notices[1]!.title).toBe([
      "dude asked lux 120ms", "lux placed it 300ms", "image ready 1.0s", "restored 1.2s", "started 800ms",
      "agent reloaded 2.1s", "took its input 400ms", "first words 1.6s",
    ].join("\n"));
  });

  test("one placed on another host says so, and a phase it does not know is left out", () => {
    const { turns } = project([
      ev("run.unparked", { reason: "repository" }),
      timed({ epoch: 3, cause: "repository", moved: true, totalMs: 12_300, phases: { react: 50, reload: 3000 } }),
    ]);
    expect(turns).toEqual([expect.objectContaining({
      kind: "notice", notice: "unparked", text: "Taken back up in 12s, on another host.",
      title: "dude asked lux 50ms\nagent reloaded 3.0s",
    })]);
  });

  test("a person's resume of their own pause gets a notice of its own", () => {
    const { turns } = project([
      ev(EventTypes.RunPaused, { requested: true }),
      ev(EventTypes.RunResumed, {}),
      timed({ epoch: 2, cause: "person", moved: false, totalMs: 6400, phases }),
    ]);
    expect(turns).toEqual([expect.objectContaining({ kind: "notice", notice: "unparked", text: "Resumed in 6.4s." })]);
  });

  test("a park's return never takes a later resume's numbers", () => {
    const { turns } = project([
      ev("run.unparked", { reason: "person" }),
      ev(EventTypes.RunPaused, { requested: true }),
      timed({ epoch: 3, cause: "person", moved: false, totalMs: 2000, phases: {} }),
    ]);
    expect(noticeTexts(turns)).toEqual(["Taken back up where it left off.", "Resumed in 2.0s."]);
  });

  test("folded one event at a time, the notice is the same", () => {
    const events = [
      ev("run.unparked", { reason: "person" }),
      timed({ epoch: 2, cause: "answer", moved: false, totalMs: 6400, phases }),
    ];
    expect(folded(events)).toEqual(project(events).turns);
  });

  // Two parks, each resume's timing arriving late: each goes to its own
  // notice, in whichever order they arrive.
  for (const order of ["in order", "the later first"] as const) {
    test(`two parks' delayed timings, ${order}, each say their own resume's numbers`, () => {
      const parks = [
        ev("run.parked", { reason: "person" }),
        ev("run.unparked", { reason: "person", epoch: 2 }),
        ev("run.parked", { reason: "person" }),
        ev("run.unparked", { reason: "person", epoch: 3 }),
      ];
      const timing = (epoch: number) => epoch === 2
        ? timed({ epoch: 2, cause: "answer", moved: false, totalMs: 6400, phases: { react: 120 } })
        : timed({ epoch: 3, cause: "answer", moved: true, totalMs: 2000, phases: { react: 50 } });
      const late = order === "in order" ? [timing(2), timing(3)] : [timing(3), timing(2)];
      const { turns } = project([...parks, ...late]);
      expect(unparkedNotices(turns)).toEqual([
        expect.objectContaining({ text: "Taken back up in 6.4s.", title: "dude asked lux 120ms" }),
        expect.objectContaining({ text: "Taken back up in 2.0s, on another host.", title: "dude asked lux 50ms" }),
      ]);
    });
  }

  test("a timing no notice is waiting for is a resume of its own", () => {
    const { turns } = project([
      ev("run.unparked", { reason: "person", epoch: 2 }),
      timed({ epoch: 2, cause: "answer", moved: false, totalMs: 6400, phases: {} }),
      ev(EventTypes.RunPaused, { requested: true }),
      ev(EventTypes.RunResumed, {}),
      timed({ epoch: 3, cause: "person", moved: false, totalMs: 1500, phases: {} }),
    ]);
    expect(noticeTexts(turns)).toEqual(["Taken back up in 6.4s.", "Resumed in 1.5s."]);
  });

  // lux streams the resumed agent's first words before its answer to the
  // resume is back, so the timing is written before the park's return.
  test("a park's timing that arrives before its return is said on that return, once", () => {
    const events = [
      ev("run.parked", { reason: "person" }),
      timed({ epoch: 2, cause: "answer", moved: false, totalMs: 6400, phases: { react: 120, take: 400 } }),
      ev("run.unparked", { reason: "person", epoch: 2 }),
    ];
    const { turns } = project(events);
    expect(unparkedNotices(turns)).toEqual([
      expect.objectContaining({ text: "Taken back up in 6.4s.", title: "dude asked lux 120ms\ntook its input 400ms" }),
    ]);
    expect(folded(events)).toEqual(turns);
  });

  test("a person's resume of a Run no park holds is said at once", () => {
    const { turns } = project([
      ev("run.parked", { reason: "person" }),
      timed({ epoch: 2, cause: "answer", moved: false, totalMs: 6400, phases: {} }),
      ev("run.unparked", { reason: "person", epoch: 2 }),
      ev(EventTypes.RunPaused, { requested: true }),
      ev(EventTypes.RunResumed, {}),
      timed({ epoch: 3, cause: "person", moved: false, totalMs: 1500, phases: {} }),
    ]);
    expect(noticeTexts(turns)).toEqual([
      expect.stringContaining("Parked"), "Taken back up in 6.4s.", "Resumed in 1.5s.",
    ]);
  });

  // A person's own resume (epoch 2, no run.unparked) is timed only after
  // the agent was parked again: its timing is still said, and the next
  // park's return gets its own.
  const delayedPersonal = [
    ev(EventTypes.RunPaused, { requested: true }),
    ev(EventTypes.RunResumed, {}),
    ev("run.parked", { reason: "person" }),
    timed({ epoch: 2, cause: "person", moved: false, totalMs: 6400, phases: {} }),
    ev("run.unparked", { reason: "person", epoch: 3 }),
    timed({ epoch: 3, cause: "answer", moved: false, totalMs: 1500, phases: {} }),
  ];

  test("a person's resume timed after the next park is still said, and so is that park's return", () => {
    const { turns } = project(delayedPersonal);
    expect(noticeTexts(turns)).toEqual([
      expect.stringContaining("Parked"), "Resumed in 6.4s.", "Taken back up in 1.5s.",
    ]);
  });

  test("folded one event at a time, a delayed personal resume and the park's return are the same", () => {
    expect(folded(delayedPersonal)).toEqual(project(delayedPersonal).turns);
  });
});

describe("how a run ended", () => {
  test("a failure says why, and ends whatever the agent was doing", () => {
    const conversation = project([
      ev(EventTypes.ToolCalled, { tool: "bash", callId: "c1" }),
      ev("run.parked", { reason: "person" }),
      ev(EventTypes.RunFailed, { status: "failed", error: "lux refused to resume the run: invalid spec" }),
    ], "failed");

    expect(conversation.turns.at(-1)).toMatchObject({
      kind: "ended", outcome: "failed", text: "Failed: lux refused to resume the run: invalid spec",
    });
    expect(conversation.activity).toBeNull();
  });

  test("an abort names who did it, and their reason when they gave one", () => {
    const byAna = (payload: Record<string, unknown>) => ({ ...ev(EventTypes.RunAborted, payload), actor: { type: "human", id: "key_ana" } }) as PersistedEvent;
    expect(project([byAna({ reason: null })]).turns[0]).toMatchObject({
      kind: "ended", outcome: "aborted", text: "Aborted.", by: { id: "key_ana", name: null }, why: null,
    });
    expect(project([byAna({ reason: "wrong task" })]).turns[0]).toMatchObject({
      text: "Aborted: wrong task", why: "wrong task",
    });
  });

  test("finishing needs no line: the header says so", () => {
    expect(project([ev(EventTypes.RunCompleted, { status: "completed" })]).turns).toEqual([]);
  });
});

describe("who said it", () => {
  const human = (type: string, payload: Record<string, unknown>, actor: Record<string, unknown>) =>
    ({ ...ev(type, payload), actor }) as PersistedEvent;

  test("a steer and an answer carry the person who sent them", () => {
    const { turns } = project([
      human(EventTypes.RunSteered, { text: "Use the helper", directiveId: "d1" }, { type: "human", id: "key_ana" }),
      human(EventTypes.QuestionAnswered, { answer: "Yes" }, { type: "human", id: "key_bo", name: "Bo Lindqvist" }),
    ]);
    expect(turns.map((t) => (t.kind === "human" ? t.by : null))).toEqual([
      { id: "key_ana", name: null },
      { id: "key_bo", name: "Bo Lindqvist" },
    ]);
  });

  test("person actors retain attribution for steering, answers and aborts", () => {
    const actor = { type: "person", id: "per_ana", name: "Ana Ribeiro" };
    const { turns } = project([
      human(EventTypes.RunSteered, { text: "Use the helper", directiveId: "d1" }, actor),
      human(EventTypes.QuestionAnswered, { answer: "Yes" }, actor),
      human(EventTypes.RunAborted, { reason: "wrong task" }, actor),
    ]);
    expect(turns.map((turn) => "by" in turn ? turn.by : null)).toEqual([
      { id: "per_ana", name: "Ana Ribeiro" },
      { id: "per_ana", name: "Ana Ribeiro" },
      { id: "per_ana", name: "Ana Ribeiro" },
    ]);
  });

  test("nobody is named for an actor that is not a person, or unknown", () => {
    expect(humanActor(human(EventTypes.RunSteered, {}, { type: "system", id: "dude" }))).toBeNull();
    expect(humanActor(human(EventTypes.RunSteered, {}, { type: "human", id: "unknown" }))).toBeNull();
  });

  test("a name is the event's own, then the organisation's, else unknown", () => {
    const people = new Map([["key_ana", "Ana Ribeiro"]]);
    expect(actorName({ id: "key_ana", name: null }, people)).toBe("Ana Ribeiro");
    expect(actorName({ id: "key_ana", name: "Ana R." }, people)).toBe("Ana R.");
    expect(actorName({ id: "key_gone", name: null }, people)).toBeNull();
    expect(actorName(null, people)).toBeNull();
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

describe("images in the conversation", () => {
  const image = (id: string) => ({ id, name: `${id}.png`, contentType: "image/png", width: 1200, height: 760, bytes: 412,
    original: { contentType: "image/png", width: 2400, height: 1520, bytes: 1900 } });

  test("a steer, an answer and the prompt keep the images they were sent with, in order; a retry keeps its turn's", () => {
    const turns = project([
      ev(EventTypes.PromptDelivered, { text: "Build it", attachments: [image("att_p")] }),
      ev(EventTypes.RunSteered, { text: "see these", directiveId: "dir_1", attachments: [image("att_b"), image("att_a")] }),
      ev(EventTypes.DirectiveFailed, { directiveId: "dir_1", error: "the run stopped" }),
      ev(EventTypes.RunSteered, { text: "see these", directiveId: "dir_2", supersedes: "dir_1" }),
      ev(EventTypes.QuestionAsked, { kind: "agent", questionId: "q1", prompt: "Phone?" }),
      ev(EventTypes.QuestionAnswered, { questionId: "q1", answer: "", directiveId: "dir_3", attachments: [image("att_q")] }),
      // An image alone is a message.
      ev(EventTypes.RunSteered, { text: "", directiveId: "dir_4", attachments: [image("att_only"), { id: 7 }] }),
    ]).turns;
    const prompt = turns.find((t) => t.kind === "prompt");
    expect(prompt?.kind === "prompt" && prompt.attachments.map((a) => a.id)).toEqual(["att_p"]);
    const human = turns.filter((t): t is HumanTurn => t.kind === "human");
    expect(human.map((t) => t.attachments.map((a) => a.id))).toEqual([["att_b", "att_a"], ["att_q"], ["att_only"]]);
    // The retry folded into the first turn, which keeps its images.
    expect(human[0]!.directiveId).toBe("dir_2");
  });
});

describe("what happened to a talker's agent", () => {
  const people = { you: null, me: null, all: [], byId: new Map(), names: new Map(), refresh: async () => people, seen: () => false } as unknown as Parameters<typeof renderTurn>[4];
  const notes = (events: PersistedEvent[]) => project(events).turns.filter((t) => t.kind === "notice");

  test("a session replaced is a notice, rendered as dude's margin note", () => {
    const turns = notes([ev(EventTypes.SessionReplaced, { from: "ses_a", to: "ses_b", reason: "session/load failed" })]);
    expect(turns).toEqual([expect.objectContaining({ kind: "notice", notice: "notice", text: "The agent restarted without its earlier conversation." })]);
    const html = renderToStaticMarkup(createElement("div", null, renderTurn(turns[0]!, "brainstorm", 0, false, people, "El Duderino")));
    expect(html).toContain("The agent restarted without its earlier conversation.");
    expect(html).toContain('data-kind="notice"');
  });

  test("a compaction is a quiet notice that never shows the summary it kept", () => {
    const summary = "SECRET-LONG-SUMMARY ".repeat(500);
    const turns = notes([ev(EventTypes.ContextCompacted, { trigger: "auto", preTokens: 167012, postTokens: 9120, summary })]);
    expect(turns).toEqual([expect.objectContaining({ kind: "notice", notice: "notice", text: "The agent compacted its context." })]);
    const html = renderToStaticMarkup(createElement("div", null, renderTurn(turns[0]!, "conductor", 0, false, people, "El Duderino")));
    expect(html).toContain("The agent compacted its context.");
    expect(html).not.toContain("SECRET-LONG-SUMMARY");
  });

  test("a park after a stopped container or a failed turn says so, not that nobody wrote", () => {
    expect(notes([
      ev("run.parked", { reason: "session", stopped: "lost" }),
      ev("run.parked", { reason: "conductor", failedTurns: 1 }),
      ev("run.parked", { reason: "conductor" }),
    ]).map((t) => t.text)).toEqual([
      "Its container stopped: your next message resumes it.",
      "Parked after its turn failed: your next message resumes it.",
      expect.stringContaining("Parked while nobody is writing"),
    ]);
  });

  test("a failed turn that kept the agent says its turn failed, not the Run", () => {
    const { turns } = project([ev(EventTypes.RunFailed, { status: "failed", error: "the agent's turn failed: overloaded", kept: true })]);
    expect(turns).toEqual([expect.objectContaining({ kind: "ended", outcome: "failed", text: "Its turn failed: the agent's turn failed: overloaded" })]);
  });
});
