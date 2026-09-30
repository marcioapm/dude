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
import { summarize } from "../src/screens/RunScreen.tsx";
import { EventTypes } from "@dude/domain";
import type { PersistedEvent } from "@dude/domain";
import { actorName, apply, emptyProjection, humanActor, project, snapshot } from "../src/api/conversation.ts";

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
    expect(project(events)).toMatchObject({ costSource: { from: "harness", settled: false } });

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
    expect(c.costSource.from).toBe("harness");
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
