import { expect, spyOn, test } from "bun:test";
import { createRoot } from "react-dom/client";
import type { PersistedEvent, Run } from "@dude/domain";
import { act } from "./dom.ts";
import { FixtureClient } from "../src/fixtures/client.ts";
import { TASK_ID, RUN_IMPLEMENT, YOU } from "../src/fixtures/data.ts";
import { PeopleProvider } from "../src/people.tsx";
import { ChatSection, type ChatSectionProps } from "../src/screens/ChatSection.tsx";
import { EndedLedgers } from "../src/screens/endedLedgers.ts";
import * as transcript from "../src/screens/transcriptTurns.tsx";
import * as selections from "../src/screens/questionSettlements.ts";
import * as conversation from "../src/api/conversation.ts";

test("production ChatSection keeps ended transcript work local across reloads, corrections and truth changes", async () => {
  const client = new FixtureClient("d");
  const base = await client.getTask(TASK_ID);
  const run = (id: string): Run => ({ ...RUN_IMPLEMENT, id, taskId: TASK_ID, role: "conductor", phase: null, attempt: 1, status: "failed" });
  const event = (id: string, runId: string, eventType: string, payload: Record<string, unknown>, cursor: number): PersistedEvent => ({
    eventId: id, runId, eventType, payload, cursor, taskId: TASK_ID, organizationId: "org", projectId: "prj", sessionId: null,
    workflowRunId: null, occurredAt: `2026-10-02T10:00:0${cursor}Z`, actor: { type: "person", id: "ben", name: "Ben Baker" } as PersistedEvent["actor"],
    source: "control-plane", correlationId: null, causationId: null,
  });
  const old = [0, 1].map((i) => [event(`ask${i}`, `r${i}`, "question.asked", { kind: "agent", questionId: `q${i}`, prompt: `Question ${i}?`, options: ["Keep", "Drop"] }, 1),
    event(`end${i}`, `r${i}`, "run.failed", { error: "lux no longer has this Run" }, 2)]);
  client.events = async ({ runId }) => ({ events: old[runId === "r0" ? 0 : 1]!, nextCursor: 2 });
  const ledgers = new EndedLedgers(client);
  await ledgers.read("r0", () => false);
  await ledgers.read("r1", () => false);
  const answers = [0, 1].map((i) => event(`answer${i}`, "replacement", "question.answered", { questionId: `q${i}`, answers: [{ choices: [0], text: "" }], note: "original" }, 3));
  const noop = () => {};
  let props: ChatSectionProps = {
    client, task: { ...base, runs: [run("r0"), run("r1"), run("replacement")], questions: [] }, conductorId: null,
    earlier: [{ id: "r0", status: "failed" }, { id: "r1", status: "failed" }], ledgers, findings: [], pullRequests: [], events: answers,
    owner: { owner: null }, version: 0, onSent: noop, onOpenRun: noop, onBack: noop, canSwitchDriver: false,
    driverControl: { busy: false, problem: null, confirmOpen: false, change: async () => {}, cancel: noop },
  };
  const work = [0, 0];
  const originalInterleaved = transcript.interleaved;
  const body = spyOn(transcript, "interleaved").mockImplementation((groups, lines) => {
    for (const turn of groups.flat()) if (turn.kind === "question") work[turn.questionId === "q0" ? 0 : 1]!++;
    return originalInterleaved(groups, lines);
  });
  const selector = spyOn(selections, "useLedgerSettlements");
  const projections = [0, 0];
  const originalProject = conversation.project;
  const projection = spyOn(conversation, "project").mockImplementation((events, ...args) => {
    for (const e of events) if (e.eventType === "question.asked") projections[e.payload.questionId === "q0" ? 0 : 1]!++;
    return originalProject(events, ...args);
  });
  const container = document.createElement("div");
  document.body.appendChild(container);
  const root = createRoot(container);
  const render = async () => { await act(async () => root.render(<PeopleProvider client={client}><ChatSection {...props} /></PeopleProvider>)); };
  const card = (i: number) => container.querySelector<HTMLElement>(`[data-question=q${i}]`)!;
  try {
    await render();
    const initial = [...work];
    const initialProjections = [...projections];
    expect(initial.every((n) => n > 0)).toBe(true);
    // This hook is called once per selector-wrapper render, including render-phase retries.
    const selected = selector.mock.calls.length;
    props = { ...props, task: { ...props.task, runs: structuredClone(props.task.runs) }, events: structuredClone(answers) };
    await render();
    expect(work).toEqual(initial);
    expect(projections).toEqual(initialProjections);
    expect(selector.mock.calls.length).toBe(selected);
    props = { ...props, events: [...structuredClone(answers), event("unrelated", "replacement", "agent.message", { text: "unrelated" }, 4)] };
    await render();
    expect(work).toEqual(initial);
    expect(projections).toEqual(initialProjections);
    expect(selector.mock.calls.length).toBe(selected);
    const corrected = { ...answers[0]!, actor: { type: "person", id: "ana", name: "Ana Updated" } as PersistedEvent["actor"],
      payload: { questionId: "q0", answers: [{ choices: [1], text: "" }], note: "corrected" } };
    props = { ...props, events: [corrected, answers[1]!] };
    await render();
    expect(work).toEqual([initial[0]! + 1, initial[1]!]);
    expect(projections).toEqual([initialProjections[0]! + 1, initialProjections[1]!]);
    expect(card(0).textContent).toContain("Ana");
    expect(card(0).textContent).toContain("Drop");
    expect(card(1).textContent).toContain("Ben");
    console.log({ productionBodyWork: { initial, corrected: [...work] }, projections: { initial: initialProjections, corrected: [...projections] }, unchangedReloadSelectorCalls: selected });
    props = { ...props, task: { ...props.task, id: "different-task" } };
    await render();
    expect(card(0).textContent).not.toContain("Ana");
    expect(card(1).textContent).not.toContain("Ben");
    props = { ...props, task: { ...props.task, id: TASK_ID }, events: [corrected, answers[1]!] };
    await render();
    expect(card(0).textContent).toContain("Ana");
    const beforeRemoval = [...work];
    props = { ...props, events: [] };
    await render();
    expect(work).toEqual(beforeRemoval.map((n) => n + 1));
    expect(card(0).getAttribute("data-state")).not.toBe("answered");
    expect(card(0).textContent).not.toContain("Ana");
    expect(card(1).textContent).not.toContain("Ben");
    const question = { id: "q0", runId: "replacement", status: "open" as const, addressee: "person" as const, prompt: "Question 0?", items: [], askedAt: old[0]![0]!.occurredAt, toPerson: null, askerError: null, endedBy: null };
    props = { ...props, task: { ...props.task, owner: { ...base.owner!, id: "other", name: "Other Owner" }, questions: [question] } };
    const beforeTruth = [...work];
    await render();
    expect(work).toEqual(beforeTruth);
    expect(card(0).textContent).toContain("Waiting for Other");
    expect(card(0).querySelectorAll("[role=radio]")).toHaveLength(0);
    let sent = "";
    client.chatAnswer = async (taskId) => { sent = taskId; return { taskId, runId: "replacement", created: false }; };
    props = { ...props, onSent: () => { sent += ":fresh"; }, task: { ...props.task, owner: { ...base.owner!, id: YOU, name: "You" } } };
    await render();
    expect(card(0).querySelectorAll("[role=radio]").length).toBeGreaterThan(0);
    await act(async () => card(0).querySelector<HTMLElement>("[role=radio]")!.click());
    expect(sent).toBe(`${TASK_ID}:fresh`);
    props = { ...props, task: { ...props.task, questions: [{ ...question, status: "cancelled", endedBy: null }] } };
    await render();
    expect(card(0).textContent).toContain("No longer needed");
    props = { ...props, task: { ...props.task, questions: [{ ...question, status: "cancelled", endedBy: null, askerError: "lux no longer has this Run" }] } };
    await render();
    expect(card(0).textContent).toContain("Not heard: dude lost the conductor that asked this");
    props = { ...props, task: { ...props.task, questions: [] } };
    await render();
    expect(card(0).textContent).toContain("No longer needed");
    expect(card(0).querySelectorAll("[role=radio]")).toHaveLength(0);
  } finally {
    await act(async () => root.unmount());
    container.remove();
    body.mockRestore();
    selector.mockRestore();
    projection.mockRestore();
  }
});
