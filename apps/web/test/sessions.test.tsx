/**
 * A brainstorm session's page, its inbox lines and its URL: what each
 * member sees and may do, driven through the screens with the fixture
 * stream and a client that answers as the API does.
 */

import { afterEach, describe, expect, test } from "bun:test";
import type { PersistedEvent, SessionDetail, SessionsList } from "@dude/domain";
import { act, click, mount, settle, type, until } from "./dom.ts";
import { FixtureClient, emit, type LedgerQuery } from "../src/fixtures/client.ts";
import { PEOPLE, YOU } from "../src/fixtures/data.ts";
import { PeopleProvider } from "../src/people.tsx";
import { SessionScreen, sessionNotice } from "../src/screens/SessionScreen.tsx";
import { InboxScreen } from "../src/screens/InboxScreen.tsx";
import { WelcomeScreen } from "../src/screens/WelcomeScreen.tsx";
import { formatPlace, parsePlace } from "../src/place.ts";
import type { FileResult } from "@dude/domain";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import type { Artifact, SentAnswer } from "../src/api/client.ts";
import { App } from "../src/App.tsx";

let mounted: Array<() => Promise<void>> = [];
afterEach(async () => {
  for (const unmount of mounted) await unmount();
  mounted = [];
  localStorage.clear();
});

const SESSION = "ssn_billing";
const RUN = "run_brainstorm1";
const ANA = PEOPLE.find((p) => p.id !== YOU)!;
const ME = PEOPLE.find((p) => p.id === YOU)!;
const ref = (p: { id: string; name: string }) => ({ id: p.id, name: p.name, photoUrl: null, online: true });
const at = (s: number) => new Date(Date.UTC(2026, 9, 7, 11, 20, s)).toISOString();

let cursor = 0;
function ev(eventType: string, payload: Record<string, unknown>, actor: PersistedEvent["actor"] = { type: "system", id: "dude" }): PersistedEvent {
  cursor += 1;
  return { eventId: `evt_s${cursor}`, cursor, eventType, occurredAt: at(cursor), organizationId: "org_1", projectId: null as unknown as string,
    taskId: null as unknown as string, runId: RUN, sessionId: SESSION, workflowRunId: null, actor, source: "orchestrator",
    correlationId: null, causationId: null, payload };
}

function detail(role: "owner" | "chat" | "read", over: Partial<SessionDetail> = {}): SessionDetail {
  const owner = role === "owner" ? ME : ANA;
  const other = role === "owner" ? ANA : ME;
  return {
    session: {
      id: SESSION, title: "Usage-based billing", titledBy: "agent", createdAt: at(0),
      people: [
        { person: ref(owner), role: "owner", accepted: true, becomesOwner: false, open: true },
        { person: ref(other), role: role === "owner" ? "chat" : role, accepted: true, becomesOwner: false, open: false },
      ],
      projects: [{ id: "prj_bl", key: "BL", name: "billing", repositories: [{ id: "repo_bl", name: "billing", defaultBranch: "main" }] }],
      run: { id: RUN, status: "running", dudePause: null, model: "claude-opus-5-5", modelTier: "small", machine: null, waiting: false },
      runs: [RUN], costUsd: 0.71, messages: 2,
    },
    you: { id: YOU, role, archived: false },
    proposals: [],
    question: null,
    ...over,
  };
}

class SessionClient extends FixtureClient {
  constructor(public detail: SessionDetail, public ledger: PersistedEvent[] = []) {
    super("a");
  }
  sent: string[] = [];
  renamed: string[] = [];
  filed: number[][] = [];
  fileResult: FileResult | null = null;
  override getSession(): Promise<SessionDetail> {
    return Promise.resolve(this.detail);
  }
  override renameSession(_id: string, title: string) {
    this.renamed.push(title);
    return Promise.resolve({ id: SESSION, title });
  }
  protected override ledgerFor(q: LedgerQuery): PersistedEvent[] {
    if (q.sessionId !== SESSION) return super.ledgerFor(q);
    return this.ledger.filter((e) => e.cursor > (q.after ?? 0));
  }
  override sessionChat(_id: string, text: string, opts: { aside?: boolean } = {}) {
    this.sent.push(`${opts.aside ? "aside:" : ""}${text}`);
    return Promise.resolve({ runId: RUN, created: false });
  }
  override sessionAnswer(_id: string, questionId: string, answers: ReadonlyArray<SentAnswer>, note = "") {
    this.sent.push(`${questionId}:${JSON.stringify(answers)}${note ? `:${note}` : ""}`);
    return Promise.resolve({ id: questionId, status: "answered" as const });
  }
  override fileProposal(_id: string, _proposal: string, items: number[]): Promise<FileResult> {
    this.filed.push(items);
    return Promise.resolve(this.fileResult ?? { results: items.map((item) => ({ item, status: "filed" as const, key: `BL-${60 + item}` })) });
  }
}

async function sessionPage(client: SessionClient) {
  const { container, unmount } = await mount(
    <PeopleProvider client={client}>
      <ToastProvider>
        <SessionScreen client={client} sessionId={SESSION} projects={[]} onBack={() => {}} onChanged={() => {}} onArchived={() => {}} />
      </ToastProvider>
    </PeopleProvider>,
  );
  mounted.push(unmount);
  await until(() => container.querySelector("[data-testid=session-screen]"), "the session page");
  return container;
}

/** The whole shell at `hash`, as a browser opens it. */
async function shell(hash: string, client: FixtureClient) {
  window.history.replaceState(null, "", hash);
  const { container, unmount } = await mount(
    <TooltipProvider>
      <ToastProvider>
        <PeopleProvider client={client}>
          <App client={client} onSignOut={() => {}} onKeyRefused={() => {}} />
        </PeopleProvider>
      </ToastProvider>
    </TooltipProvider>,
  );
  mounted.push(async () => {
    await unmount();
    window.history.replaceState(null, "", " ");
  });
  return container;
}

async function write(page: HTMLElement, text: string) {
  const composer = await until(() => page.querySelector<HTMLTextAreaElement>("[data-testid=session-screen] textarea"), "the composer");
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(composer, text);
    composer.dispatchEvent(new Event("input", { bubbles: true }));
  });
  await act(async () => {
    composer.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
  });
  await settle();
}

