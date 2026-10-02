/**
 * A task's Chat: the history line it opens on, the conductor's transcript
 * as the projection folds it (dude's briefing and notices, a person's
 * messages, the conductor's answers and questions), and the Chat tab in
 * each of its states — no conductor, live, parked, asking — mounted in
 * happy-dom against the fixture client.
 */

import { afterEach, describe, expect, test } from "bun:test";
import type { PersistedEvent, Run } from "@dude/domain";
import { act, mount, settle, until } from "./dom.ts";
import { FixtureClient, type LedgerQuery } from "../src/fixtures/client.ts";
import { FINDINGS, PULL_REQUEST, TASK_ID } from "../src/fixtures/data.ts";
import { PeopleProvider } from "../src/people.tsx";
import { TaskScreen } from "../src/screens/TaskScreen.tsx";
import { project } from "../src/api/conversation.ts";
import { taskHistory } from "../src/taskHistory.ts";
import { dudeName } from "../src/DudeMark.tsx";
import type { ChatSent, RunDetail, TaskDetail } from "../src/api/client.ts";

let mounted: Array<() => Promise<void>> = [];
afterEach(async () => {
  for (const unmount of mounted) await unmount();
  mounted = [];
});

const CONDUCTOR = "run_conductor01";
const at = (s: number) => new Date(Date.UTC(2026, 9, 2, 10, 0, s)).toISOString();
let cursor = 0;
function ev(eventType: string, payload: Record<string, unknown>, actor: PersistedEvent["actor"] = { type: "system", id: "dude" }, s = cursor): PersistedEvent {
  cursor++;
  return {
    eventId: `evt_${cursor}`, cursor, eventType, occurredAt: at(s), organizationId: "org", projectId: "prj", taskId: TASK_ID,
    runId: CONDUCTOR, sessionId: null, workflowRunId: null, actor, source: "orchestrator", correlationId: null, causationId: null, payload,
  };
}
const MARCIO = { type: "person", id: "u_marcio" } as const;
const BRIEFING = "Conductor, Márcio wrote in the Chat of WC-214.\n\n## The task\n\nWC-214 · tsk · status review\n\n## Márcio's message\n\nwhy 8s?";

/** A conductor that was briefed, answered, parked, and resumed for a second message. */
function conductorEvents(): PersistedEvent[] {
  cursor = 0;
  return [
    ev("run.created", { role: "conductor" }, MARCIO),
    ev("chat.message", { text: "why 8s?" }, MARCIO),
    ev("conductor.briefed", { text: BRIEFING }),
    ev("agent.prompt.delivered", { text: "echo Briefed on …", lands: "next_step" }),
    ev("agent.tool.called", { tool: "bash", callId: "c1", input: { command: "rg maxBackoff" } }, { type: "agent", id: CONDUCTOR }),
    ev("agent.tool.completed", { tool: "bash", callId: "c1", status: "completed" }, { type: "agent", id: CONDUCTOR }),
    ev("agent.message", { text: "8s: Tiago asked for it on the PR." }, { type: "agent", id: CONDUCTOR }),
    ev("run.parked", { reason: "conductor", message: "parked after its warm period" }),
    ev("chat.message", { text: "and POSTs?", directiveId: "dir_2" }, MARCIO),
    ev("run.unparked", { reason: "conductor", epoch: 2 }),
    ev("run.directive.delivered", { directiveId: "dir_2", read: true }),
    ev("agent.message", { text: "Only with an Idempotency-Key." }, { type: "agent", id: CONDUCTOR }),
  ];
}

describe("the history line", () => {
  const run = (p: Partial<Run>): Run => ({ id: "r", organizationId: "o", projectId: "p", taskId: TASK_ID, attempt: 1, status: "completed",
    workerId: null, workspacePath: null, error: null, kind: "agent", phase: "implement", role: "implementer", category: null,
    parentRunId: null, baseRefs: {}, heads: {}, branch: null, harness: null, model: null, dudePause: null,
    tokens: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, context: 0 }, machine: null, createdAt: at(0), startedAt: null, endedAt: null, ...p });

  test("a delivered task: its pipeline with fan-outs folded, its pull request, findings and cost", () => {
    const runs = [
      run({ id: "1", phase: "implement", createdAt: at(1) }),
      run({ id: "2", phase: "review", role: "reviewer", category: "correctness", createdAt: at(2) }),
      run({ id: "3", phase: "review", role: "reviewer", category: "security", createdAt: at(3) }),
      run({ id: "4", phase: "review", role: "reviewer", category: "api", createdAt: at(4) }),
      run({ id: "5", phase: "fix", createdAt: at(5) }),
      run({ id: "6", phase: "review", role: "reviewer", createdAt: at(6) }),
      run({ id: "7", phase: "simplify", role: "simplifier", createdAt: at(7) }),
      // The conductor is not a step of the pipeline.
      run({ id: "c", phase: null, role: "conductor", status: "running", createdAt: at(8) }),
    ];
    const line = taskHistory({ status: "done", runs }, FINDINGS.map((f) => ({ ...f, status: "resolved" as const })), [PULL_REQUEST], 9.8, (n) => `$${n.toFixed(2)}`);
    expect(line).toEqual({
      lead: "Delivered automatically, merged",
      steps: ["implementer", "reviewers ×3", "fixer", "reviewer", "simplifier", `PR #${PULL_REQUEST.number}`],
      facts: [`${FINDINGS.length} findings, all settled`, "$9.80"],
    });
  });

  test("a task not started says so, and nothing else", () => {
    expect(taskHistory({ status: "received", runs: [] }, [], [], null, String)).toEqual({ lead: "Not started", steps: [], facts: [] });
  });

  test("open findings are counted as open; a task at work says it is", () => {
    const line = taskHistory({ status: "running", runs: [run({ status: "running" })] }, [{ ...FINDINGS[0]!, status: "open" }], [], 0, String);
    expect(line.lead).toBe("Delivering automatically");
    expect(line.facts).toEqual(["1 finding, 1 open"]);
  });
});

