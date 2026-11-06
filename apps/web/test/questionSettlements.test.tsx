import { expect, test } from "bun:test";
import { useMemo } from "react";
import { createRoot } from "react-dom/client";
import type { PersistedEvent, Run } from "@dude/domain";
import { act } from "./dom.ts";
import { ledgerQuestionIds, ledgerSettlements, settlementIndex, useLedgerSettlements, useSettlementIndex } from "../src/screens/questionSettlements.ts";
import { project } from "../src/api/conversation.ts";
import { TASK_ID, RUN_IMPLEMENT } from "../src/fixtures/data.ts";

const base = RUN_IMPLEMENT;
const run = (id: string, taskId = TASK_ID): Run => ({ ...base, id, taskId, role: "conductor", phase: null });
const event = (id: string, questionId: string, overrides: Partial<PersistedEvent> = {}): PersistedEvent => ({
  eventId: id, cursor: 2, eventType: "question.answered", occurredAt: "2026-10-02T10:00:02Z", organizationId: "org", projectId: "prj", taskId: TASK_ID,
  runId: "replacement", sessionId: null, workflowRunId: null, actor: { type: "person", id: "ben" }, source: "control-plane", correlationId: null, causationId: null,
  payload: { questionId, answers: [{ choices: [1], text: "" }], note: "original" }, ...overrides,
});
const ask = (i: number) => event(`ask${i}`, `q${i}`, { cursor: 1, eventType: "question.asked", runId: `r${i}`,
  payload: { kind: "agent", questionId: `q${i}`, prompt: "Which?", options: ["A", "B"] } });

test("10/20/40/100 ended ledgers read settlements once plus one bucket per ask, never R*S", () => {
  for (const n of [10, 20, 40, 100]) {
    let eventReads = 0;
    const events = Array.from({ length: n }, (_, i) => new Proxy(event(`a${i}`, `q${i}`), {
      get(target, key, receiver) { if (key === "eventType") eventReads++; return Reflect.get(target, key, receiver); },
    }));
    const index = settlementIndex(TASK_ID, [run("replacement")], events);
    let lookups = 0;
    const counted = { get: (id: string) => { lookups++; return index.get(id); } } as typeof index;
    for (let i = 0; i < n; i++) expect(ledgerSettlements(ledgerQuestionIds([ask(i)]), counted)).toEqual([events[i]!]);
    expect(eventReads).toBe(n);
    expect(lookups).toBe(n);
    expect(ledgerSettlements(ledgerQuestionIds([]), counted)).toEqual([]);
    expect(lookups).toBe(n);
    console.log({ ledgers: n, indexReads: eventReads, bucketLookups: lookups, oldScans: n * n });
  }
});

test("same-id corrections to actor, note, answers, timestamp and cursor invalidate only their bucket; removals are authoritative", () => {
  const runs = [run("replacement")];
  const original = event("answer", "q0");
  const other = event("other", "q1");
  const initial = settlementIndex(TASK_ID, runs, [original, other]);
  const reload = settlementIndex(TASK_ID, [...runs], structuredClone([original, other]), initial);
  expect(reload.get("q0")).toBe(initial.get("q0"));
  expect(reload.get("q1")).toBe(initial.get("q1"));
  for (const change of [
    { actor: { type: "person" as const, id: "updated-person" } },
    { payload: { ...original.payload, note: "corrected note" } },
    { payload: { ...original.payload, answers: [{ choices: [0], text: "" }] } },
    { occurredAt: "2026-10-02T10:01:00Z" },
    { cursor: 3 },
  ]) {
    const corrected = { ...original, ...change };
    const next = settlementIndex(TASK_ID, runs, [corrected, other], initial);
    expect(next.get("q0")).not.toBe(initial.get("q0"));
    expect(next.get("q0")).toEqual([corrected]);
    expect(next.get("q1")).toBe(initial.get("q1"));
  }
  expect(settlementIndex(TASK_ID, runs, [], initial).size).toBe(0);
  expect(settlementIndex(TASK_ID, [run("replacement", "another-task")], [original]).size).toBe(0);
  expect(settlementIndex(TASK_ID, [{ ...runs[0]!, role: "implementer", phase: "implement" }], [original]).size).toBe(0);
  expect(settlementIndex(TASK_ID, runs, [{ ...original, taskId: "another-task" }]).size).toBe(0);
  expect(ledgerSettlements(ledgerQuestionIds([ask(0), original]), initial)).toEqual([]);
});

test("mounted unrelated reloads retain projections, one corrected answer updates its old card once, task switch clears buckets", async () => {
  const container = document.createElement("div");
  document.body.appendChild(container);
  const root = createRoot(container);
  const ledgers = [[ask(0)], [ask(1)]];
  const counts = [0, 0];
  const references: unknown[] = [];
  function Ledger({ i, index }: { i: number; index: ReturnType<typeof useSettlementIndex> }) {
    const settlements = useLedgerSettlements(ledgers[i]!, index);
    const turns = useMemo(() => {
      counts[i]!++;
      return project([...ledgers[i]!, ...settlements].sort((a, b) => a.cursor - b.cursor), "completed").turns;
    }, [settlements]);
    references[i] = turns;
    return <div data-ledger={i}>{JSON.stringify(turns)}</div>;
  }
  function Page({ taskId, events, runs }: { taskId: string; events: PersistedEvent[]; runs: Run[] }) {
    const index = useSettlementIndex(taskId, runs, events);
    return <>{ledgers.map((_, i) => <Ledger key={i} i={i} index={index} />)}</>;
  }
  const answers = [event("a0", "q0"), event("a1", "q1")];
  const render = async (taskId: string, events: PersistedEvent[], runs = [run("replacement", taskId)]) => {
    await act(async () => root.render(<Page taskId={taskId} events={events} runs={runs} />));
  };
  try {
    await render(TASK_ID, answers);
    const before = [...references];
    expect(counts).toEqual([1, 1]);
    await render(TASK_ID, [...structuredClone(answers), event("unrelated", "none", { eventType: "agent.message" })]);
    expect(counts).toEqual([1, 1]);
    expect(references[0]).toBe(before[0]);
    expect(references[1]).toBe(before[1]);
    const corrected = { ...answers[0]!, actor: { type: "person" as const, id: "new-actor" }, occurredAt: "2026-10-02T10:01:00Z", cursor: 10,
      payload: { questionId: "q0", answers: [{ choices: [0], text: "" }], note: "new-note" } };
    await render(TASK_ID, [corrected, answers[1]!]);
    expect(counts).toEqual([2, 1]);
    expect(references[1]).toBe(before[1]);
    const text = container.querySelector('[data-ledger="0"]')!.textContent!;
    expect(text).toContain("new-actor");
    expect(text).toContain("new-note");
    expect(text).toContain('"choices":[0]');
    expect(text).toContain(corrected.occurredAt);
    expect(text.match(/"kind":"question"/g)).toHaveLength(1);
    await render("different-task", answers);
    expect(container.textContent).not.toContain("original");
    await render(TASK_ID, []);
    expect(container.textContent).not.toContain("new-note");
  } finally {
    await act(async () => root.unmount());
    container.remove();
  }
});
