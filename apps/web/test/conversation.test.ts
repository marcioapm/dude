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
import { actorName, apply, emptyProjection, humanActor, landsHint, project, snapshot, steerWait, type HumanTurn } from "../src/api/conversation.ts";

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
  test("the question waits for an answer, and the answer is queued until the agent takes it", () => {
    const events = [
      ev(EventTypes.QuestionAsked, { kind: "agent", questionId: "qst_1", prompt: "Sort the table?", options: ["yes", "no"] }),
    ];
    let conversation = project(events);
    expect(conversation.openQuestion).toMatchObject({ questionId: "qst_1", text: "Sort the table?", options: ["yes", "no"] });
    expect(conversation.activity).toBeNull();

    events.push(ev(EventTypes.QuestionAnswered, { questionId: "qst_1", answer: "yes", directiveId: "dir_1" }));
    conversation = project(events);
    expect(conversation.openQuestion).toBeNull();
    expect(conversation.turns.map((t) => t.kind)).toEqual(["question", "human"]);
    expect(conversation.turns[1]).toMatchObject({ intent: "answer", text: "yes", deliveredAt: null });

    events.push(ev(EventTypes.DirectiveDelivered, { directiveId: "dir_1" }));
    expect(project(events).turns[1]).toMatchObject({ deliveredAt: events[2]!.occurredAt });
  });

  test("the question block leaves the message, which the question turn says instead", () => {
    const { turns } = project([
      ev(EventTypes.AgentMessage, { text: "I need a decision.\n\n```question\nSort?\n- yes\n```\n" }),
    ]);
    expect(turns[0]).toMatchObject({ kind: "message", text: "I need a decision." });
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