describe("a brainstorm session's page", () => {
  test("a stopped turn is visible to readers with its tool and error kind", async () => {
    const client = new SessionClient(detail("read"), [
      ev("session.turn_stopped", { runId: RUN, tool: "bash", openSecs: 660, directiveId: "dir_stopped" }),
    ]);
    const page = await sessionPage(client);
    await settle();
    const notice = page.querySelector('[data-kind="stopped"]');
    expect(notice).not.toBeNull();
    expect(notice!.textContent).toContain("Stopped Brainstorm's turn: bash was open for 10\u00a0min.");
    expect(notice!.querySelector('[data-icon="warning"]')).not.toBeNull();
  });

  test("its conversation: each member's message signed, dude's briefing without the first message, the agent's answer", async () => {
    const client = new SessionClient(detail("owner"), [
      ev("chat.message", { text: "Can the meter take a run event?" }, { type: "human", id: YOU }),
      ev("session.briefed", { text: "Brainstorm, this is the session.\n\n## People\n- Márcio\n\n## The first message\n\nMárcio: Can the meter take a run event?" }),
      ev("chat.message", { text: "It can, but keys expire after 24h", directiveId: "dir_1" }, { type: "human", id: ANA.id }),
      ev("agent.message", { text: "Ana, right: the rollup has to dedupe on run id." }, { type: "agent", id: RUN }),
    ]);
    const page = await sessionPage(client);
    await until(() => page.querySelectorAll("[data-testid=human-turn]").length === 2 || null, "both members' messages");
    const humans = [...page.querySelectorAll("[data-testid=human-turn]")].map((n) => n.textContent ?? "");
    expect(humans[0]).toContain("Can the meter take a run event?");
    expect(humans[1]).toContain(ANA.name.split(" ")[0]!);
    const briefing = page.querySelector("[data-testid=chat-briefing]")!.textContent ?? "";
    expect(briefing).toContain("## People".replace("## ", ""));
    expect(briefing).not.toContain("The first message");
    expect(page.textContent).toContain("the rollup has to dedupe on run id");
    // The owner shares and links; the rail lists both members and what it reads.
    expect(page.querySelector("[data-testid=share-open]")).not.toBeNull();
    expect(page.querySelector("[data-testid=link-open]")).not.toBeNull();
    expect(page.querySelector("[data-testid=session-rail]")!.textContent).toContain("billing");
  });

  test("the agent's turn over, nothing is thinking: its closing totals and its end clear it", async () => {
    const agent = { type: "agent", id: RUN } as const;
    const turn = [
      ev("chat.message", { text: "where does metering go?" }, { type: "human", id: YOU }),
      ev("session.briefed", { text: "Brainstorm, this is the session.\n\n## The first message\n\nMárcio: where does metering go?" }),
      ev("agent.prompt.delivered", { text: "Brainstorm, this is the session." }, agent),
      ev("agent.tool.called", { tool: "list_tasks", callId: "c1" }, agent),
      ev("agent.tool.completed", { tool: "list_tasks", callId: "c1", status: "completed" }, agent),
      ev("agent.model.request.completed", { tokens: { input: 10, output: 5 } }, agent),
    ];
    const thinking = await sessionPage(new SessionClient(detail("owner"), turn));
    await until(() => thinking.querySelector("[data-testid=session-screen] [data-activity=thinking]"), "thinking mid-turn");
    const ended = [...turn,
      ev("agent.message", { text: "In the meter's rollup." }, agent),
      ev("agent.model.request.completed", { turn: true, tokens: { input: 12, output: 7 } }, agent),
      ev("agent.session.stopped", { reason: "turn_complete" }, agent),
    ];
    const page = await sessionPage(new SessionClient(detail("owner"), ended));
    await until(() => (page.textContent ?? "").includes("In the meter's rollup.") || null, "the answer");
    await settle();
    // A count, not toBeNull: bun's toBeNull passes a happy-dom element.
    expect(page.querySelectorAll("[data-testid=session-screen] [data-activity]").length).toBe(0);
  });

  // The orchestrator's record of a turn dude stopped for a call open 10
  // minutes: the call never completes; the nudge is delivered; the agent
  // answers in a turn whose totals and end follow (as the fake lux plays
  // it), or the turn just ends. Either way the page shows the notice and
  // nothing still running or thinking.
  test("a turn dude stopped ends idle and keeps its notice", async () => {
    const agent = { type: "agent", id: RUN } as const;
    const stuck = [
      ev("chat.message", { text: "hello" }, { type: "human", id: YOU }),
      ev("agent.prompt.delivered", { text: "hello" }),
      ev("agent.tool.called", { tool: "task", callId: "open_0", input: { description: "Review worker state behavior" } }, agent),
      ev("session.turn_stopped", { runId: RUN, tool: "task", openSecs: 660, directiveId: "dir_stuck" }),
      ev("run.directive.accepted", { directiveId: "dir_stuck", lands: "next_step", receipt: true }),
      ev("run.directive.delivered", { directiveId: "dir_stuck", read: true }),
    ];
    const answered = [...stuck,
      ev("agent.model.request.completed", { costUsd: 0.01, contextTokens: 1000, contextWindow: 200000 }, agent),
      ev("agent.message", { text: "I was reading the worker through a sub-agent and it got stuck." }, agent),
      ev("agent.model.request.completed", { turn: true, tokens: { input: 12, output: 34 }, contextTokens: 1000 }, agent),
      ev("agent.session.stopped", { reason: "turn_complete" }, agent),
    ];
    const ended = [...stuck, ev("agent.session.stopped", { reason: "turn_complete" }, agent)];
    for (const [name, events] of [["answered", answered], ["ended", ended]] as const) {
      const page = await sessionPage(new SessionClient(detail("owner"), [...events]));
      await until(() => page.querySelector('[data-kind="stopped"]'), `${name}: the stopped notice`);
      await settle();
      expect(page.querySelector('[data-kind="stopped"]')!.textContent).toContain("Stopped Brainstorm's turn: task was open for 10\u00a0min.");
      expect(`${name}: ${page.querySelectorAll("[data-testid=session-screen] [data-activity]").length}`).toBe(`${name}: 0`);
    }
  });

  test("a message goes to the session's chat", async () => {
    const placeholder = async (d: SessionDetail) =>
      (await sessionPage(new SessionClient(d))).querySelector<HTMLTextAreaElement>("[data-testid=session-screen] textarea")!.placeholder;
    // A session has no task: its composer says who it writes to, not the task Chat's words.
    expect(await placeholder(detail("chat"))).toBe("Message the brainstorm…");
    expect(await placeholder(detail("owner"))).not.toContain("task");
    const client = new SessionClient(detail("chat"));
    const page = await sessionPage(client);
    await write(page, "also, can the panel show cost estimates?");
    expect(client.sent).toEqual(["also, can the panel show cost estimates?"]);
    // Only the owner shares or links.
    expect(page.querySelector("[data-testid=share-open]")).toBeNull();
    expect(page.querySelector("[data-testid=link-open]")).toBeNull();
  });

  test("a reader's composer is closed, with why, and the card files nothing", async () => {
    const client = new SessionClient(detail("read", {
      proposals: [{ id: "prp_1", runId: RUN, createdAt: at(30), items: [{ kind: "task", project: "BL", title: "Dedupe on run id", goal: "g" }],
        status: [{ canFile: false, why: "You can read this session" }] }],
    }));
    const page = await sessionPage(client);
    const composer = page.querySelector<HTMLTextAreaElement>("[data-testid=session-screen] textarea")!;
    expect(composer.disabled).toBe(true);
    expect(composer.placeholder).toContain("You can read this session");
    await until(() => page.querySelector("[data-testid=proposal-card]"), "the card");
    expect(page.querySelector("[data-testid=file-proposal]")).toBeNull();
    await write(page, "let me in");
    expect(client.sent).toEqual([]);
  });

  test("a question put to you: its turn is the form and answers it; one put to someone else is theirs, and your message waits", async () => {
    const asked = { id: "q_1", prompt: "Grow the 24h window for every kind?", options: ["Every kind", "Experiment runs only"], askedAt: at(40) };
    const mine = new SessionClient(detail("chat", { question: { ...asked, to: ref(ME), yours: true } }), [
      ev("question.asked", { kind: "agent", questionId: "q_1", prompt: asked.prompt, options: asked.options, to: YOU, toName: ME.name }, { type: "agent", id: RUN }),
    ]);
    const page = await sessionPage(mine);
    const waitingLine = await until(() => page.querySelector("[data-testid=session-screen] [data-testid=composer-waiting]"), "the waiting line");
    expect(waitingLine.textContent).toContain("The brainstorm is waiting for your answer above.");
    const choice = await until(() => [...page.querySelectorAll<HTMLElement>("[data-testid=question-turn] [role=radio]")]
      .find((b) => b.textContent?.startsWith("Experiment runs only")) ?? null, "the choice");
    await click(choice);
    await settle();
    expect(mine.sent).toEqual([`q_1:[{"choices":[1],"text":""}]`]);

    const theirs = new SessionClient(detail("owner", { question: { ...asked, to: ref(ANA), yours: false } }), [
      ev("question.asked", { kind: "agent", questionId: "q_1", prompt: asked.prompt, options: asked.options, to: ANA.id, toName: ANA.name }, { type: "agent", id: RUN }),
      ev("chat.message", { text: "also the panel", directiveId: "dir_9", heldFor: "q_1" }, { type: "human", id: YOU }),
    ]);
    const other = await sessionPage(theirs);
    await until(() => other.querySelector("[data-testid=human-turn]"), "the held message");
    // No form for you; the composer still writes, after their answer.
    expect(other.querySelector("[data-testid=question-form]")).toBeNull();
    expect(other.querySelector("[data-testid=composer-waiting]")).toBeNull();
    const composer = other.querySelector<HTMLTextAreaElement>("[data-testid=session-screen] textarea")!;
    expect(composer.disabled).toBe(false);
    expect(composer.placeholder).toContain(`Waiting for ${ANA.name.split(" ")[0]}`);
    expect(other.querySelector("[data-testid=human-turn]")!.textContent).toContain("goes after the answer");
    const waiting = other.querySelector("[data-testid=waiting-on]")!.textContent ?? "";
    expect(waiting).toContain(`Waiting for ${ANA.name.split(" ")[0]} to answer`);
    expect(waiting).toContain(`Only ${ANA.name.split(" ")[0]} can answer this one`);
    expect(waiting).not.toContain("Take over");
  });

  test("your question waiting: Write to the agent instead sends an aside, never the answer", async () => {
    const asked = { id: "q_1", prompt: "Grow the 24h window for every kind?", options: ["Every kind", "Experiment runs only"], askedAt: at(40) };
    const mine = new SessionClient(detail("chat", { question: { ...asked, to: ref(ME), yours: true } }), [
      ev("question.asked", { kind: "agent", questionId: "q_1", prompt: asked.prompt, options: asked.options, to: YOU, toName: ME.name }, { type: "agent", id: RUN }),
    ]);
    const page = await sessionPage(mine);
    const instead = await until(() => page.querySelector<HTMLElement>("[data-testid=session-screen] [data-testid=write-instead]"), "write instead");
    await click(instead);
    await write(page, "Experiment runs only");
    expect(mine.sent).toEqual(["aside:Experiment runs only"]);
  });

  test("Write to the agent instead lasts for that question: the next one read with no wait between steps the composer back", async () => {
    const q1 = { id: "q_1", prompt: "Grow the 24h window for every kind?", options: ["Every kind", "Experiment runs only"], askedAt: at(40) };
    const q2 = { id: "q_2", prompt: "And the 7d window?", options: ["Yes", "No"], askedAt: at(42) };
    const mine = new SessionClient(detail("chat", { question: { ...q1, to: ref(ME), yours: true } }), [
      ev("question.asked", { kind: "agent", questionId: "q_1", prompt: q1.prompt, options: q1.options, to: YOU, toName: ME.name }, { type: "agent", id: RUN }),
    ]);
    const page = await sessionPage(mine);
    const waiting = () => page.querySelectorAll("[data-testid=session-screen] [data-testid=composer-waiting]").length;
    await click(await until(() => page.querySelector<HTMLElement>("[data-testid=session-screen] [data-testid=write-instead]"), "write instead"));
    expect(waiting()).toBe(0);
    // The next read finds q_2 already open: q_1's answer and q_2 came between two reads.
    mine.detail = detail("chat", { question: { ...q2, to: ref(ME), yours: true } });
    await act(async () => {
      emit({ eventType: "question.asked", occurredAt: at(42), organizationId: "org_1", projectId: null as unknown as string,
        taskId: null as unknown as string, runId: RUN, sessionId: SESSION, workflowRunId: null, actor: { type: "agent", id: RUN },
        source: "orchestrator", correlationId: null, causationId: null,
        payload: { kind: "agent", questionId: "q_2", prompt: q2.prompt, options: q2.options, to: YOU, toName: ME.name } });
    });
    await until(() => (waiting() === 1 ? true : null), "the composer stepped back for q_2");
  });

  test("the card files what you tick, as you, and says what was refused", async () => {
    const client = new SessionClient(detail("chat", {
      proposals: [{ id: "prp_1", runId: RUN, createdAt: at(30), items: [
        { kind: "task", project: "BL", title: "Dedupe on run id", goal: "g" },
        { kind: "edit", task: "BL-58", after: { goal: "new" } },
        { kind: "comment", task: "WC-214", text: "keep it there" },
      ], status: [{ canFile: true }, { canFile: false, why: "Only Márcio can file this: it's his task" }, { canFile: true }] }],
    }));
    client.fileResult = { results: [{ item: 0, status: "filed", key: "BL-61" }, { item: 2, status: "refused", why: "already filed" }] };
    const page = await sessionPage(client);
    const card = await until(() => page.querySelector("[data-testid=proposal-card]"), "the card");
    expect(card.querySelector("[data-testid=cannot-file]")!.textContent).toContain("Only Márcio can file this");
    await click(card.querySelector("[data-testid=file-proposal]")!);
    await settle();
    // The edit it may not file is never ticked, so never sent.
    expect(client.filed).toEqual([[0, 2]]);
    expect(page.querySelector("[data-testid=proposal-problem]")!.textContent).toContain("already filed");
  });

  test("an item another member filed while it was ticked here is not sent again", async () => {
    const proposal = (filed: boolean) => ({ id: "prp_1", runId: RUN, createdAt: at(30), items: [
      { kind: "task" as const, project: "BL", title: "Dedupe on run id", goal: "g" },
      { kind: "comment" as const, task: "WC-214", text: "keep it there" },
    ], status: [filed ? { filed: true, filedBy: ANA.name, key: "BL-61" } : { canFile: true }, { canFile: true }] });
    const client = new SessionClient(detail("chat", { proposals: [proposal(false)] }));
    const page = await sessionPage(client);
    await until(() => page.querySelector("[data-testid=proposal-card]"), "the card");
    client.detail = detail("chat", { proposals: [proposal(true)] });
    await act(async () => {
      emit({ eventType: "session.filed", occurredAt: at(50), organizationId: "org_1", projectId: null as unknown as string,
        taskId: null as unknown as string, runId: null, sessionId: SESSION, workflowRunId: null, actor: { type: "human", id: ANA.id },
        source: "orchestrator", correlationId: null, causationId: null, payload: { proposalId: "prp_1" } });
    });
    await until(() => page.querySelector("[data-testid=proposal-card] [data-filed=true]"), "Ana's filing");
    await click(page.querySelector("[data-testid=file-proposal]")!);
    await settle();
    expect(client.filed).toEqual([[1]]);
  });

  test("a question withdrawn when its one recipient left says so, and asks for nothing", async () => {
    const client = new SessionClient(detail("owner"), [
      ev("question.asked", { kind: "agent", questionId: "q_w", prompt: "Grow the window?", options: ["Yes", "No"], to: ANA.id, toName: ANA.name },
        { type: "agent", id: RUN }),
      ev("question.closed", { questionId: "q_w", by: "withdrawn" }),
    ]);
    const page = await sessionPage(client);
    const settled = await until(() => page.querySelector("[data-testid=settled-by]"), "the closed card");
    expect(settled.textContent).toBe("Withdrawn");
    expect(page.querySelector("[data-testid=session-screen] form[data-mode=answer]")).toBeNull();
    expect(page.querySelector("[data-testid=waiting-on]")).toBeNull();
  });

  test("a member opening the session updates the people where they are; the page is not read again", async () => {
    class Counting extends SessionClient {
      reads = 0;
      override getSession(): Promise<SessionDetail> {
        this.reads++;
        return super.getSession();
      }
    }
    const client = new Counting(detail("owner"));
    const page = await sessionPage(client);
    await until(() => page.querySelector("[data-testid=session-rail]"), "the rail");
    await settle(400);
    const reads = client.reads;
    const anaOpen = () => [...page.querySelectorAll("[data-testid=session-rail] [data-open=true]")]
      .some((li) => li.textContent?.includes(ANA.name));
    expect(anaOpen()).toBe(false);
    await act(async () => {
      emit({ eventType: "session.open", occurredAt: new Date().toISOString(), organizationId: "org_1", projectId: null as unknown as string,
        taskId: null as unknown as string, runId: null, sessionId: SESSION, workflowRunId: null, actor: { type: "person", id: ANA.id },
        source: "control-plane", correlationId: null, causationId: null, payload: { personId: ANA.id, open: true } });
    });
    await until(() => (anaOpen() ? true : null), "Ana shown as here");
    await settle(400);
    expect(client.reads).toBe(reads);
  });

  test("the open heartbeat is not sent while the page is hidden, and is sent once it is shown again", async () => {
    let hidden = false;
    Object.defineProperty(document, "hidden", { configurable: true, get: () => hidden });
    try {
      class Beats extends SessionClient {
        beats: boolean[] = [];
        override sessionOpen(_id: string, open: boolean) {
          this.beats.push(open);
          return Promise.resolve({ open });
        }
      }
      const client = new Beats(detail("chat"));
      await sessionPage(client);
      await until(() => (client.beats.length >= 1 ? true : null), "the first heartbeat");
      const at = client.beats.length;
      hidden = true;
      await act(async () => {
        document.dispatchEvent(new Event("visibilitychange"));
      });
      await settle(50);
      expect(client.beats.length).toBe(at);
      hidden = false;
      await act(async () => {
        document.dispatchEvent(new Event("visibilitychange"));
      });
      await settle(50);
      expect(client.beats.slice(at)).toEqual([true]);
    } finally {
      delete (document as unknown as Record<string, unknown>).hidden;
    }
  });

  test("a session you are not in is not there", async () => {
    const { ApiError } = await import("../src/api/client.ts");
    class Gone extends SessionClient {
      override getSession(): Promise<SessionDetail> {
        return Promise.reject(new ApiError(404, "not_found", "no such session"));
      }
    }
    const client = new Gone(detail("chat"));
    const { container, unmount } = await mount(
      <PeopleProvider client={client}>
        <ToastProvider>
          <SessionScreen client={client} sessionId={SESSION} projects={[]} onBack={() => {}} onChanged={() => {}} onArchived={() => {}} />
        </ToastProvider>
      </PeopleProvider>,
    );
    mounted.push(unmount);
    const shown = await until(() => container.querySelector("[data-testid=not-found]"), "not found");
    expect(shown.textContent).toContain("you're not in it");
    expect(container.querySelector("[data-testid=session-screen]")).toBeNull();
  });
});

