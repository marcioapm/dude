/**
 * Agents on an epic in a Run's transcript: the ledger's peer events fold
 * into turns (a message where it was sent or read, a wait and how it
 * ended, a note change the agent was told, a refused message as a failed
 * call, the conductor's digest just before its briefing), the peer tools
 * render compactly, and the turns draw with the epic's threads and notes.
 */

import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import type { ReactNode } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import { EventTypes, type EpicNotes, type EpicPeers, type EpicThreads, type PersistedEvent } from "@dude/domain";
import { act, click, mount, until } from "./dom.ts";
import { ApiError } from "../src/api/client.ts";
import { FixtureClient } from "../src/fixtures/client.ts";
import { EPIC, RUN_ID } from "../src/fixtures/data.ts";
import { CONDUCTOR_ID } from "../src/fixtures/peers.ts";
import { EpicCoordination } from "../src/screens/EpicCoordination.tsx";
import { project, type DigestTurn, type PeerWaitTurn, type ToolTurn } from "../src/api/conversation.ts";
import { RunScreen, renderTurn } from "../src/screens/RunScreen.tsx";
import { listedPeers, messagesById, noPeers, waitWords, type PeerContext } from "../src/coordination.tsx";
import { PeerList } from "@dude/design-system/components";
import { PeopleProvider, type People } from "../src/people.tsx";

let cursor = 0;
function ev(eventType: string, payload: Record<string, unknown> = {}, at?: string): PersistedEvent {
  cursor += 1;
  return {
    eventId: `evt_${cursor}`, cursor, eventType, organizationId: "org", projectId: null, taskId: null, runId: "run_4", sessionId: null,
    workflowRunId: null, actor: { type: "system", id: "dude" }, source: "orchestrator", correlationId: null, causationId: null, payload,
    occurredAt: at ?? new Date(Date.UTC(2026, 9, 12, 13, 40, cursor)).toISOString(),
  } as unknown as PersistedEvent;
}
const call = (tool: string, callId: string, input: unknown) => ev(EventTypes.ToolCalled, { tool, callId, input });
const done = (tool: string, callId: string, output?: unknown) =>
  ev(EventTypes.ToolCompleted, { tool, callId, status: "completed", ...(output ? { output: { head: JSON.stringify(output) } } : {}) });

const PEOPLE: People = { you: "u_m", me: null, all: [], byId: new Map(), names: new Map([["u_m", "Márcio Martins"]]), refresh: async () => PEOPLE, seen: () => false };
const html = (node: unknown) => renderToStaticMarkup(node as never).replace(/<[^>]+>/g, " ").replace(/\s+/g, " ");