describe("the conductor's transcript", () => {
  test("Márcio's message, then dude's briefing without it, the conductor's answer, a quiet park, the next message and answer", () => {
    const turns = project(conductorEvents(), "running").turns;
    expect(turns.map((t) => t.kind === "human" ? `human:${t.intent}:${t.text}` : t.kind === "prompt" ? `prompt:${t.briefing ? "briefing" : "task"}`
      : t.kind === "message" ? `message:${t.text}` : t.kind === "notice" ? `notice:${t.notice}` : t.kind)).toEqual([
      "human:message:why 8s?",
      "prompt:briefing",
      "tool",
      "message:8s: Tiago asked for it on the PR.",
      "notice:parked",
      "notice:unparked",
      "human:message:and POSTs?",
      "message:Only with an Idempotency-Key.",
    ]);
    const briefing = turns[1]!;
    // The message closing the briefing is Márcio's own turn, not said twice.
    expect(briefing.kind === "prompt" && briefing.text.endsWith("status review")).toBe(true);
    const parked = turns[4]!;
    expect(parked.kind === "notice" && parked.text).toBe("Parked while nobody is writing — nothing is held; your next message resumes it.");
    const second = turns[6]!;
    expect(second.kind === "human" && second.read && second.deliveredAt !== null).toBe(true);
  });
});

/** The fixture task, with a conductor (or none) and its events. */
class ChatClient extends FixtureClient {
  sent: string[] = [];
  constructor(private readonly conductor: Partial<Run> | null, private readonly extra: PersistedEvent[] = [], private readonly reply?: ChatSent) {
    super("d");
  }
  #conductorRun(): Run | null {
    return this.conductor ? { ...this.conductorBase(), ...this.conductor } : null;
  }
  conductorBase(): Run {
    return { id: CONDUCTOR, organizationId: "org", projectId: "prj", taskId: TASK_ID, attempt: 1, status: "running", workerId: null,
      workspacePath: null, error: null, kind: "agent", phase: null, role: "conductor", category: null, parentRunId: null, baseRefs: {},
      heads: {}, branch: null, harness: "scripted", model: "fake/scripted", dudePause: null,
      tokens: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, context: 0 },
      machine: { sizeId: "msz_s", name: "Small", cpus: 0.5, memoryMiB: 1024, diskGiB: 10, poolId: null, pool: null },
      createdAt: new Date().toISOString(), startedAt: new Date().toISOString(), endedAt: null };
  }
  override async getTask(id: string): Promise<TaskDetail> {
    const task = await super.getTask(id);
    const c = this.#conductorRun();
    return c ? { ...task, runs: [...task.runs, c] } : task;
  }
  override async getRun(id: string): Promise<RunDetail> {
    const c = this.#conductorRun();
    if (c && id === CONDUCTOR) return { ...c, sessions: [] } as unknown as RunDetail;
    return super.getRun(id);
  }
  protected override ledgerFor(params: LedgerQuery): PersistedEvent[] {
    if (params.runId === CONDUCTOR) return this.extra.filter((e) => e.cursor > (params.after ?? 0));
    return super.ledgerFor(params);
  }
  override async chat(taskId: string, text: string): Promise<ChatSent> {
    this.sent.push(`${taskId}:${text}`);
    return this.reply ?? { runId: CONDUCTOR, taskId, created: this.conductor === null };
  }
}

async function chatPage(client: ChatClient, props: Partial<Parameters<typeof TaskScreen>[0]> = {}) {
  const { container, unmount } = await mount(
    <PeopleProvider client={client}>
      <TaskScreen client={client} taskId={TASK_ID} onOpenRun={() => {}} onBack={() => {}} {...props} />
    </PeopleProvider>,
  );
  mounted.push(unmount);
  await until(() => container.querySelector("[data-testid=task-screen]"), "the task page");
  return container;
}

