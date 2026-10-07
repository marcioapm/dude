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
import { FINDINGS, METRICS, PULL_REQUEST, TASK_ID } from "../src/fixtures/data.ts";
import { PeopleProvider } from "../src/people.tsx";
import { TaskScreen } from "../src/screens/TaskScreen.tsx";
import { project } from "../src/api/conversation.ts";
import { taskHistory } from "../src/taskHistory.ts";
import { dudeName } from "../src/DudeMark.tsx";
import type { ChatSent, RunDetail, TaskDetail, TaskMetrics } from "../src/api/client.ts";
import { ApiError } from "../src/api/client.ts";

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

  test("a message handed on from an earlier conductor shows its images", () => {
    cursor = 0;
    const image = { id: "att_x", name: "x.png", contentType: "image/png", width: 1, height: 1, bytes: 1,
      original: { contentType: "image/png", width: 1, height: 1, bytes: 1 } };
    const turns = project([ev("chat.message", { text: "see", directiveId: "dir_x", attachments: [image] }, MARCIO)], "running").turns;
    expect(turns[0]?.kind === "human" && turns[0].attachments.map((a) => a.id)).toEqual(["att_x"]);
  });

  test("a message from a pull request is its GitHub login's; the conductor's reply there is the conductor's", () => {
    const turns = project(githubEvents(), "running").turns.filter((t) => t.kind === "human");
    const [asked, replied] = turns;
    expect(asked?.kind === "human" && [asked.by?.name, asked.conductor, asked.github?.url, asked.deliveredAt]).toEqual(
      ["alice (GitHub)", false, "https://github.test/acme/greeter/pull/3#issuecomment-9", at(1)]);
    expect(replied?.kind === "human" && [replied.conductor, replied.text, replied.github?.repo, replied.deliveredAt]).toEqual(
      [true, "Because the task says greet.", "greeter", at(2)]);
  });
});

/** A conductor asked on a pull request, which replied there. */
function githubEvents(): PersistedEvent[] {
  cursor = 200;
  return [
    ev("chat.message", { text: "@dude why greet()?", directiveId: "dir_gh", github: { login: "alice", repo: "greeter", number: 3,
      feedbackId: "issue-comment-9", kind: "comment", url: "https://github.test/acme/greeter/pull/3#issuecomment-9" } },
    { type: "integration", id: "github:alice" }, 0),
    ev("run.directive.delivered", { directiveId: "dir_gh", read: true }, undefined, 1),
    ev("chat.message", { text: "Because the task says greet.", by: "conductor", github: { repo: "greeter", number: 3,
      feedbackId: "issue-comment-10", url: "https://github.test/acme/greeter/pull/3#issuecomment-10", inReplyTo: "issue-comment-9" } },
    { type: "agent", id: CONDUCTOR }, 2),
  ];
}