describe("the projection", () => {
  test("a message sent and carried on, the wait at the API call, the reply read, the wait answered", () => {
    const c = project([
      call("message", "c1", { to: "ABS-2", text: "Which body?" }),
      ev(EventTypes.AgentMessageSent, { message: "pm_1", to: "ABS-2", reply_to: null, lands: "next_step", text: "Which body?" }),
      done("message", "c1", { id: "pm_1", to: "ABS-2", lands: "next_step" }),
      call("wait_for_reply", "c2", { message: "pm_1" }),
      ev(EventTypes.AgentPeerWaiting, { message: "pm_1", on: "ABS-2", how: "wait", parks_at: "2026-10-12T13:57:02Z" }),
    ], "running");
    expect(c.turns.map((t) => t.kind)).toEqual(["tool", "peer", "tool", "peerWait"]);
    const sent = c.turns[1]!;
    expect(sent).toMatchObject({ kind: "peer", direction: "sent", peer: "ABS-2", text: "Which body?", lands: "next_step" });
    expect(c.openWait).toMatchObject({ on: "ABS-2", how: "wait", ended: null });
    expect(c.activity).toBeNull();

    const all = project([
      call("message", "c1", { to: "ABS-2" }),
      ev(EventTypes.AgentMessageSent, { message: "pm_1", to: "ABS-2", lands: "next_step", text: "Which body?" }),
      done("message", "c1"),
      ev(EventTypes.AgentPeerWaiting, { message: "pm_1", on: "ABS-2", how: "wait", parks_at: null }),
      ev(EventTypes.AgentMessageReceived, { message: "pm_4", from: "ABS-2", directive: "dir_1", after: "bash" }),
      ev(EventTypes.AgentPeerWaitEnded, { message: "pm_1", on: "ABS-2", by: "reply" }),
    ], "running");
    expect(all.openWait).toBeNull();
    expect((all.turns.find((t) => t.kind === "peerWait") as PeerWaitTurn).ended?.by).toBe("reply");
    expect(all.turns.at(-1)).toMatchObject({ kind: "peer", direction: "received", peer: "ABS-2", after: "bash", via: "step" });
  });

  test("a wait the peer's end released says so, with its status", () => {
    const c = project([
      ev(EventTypes.AgentPeerWaiting, { message: "pm_1", on: "ABS-2", how: "turn_ended", parks_at: null }),
      ev(EventTypes.AgentPeerWaitEnded, { message: "pm_1", on: "ABS-2", by: "peer_ended", status: "aborted" }),
    ], "running");
    expect((c.turns[0] as PeerWaitTurn).ended).toMatchObject({ by: "peer_ended", status: "aborted" });
    expect(c.openWait).toBeNull();
  });

  test("a refused message is its call, failed, with the refusal's words, whichever comes first", () => {
    const refused = () => ev(EventTypes.AgentMessageRefused, { reason: "rate", to: "ABS-2", retry_at: "2026-10-12T15:12:00Z" });
    for (const order of [
      () => [call("message", "c1", { to: "ABS-2" }), refused(), done("message", "c1")],
      () => [refused(), call("message", "c1", { to: "ABS-2" }), done("message", "c1")],
    ]) {
      const tool = project(order(), "running").turns.find((t): t is ToolTurn => t.kind === "tool")!;
      expect(tool.status).toBe("failed");
      expect(tool.refusal).toContain("Refused: this Run has sent 5 messages in the last hour");
    }
    // A message sent goes through: a refusal after it is the next call's.
    const ok = project([
      call("message", "c1", { to: "ABS-2" }), ev(EventTypes.AgentMessageSent, { message: "pm_1", to: "ABS-2", lands: "next_step" }), done("message", "c1"),
      ev(EventTypes.AgentMessageRefused, { reason: "not_a_peer", to: "XYZ-1" }), call("message", "c2", { to: "XYZ-1" }), done("message", "c2"),
    ], "running").turns.filter((t): t is ToolTurn => t.kind === "tool");
    expect(ok.map((t) => t.status)).toEqual(["completed", "failed"]);
    expect(ok[1]!.refusal).toBe("Refused: XYZ-1 is not one of your peers.");
  });

  test("a note change told at a step, after the tool it had just run", () => {
    const c = project([ev(EventTypes.AgentNoteTold, { note: "en_4", n: 4, change: "replaced", was: 3, after: "edit", directive: "d" })], "running");
    expect(c.turns[0]).toMatchObject({ kind: "noteTold", n: 4, change: "replaced", was: 3, after: "edit" });
  });

  test("the conductor's digest goes just before the briefing that carried it", () => {
    const c = project([
      ev(EventTypes.ChatMessage, { text: "What's up?" }),
      ev(EventTypes.ConductorDigest, { since: "2026-10-12T13:44:00Z", through: "2026-10-12T14:20:00Z", where: "briefing", task: "ABS-2", more: 2,
        messages: [{ message: "pm_1", at: "2026-10-12T13:43:00Z", from: "ABS-4", to: "ABS-2", text: "Which body?", replyTo: null, answeredAt: "2026-10-12T13:49:00Z" }],
        notes: [{ at: "2026-10-12T13:51:00Z", n: 4, change: "replaced", was: 3, line: "note 4, ABS-2's implementer, replaces note 3: body" }],
        peers: [{ task: "ABS-1", status: "review", at: "2026-10-12T14:12:00Z" }] }),
      ev(EventTypes.ConductorBriefed, { text: "Conductor, Márcio wrote…" }),
    ], "running");
    expect(c.turns.map((t) => t.kind)).toEqual(["human", "digest", "prompt"]);
    const digest = c.turns[1] as DigestTurn;
    expect(digest).toMatchObject({ where: "briefing", task: "ABS-2", more: 2 });
    expect(digest.messages[0]).toMatchObject({ from: "ABS-4", text: "Which body?" });
    expect(digest.notes[0]).toMatchObject({ n: 4, change: "replaced", was: 3 });
  });
});