const tabs = (page: HTMLElement) => [...page.querySelectorAll("[role=tab]")].map((t) => t.textContent?.replace(/\d+$/, ""));
const selected = (page: HTMLElement) => page.querySelector("[role=tab][aria-selected=true]")?.textContent ?? "";

describe("the Chat tab", () => {
  test("is first; with no conductor the task opens on its Overview, and Chat shows the history and an empty composer", async () => {
    const client = new ChatClient(null);
    const page = await chatPage(client);
    expect(tabs(page)[0]).toBe("Chat");
    expect(selected(page)).toBe("Overview");

    const chat = [...page.querySelectorAll<HTMLElement>("[role=tab]")].find((t) => t.textContent === "Chat")!;
    await act(async () => {
      chat.dispatchEvent(new MouseEvent("mousedown", { bubbles: true, button: 0 }));
      chat.click();
    });
    const history = await until(() => page.querySelector("[data-testid=chat-history]"), "the history line");
    expect(history.textContent).toContain("Delivered automatically");
    expect(history.textContent).toContain("implementer → reviewer → fixer → reviewer → simplifier → PR #");
    const composer = page.querySelector<HTMLTextAreaElement>("[data-testid=task-chat] textarea")!;
    expect(composer.placeholder).toBe("Ask about this task…");
    expect(page.querySelector("[data-testid=composer-to]")?.textContent).toBe("To Conductor · read-only");

    // Sending goes to the task's Chat.
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(composer, "why 8s?");
      composer.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await act(async () => {
      composer.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    });
    await settle();
    expect(client.sent).toEqual([`${TASK_ID}:why 8s?`]);
  });

  test("with a live conductor the task opens on Chat: dude's briefing, the conductor's answer with its face, the history above", async () => {
    const page = await chatPage(new ChatClient({ status: "running" }, conductorEvents()));
    expect(selected(page)).toBe("Chat");
    const briefing = await until(() => page.querySelector("[data-testid=chat-briefing]"), "the briefing");
    expect(briefing.textContent).toContain(dudeName(TASK_ID));
    expect(briefing.textContent).toContain("Briefing");
    const answers = await until(() => {
      const all = page.querySelectorAll("[data-testid=conductor-turn]");
      return all.length === 2 ? all : null;
    }, "the conductor's answers");
    expect(answers[0]!.textContent).toContain("8s: Tiago asked for it on the PR.");
    expect(answers[0]!.getAttribute("data-role")).toBe("conductor");
    expect(page.querySelector("[data-testid=chat-history]")?.textContent).toContain("Delivered automatically");
    // dude signs its notices in Chat.
    const notices = [...page.querySelectorAll("[data-testid=chat-notice]")].map((n) => n.textContent);
    expect(notices.some((n) => n?.startsWith(`${dudeName(TASK_ID)}: Parked while nobody is writing`))).toBe(true);
    expect(page.querySelector<HTMLTextAreaElement>("[data-testid=task-chat] textarea")?.disabled).toBe(false);
  });

  test("a parked conductor still takes a message: the composer is open, and it goes to the task's Chat", async () => {
    const client = new ChatClient({ status: "paused", dudePause: "conductor" }, conductorEvents());
    const page = await chatPage(client);
    await until(() => page.querySelector("[data-testid=chat-briefing]"), "the briefing");
    const composer = page.querySelector<HTMLTextAreaElement>("[data-testid=task-chat] textarea")!;
    expect(composer.disabled).toBe(false);
    expect(composer.placeholder).toBe("Ask about this task…");
  });

  test("a conductor asking: its question card, and the composer answers it", async () => {
    const asking = [...conductorEvents(),
      ev("question.asked", { kind: "agent", questionId: "q_1", prompt: "Make it a follow-up task?", options: ["Yes", "No"] }, { type: "agent", id: CONDUCTOR })];
    const client = new ChatClient({ status: "running" }, asking);
    const page = await chatPage(client);
    await until(() => page.textContent?.includes("Make it a follow-up task?") ? page : null, "the question");
    const composer = await until(() => page.querySelector("[data-testid=task-chat] form[data-mode=answer]"), "the answer composer");
    expect(composer.textContent).toContain("Answering the conductor");
    const yes = [...composer.querySelectorAll("button")].find((b) => b.textContent === "Yes")!;
    await act(async () => yes.click());
    await settle();
    expect(client.sent).toEqual([`${TASK_ID}:Yes`]);
  });

  test("Sessions lists the conductor first", async () => {
    const page = await chatPage(new ChatClient({ status: "paused", dudePause: "conductor" }, conductorEvents()), { runId: CONDUCTOR });
    const list = await until(() => page.querySelector("[data-testid=sessions]"), "the sessions");
    expect(list.querySelector("li")?.textContent).toStartWith("Conductor");
  });
});
