/**
 * Organisation sessions, admin-owned sessions and @-mentions in the web
 * app: the Share dialog's General access (one way, confirmed; read-only
 * once open; no "can read" there; Make me owner while its admins own it),
 * the header's marks, the composer's @ picker and the private session's
 * share confirm, the chips on a sent message, the Waiting on you mention
 * row, and the sidebar's Open to everyone link and its list. Driven
 * through the screens with a client that answers as the API does.
 */

import { afterEach, describe, expect, test } from "bun:test";
import type { OpenSessionsList, PersistedEvent, SessionDetail, SessionsList } from "@dude/domain";
import { act, click, mount, settle, until } from "./dom.ts";
import { FixtureClient, type LedgerQuery } from "../src/fixtures/client.ts";
import { PEOPLE, YOU } from "../src/fixtures/data.ts";
import { PeopleProvider } from "../src/people.tsx";
import { SessionScreen } from "../src/screens/SessionScreen.tsx";
import { InboxScreen } from "../src/screens/InboxScreen.tsx";
import { OpenSessionsScreen } from "../src/screens/OpenSessionsScreen.tsx";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import { App } from "../src/App.tsx";
import { formatPlace, parsePlace } from "../src/place.ts";
import { mentionables } from "../src/mentions.ts";
import { RUN_KEY } from "../src/fixtures/scenario.ts";
import { accessibleName } from "../../../packages/design-system/test/queries.ts";

let mounted: Array<() => Promise<void>> = [];
afterEach(async () => {
  for (const unmount of mounted) await unmount();
  mounted = [];
  localStorage.clear();
});

const SESSION = "ssn_org";
const RUN = "run_org1";
const person = (id: string) => {
  const p = PEOPLE.find((x) => x.id === id)!;
  return { id: p.id, name: p.name, photoUrl: null, online: true };
};
const at = (s: number) => new Date(Date.UTC(2026, 9, 7, 11, 20, s)).toISOString();

type Role = "owner" | "chat" | "read";
function detail(you: Role, over: Partial<SessionDetail["session"]> = {}, youOver: Partial<SessionDetail["you"]> = {}): SessionDetail {
  const owner = you === "owner" ? YOU : "u_ana";
  return {
    session: {
      id: SESSION, title: "CSV export times out", titledBy: "agent", createdAt: at(0),
      people: [
        { person: person(owner), role: "owner", accepted: true, becomesOwner: false, open: false },
        ...(you === "owner" ? [] : [{ person: person(YOU), role: you, accepted: true, becomesOwner: false, open: true }]),
        { person: person("u_tom"), role: "read", accepted: true, becomesOwner: false, open: false },
      ],
      projects: [], run: null, runs: [], costUsd: 0, messages: 1, access: "private", ownedByAdmins: false, ...over,
    },
    you: { id: YOU, role: you, archived: false, member: true, admin: true, ...youOver },
    proposals: [], question: null, agentMachines: [],
  };
}

let cursor = 0;
function ev(eventType: string, payload: Record<string, unknown>, actor: PersistedEvent["actor"]): PersistedEvent {
  cursor += 1;
  return { eventId: `evt_o${cursor}`, cursor, eventType, occurredAt: at(cursor), organizationId: "org_1", projectId: null as unknown as string,
    taskId: null as unknown as string, runId: RUN, sessionId: SESSION, workflowRunId: null, actor, source: "orchestrator",
    correlationId: null, causationId: null, payload };
}

