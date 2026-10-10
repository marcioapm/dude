/**
 * The welcome screen: the greeting by the reader's clock, starters that
 * fill and never send, what the session will read said in the composer,
 * the recent sessions and the first-time line, and a send that is made
 * once however often Enter is pressed while it is on its way.
 */

import { afterEach, describe, expect, test } from "bun:test";
import type { SessionSummary } from "@dude/domain";
import { act, click, mount, settle, until } from "./dom.ts";
import { FixtureClient } from "../src/fixtures/client.ts";
import { PEOPLE, YOU } from "../src/fixtures/data.ts";
import { WelcomeScreen, partOfDay } from "../src/screens/WelcomeScreen.tsx";
import { WELCOME_FIRST_TIME as FIRST_TIME, WELCOME_STARTERS as STARTERS } from "@dude/design-system/components";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import type { SessionLink } from "@dude/domain";

let mounted: Array<() => Promise<void>> = [];
afterEach(async () => {
  for (const unmount of mounted) await unmount();
  mounted = [];
});

const ME = PEOPLE.find((p) => p.id === YOU)!;
const PROJECTS = [{ id: "prj_bl", name: "billing" }, { id: "prj_wc", name: "web-console" }];
const ref = { id: ME.id, name: ME.name, photoUrl: null, online: true };
const summary = (id: string, title: string | null, filed = 0, over: Partial<SessionSummary> = {}): SessionSummary => ({
  id, title, role: "owner", createdAt: "2026-10-01T10:00:00Z", owner: ref, shared: false, projects: [], runStatus: null, dudePause: null,
  filed, lastActivityAt: "2026-10-01T10:00:00Z", ...over,
});

class Welcoming extends FixtureClient {
  made: Array<{ message?: string; projects?: SessionLink[] }> = [];
  repoReads: string[] = [];
  /** Held until released: a send on its way. */
  hold: PromiseWithResolvers<void> | null = null;
  constructor() {
    super("a");
  }
  override async createSession(input: { message?: string; projects?: SessionLink[] } = {}) {
    this.made.push(input);
    await this.hold?.promise;
    return { id: "ssn_new", title: null, runId: "run_new" };
  }
  override getProject(id: string) {
    this.repoReads.push(id);
    return Promise.resolve({ id, repositories: [{ id: `repo_${id}_1` }, { id: `repo_${id}_2` }] } as unknown as Awaited<ReturnType<FixtureClient["getProject"]>>);
  }
}

async function welcome(client: Welcoming, opts: { sessions?: SessionSummary[] | null; at?: Date; onOpen?: (id: string) => void; onAll?: () => void; onCreated?: (id: string) => void } = {}) {
  const { container, unmount } = await mount(
    <TooltipProvider>
      <ToastProvider>
        <WelcomeScreen client={client} projects={PROJECTS} sessions={opts.sessions === undefined ? [] : opts.sessions} name={ME.name}
          now={opts.at ? () => opts.at! : undefined} onOpenSession={opts.onOpen ?? (() => {})} onAllSessions={opts.onAll ?? (() => {})}
          onCreated={opts.onCreated ?? (() => {})} />
      </ToastProvider>
    </TooltipProvider>,
  );
  mounted.push(unmount);
  return container;
}

const textarea = (page: HTMLElement) => page.querySelector<HTMLTextAreaElement>("[data-testid=welcome] textarea")!;

async function write(page: HTMLElement, text: string) {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(textarea(page), text);
    textarea(page).dispatchEvent(new Event("input", { bubbles: true }));
  });
}
async function enter(page: HTMLElement) {
  await act(async () => {
    textarea(page).dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
  });
}
async function openLinkMenu(page: HTMLElement) {
  await act(async () => void page.querySelector("[data-testid=composer-link]")!.dispatchEvent(
    new PointerEvent("pointerdown", { bubbles: true, button: 0, pointerType: "mouse" })));
  return [...document.querySelectorAll<HTMLElement>("[role=menuitem]")];
}

describe("the greeting", () => {
  test("is the time of day on the reader's clock, then their first name", async () => {
    const at = (h: number) => new Date(2026, 9, 10, h, 30);
    expect([1, 4, 5, 11, 12, 17, 18, 23].map((h) => partOfDay(at(h)))).toEqual([
      "Late one", "Late one", "Morning", "Morning", "Afternoon", "Afternoon", "Evening", "Evening",
    ]);
    const page = await welcome(new Welcoming(), { at: at(15) });
    expect(page.querySelector("h1")!.textContent).toBe("Afternoon, Márcio");
    expect(page.textContent).toContain("What are we working out today?");
  });

  test("the composer is focused, says what to write, and goes to the brainstorm", async () => {
    const page = await welcome(new Welcoming());
    expect(document.activeElement).toBe(textarea(page));
    expect(textarea(page).placeholder).toBe("Start a session: an idea, a question, a plan…");
    expect(page.querySelector("[data-testid=composer-to]")!.textContent).toBe("To Brainstorm");
  });
});