describe("a session's name", () => {
  const untitled = (role: "owner" | "chat" | "read") => {
    const d = detail(role);
    return { ...d, session: { ...d.session, title: null, titledBy: null, messages: 0 } };
  };

  test("untitled, its header says New session, muted, and a new one opens with the composer focused", async () => {
    const client = new SessionClient(untitled("owner"));
    const page = await sessionPage(client);
    const title = page.querySelector("[data-testid=session-title]")!;
    expect(title.textContent).toBe("New session");
    expect(title.querySelector("[data-untitled=true]")).not.toBeNull();
    const composer = page.querySelector("[data-testid=session-screen] textarea");
    expect(composer).not.toBeNull();
    expect(document.activeElement === composer).toBe(true);
  });

  test("a member who can chat renames it in place: Enter saves, Escape cancels", async () => {
    const client = new SessionClient(detail("chat"));
    const page = await sessionPage(client);
    await click(page.querySelector("[data-testid=session-title]")!);
    let input = await until(() => page.querySelector<HTMLInputElement>("[data-testid=session-title-input]"), "the name field");
    expect(input.value).toBe("Usage-based billing");
    await type(input, "Throwaway");
    await act(async () => { input.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })); });
    expect(page.querySelector("[data-testid=session-title-input]")).toBeNull();
    expect(page.querySelector("[data-testid=session-title]")!.textContent).toBe("Usage-based billing");
    expect(client.renamed).toEqual([]);

    await click(page.querySelector("[data-testid=session-title]")!);
    input = await until(() => page.querySelector<HTMLInputElement>("[data-testid=session-title-input]"), "the name field again");
    await type(input, "  Billing   v2 ");
    await act(async () => { input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true })); });
    await settle();
    expect(client.renamed).toEqual(["Billing v2"]);
    expect(page.querySelector("[data-testid=session-title]")!.textContent).toBe("Billing v2");
  });

  test("a reader reads its name and cannot rename it", async () => {
    const client = new SessionClient(untitled("read"));
    const page = await sessionPage(client);
    const title = page.querySelector("[data-testid=session-title]")!;
    expect(title.tagName).not.toBe("BUTTON");
    expect(title.textContent).toBe("New session");
    await click(title);
    expect(page.querySelector("[data-testid=session-title-input]")).toBeNull();
    expect(document.activeElement === page.querySelector("[data-testid=session-screen] textarea")).toBe(false);
  });

  test("the Chat says who named it: the agent, or the person by name", async () => {
    const client = new SessionClient(detail("owner"), [
      ev("session.renamed", { title: "Usage metering", by: "agent" }, { type: "agent", id: RUN }),
      ev("session.renamed", { title: "Billing v2", by: ANA.id }, { type: "human", id: ANA.id }),
    ]);
    const page = await sessionPage(client);
    const notices = await until(() => {
      const found = [...page.querySelectorAll("[data-kind=renamed]")];
      return found.length === 2 ? found : null;
    }, "both renames");
    expect(notices[0]!.textContent).toContain("Brainstorm: Named it “Usage metering”");
    expect(notices[1]!.textContent).toContain(`${ANA.name.split(" ")[0]} renamed it “Billing v2”`);
  });

  test("untitled in the list, the sidebar's list and the inbox: New session", async () => {
    const summary = { id: SESSION, title: null, role: "owner" as const, createdAt: at(0), owner: ref(ME), shared: false, projects: [],
      runStatus: null, dudePause: null, filed: 0, lastActivityAt: at(0), archived: false };
    const { SessionsScreen } = await import("../src/screens/SessionsScreen.tsx");
    const client = new SessionClient(detail("owner"));
    const list = await mount(<ToastProvider><SessionsScreen client={client} sessions={[summary]} onOpen={() => {}} onNew={() => {}} /></ToastProvider>);
    mounted.push(list.unmount);
    expect(list.container.querySelector("[data-testid=session-row]")!.textContent).toContain("New session");

    const inbox = await mount(
      <PeopleProvider client={client}>
        <ToastProvider>
          <InboxScreen client={client} projects={[]} onSelect={() => {}} onOpenSession={() => {}} onChanged={() => {}} sessions={{
            sessions: [],
            invitations: [{ id: SESSION, title: null, role: "chat", becomesOwner: false, invitedAt: at(0), invitedBy: ref(ME), people: [ref(ME)],
              projects: [], messages: 0 }],
            questions: [{ id: "q_u", prompt: "Which window?", options: [], askedAt: at(1), sessionId: SESSION, title: null }],
          }} />
        </ToastProvider>
      </PeopleProvider>,
    );
    mounted.push(inbox.unmount);
    expect(inbox.container.querySelector("[data-testid=session-invitation]")!.textContent).toContain("shared a session with you: New session");
    expect(inbox.container.querySelector("[data-testid=session-question]")!.textContent).toContain("asked you in New session");
  });

  test("New session goes to the welcome and makes nothing", async () => {
    const { SessionsScreen } = await import("../src/screens/SessionsScreen.tsx");
    // The screen reads archived sessions through its client; New session makes nothing through it.
    class Creating extends SessionClient {
      made = 0;
      override createSession() {
        this.made++;
        return Promise.resolve({ id: "ssn_new", title: null, runId: "run_new" });
      }
    }
    const client = new Creating(detail("owner"));
    let welcomed = 0;
    const { container, unmount } = await mount(<ToastProvider><SessionsScreen client={client} sessions={[]} onOpen={() => {}} onNew={() => welcomed++} /></ToastProvider>);
    mounted.push(unmount);
    await click(container.querySelector("[data-testid=new-session]")!);
    await settle();
    expect(welcomed).toBe(1);
    expect(client.made).toBe(0);
    expect(document.querySelector("[role=dialog]") === null).toBe(true);
  });
});