class OrgClient extends FixtureClient {
  constructor(public detail: SessionDetail, public ledger: PersistedEvent[] = []) {
    super("a");
  }
  sent: Array<{ text: string; mentions?: ReadonlyArray<string>; shareWith?: ReadonlyArray<string> }> = [];
  opened: string[] = [];
  taken: string[] = [];
  override getSession(): Promise<SessionDetail> {
    return Promise.resolve(this.detail);
  }
  protected override ledgerFor(q: LedgerQuery): PersistedEvent[] {
    if (q.sessionId !== SESSION) return super.ledgerFor(q);
    return this.ledger.filter((e) => e.cursor > (q.after ?? 0));
  }
  override sessionChat(_id: string, text: string, opts: { mentions?: ReadonlyArray<string>; shareWith?: ReadonlyArray<string> } = {}) {
    this.sent.push({ text, ...(opts.mentions ? { mentions: opts.mentions } : {}), ...(opts.shareWith ? { shareWith: opts.shareWith } : {}) });
    return Promise.resolve({ runId: RUN, created: false });
  }
  override openSessionToOrganisation(id: string) {
    this.opened.push(id);
    this.detail = { ...this.detail, session: { ...this.detail.session, access: "organisation" } };
    return Promise.resolve({ id, access: "organisation" as const, changed: true });
  }
  override takeSessionOwnership(id: string) {
    this.taken.push(id);
    return Promise.resolve({ id });
  }
}

async function page(client: OrgClient) {
  const { container, unmount } = await mount(
    <TooltipProvider>
      <PeopleProvider client={client}>
        <ToastProvider>
          <SessionScreen client={client} sessionId={SESSION} projects={[]} onBack={() => {}} onChanged={() => {}} onArchived={() => {}} />
        </ToastProvider>
      </PeopleProvider>
    </TooltipProvider>,
  );
  mounted.push(unmount);
  await until(() => container.querySelector("[data-testid=session-screen]"), "the session page");
  return container;
}

const typeIn = async (area: HTMLTextAreaElement, text: string) => act(async () => {
  Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(area, text);
  area.setSelectionRange(text.length, text.length);
  area.dispatchEvent(new Event("input", { bubbles: true }));
});
const press = (el: Element, key: string) => act(async () => void el.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true })));
const composer = (p: HTMLElement) => p.querySelector<HTMLTextAreaElement>("[data-testid=session-screen] textarea")!;
const openShare = async (p: HTMLElement) => {
  await click(await until(() => p.querySelector("[data-testid=share-open]"), "Share"));
  return until(() => document.querySelector<HTMLElement>("[role=dialog]"), "the Share dialog");
};

describe("General access in the Share dialog", () => {
  test("the owner of a private session opens it only after confirming; Keep it private sends nothing", async () => {
    const client = new OrgClient(detail("owner"));
    const p = await page(client);
    const dialog = await openShare(p);
    const row = dialog.querySelector("[data-testid=general-access]")!;
    expect(row.getAttribute("data-access")).toBe("private");
    await act(async () => void row.querySelector("[data-testid=general-access-select]")!.dispatchEvent(
      new PointerEvent("pointerdown", { bubbles: true, button: 0, pointerType: "mouse" })));
    const everyone = await until(() => [...document.querySelectorAll<HTMLElement>("[role=option]")].find((o) => o.textContent?.includes("Everyone in")), "Everyone");
    expect(everyone.textContent).toContain("Can't be undone");
    await click(everyone);
    const confirm = await until(() => document.querySelector("[data-testid=general-access-confirm]"), "the confirm");
    expect(confirm.textContent).toContain("will be able to read and chat. This can't be undone.");
    await click([...confirm.querySelectorAll("button")].find((b) => b.textContent === "Keep it private")!);
    expect(document.querySelectorAll("[data-testid=general-access-confirm]").length).toBe(0);
    expect(client.opened).toEqual([]);
  });

  test("open to everyone, the row is read-only, Can read is not offered, and a reader shows as can chat", async () => {
    const client = new OrgClient(detail("owner", { access: "organisation" }));
    const p = await page(client);
    const dialog = await openShare(p);
    const row = dialog.querySelector("[data-testid=general-access]")!;
    expect(row.getAttribute("data-access")).toBe("organisation");
    expect(row.querySelectorAll("button").length).toBe(0);
    expect(row.textContent).toContain("This can't be undone.");
    const tom = dialog.querySelector('[data-person="u_tom"]')!;
    expect(tom.querySelector("[data-testid=was-read]")!.textContent).toContain("was can read; everyone in");
    await act(async () => void tom.querySelector("button[role=combobox]")!.dispatchEvent(
      new PointerEvent("pointerdown", { bubbles: true, button: 0, pointerType: "mouse" })));
    const options = await until(() => {
      const o = [...document.querySelectorAll<HTMLElement>("[role=option]")];
      return o.length ? o : null;
    }, "Tom's roles");
    expect(options.map((o) => o.textContent)).toEqual(["Can chat"]);
  });

  test("a member who can chat sees the organisation mark in the header, and no Share", async () => {
    const p = await page(new OrgClient(detail("chat", { access: "organisation" })));
    const mark = p.querySelector('[data-scope="organisation"]')!;
    expect(accessibleName(mark)).toMatch(/^Everyone in /);
    expect(p.querySelectorAll("[data-testid=share-open]").length).toBe(0);
    expect(p.querySelector("[data-testid=session-everyone-note]")!.textContent).toContain("who can chat");
  });
});

