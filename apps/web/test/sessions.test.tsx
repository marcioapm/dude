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
import { formatPlace, parsePlace } from "../src/place.ts";
import type { FileResult } from "@dude/domain";
import { ToastProvider } from "@dude/design-system/primitives";
import type { Artifact } from "../src/api/client.ts";

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
      id: SESSION, title: "Usage-based billing", titledBy: "agent", createdAt: at(0),
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
      <ToastProvider>
        <SessionScreen client={client} sessionId={SESSION} projects={[]} onBack={() => {}} onChanged={() => {}} />
      </ToastProvider>
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
        <ToastProvider>
          <SessionScreen client={client} sessionId={SESSION} projects={[]} onBack={() => {}} onChanged={() => {}} />
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
    expect(document.activeElement).toBe(page.querySelector("[data-testid=session-screen] textarea"));
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
    expect(document.activeElement).not.toBe(page.querySelector("[data-testid=session-screen] textarea"));
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
      runStatus: null, dudePause: null, filed: 0, lastActivityAt: at(0) };
    const { SessionsScreen } = await import("../src/screens/SessionsScreen.tsx");
    const client = new SessionClient(detail("owner"));
    const list = await mount(<ToastProvider><SessionsScreen client={client} sessions={[summary]} onOpen={() => {}} /></ToastProvider>);
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

  test("New session starts one at once, untitled and linked to nothing, and opens it", async () => {
    const { SessionsScreen } = await import("../src/screens/SessionsScreen.tsx");
    class Creating extends SessionClient {
      made = 0;
      override createSession() {
        this.made++;
        return Promise.resolve({ id: "ssn_new", title: null });
      }
    }
    const client = new Creating(detail("owner"));
    const opened: string[] = [];
    const { container, unmount } = await mount(<ToastProvider><SessionsScreen client={client} sessions={[]} onOpen={(id) => opened.push(id)} /></ToastProvider>);
    mounted.push(unmount);
    await click(container.querySelector("[data-testid=new-session]")!);
    await settle();
    expect(client.made).toBe(1);
    expect(opened).toEqual(["ssn_new"]);
    expect(document.querySelector("[role=dialog]")).toBeNull();
  });
});

describe("a session's files", () => {
  const art = (id: string, name: string, version: number, versions: number, contentType = "text/markdown"): Artifact => ({
    id, taskId: null, sessionId: SESSION, runId: RUN, name, contentType, sizeBytes: 40, sha256: "x", epoch: 1, createdAt: at(30 - version),
    phase: null, role: "brainstorm", version, versions });

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

  test("nothing published yet says so", async () => {
    const page = await sessionPage(new FilesClient(detail("owner")));
    await settle();
    expect(page.querySelector("[data-testid=session-files]")!.textContent).toContain("Nothing published yet");
    expect(page.querySelector("[data-testid=session-files-count]")).toBeNull();
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