describe("a pull request in Chat", () => {
  test("a mention is signed by its GitHub login and links to the comment; the conductor's reply links to where it went", async () => {
    const page = await chatPage(new ChatClient({ status: "running" }, [...conductorEvents(), ...githubEvents()]));
    const turns = await until(() => {
      const t = [...page.querySelectorAll<HTMLElement>("[data-testid=human-turn][data-by]")];
      return t.length === 2 ? t : null;
    }, "the two GitHub turns");
    const [asked, replied] = turns;
    expect(asked!.getAttribute("data-by")).toBe("github");
    expect(asked!.textContent).toContain("alice (GitHub)");
    expect(asked!.textContent).toContain("@dude why greet()?");
    expect(asked!.querySelector("[data-testid=github-source] a")?.getAttribute("href")).toBe("https://github.test/acme/greeter/pull/3#issuecomment-9");
    expect(asked!.querySelector("[data-testid=github-source]")?.textContent).toBe("On greeter#3 on GitHub");
    expect(replied!.getAttribute("data-by")).toBe("conductor");
    expect(replied!.textContent).toContain("Conductor");
    expect(replied!.querySelector("[data-testid=github-source]")?.textContent).toBe("Replied on greeter#3 on GitHub");
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
    if (this.refuse) throw this.refuse;
    return this.reply ?? { runId: CONDUCTOR, taskId, created: this.conductor === null };
  }
  /** What the next sends fail with; null: they go. */
  refuse: Error | null = null;
}

/** Types `text` into the task Chat's composer and presses Enter; returns the composer. */
async function write(page: HTMLElement, text: string): Promise<HTMLTextAreaElement> {
  const composer = await until(() => page.querySelector<HTMLTextAreaElement>("[data-testid=task-chat] textarea"), "the composer");
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(composer, text);
    composer.dispatchEvent(new Event("input", { bubbles: true }));
  });
  await act(async () => {
    composer.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
  });
  await settle();
  return composer;
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

const pickTab = async (page: HTMLElement, name: string) => {
  const tab = [...page.querySelectorAll<HTMLElement>("[role=tab]")].find((t) => t.textContent?.replace(/\d+$/, "") === name)!;
  await act(async () => {
    tab.dispatchEvent(new MouseEvent("mousedown", { bubbles: true, button: 0 }));
    tab.click();
  });
  await settle();
};

describe("the Chat tab", () => {
  test("is first; with no conductor the task opens on its Overview, and Chat shows the history and an empty composer", async () => {
    const client = new ChatClient(null);
    const page = await chatPage(client);
    expect(tabs(page)[0]).toBe("Chat");
    expect(selected(page)).toBe("Overview");

    await pickTab(page, "Chat");
    const history = await until(() => page.querySelector("[data-testid=chat-history]"), "the history line");
    expect(history.textContent).toContain("Delivered automatically");
    expect(history.textContent).toContain("implementer → reviewer → fixer → reviewer → simplifier → PR #");
    const composer = page.querySelector<HTMLTextAreaElement>("[data-testid=task-chat] textarea")!;
    expect(composer.placeholder).toBe("Ask about this task…");
    // Deliver is still delivering it: the first message hands the decisions over, so the composer is not read-only.
    expect(page.querySelector("[data-testid=composer-to]")?.textContent).toBe("To Conductor");
    expect(page.querySelector("[data-testid=task-chat]")?.textContent).toContain("hands this delivery's decisions to its conductor");

    // Sending goes to the task's Chat.
    await write(page, "why 8s?");
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

  test("a parked conductor still takes a message: the composer is open, and Enter sends it to the task's Chat", async () => {
    // Its ledger ends at the park: nothing has woken it.
    const parkedAt = conductorEvents().findIndex((e) => e.eventType === "run.parked");
    const client = new ChatClient({ status: "paused", dudePause: "conductor" }, conductorEvents().slice(0, parkedAt + 1));
    const page = await chatPage(client);
    await until(() => page.querySelector("[data-testid=chat-briefing]"), "the briefing");
    const composer = page.querySelector<HTMLTextAreaElement>("[data-testid=task-chat] textarea")!;
    expect(composer.disabled).toBe(false);
    expect(composer.placeholder).toBe("Ask about this task…");
    await write(page, "and the tests?");
    expect(client.sent).toEqual([`${TASK_ID}:and the tests?`]);
    expect(composer.value).toBe("");
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

  test("the conductor's escalation question, decided on the banner: its card says so, and the composer no longer answers it", async () => {
    const decided = [...conductorEvents(),
      ev("question.asked", { kind: "agent", questionId: "q_e", prompt: "Stuck: retry once more?", options: ["Retry", "Stop"] }, { type: "agent", id: CONDUCTOR }),
      ev("question.closed", { questionId: "q_e", by: "decision", action: "retry" })];
    const page = await chatPage(new ChatClient({ status: "running" }, decided));
    await until(() => page.textContent?.includes("Stuck: retry once more?") ? page : null, "the question");
    const card = page.querySelector("[data-testid=task-chat] article[data-state]")!;
    expect(card.getAttribute("data-state")).toBe("dismissed");
    expect(card.querySelector("[data-testid=settled-by]")?.textContent).toBe("Decided on the banner");
    expect(page.querySelector("[data-testid=task-chat] form[data-mode=answer]")).toBeNull();
  });

  test("Sessions lists the conductor first", async () => {
    const page = await chatPage(new ChatClient({ status: "paused", dudePause: "conductor" }, conductorEvents()), { runId: CONDUCTOR });
    const list = await until(() => page.querySelector("[data-testid=sessions]"), "the sessions");
    expect(list.querySelector("li")?.textContent).toStartWith("Conductor");
  });

  test("a send that fails keeps the words and says why; sent again, it goes and the composer clears", async () => {
    for (const conductor of [null, { status: "running" as const }]) {
      const client = new ChatClient(conductor, conductor ? conductorEvents() : []);
      client.refuse = new ApiError(500, "internal", "the orchestrator is down");
      const page = await chatPage(client, conductor ? {} : { tab: "chat" });
      if (conductor) await until(() => page.querySelector("[data-testid=chat-briefing]"), "the briefing");
      const composer = await write(page, "and the tests?");
      expect(client.sent).toEqual([`${TASK_ID}:and the tests?`]);
      expect(composer.value).toBe("and the tests?");
      expect(page.textContent).toContain("Could not send the message: the orchestrator is down");

      client.refuse = null;
      await act(async () => {
        composer.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
      });
      await settle();
      expect(client.sent).toEqual([`${TASK_ID}:and the tests?`, `${TASK_ID}:and the tests?`]);
      expect(composer.value).toBe("");
    }
  });

  test("the rail's Cost is the conductor's whole cost: its tokens and its machine time", async () => {
    class Priced extends ChatClient {
      override async taskMetrics(): Promise<TaskMetrics> {
        const conductor = { id: CONDUCTOR, phase: null, role: "conductor", category: null, status: "running", activeMs: 600_000, parkedMs: 0,
          costUsd: 0.5, cost: { totalUsd: 0.54, tokensUsd: 0.5, machineUsd: 0.04, origin: { tokens: "lux" as const, machine: "estimate" as const, settled: false } },
          tokens: { input: 40_000, output: 2_000 } };
        return { ...METRICS, runs: [...METRICS.runs, conductor] };
      }
    }
    const page = await chatPage(new Priced({ status: "running" }, conductorEvents()));
    const rail = await until(() => page.querySelector("[data-testid=chat-rail]"), "the rail");
    const cost = await until(() => rail.querySelector("[aria-label^='$0.54']"), "the conductor's total cost");
    expect(cost.getAttribute("aria-label")).toContain("machine time $0.04");
    expect(cost.getAttribute("aria-label")).toContain("model tokens $0.50");
  });

  test("a conductor that ended stays in Chat above the next, read-only; only the latest takes input", async () => {
    const EARLIER = "run_conductor00";
    cursor = 100;
    const ended = [
      ev("chat.message", { text: "who asked for 8s?" }, MARCIO),
      ev("conductor.briefed", { text: BRIEFING.replace("why 8s?", "who asked for 8s?") }),
      ev("agent.message", { text: "Tiago, on the PR." }, { type: "agent", id: EARLIER }),
      ev("chat.message", { text: "and when?", directiveId: "dir_lost" }, MARCIO),
      ev("run.directive.failed", { directiveId: "dir_lost", error: `the conductor stopped before reading it; the next conductor, ${CONDUCTOR}, has it` }),
      ev("run.completed", { status: "completed", reason: "its container stopped" }),
    ].map((e) => ({ ...e, runId: EARLIER }));
    class TwoConductors extends ChatClient {
      override async getTask(id: string): Promise<TaskDetail> {
        const task = await super.getTask(id);
        const first = { ...this.conductorBase(), id: EARLIER, status: "completed" as const, createdAt: "2026-10-02T09:00:00.000Z" };
        return { ...task, runs: [...task.runs, first] };
      }
      protected override ledgerFor(params: LedgerQuery): PersistedEvent[] {
        if (params.runId === EARLIER) return ended.filter((e) => e.cursor > (params.after ?? 0));
        return super.ledgerFor(params);
      }
    }
    const page = await chatPage(new TwoConductors({ status: "running" }, conductorEvents()));
    const earlier = await until(() => page.querySelector("[data-testid=earlier-conductor]"), "the ended conductor's conversation");
    expect(earlier.getAttribute("data-run")).toBe(EARLIER);
    await until(() => page.querySelectorAll("[data-testid=conductor-turn]").length === 3 ? page : null, "every answer");
    const answers = [...page.querySelectorAll("[data-testid=conductor-turn]")].map((a) => a.textContent ?? "");
    expect(answers[0]).toContain("Tiago, on the PR.");
    expect(answers[1]).toContain("8s: Tiago asked for it on the PR.");
    // The message it never read says where it went.
    expect(earlier.textContent).toContain(`the next conductor, ${CONDUCTOR}, has it`);
    // In order: the ended conversation, then the next one's briefing.
    const briefings = [...page.querySelectorAll("[data-testid=chat-briefing]")];
    expect(briefings.length).toBe(2);
    expect(earlier.contains(briefings[0]!)).toBe(true);
    expect(earlier.compareDocumentPosition(briefings[1]!) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    // One composer, the latest's.
    expect(page.querySelectorAll("[data-testid=task-chat] textarea").length).toBe(1);
  });
});

describe("an earlier conductor's conversation", () => {
  const EARLIER = "run_conductor00";
  const IMAGE = { id: "att_layout", name: "layout.png", contentType: "image/png", width: 10, height: 10, bytes: 4,
    original: { contentType: "image/png", width: 20, height: 20, bytes: 8 } };
  /** An earlier conductor that read a steer with an image, and ended. */
  function endedEvents(): PersistedEvent[] {
    cursor = 100;
    return [
      ev("chat.message", { text: "who asked for 8s?" }, MARCIO),
      ev("conductor.briefed", { text: BRIEFING.replace("why 8s?", "who asked for 8s?") }),
      ev("agent.message", { text: "Tiago, on the PR." }, { type: "agent", id: EARLIER }),
      ev("run.steered", { text: "see the layout", directiveId: "dir_image", attachments: [IMAGE] }, MARCIO),
      ev("run.directive.delivered", { directiveId: "dir_image", read: true }),
      ev("agent.message", { text: "The header overflows." }, { type: "agent", id: EARLIER }),
      ev("run.completed", { status: "completed", reason: "its container stopped" }),
    ].map((e) => ({ ...e, runId: EARLIER }));
  }
  /** Two conductors, the earlier ended; every read of the earlier's ledger counted, and failing while `failing`. */
  class TwoConductors extends ChatClient {
    reads: Array<number> = [];
    failing = false;
    /** When set, the earlier's next read waits on it. */
    hold: Promise<void> | null = null;
    constructor(private readonly earlierEvents: PersistedEvent[] = endedEvents()) {
      super({ status: "running" }, conductorEvents());
    }
    override async getTask(id: string): Promise<TaskDetail> {
      const task = await super.getTask(id);
      const first = { ...this.conductorBase(), id: EARLIER, status: "completed" as const, createdAt: "2026-10-02T09:00:00.000Z" };
      return { ...task, runs: [...task.runs, first] };
    }
    override async events(params: LedgerQuery & { limit?: number }) {
      if (params.runId !== EARLIER) return super.events(params);
      this.reads.push(params.after ?? 0);
      if (this.hold) await this.hold;
      if (this.failing) throw new ApiError(503, "unavailable", "the ledger is unreachable");
      const events = this.earlierEvents.filter((e) => e.cursor > (params.after ?? 0)).slice(0, params.limit ?? 1000);
      return { events, nextCursor: events.at(-1)?.cursor ?? params.after ?? 0 };
    }
    override async attachment(): Promise<Blob> {
      return new Blob([new Uint8Array([0x89, 0x50, 0x4e, 0x47])], { type: "image/png" });
    }
  }
  test("shows its images, and opens them in the viewer, as the live one's", async () => {
    const page = await chatPage(new TwoConductors());
    const earlier = await until(() => page.querySelector<HTMLElement>("[data-testid=earlier-conductor]"), "the earlier conversation");
    const thumbnail = await until(() => earlier.querySelector<HTMLButtonElement>("[data-testid=message-image]"), "the image's thumbnail");
    expect(thumbnail.getAttribute("aria-label")).toBe("Open layout.png");
    await act(async () => thumbnail.click());
    const viewer = await until(() => document.querySelector<HTMLElement>("[data-testid=image-viewer]"), "the viewer");
    expect(viewer.textContent).toContain("layout.png");
    expect(viewer.textContent).toContain("steer to Conductor");
  });

  test("a read that failed says so with a retry, never an empty conversation; Retry reads it", async () => {
    const client = new TwoConductors();
    client.failing = true;
    const page = await chatPage(client);
    const failed = await until(() => page.querySelector<HTMLElement>("[data-testid=earlier-conductor-failed]"), "the failed read");
    expect(failed.textContent).toContain("the ledger is unreachable");
    expect(page.querySelector("[data-testid=earlier-conductor-ended]")).toBeNull();
    client.failing = false;
    await act(async () => failed.querySelector<HTMLButtonElement>("[data-testid=earlier-conductor-retry]")!.click());
    const earlier = await until(() => page.querySelector<HTMLElement>("[data-testid=earlier-conductor-ended]")?.parentElement, "the conversation, read");
    expect(earlier.textContent).toContain("Tiago, on the PR.");
    expect(page.querySelector("[data-testid=earlier-conductor-failed]")).toBeNull();
  });

  test("leaving Chat while a page is on its way reads no more pages", async () => {
    // A full first page, so it would read on.
    cursor = 100;
    const long = Array.from({ length: 1000 }, (_, i) => ({ ...ev("agent.thought", { text: `t${i}` }), runId: EARLIER }));
    const client = new TwoConductors(long);
    let release: () => void = () => {};
    client.hold = new Promise((r) => (release = r));
    const page = await chatPage(client);
    await until(() => client.reads.length === 1 ? page : null, "the first page asked for");
    await pickTab(page, "Overview");
    expect(page.querySelector("[data-testid=earlier-conductor]")).toBeNull();
    client.hold = null;
    await act(async () => release());
    await settle();
    expect(client.reads).toEqual([0]);
  });

  test("coming back to Chat does not read an ended conductor's ledger again", async () => {
    const client = new TwoConductors();
    const page = await chatPage(client);
    await until(() => page.querySelector("[data-testid=earlier-conductor-ended]"), "the earlier conversation");
    expect(client.reads).toEqual([0]);
    await pickTab(page, "Overview");
    await pickTab(page, "Chat");
    await until(() => page.querySelector("[data-testid=earlier-conductor-ended]"), "the earlier conversation again");
    expect(page.querySelector("[data-testid=earlier-conductor]")?.textContent).toContain("The header overflows.");
    expect(client.reads).toEqual([0]);
  });

  test("a ledger that may still hear its hand-over is read again from where it stopped", async () => {
    // Failed, with a message it never read: the sweep's failed-delivery line is still to come.
    cursor = 100;
    const unsettled = [
      ev("chat.message", { text: "who asked for 8s?" }, MARCIO),
      ev("agent.message", { text: "Tiago, on the PR." }, { type: "agent", id: EARLIER }),
      ev("chat.message", { text: "and when?", directiveId: "dir_lost" }, MARCIO),
      ev("run.failed", { status: "failed", error: "host lost" }),
    ].map((e) => ({ ...e, runId: EARLIER }));
    const client = new TwoConductors(unsettled);
    const page = await chatPage(client);
    await until(() => page.querySelector("[data-testid=earlier-conductor-ended]"), "the earlier conversation");
    const last = unsettled.at(-1)!.cursor;
    unsettled.push({ ...ev("run.directive.failed", { directiveId: "dir_lost", error: `the next conductor, ${CONDUCTOR}, has it` }),
      runId: EARLIER, cursor: last + 1 });
    await pickTab(page, "Overview");
    await pickTab(page, "Chat");
    await until(() => page.querySelector("[data-testid=earlier-conductor]")?.textContent?.includes(`the next conductor, ${CONDUCTOR}, has it`) ? page : null,
      "the hand-over's line");
    expect(client.reads).toEqual([0, last]);
    // Settled now: final.
    await pickTab(page, "Overview");
    await pickTab(page, "Chat");
    await until(() => page.querySelector("[data-testid=earlier-conductor-ended]"), "the earlier conversation again");
    expect(client.reads).toEqual([0, last]);
  });
});