const NOTES: EpicNotes = {
  epicId: "e", limit: 10, live: 4,
  current: [{ id: "en_4", n: 4, text: "**Preview API.** Columns `_mean`, `_variance`.", createdAt: "2026-10-12T13:51:20Z", replaces: 3, replacedBy: null, retired: null, readBy: 3,
    by: { kind: "agent", label: "ABS-2's implementer", runId: "r2", task: { taskId: "t2", key: "ABS-2", title: "Backend" }, personId: null, fromSessionId: null } }],
  history: [{ id: "en_3", n: 3, text: "**Preview API.** Columns `_mean`, `_var`.", createdAt: "2026-10-12T13:48:40Z", replaces: null,
    replacedBy: { n: 4, at: "2026-10-12T13:51:20Z" }, retired: null, readBy: 4,
    by: { kind: "agent", label: "ABS-2's implementer", runId: "r2", task: { taskId: "t2", key: "ABS-2", title: "Backend" }, personId: null, fromSessionId: null } }],
};
const ref = (key: string) => ({ taskId: `t_${key}`, key, title: `Task ${key}` });
const THREADS: EpicThreads = {
  epicId: "e", waiting: 0, threads: [{ id: "pm_1", pair: [ref("ABS-4"), ref("ABS-2")], state: "answered", wait: null, lastAt: "2026-10-12T13:49:05Z", messages: [
    { id: "pm_1", from: ref("ABS-4"), to: ref("ABS-2"), fromRunId: "r4", fromRole: "implementer", fromPhase: "implement", replyTo: null, text: "Which body?",
      sentAt: "2026-10-12T13:43:20Z", readAt: "2026-10-12T13:43:52Z", deliveredRunId: "r2", briefedRunId: null, lands: "next_step" },
    { id: "pm_4", from: ref("ABS-2"), to: ref("ABS-4"), fromRunId: "r2", fromRole: "implementer", fromPhase: "implement", replyTo: "pm_1", text: "Body `{draft}`.",
      sentAt: "2026-10-12T13:49:05Z", readAt: "2026-10-12T13:49:06Z", deliveredRunId: "r4", briefedRunId: null, lands: "next_step" },
  ] }],
};
const PEERS: EpicPeers = { epicId: "e", tasks: [
  { ...ref("ABS-2"), status: "running", phase: "implement", role: "implementer", run: "live", runId: "r2", waitingOn: null, inbox: 0 },
  { ...ref("ABS-4"), status: "running", phase: "implement", role: "implementer", run: "live", runId: "r4", waitingOn: null, inbox: 0 },
] };
const CTX: PeerContext = { self: "ABS-4", role: "implementer", coordination: { notes: NOTES, threads: THREADS, peers: PEERS }, messages: messagesById(THREADS), people: PEOPLE };
const draw = (events: PersistedEvent[], ctx: PeerContext = CTX) =>
  project(events, "running").turns.map((t) => html(renderTurn(t, "implementer", 0, false, PEOPLE, "Duder", undefined, undefined, undefined, undefined, undefined, undefined, ctx)));