describe("a session its admins own", () => {
  test("its header and rail say so; an admin may Make me owner", async () => {
    const d = detail("owner", { ownedByAdmins: true, access: "organisation",
      people: [{ person: person("u_sam"), role: "owner", accepted: true, becomesOwner: false, open: false },
        { person: person("u_ana"), role: "chat", accepted: true, becomesOwner: false, open: false }] }, { member: false, admin: true });
    const client = new OrgClient(d);
    const p = await page(client);
    expect(p.querySelectorAll("[data-testid=admin-owned]").length).toBe(2);
    const dialog = await openShare(p);
    expect(dialog.querySelector('[data-person="u_sam"]')!.textContent).toContain("No access");
    expect(dialog.querySelectorAll("[data-testid=make-owner]").length).toBe(0);
    await click(dialog.querySelector("[data-testid=take-ownership]")!);
    expect(client.taken).toEqual([SESSION]);
  });

  test("a member who is not an admin is not offered Make me owner", async () => {
    const d = detail("chat", { ownedByAdmins: true, access: "organisation" }, { admin: false });
    const p = await page(new OrgClient(d));
    expect(p.querySelectorAll("[data-testid=admin-owned]").length).toBe(2);
    const dialog = await openShare(p);
    expect(dialog.querySelectorAll("[data-testid=take-ownership]").length).toBe(0);
    expect(dialog.querySelector("[data-testid=admin-owned-callout]")!.textContent).toContain("admins own it now");
  });
});