describe("a session made from the welcome", () => {
  const PROJECTS = [{ id: "prj_bl", name: "billing" }, { id: "prj_wc", name: "web-console" }];

  class Making extends SessionClient {
    made: Array<{ message?: string; projects?: Array<{ projectId: string; repositoryIds: string[] }> }> = [];
    fail: Error | null = null;
    override createSession(input: { message?: string; projects?: Array<{ projectId: string; repositoryIds: string[] }> } = {}) {
      if (this.fail) return Promise.reject(this.fail);
      this.made.push(input);
      return Promise.resolve({ id: "ssn_new", title: null, runId: "run_new" });
    }
    override getProject(id: string) {
      return Promise.resolve({ id, repositories: [{ id: `repo_${id}` }] } as unknown as Awaited<ReturnType<FixtureClient["getProject"]>>);
    }
  }

  async function welcome(client: Making, opened: string[]) {
    const { container, unmount } = await mount(
      <ToastProvider><WelcomeScreen client={client} projects={PROJECTS} sessions={[]} name="Márcio Martins"
        onOpenSession={() => {}} onAllSessions={() => {}} onCreated={(id) => opened.push(id)} /></ToastProvider>,
    );
    mounted.push(unmount);
    return container;
  }

  async function send(page: HTMLElement, text: string) {
    const composer = page.querySelector<HTMLTextAreaElement>("[data-testid=welcome] textarea")!;
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(composer, text);
      composer.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await act(async () => {
      composer.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    });
    await settle();
  }

  test("sending makes the session with the message and each linked project's repositories, and opens it", async () => {
    const client = new Making(detail("owner"));
    const opened: string[] = [];
    const page = await welcome(client, opened);
    await act(async () => void page.querySelector("[data-testid=composer-link]")!.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true, button: 0, pointerType: "mouse" })));
    await click([...document.querySelectorAll("[role=menuitem]")].find((i) => i.textContent?.includes("web-console"))!);
    await send(page, "where does metering go?");
    expect(client.made).toEqual([{ message: "where does metering go?", projects: [{ projectId: "prj_wc", repositoryIds: ["repo_prj_wc"] }] }]);
    expect(opened).toEqual(["ssn_new"]);
  });

  test("a failed send keeps the words in the composer, says why, and makes nothing", async () => {
    const client = new Making(detail("owner"));
    client.fail = new Error("the orchestrator is down");
    const opened: string[] = [];
    const page = await welcome(client, opened);
    await send(page, "where does metering go?");
    expect(client.made).toEqual([]);
    expect(opened).toEqual([]);
    expect(page.querySelector<HTMLTextAreaElement>("[data-testid=welcome] textarea")!.value).toBe("where does metering go?");
    expect(page.querySelector("[data-testid=welcome-problem]")!.textContent).toContain("Could not start the session");
  });
});