describe("the turns, drawn", () => {
  test("a reply read here: its words from the thread, what it answers, and when it was read", () => {
    const [reply] = draw([ev(EventTypes.AgentMessageReceived, { message: "pm_4", from: "ABS-2", after: "bash" }, "2026-10-12T13:49:06Z")]);
    expect(reply).toContain("Body {draft}");
    expect(reply).toContain("Replying to Which body?");
    expect(reply).toContain("Reply");
    expect(reply).toContain(", after Bash");
  });

  test("a message sent here carries on, and a held one says its peer has no Run going", () => {
    const [carry] = draw([ev(EventTypes.AgentMessageSent, { message: "pm_1", to: "ABS-2", lands: "next_step", text: "Which body?" })]);
    expect(carry).toContain("Sent · carrying on.");
    const [held] = draw([ev(EventTypes.AgentMessageSent, { message: "pm_9", to: "ABS-3", lands: "next_run", text: "Bins?" })], noPeers("implementer", PEOPLE));
    expect(held).toContain("ABS-3 has no Run going; it will see this when its next Run starts.");
  });

  test("a replaced note is told with the words that changed", () => {
    const [told] = draw([ev(EventTypes.AgentNoteTold, { note: "en_4", n: 4, change: "replaced", was: 3, after: "edit" })]);
    expect(told).toContain("Epic note replaced: 3 → 4");
    expect(told).toContain("_variance (was: _var )");
    expect(told).toContain("after Edit");
  });

  test("the peer tools are drawn compactly; list_tasks --peers is a PeerList", () => {
    const out = { tasks: [
      { key: "ABS-4", title: "Frontend", status: "running", self: true, run: "live", heads: [], files: [], messagesForYou: 0 },
      { key: "ABS-2", title: "Backend", status: "running", phase: "implement", role: "implementer", run: "live", heads: [{ repo: "abs", branch: "dude/abs2", head: "b71e04d9" }],
        files: [{ path: "src/preview.ts", additions: 188, deletions: 0 }], lastSaid: "Pinned it.", messagesForYou: 1, waitsOnYourReply: true },
    ] };
    const [list, wait, note] = draw([
      call("list_tasks", "l", { peers: true }), done("list_tasks", "l", out),
      call("wait_for_reply", "w", { message: "pm_1" }), done("wait_for_reply", "w", { waiting: { on: "ABS-2", message: "pm_1" }, next: "End your turn" }),
      call("note", "n", { text: "x", replaces: 3 }), done("note", "n", { id: "en_4", n: 4, replaces: 3, told: 3, current: [] }),
    ]);
    expect(list).toContain("dude task list --peers · 1 peer · 1 message for you");
    // Open, its result is the peers as rows, the caller's own row saying "you".
    const rows = listedPeers(out);
    expect(rows.map((r) => [r.key, r.branch, r.head, r.forYou?.count])).toEqual([["ABS-4", null, null, 0], ["ABS-2", "dude/abs2", "b71e04d9", 1]]);
    const open = html(<PeerList variant="tool" label="Peers of ABS-4" self="ABS-4" peers={rows} />);
    expect(open).toContain("Frontend you");
    expect(open).toContain("dude/abs2 @ b71e04d");
    expect(open).toContain("1 message for you, waits on your reply");
    expect(wait).toContain("dude wait pm_1 · ends this turn: waiting on ABS-2");
    expect(note).toContain("dude note --replaces 3 · note 4, told to 3 agents at their next step");
  });

  test("the digest is one folded line saying what it holds", () => {
    const [, digest] = draw([
      ev(EventTypes.ChatMessage, { text: "What's up?" }),
      ev(EventTypes.ConductorDigest, { since: "2026-10-12T13:44:00Z", through: "2026-10-12T14:20:00Z", where: "briefing", task: "ABS-2", more: 0,
        messages: [{ message: "pm_1", at: "2026-10-12T13:43:00Z", from: "ABS-4", to: "ABS-2", text: "Which body?", replyTo: null, answeredAt: null }],
        notes: [], peers: [{ task: "ABS-1", status: "review", at: "2026-10-12T14:12:00Z" }] }),
    ]);
    expect(digest).toContain("Since the conductor last looked, on this epic");
    expect(digest).toContain("1 message · ABS-1 review");
    expect(digest).toContain("in its briefing");
    expect(digest).not.toContain("Which body?");
  });

  test("the header's words for a wait", () => {
    const now = Date.parse("2026-10-12T13:47:02Z");
    expect(waitWords("ABS-2", "2026-10-12T13:57:02Z", false, now)).toBe("Waiting on ABS-2 · parks in 10 min");
    expect(waitWords("ABS-2", null, true, now)).toBe("Waiting on ABS-2 · parked");
  });
});

let mounted: Array<() => Promise<void>> = [];
beforeEach(() => localStorage.setItem("dude.fixtures.run", "peers"));
afterEach(async () => {
  for (const unmount of mounted) await unmount();
  mounted = [];
  localStorage.clear();
});
async function show(node: ReactNode, client: FixtureClient) {
  const { container, unmount } = await mount(
    <TooltipProvider><ToastProvider><PeopleProvider client={client}>{node}</PeopleProvider></ToastProvider></TooltipProvider>,
  );
  mounted.push(unmount);
  return container;
}
const setText = (el: HTMLTextAreaElement, text: string) => act(async () => {
  Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(el, text);
  el.dispatchEvent(new Event("input", { bubbles: true }));
});