describe("mentions in the composer", () => {
  test("in an organisation session @ offers everyone; Enter picks, and the message carries the mention", async () => {
    const client = new OrgClient(detail("owner", { access: "organisation" }));
    const p = await page(client);
    const area = composer(p);
    expect(area.getAttribute("role")).toBe("combobox");
    await typeIn(area, "can you look @lin");
    const options = await until(() => {
      const o = [...p.querySelectorAll<HTMLElement>("[role=option]")];
      return o.length ? o : null;
    }, "the mention menu");
    expect(options.length).toBe(1);
    expect(options[0]!.textContent).toContain("Not in it yet · can open it and chat");
    await press(area, "Enter");
    await settle();
    expect(area.value).toBe("can you look @Lin Zhao ");
    await press(area, "Enter");
    await settle();
    expect(client.sent).toEqual([{ text: "can you look @Lin Zhao", mentions: ["u_lin"] }]);
  });

  test("the owner of a private session picking someone outside it: Share and send adds them and mentions them", async () => {
    const client = new OrgClient(detail("owner"));
    const p = await page(client);
    const area = composer(p);
    await typeIn(area, "@lin");
    const option = await until(() => p.querySelector<HTMLElement>("[role=option]"), "Lin");
    expect(option.textContent).toContain("Not in this session");
    await press(area, "Enter");
    const confirm = await until(() => p.querySelector<HTMLElement>("[data-testid=mention-share-confirm]"), "the confirm");
    expect(confirm.getAttribute("data-owner")).toBe("true");
    await click(confirm.querySelector("[data-testid=mention-share-send]")!);
    await settle();
    expect(client.sent).toEqual([{ text: "@Lin Zhao", mentions: ["u_lin"], shareWith: ["u_lin"] }]);
    expect(p.querySelectorAll("[data-testid=mention-share-confirm]").length).toBe(0);
  });

  test("Send without sharing sends the name as text, mentioning nobody", async () => {
    const client = new OrgClient(detail("owner"));
    const p = await page(client);
    const area = composer(p);
    await typeIn(area, "@lin");
    await until(() => p.querySelector("[role=option]"), "Lin");
    await press(area, "Enter");
    const confirm = await until(() => p.querySelector<HTMLElement>("[data-testid=mention-share-confirm]"), "the confirm");
    await click(confirm.querySelector("[data-testid=mention-send-plain]")!);
    await settle();
    expect(client.sent).toEqual([{ text: "@Lin Zhao" }]);
  });

  test("anyone but the owner is told to ask the owner, and may only send plain text", async () => {
    const client = new OrgClient(detail("chat"));
    const p = await page(client);
    const area = composer(p);
    await typeIn(area, "@lin");
    await until(() => p.querySelector("[role=option]"), "Lin");
    await press(area, "Enter");
    const confirm = await until(() => p.querySelector<HTMLElement>("[data-testid=mention-share-confirm]"), "the confirm");
    expect(confirm.getAttribute("data-owner")).toBe("false");
    expect(confirm.textContent).toContain("Ask Ana to share it");
    expect(confirm.querySelectorAll("[data-testid=mention-share-send]").length).toBe(0);
    await click(confirm.querySelector("[data-testid=mention-send-plain]")!);
    await settle();
    expect(client.sent).toEqual([{ text: "@Lin Zhao" }]);
  });

  test("a sent message shows a chip per mention, the reader's own marked", async () => {
    const msg = ev("chat.message", { text: "@Márcio Martins and @Lin Zhao", mentions: [YOU, "u_lin"] }, { type: "human", id: "u_ana" });
    const p = await page(new OrgClient(detail("chat", { access: "organisation" }), [msg]));
    const chips = await until(() => {
      const c = [...p.querySelectorAll("[data-testid=mention-chip]")];
      return c.length === 2 ? c : null;
    }, "the chips");
    expect(chips.map(accessibleName)).toEqual(["Mention: Márcio Martins (you)", "Mention: Lin Zhao"]);
  });

  test("who the picker offers: the session's people first, then the rest; outside ones only in a private session", () => {
    const members = detail("owner").session.people;
    const everyone = PEOPLE.map((p) => ({ id: p.id, name: p.name }));
    const priv = mentionables(YOU, members, everyone, "private");
    expect(priv[0]).toMatchObject({ person: { id: "u_tom" }, detail: "In this session · can read" });
    expect(priv.slice(1).every((m) => m.outside === true)).toBe(true);
    expect(priv.some((m) => m.person.id === YOU)).toBe(false);
    const open = mentionables(YOU, members, everyone, "organisation");
    expect(open[0]).toMatchObject({ detail: "In this session · can chat" });
    expect(open.some((m) => m.outside)).toBe(false);
  });
});