describe("a session's files", () => {
  const art = (id: string, name: string, version: number, versions: number, contentType = "text/markdown", description = ""): Artifact => ({
    id, taskId: null, sessionId: SESSION, runId: RUN, name, contentType, sizeBytes: 40, sha256: "x", description, epoch: 1,
    createdAt: at(30 - version), phase: null, role: "brainstorm", version, versions });

  class FilesClient extends SessionClient {
    artifacts: Artifact[] = [];
    asked = 0;
    override listSessionArtifacts() {
      this.asked++;
      return Promise.resolve({ artifacts: this.artifacts });
    }
    override artifactContent(id: string) {
      return Promise.resolve(new Blob([`# ${id}\n\nThe design.`], { type: "text/markdown" }));
    }
  }

  test("the rail lists what its agent published, with a count; one opens in the files' viewer, and a new one is read as it is recorded", async () => {
    const client = new FilesClient(detail("read"));
    client.artifacts = [art("art_d2", "design.md", 2, 2), art("art_csv", "usage.csv", 1, 1, "text/csv"), art("art_d1", "design.md", 1, 2)];
    const page = await sessionPage(client);
    const block = await until(() => page.querySelector("[data-testid=session-files] [data-testid=published-files]"), "the files");
    expect(page.querySelector("[data-testid=session-files-count]")!.textContent).toBe("2");
    expect([...block.querySelectorAll("button")].map((b) => b.textContent)).toEqual(["design.mdv2", "usage.csv"]);
    await click(block.querySelector("[data-name='design.md']")!);
    const viewer = await until(() => document.querySelector("[data-testid=file-viewer]"), "the viewer");
    await until(() => viewer.querySelector("[data-testid=file-content]")?.textContent?.includes("The design.") || null, "its content");
    expect(viewer.textContent).toContain("art_d2");
    expect(viewer.querySelectorAll("[data-testid=viewer-version]")).toHaveLength(2);

    const asked = client.asked;
    client.artifacts = [art("art_n", "notes.md", 1, 1), ...client.artifacts];
    await act(async () => {
      emit({ ...ev("artifact.created", { artifactId: "art_n", name: "notes.md" }, { type: "agent", id: RUN }), eventId: "evt_art_n", cursor: 9999 });
    });
    await until(() => page.querySelector("[data-testid=session-files-count]")?.textContent === "3" || null, "the new file");
    expect(client.asked).toBeGreaterThan(asked);
  });

  test("a file's latest description is a line under its name in the rail", async () => {
    const client = new FilesClient(detail("read"));
    client.artifacts = [art("art_d2", "design.md", 2, 2, "text/markdown", "Invoice PDF export, with the numbers"),
      art("art_csv", "usage.csv", 1, 1, "text/csv"), art("art_d1", "design.md", 1, 2, "text/markdown", "First draft")];
    const page = await sessionPage(client);
    const block = await until(() => page.querySelector("[data-testid=session-files] [data-testid=published-files]"), "the files");
    expect([...block.querySelectorAll("[data-testid=published-file-description]")].map((d) => d.textContent))
      .toEqual(["Invoice PDF export, with the numbers"]);
  });

  // The listing returns a name's newest 50 versions; the rail names the latest's number, not how many came.
  test("a file published more often than the listing returns is shown at its latest version", async () => {
    const client = new FilesClient(detail("read"));
    client.artifacts = Array.from({ length: 50 }, (_, i) => art(`art_n${60 - i}`, "notes.md", 60 - i, 60));
    const page = await sessionPage(client);
    const block = await until(() => page.querySelector("[data-testid=session-files] [data-testid=published-files]"), "the files");
    expect([...block.querySelectorAll("button")].map((b) => b.textContent)).toEqual(["notes.mdv60"]);
  });

  test("nothing published yet says so", async () => {
    const page = await sessionPage(new FilesClient(detail("owner")));
    await settle();
    expect(page.querySelector("[data-testid=session-files]")!.textContent).toContain("Nothing published yet");
    expect(page.querySelector("[data-testid=session-files-count]")).toBeNull();
  });
});