describe("an epic's Coordination view", () => {
  async function view(client = new FixtureClient("a")) {
    const opened: string[] = [];
    const page = await show(<EpicCoordination client={client} epicId={EPIC.id} epic={null} version={0} onOpenTask={(id) => opened.push(id)} />, client);
    await until(() => page.querySelector("[data-testid=coordination]"), "the view");
    return { page, client, opened };
  }

  test("has the epic's notes, its threads newest first, and its tasks; a task opens", async () => {
    const { page, opened } = await view();
    const notes = page.querySelector("[data-testid=epic-notes]")!;
    expect(notes.textContent).toContain("Read them with useTotals(country)");
    expect(notes.textContent).toContain("Everything new in checkout goes behind checkout_v2");
    const threads = page.querySelector("[data-testid=epic-threads]")!;
    const text = threads.textContent ?? "";
    expect(text.indexOf("still write billing.country")).toBeLessThan(text.indexOf("Which hook gives the totals"));
    expect(text).toContain("only the two agents write in it");
    const peers = page.querySelector<HTMLElement>("[data-testid=epic-peers]")!;
    expect(peers.textContent).toContain("WC-212");
    const row = [...peers.querySelectorAll<HTMLElement>("button, a")].find((b) => b.textContent?.includes("WC-215"))!;
    await click(row);
    expect(opened).toEqual(["t_215"]);
  });

  test("a person pins a note, replaces one with its words to start from, and retires one", async () => {
    const { page } = await view();
    const field = page.querySelector<HTMLTextAreaElement>("textarea[data-testid=note-text], [data-testid=note-text] textarea")!;
    await setText(field, "**Copy.** Button labels come from `checkout.json`.");
    await click(page.querySelector("[data-testid=pin-note]")!);
    await until(() => page.querySelector("[data-testid=epic-notes] ul")!.textContent?.includes("Button labels come from checkout.json"), "the pinned note");
    expect(field.value).toBe("");

    const replace = [...page.querySelectorAll<HTMLElement>("[data-testid=epic-notes] button")].find((b) => b.textContent === "Replace")!;
    await click(replace);
    expect(field.value).not.toBe("");
    expect(page.querySelector("[data-testid=pin-note]")!.textContent).toMatch(/^Replace note \d+$/);
    const replacing = Number(page.querySelector("[data-testid=pin-note]")!.textContent!.replace(/\D/g, ""));
    await setText(field, "**Copy.** Labels come from `checkout.v2.json`.");
    await click(page.querySelector("[data-testid=pin-note]")!);
    await until(() => page.querySelector("[data-testid=epic-notes] ul")!.textContent?.includes("checkout.v2.json"), "the replacement");
    const current = page.querySelector("[data-testid=epic-notes] ul")!.textContent ?? "";
    expect(current).not.toContain("checkout.json");
    expect(replacing).toBeGreaterThan(0);

    const before = page.querySelectorAll("[data-testid=epic-notes] ul > *").length;
    const retire = [...page.querySelectorAll<HTMLElement>("[data-testid=epic-notes] ul button")].find((b) => b.textContent === "Retire")!;
    await click(retire);
    await click(await until(() => document.querySelector("[data-testid=retire-confirm]"), "the confirmation"));
    await until(() => page.querySelectorAll("[data-testid=epic-notes] ul > *").length === before - 1, "the note retired");
  });

  test("a pin the orchestrator refuses keeps the words and says why", async () => {
    const client = new FixtureClient("a");
    client.pinEpicNote = () => Promise.reject(new ApiError(409, "note_limit", "The epic has 10 notes; replace or retire one first."));
    const { page } = await view(client);
    const field = page.querySelector<HTMLTextAreaElement>("textarea[data-testid=note-text], [data-testid=note-text] textarea")!;
    await setText(field, "One more.");
    await click(page.querySelector("[data-testid=pin-note]")!);
    await until(() => page.textContent?.includes("The epic has 10 notes; replace or retire one first."), "the refusal");
    expect(field.value).toBe("One more.");
  });
});

describe("a Run on an epic", () => {
  test("its header says whom it waits on, its rail its peers and the epic's notes, its transcript its peer turns", async () => {
    const client = new FixtureClient("a");
    const page = await show(<RunScreen client={client} runId={RUN_ID} onBack={() => {}} />, client);
    const waiting = await until(() => page.querySelector("[data-testid=run-waiting-peer]"), "the header's wait");
    expect(waiting.textContent).toMatch(/^Waiting on WC-212 · parks in \d+ min$/);
    const rail = await until(() => page.querySelector("[data-testid=rail-peers]"), "the rail's peers");
    expect(rail.textContent).toContain("WC-215");
    expect(rail.textContent).not.toContain("WC-214");
    expect(page.querySelector("[data-testid=rail-notes]")!.textContent).toContain("2 of 10 pinned · all read");
    const text = page.textContent ?? "";
    expect(text).toContain("useTotals(country)");
    expect(text).toContain("Epic note replaced: 3 → 4");
  });

  test("an ended conductor's transcript has the digest it was briefed with, before its briefing", async () => {
    const client = new FixtureClient("a");
    const page = await show(<RunScreen client={client} runId={CONDUCTOR_ID} onBack={() => {}} />, client);
    const digest = await until(() => [...page.querySelectorAll("*")].find((e) => e.children.length < 6 && e.textContent?.startsWith("Since the conductor last looked")), "the digest");
    expect(digest.textContent).toContain("4 messages · 1 note change · WC-215 awaiting input");
    const text = page.textContent ?? "";
    expect(text.indexOf("Since the conductor last looked")).toBeLessThan(text.indexOf("Conductor for WC-214."));
  });
});