describe("Waiting on you", () => {
  test("a mention is a row of yours: who, where, that you are not in it yet, and Open", async () => {
    const client = new OrgClient(detail("chat"));
    const opened: string[] = [];
    const list: SessionsList = { sessions: [], invitations: [], questions: [], mentions: [
      { id: "1", sessionId: SESSION, title: "CSV export times out", by: person("u_ana"), text: "@Márcio Martins look", createdAt: at(1),
        access: "organisation", member: false }] };
    const { container, unmount } = await mount(
      <PeopleProvider client={client}><ToastProvider>
        <InboxScreen client={client} projects={[]} sessions={list} onSelect={() => {}} onOpenSession={(id) => opened.push(id)} onChanged={() => {}} />
      </ToastProvider></PeopleProvider>,
    );
    mounted.push(unmount);
    const row = await until(() => container.querySelector<HTMLElement>("[data-testid=session-mention]"), "the mention");
    expect(row.textContent).toContain("Ana mentioned you in CSV export times out: “@Márcio Martins look”");
    expect(row.textContent).toContain("you're not in it yet");
    expect(row.getAttribute("data-mine")).toBe("true");
    await click([...row.querySelectorAll("button")].find((b) => b.textContent === "Open")!);
    expect(opened).toEqual([SESSION]);
  });
});

describe("Open to everyone", () => {
  const LIST: OpenSessionsList = { joined: 1, sessions: [
    { id: "ssn_a", title: "Joined one", owner: person("u_ana"), joined: true, people: 2, projects: [], runStatus: "running", lastActivityAt: at(1), ownedByAdmins: false },
    { id: "ssn_b", title: "Not yet", owner: person("u_tom"), joined: false, people: 1, projects: [], runStatus: null, lastActivityAt: at(2), ownedByAdmins: false },
    { id: "ssn_c", title: "Left behind", owner: null, joined: false, people: 1, projects: [], runStatus: null, lastActivityAt: at(3), ownedByAdmins: true },
  ] };

  test("its list groups Joined · active and Not joined, each row opening its session", async () => {
    const opened: string[] = [];
    const { container, unmount } = await mount(<OpenSessionsScreen list={LIST} org="Acme" onOpen={(id) => opened.push(id)} />);
    mounted.push(unmount);
    const ids = (sel: string) => [...container.querySelectorAll(`${sel} [data-testid=open-session-row]`)].map((r) => r.getAttribute("data-session"));
    expect(ids("[data-testid=open-joined]")).toEqual(["ssn_a"]);
    expect(ids("[data-testid=open-not-joined]")).toEqual(["ssn_b", "ssn_c"]);
    expect(container.querySelector('[data-session="ssn_c"]')!.textContent).toContain("Owner left Acme · owned by Acme's admins");
    await click(container.querySelector('[data-session="ssn_b"] button')!);
    expect(opened).toEqual(["ssn_b"]);
  });

  test("its URL is #/sessions/open, apart from a session's", () => {
    expect(parsePlace("#/sessions/open")).toEqual({ view: "openSessions" });
    expect(formatPlace({ view: "openSessions" })).toBe("#/sessions/open");
    expect(parsePlace("#/sessions/ssn_x")).toEqual({ view: "brainstorm", id: "ssn_x" });
  });

  test("the sidebar's Open to everyone row counts the joined ones and opens the list (fixtures)", async () => {
    localStorage.setItem(RUN_KEY, "org-sessions");
    const client = new FixtureClient("a");
    window.history.replaceState(null, "", "#/sessions");
    const { container, unmount } = await mount(
      <TooltipProvider><ToastProvider><PeopleProvider client={client}>
        <App client={client} onSignOut={() => {}} onKeyRefused={() => {}} />
      </PeopleProvider></ToastProvider></TooltipProvider>,
    );
    mounted.push(async () => {
      await unmount();
      window.history.replaceState(null, "", " ");
    });
    const link = await until(() => container.querySelector<HTMLElement>("[data-testid=sidebar-open-to-everyone]"), "the sidebar link");
    expect(accessibleName(link)).toBe("Open to everyone, 2 joined");
    await click(link);
    await until(() => container.querySelector("[data-testid=open-sessions]"), "the list");
    expect(container.querySelectorAll("[data-testid=open-not-joined] [data-testid=open-session-row]").length).toBe(1);
    // Sessions stays yours: the list of those you are in.
    expect(window.location.hash).toContain("sessions/open");
  });
});