describe("a session's events", () => {
  // What arrives once the turn is over: its name (name_session, called in
  // the turn but recorded with its rename) and the file lux collects when
  // the container stops. Neither is the agent working, and a trip through
  // Events and back reads the same ledger.
  test("a rename and a published file after the turn's end start no Thinking, through Events and back", async () => {
    const agent = { type: "agent", id: RUN } as const;
    const client = new SessionClient(detail("owner"), [
      ev("chat.message", { text: "metering" }, { type: "human", id: YOU }),
      ev("agent.prompt.delivered", { text: "metering" }, agent),
      ev("agent.tool.called", { tool: "name_session", callId: "n1" }, agent),
      ev("agent.tool.completed", { tool: "name_session", callId: "n1", status: "completed" }, agent),
      // Between requests, mid-turn: the agent is thinking until the turn's totals and end say otherwise.
      ev("agent.model.request.completed", { tokens: { input: 1, output: 1 } }, agent),
      ev("agent.message", { text: "Named it; the note is in Files." }, agent),
      ev("agent.model.request.completed", { turn: true, tokens: { input: 1, output: 1 } }, agent),
      ev("agent.session.stopped", { reason: "turn_complete" }, agent),
      ev("session.renamed", { title: "Usage metering", by: "agent" }, agent),
      ev("artifact.created", { artifactId: "art_1", name: "design.md" }, agent),
    ]);
    const page = await sessionPage(client);
    await until(() => page.querySelector("[data-kind=renamed]"), "the rename notice");
    await settle();
    const activity = () => page.querySelectorAll("[data-testid=session-screen] [data-activity]").length;
    expect(activity()).toBe(0);
    const bar = page.querySelector("[data-testid=session-view]")!;
    await click([...bar.querySelectorAll("button")].find((b) => b.textContent?.startsWith("Events"))!);
    await until(() => page.querySelector("[data-testid=event-log]"), "the ledger");
    await click([...bar.querySelectorAll("button")].find((b) => b.textContent === "Conversation")!);
    await until(() => page.querySelector("[data-kind=renamed]"), "the conversation again");
    await settle();
    expect(activity()).toBe(0);
  });

  test("the switch is Conversation | Events (count), with no Changes; Events lists every Run's events and the session's own", async () => {
    const other = "run_brainstorm2";
    const client = new SessionClient(detail("read"), [
      ev("chat.message", { text: "first" }, { type: "human", id: YOU }),
      ev("session.renamed", { title: "Usage metering", by: "agent" }, { type: "agent", id: RUN }),
      { ...ev("agent.message", { text: "from the second Run" }, { type: "agent", id: other }), runId: other },
      { ...ev("session.linked", { projects: [] }, { type: "human", id: YOU }), runId: null },
    ]);
    const page = await sessionPage(client);
    const bar = await until(() => page.querySelector("[data-testid=session-view]"), "the switch");
    const options = [...bar.querySelectorAll("button")].map((b) => b.textContent);
    expect(options).toEqual(["Conversation", "Events4"]);
    expect(page.querySelector("[data-testid=event-log]")).toBeNull();
    await click([...bar.querySelectorAll("button")].find((b) => b.textContent?.startsWith("Events"))!);
    const log = await until(() => page.querySelector("[data-testid=event-log]"), "the ledger");
    expect(page.querySelector("[data-testid=session-composer]")).toBeNull();
    const rows = log.textContent ?? "";
    for (const type of ["chat.message", "session.renamed", "agent.message", "session.linked"]) expect(rows).toContain(type);
    expect(rows).toContain("from the second Run");
    await click([...bar.querySelectorAll("button")].find((b) => b.textContent === "Conversation")!);
    await until(() => page.querySelector("[data-testid=session-composer]"), "the conversation again");
  });
});

