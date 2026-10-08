/**
 * A brainstorm session's page, its inbox lines and its URL: what each
 * member sees and may do, driven through the screens with the fixture
 * stream and a client that answers as the API does.
 */

import { afterEach, describe, expect, test } from "bun:test";
import type { PersistedEvent, SessionDetail, SessionsList } from "@dude/domain";
import { act, click, mount, settle, until } from "./dom.ts";
import { FixtureClient, emit, type LedgerQuery } from "../src/fixtures/client.ts";
import { PEOPLE, YOU } from "../src/fixtures/data.ts";
import { PeopleProvider } from "../src/people.tsx";
import { SessionScreen, sessionNotice } from "../src/screens/SessionScreen.tsx";
import { InboxScreen } from "../src/screens/InboxScreen.tsx";
import { formatPlace, parsePlace } from "../src/place.ts";
import type { FileResult } from "@dude/domain";
import { ToastProvider } from "@dude/design-system/primitives";

let mounted: Array<() => Promise<void>> = [];
afterEach(async () => {
  for (const unmount of mounted) await unmount();
  mounted = [];
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
      id: SESSION, title: "Usage-based billing", createdAt: at(0),
      people: [
        { person: ref(owner), role: "owner", accepted: true, becomesOwner: false, open: true },
        { person: ref(other), role: role === "owner" ? "chat" : role, accepted: true, becomesOwner: false, open: false },
      ],
      projects: [{ id: "prj_bl", key: "BL", name: "billing", repositories: [{ id: "repo_bl", name: "billing", defaultBranch: "main" }] }],
      run: { id: RUN, status: "running", dudePause: null, model: "claude-opus-5-5", modelTier: "small", machine: null, waiting: false },
      runs: [RUN], costUsd: 0.71, messages: 2,
    },
    you: { id: YOU, role },
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
  filed: number[][] = [];
  fileResult: FileResult | null = null;
  override getSession(): Promise<SessionDetail> {
    return Promise.resolve(this.detail);
  }
  protected override ledgerFor(q: LedgerQuery): PersistedEvent[] {
    if (q.sessionId !== SESSION) return super.ledgerFor(q);
    return this.ledger.filter((e) => e.cursor > (q.after ?? 0));
  }
  override sessionChat(_id: string, text: string) {
    this.sent.push(text);
    return Promise.resolve({ runId: RUN, created: false });
  }
  override fileProposal(_id: string, _proposal: string, items: number[]): Promise<FileResult> {
    this.filed.push(items);
    return Promise.resolve(this.fileResult ?? { results: items.map((item) => ({ item, status: "filed" as const, key: `BL-${60 + item}` })) });
  }
}

async function sessionPage(client: SessionClient) {
  const { container, unmount } = await mount(
    <PeopleProvider client={client}>
      <SessionScreen client={client} sessionId={SESSION} projects={[]} onBack={() => {}} onChanged={() => {}} />
    </PeopleProvider>,
  );
  mounted.push(unmount);
  await until(() => container.querySelector("[data-testid=session-screen]"), "the session page");
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

  test("a link the API refuses as two projects under one key says so in full in the dialog, which stays open", async () => {
    const message = "Billing API and Billing Worker both use the key BILL; a session tells its projects apart by key, so link one of them.";
    const client = new SessionClient(detail("owner"));
    const { ApiError } = await import("../src/api/client.ts");
    const links: unknown[] = [];
    client.linkSession = (_id, projects) => {
      links.push(projects);
      return Promise.reject(new ApiError(409, "conflict", message));
    };
    const page = await sessionPage(client);
    await click(await until(() => page.querySelector("[data-testid=link-open]"), "Link"));
    const dialog = await until(() => document.querySelector<HTMLElement>("[role=dialog]"), "the link dialog");
    await click(dialog.querySelector("[data-testid=link-save]")!);
    const refused = await until(() => dialog.querySelector("[data-testid=link-refused]"), "the refusal");
    expect(refused.textContent).toBe(message);
    expect(links.length).toBe(1);
    expect(document.querySelector("[role=dialog]")).toBe(dialog);
  });

  test("a message goes to the session's chat", async () => {
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

  test("a question put to you: its chips answer it; one put to someone else is theirs, and your message waits", async () => {
    const asked = { id: "q_1", prompt: "Grow the 24h window for every kind?", options: ["Every kind", "Experiment runs only"], askedAt: at(40) };
    const mine = new SessionClient(detail("chat", { question: { ...asked, to: ref(ME), yours: true } }), [
      ev("question.asked", { kind: "agent", questionId: "q_1", prompt: asked.prompt, options: asked.options, to: YOU, toName: ME.name }, { type: "agent", id: RUN }),
    ]);
    const page = await sessionPage(mine);
    const form = await until(() => page.querySelector("[data-testid=session-screen] form[data-mode=answer]"), "the answer composer");
    const chip = [...form.querySelectorAll("button")].find((b) => b.textContent === "Experiment runs only")!;
    await click(chip);
    await settle();
    expect(mine.sent).toEqual(["Experiment runs only"]);

    const theirs = new SessionClient(detail("owner", { question: { ...asked, to: ref(ANA), yours: false } }), [
      ev("question.asked", { kind: "agent", questionId: "q_1", prompt: asked.prompt, options: asked.options, to: ANA.id, toName: ANA.name }, { type: "agent", id: RUN }),
      ev("chat.message", { text: "also the panel", directiveId: "dir_9", heldFor: "q_1" }, { type: "human", id: YOU }),
    ]);
    const other = await sessionPage(theirs);
    await until(() => other.querySelector("[data-testid=human-turn]"), "the held message");
    // No chips for you; the composer still writes, after their answer.
    expect(other.querySelector("[data-testid=session-screen] form[data-mode=answer]")).toBeNull();
    const composer = other.querySelector<HTMLTextAreaElement>("[data-testid=session-screen] textarea")!;
    expect(composer.disabled).toBe(false);
    expect(composer.placeholder).toContain(`Waiting for ${ANA.name.split(" ")[0]}`);
    expect(other.querySelector("[data-testid=human-turn]")!.textContent).toContain("goes after the answer");
    const waiting = other.querySelector("[data-testid=waiting-on]")!.textContent ?? "";
    expect(waiting).toContain(`Waiting for ${ANA.name.split(" ")[0]} to answer`);
    expect(waiting).toContain(`Only ${ANA.name.split(" ")[0]} can answer this one`);
    expect(waiting).not.toContain("Take over");
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
        <SessionScreen client={client} sessionId={SESSION} projects={[]} onBack={() => {}} onChanged={() => {}} />
      </PeopleProvider>,
    );
    mounted.push(unmount);
    const shown = await until(() => container.querySelector("[data-testid=not-found]"), "not found");
    expect(shown.textContent).toContain("you're not in it");
    expect(container.querySelector("[data-testid=session-screen]")).toBeNull();
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