describe("a starter", () => {
  test("fills the composer with its words, the caret at their end, and sends nothing", async () => {
    const client = new Welcoming();
    const page = await welcome(client);
    expect([...page.querySelectorAll("[data-starter]")].map((b) => b.textContent)).toEqual(STARTERS.map((s) => s.title));
    await click(page.querySelector("[data-starter=epic]")!);
    await settle(40);
    expect(textarea(page).value).toBe("I want to plan an epic for ");
    expect(document.activeElement).toBe(textarea(page));
    expect(textarea(page).selectionStart).toBe("I want to plan an epic for ".length);
    expect(client.made).toEqual([]);
  });
});

describe("what it will read", () => {
  test("nothing linked reads memory only; a picked project is a chip, its repositories fetched; its close unlinks it", async () => {
    const client = new Welcoming();
    const page = await welcome(client);
    expect(page.querySelector("[data-testid=composer-links]")!.textContent).toContain("Reads memory only");
    const items = await openLinkMenu(page);
    expect(items.map((i) => i.textContent)).toEqual(["BIbilling", "WCweb-console"]);
    await click(items[1]!);
    expect([...page.querySelectorAll("[data-project]")].map((c) => c.getAttribute("data-project"))).toEqual(["prj_wc"]);
    expect(client.repoReads).toEqual(["prj_wc"]);
    await click(page.querySelector("[aria-label='Stop reading web-console']")!);
    expect(page.querySelectorAll("[data-project]").length).toBe(0);
    expect(page.querySelector("[data-testid=composer-links]")!.textContent).toContain("Reads memory only");
  });

  test("a send carries every linked project with all its repositories, fetched then if they had not arrived", async () => {
    const client = new Welcoming();
    const created: string[] = [];
    const page = await welcome(client, { onCreated: (id) => created.push(id) });
    await click((await openLinkMenu(page))[0]!);
    await write(page, "where does metering go?");
    await enter(page);
    await settle();
    expect(client.made).toEqual([{ message: "where does metering go?",
      projects: [{ projectId: "prj_bl", repositoryIds: ["repo_prj_bl_1", "repo_prj_bl_2"] }] }]);
    expect(created).toEqual(["ssn_new"]);
  });
});

describe("recent sessions", () => {
  test("newest first as the list gives them, four in comfortable, each opening its session; All sessions opens the list", async () => {
    const opened: string[] = [];
    let all = 0;
    const sessions = [summary("s1", "Usage-based billing", 5), summary("s2", null), summary("s3", "C"), summary("s4", "D"), summary("s5", "E")];
    const page = await welcome(new Welcoming(), { sessions, onOpen: (id) => opened.push(id), onAll: () => all++ });
    const rows = [...page.querySelectorAll<HTMLElement>("[data-testid=recent-session]")];
    expect(rows.map((r) => r.getAttribute("data-session"))).toEqual(["s1", "s2", "s3", "s4"]);
    expect(rows[0]!.textContent).toContain("Filed 5");
    expect(rows[1]!.textContent).toContain("New session");
    expect(rows[1]!.textContent).toContain("Nothing filed yet");
    await click(rows[1]!);
    await click(page.querySelector("[data-testid=all-sessions]")!);
    expect(opened).toEqual(["s2"]);
    expect(all).toBe(1);
    expect(page.querySelector("[data-testid=welcome-note]")).toBeNull();
  });

  test("six in compact", async () => {
    document.documentElement.setAttribute("data-density", "compact");
    try {
      const sessions = ["a", "b", "c", "d", "e", "f", "g"].map((id) => summary(id, id));
      const page = await welcome(new Welcoming(), { sessions });
      expect(page.querySelectorAll("[data-testid=recent-session]").length).toBe(6);
    } finally {
      document.documentElement.removeAttribute("data-density");
    }
  });

  test("none yet: the first-time line in their place", async () => {
    const page = await welcome(new Welcoming(), { sessions: [] });
    expect(page.querySelector("[data-testid=welcome-note]")!.textContent).toBe(FIRST_TIME);
    expect(page.querySelector("[data-testid=recent-session]")).toBeNull();
  });
});

describe("sending", () => {
  test("while it is on its way the composer is busy, and a second Enter makes nothing", async () => {
    const client = new Welcoming();
    client.hold = Promise.withResolvers<void>();
    const created: string[] = [];
    const page = await welcome(client, { onCreated: (id) => created.push(id) });
    await write(page, "plan the meter");
    await enter(page);
    await settle(20);
    const send = page.querySelector<HTMLButtonElement>("[data-testid=welcome] button[type=submit]")!;
    expect(send.getAttribute("aria-busy")).toBe("true");
    await enter(page);
    await enter(page);
    await settle(20);
    expect(client.made.length).toBe(1);
    await act(async () => client.hold!.resolve());
    await until(() => (created.length ? true : null), "the session opened");
    expect(client.made.length).toBe(1);
    expect(created).toEqual(["ssn_new"]);
  });
});