describe("what a session's ledger says in its Chat", () => {
  test("membership and handover, by name", () => {
    const people = { you: YOU, me: null, all: [], byId: new Map(), names: new Map([[YOU, ME.name], [ANA.id, ANA.name]]),
      refresh: async () => people, seen: () => false } as unknown as Parameters<typeof sessionNotice>[1];
    expect(sessionNotice(ev("session.shared", { people: [ANA.id], role: "chat" }, { type: "human", id: YOU }), people))
      .toBe(`${ME.name} shared this with ${ANA.name} (can chat).`);
    expect(sessionNotice(ev("session.owner_changed", { from: YOU, to: ANA.id, keep: "chat", fromName: ME.name, toName: ANA.name }), people))
      .toBe(`${ME.name.split(" ")[0]} made ${ANA.name} the owner, and stays as can chat.`);
    expect(sessionNotice(ev("agent.message", { text: "hi" }), people)).toBeNull();
  });
});

describe("Waiting on you", () => {
  class InboxClient extends FixtureClient {
    accepted: string[] = [];
    declined: string[] = [];
    override acceptSession(id: string) {
      this.accepted.push(id);
      return Promise.resolve({ id });
    }
    override declineSession(id: string) {
      this.declined.push(id);
      return Promise.resolve({ id });
    }
  }
  const list: SessionsList = {
    sessions: [],
    invitations: [{ id: SESSION, title: "Usage-based billing", role: "chat", becomesOwner: false, invitedAt: at(0), invitedBy: ref(ME),
      people: [ref(ME), { ...ref(ANA), name: "João Reis", id: "u_joao" }], projects: [{ id: "p", key: "BL", name: "billing", repositories: [] }], messages: 38 }],
    questions: [{ id: "q_1", prompt: "Grow the window?", options: [], askedAt: at(5), sessionId: "ssn_other", title: "Meter v2" }],
  };

  test("an invitation says who, with whom, what it reads and how much was said, never a word of it; Open accepts, Decline declines", async () => {
    const client = new InboxClient("a");
    const opened: string[] = [];
    const { container, unmount } = await mount(
      <PeopleProvider client={client}>
        <ToastProvider>
          <InboxScreen client={client} projects={[]} sessions={list} onSelect={() => {}} onOpenSession={(id) => opened.push(id)} onChanged={() => {}} />
        </ToastProvider>
      </PeopleProvider>,
    );
    mounted.push(unmount);
    const line = await until(() => container.querySelector("[data-testid=session-invitation]"), "the invitation");
    expect(line.textContent).toContain("shared a session with you");
    expect(line.textContent).toContain("Usage-based billing");
    expect(line.textContent).toContain("with João Reis");
    expect(line.textContent).toContain("billing");
    expect(line.textContent).toContain("38 messages");
    const question = container.querySelector("[data-testid=session-question]")!;
    expect(question.textContent).toContain("Grow the window?");
    await click(line.querySelector("[data-testid=invitation-decline]")!);
    await settle();
    expect(client.declined).toEqual([SESSION]);
    await click(line.querySelector("[data-testid=invitation-open]")!);
    await settle();
    expect(client.accepted).toEqual([SESSION]);
    expect(opened).toEqual([SESSION]);
  });

  test("a question of several is named as the board names it: how many, and their headers", async () => {
    const items = ["Retry scope", "Old route", "Tests"].map((header) => ({ header, question: `${header}?`, multiple: false, choices: [] }));
    const several: SessionsList = { ...list, invitations: [],
      questions: [{ id: "q_3", prompt: "3 questions: Retry scope, Old route, Tests", options: [], items, askedAt: at(5), sessionId: "ssn_other", title: "Meter v2" }] };
    const client = new InboxClient("a");
    const { container, unmount } = await mount(
      <PeopleProvider client={client}>
        <ToastProvider>
          <InboxScreen client={client} projects={[]} sessions={several} onSelect={() => {}} onOpenSession={() => {}} onChanged={() => {}} />
        </ToastProvider>
      </PeopleProvider>,
    );
    mounted.push(unmount);
    const question = await until(() => container.querySelector("[data-testid=session-question]"), "the question");
    expect(question.textContent).toContain("The brainstorm in Meter v2 asks 3 questions · Retry scope, Old route, Tests");
    expect(question.textContent).not.toContain("3 questions: Retry scope");
  });
});

describe("archiving a session, for yourself", () => {
  const OTHER = "ssn_pricing";
  const summary = (id: string, title: string, archived: boolean) => ({ id, title, role: "owner" as const, createdAt: at(0), owner: ref(ME),
    shared: false, projects: [], runStatus: null, dudePause: null, filed: 0, lastActivityAt: at(0), archived });

  // Answers as the API does: the list leaves out what you archived unless asked, and the page says whether it is.
  class Archiving extends SessionClient {
    archivedIds = new Set<string>();
    calls: Array<[string, boolean]> = [];
    refuse = false;
    refuseArchived = false;
    // Whether each list read asked for archived sessions too.
    reads: boolean[] = [];
    // While set, a list read answers only once it settles, with what was true when it was asked.
    hold: Promise<void> | null = null;
    // Holds the list reads asked from now on; the returned function answers them.
    holdReads(): () => void {
      let release!: () => void;
      this.hold = new Promise((r) => { release = r; });
      return release;
    }
    override async sessions(opts: { archived?: boolean } = {}): Promise<SessionsList> {
      this.reads.push(opts.archived === true);
      if (opts.archived && this.refuseArchived) throw new Error("the orchestrator is down");
      const hold = this.hold;
      const all = [summary(SESSION, "Usage-based billing", this.archivedIds.has(SESSION)), summary(OTHER, "Pricing", this.archivedIds.has(OTHER))];
      if (hold) await hold;
      return { sessions: all.filter((s) => opts.archived || !s.archived), invitations: [], questions: [] };
    }
    override getSession(): Promise<SessionDetail> {
      return Promise.resolve({ ...this.detail, you: { ...this.detail.you, archived: this.archivedIds.has(SESSION) } });
    }
    override archiveSession(id: string, archived: boolean) {
      this.calls.push([id, archived]);
      if (this.refuse) return Promise.reject(new Error("the orchestrator is down"));
      if (archived) this.archivedIds.add(id);
      else this.archivedIds.delete(id);
      return Promise.resolve({ id, archived });
    }
  }

  const listed = (page: HTMLElement) => [...page.querySelectorAll("[data-testid=sessions] [data-testid=session-row]")]
    .map((r) => r.getAttribute("data-session"));
  const inSidebar = (page: HTMLElement) => [...page.querySelectorAll("[data-testid=sidebar-sessions] [data-session]")]
    .map((r) => r.getAttribute("data-session"));
  // The shell on the session's page, once the sidebar lists it.
  const onSessionPage = async (client: Archiving) => {
    const page = await shell(`#/sessions/${SESSION}`, client);
    await until(() => (inSidebar(page).includes(SESSION) ? true : null), "the session in the sidebar");
    return page;
  };
  const shownOption = (page: HTMLElement, label: string) =>
    [...page.querySelectorAll("[data-testid=sessions-shown] button")].find((b) => b.textContent === label) ?? null;

  test("Archive on its page calls the API and returns you to the list, its row gone there and from the sidebar at once", async () => {
    const client = new Archiving(detail("read"));
    const page = await onSessionPage(client);
    expect(page.querySelector("[data-testid=session-archived]")).toBeNull();
    const release = client.holdReads();
    // A reader archives it too: it is their own list.
    await click(await until(() => page.querySelector("[data-testid=session-archive]"), "Archive"));
    await until(() => page.querySelector("[data-testid=sessions]"), "the list");
    expect(client.calls).toEqual([[SESSION, true]]);
    expect(window.location.hash).toBe("#/sessions");
    // The list read that follows the archive has not answered yet.
    expect(listed(page)).toEqual([OTHER]);
    expect(inSidebar(page)).toEqual([OTHER]);
    await act(async () => release());
    await settle();
    expect(listed(page)).toEqual([OTHER]);
    expect(inSidebar(page)).toEqual([OTHER]);
  });

  test("a list read asked before the archive and answered after the one that follows it does not bring the row back", async () => {
    const client = new Archiving(detail("owner"));
    const page = await onSessionPage(client);
    const release = client.holdReads();
    // A renamed session re-reads the list; that read is asked now and answers last.
    await act(async () => {
      emit({ eventType: "session.renamed", occurredAt: at(50), organizationId: "org_1", projectId: null as unknown as string,
        taskId: null as unknown as string, runId: null, sessionId: OTHER, workflowRunId: null, actor: { type: "human", id: ANA.id },
        source: "orchestrator", correlationId: null, causationId: null, payload: { title: "Pricing" } });
    });
    client.hold = null;
    await click(await until(() => page.querySelector("[data-testid=session-archive]"), "Archive"));
    await until(() => page.querySelector("[data-testid=sessions]"), "the list");
    await settle();
    expect(inSidebar(page)).toEqual([OTHER]);
    await act(async () => release());
    await settle();
    expect(listed(page)).toEqual([OTHER]);
    expect(inSidebar(page)).toEqual([OTHER]);
  });

  test("Archived shows what you archived; opened, it is marked, and Unarchive brings it back to the list and the sidebar", async () => {
    const client = new Archiving(detail("chat"));
    client.archivedIds.add(SESSION);
    const page = await shell("#/sessions", client);
    await until(() => (listed(page).includes(OTHER) ? true : null), "your sessions");
    expect(listed(page)).toEqual([OTHER]);
    expect(inSidebar(page)).toEqual([OTHER]);
    await settle();
    expect(client.reads.filter((archived) => archived)).toEqual([]);

    await click(await until(() => shownOption(page, "Archived"), "the Archived filter"));
    await until(() => (listed(page).includes(SESSION) ? true : null), "the archived session");
    expect(listed(page)).toEqual([SESSION]);
    await settle();
    expect(client.reads.filter((archived) => archived)).toEqual([true]);
    // The sidebar never shows it.
    expect(inSidebar(page)).toEqual([OTHER]);

    await click(page.querySelector(`[data-testid=session-row][data-session=${SESSION}] button`)!);
    const mark = await until(() => page.querySelector("[data-testid=session-screen] [data-testid=session-archived]"), "the Archived mark");
    expect(mark.textContent).toBe("Archived");
    await click(page.querySelector("[data-testid=session-unarchive]")!);
    await until(() => (inSidebar(page).includes(SESSION) ? true : null), "the session back in the sidebar");
    expect(client.calls).toEqual([[SESSION, false]]);
    // Unarchiving keeps you on its page, unmarked.
    expect(window.location.hash).toBe(`#/sessions/${SESSION}`);
    expect(page.querySelector("[data-testid=session-archived]")).toBeNull();
    expect(page.querySelector("[data-testid=session-archive]")).not.toBeNull();

    await click(page.querySelector("[data-testid=sidebar-sessions] button")!);
    await until(() => (listed(page).length === 2 ? true : null), "both sessions in the list");
    expect(listed(page).sort()).toEqual([OTHER, SESSION].sort());
  });

  test("a failed archive says why on the page and changes nothing", async () => {
    const client = new Archiving(detail("owner"));
    client.refuse = true;
    const page = await onSessionPage(client);
    await click(await until(() => page.querySelector("[data-testid=session-archive]"), "Archive"));
    const problem = await until(() => page.querySelector("[data-testid=session-problem]"), "the problem");
    expect(problem.textContent).toContain("Could not archive it: the orchestrator is down");
    expect(client.calls).toEqual([[SESSION, true]]);
    expect(window.location.hash).toBe(`#/sessions/${SESSION}`);
    expect(page.querySelector("[data-testid=session-archived]")).toBeNull();
    expect(page.querySelector("[data-testid=session-archive]")).not.toBeNull();
    await settle(100);
    expect(inSidebar(page).sort()).toEqual([OTHER, SESSION].sort());
  });

  const archivedBody = async (client: Archiving) => {
    const page = await shell("#/sessions", client);
    await until(() => (listed(page).length > 0 ? true : null), "your sessions");
    await click(await until(() => shownOption(page, "Archived"), "the Archived filter"));
    await until(() => (page.querySelector("[data-testid=sessions] .centered") ? null : true), "the archived read");
    return page.querySelector<HTMLElement>("[data-testid=sessions] .screenBody")!;
  };

  test("Archived with nothing archived says so", async () => {
    const body = await archivedBody(new Archiving(detail("owner")));
    expect([...body.querySelectorAll("div")].filter((d) => d.children.length === 0).map((d) => d.textContent)).toEqual([
      "Nothing archived",
      "A session you archive leaves your list and sidebar, and nobody else's. Open one and Unarchive brings it back.",
    ]);
    expect(body.querySelector("[data-testid=session-row]")).toBeNull();
  });

  test("Archived that cannot be read says why", async () => {
    const client = new Archiving(detail("owner"));
    client.archivedIds.add(SESSION);
    client.refuseArchived = true;
    const body = await archivedBody(client);
    expect(body.querySelector("[data-testid=sessions-problem]")!.textContent).toBe("Could not read your archived sessions: the orchestrator is down");
    expect(body.querySelector("[data-testid=session-row]")).toBeNull();
  });
});

describe("a session's URL", () => {
  test("the list and one session read back; a task's agent session keeps its own", () => {
    expect(parsePlace("#/sessions")).toEqual({ view: "sessions" });
    expect(parsePlace("#/sessions/ssn_1")).toEqual({ view: "brainstorm", id: "ssn_1" });
    expect(formatPlace({ view: "brainstorm", id: "ssn_1" })).toBe("#/sessions/ssn_1");
    expect(formatPlace({ view: "sessions" })).toBe("#/sessions");
    expect(parsePlace("#/session/ses_1")).toEqual({ view: "tree", ref: { kind: "session", id: "ses_1" } });
  });
});
