/**
 * Several servers in one dude tab (the project Servers page's windows), and
 * Preview on a window this person already has open in another dude tab:
 * the window is reached by name in this browser, claimed, and the tab that
 * had it shows it moved and answers it no more. Windows and tabs are fakes
 * with the browser's rules: a named window is shared by the browser, its
 * location unreadable once it shows another origin's page.
 */

import { afterEach, beforeEach, expect, mock, test } from "bun:test";
import type { DudeToOverlay, FeedbackItem, PreviewFeedbackItem, RunningRun } from "@dude/domain";
import { overlaySessionSchema } from "@dude/domain";
import { ApiClient, type BranchPreviewServerAt } from "../src/api/client.ts";
import { MAX_WINDOWS, PreviewCommenting } from "../src/previewComment.ts";
import { ServerWindows, branchWindowName } from "../src/serverWindows.ts";

const WEB = "https://web-pv.preview.example.test";
const ADMIN = "https://admin-pv.preview.example.test";
const TASK = "https://web-wi.preview.example.test";

type Popup = { postMessage: ReturnType<typeof mock>; closed: boolean; focus: () => void; foreign: boolean; readonly location: { href: string } };
const popup = (): Popup => ({
  postMessage: mock(() => {}), closed: false, focus: () => {}, foreign: false,
  get location() {
    if (this.foreign) throw new DOMException("cross-origin", "SecurityError");
    return { href: "about:blank" };
  },
});

/** The browser: named windows shared by every tab of it. */
let named: Map<string, Popup>;
type Tab = EventTarget & { name: string; open: (url: string, name?: string) => Popup | null } & Record<string, unknown>;
function tab(): Tab {
  const t = new EventTarget() as Tab;
  Object.assign(t, {
    name: "", opener: null, localStorage: window.localStorage, sessionStorage: window.sessionStorage,
    setTimeout: window.setTimeout.bind(window), clearTimeout: window.clearTimeout.bind(window),
    setInterval: window.setInterval.bind(window), clearInterval: window.clearInterval.bind(window),
    open: (url: string, name = "") => {
      let w = named.get(name);
      if (!w || w.closed) {
        w = popup();
        if (name) named.set(name, w);
      }
      if (url) w.foreign = true;
      return w;
    },
  });
  return t;
}

const say = (to: Tab, data: unknown, origin: string, source: Popup) =>
  to.dispatchEvent(new window.MessageEvent("message", { data, origin, source: source as unknown as MessageEventSource }));
const told = (w: Popup): DudeToOverlay[] => w.postMessage.mock.calls.map((c) => c[0] as DudeToOverlay);
const tick = (ms = 5) => new Promise((r) => setTimeout(r, ms));

const branchRun: RunningRun = { id: "run_pv", kind: "preview", task: null, branch: "feat/checkout", state: "running", startedAt: null,
  startedBy: { id: "u_ana", name: "Ana" },
  servers: [
    { name: "web", port: 5173, url: WEB, state: "ready", since: null, readySince: null, feedback: { open: 1, resolved: 0, authors: 1 } },
    { name: "admin", port: 5174, url: ADMIN, state: "ready", since: null, readySince: null, feedback: { open: 0, resolved: 0, authors: 0 } },
  ] };
const taskRun: RunningRun = { id: "run_wi", kind: "task", task: { id: "wi_1", key: "WEB-1", title: "T" }, branch: "dude/WEB-1", state: "running",
  startedAt: null, startedBy: null,
  servers: [{ name: "web", port: 5173, url: TASK, state: "ready", since: null, readySince: null, commenting: { available: true }, feedback: { open: 0, resolved: 0, authors: 0 } }] };

const saved: PreviewFeedbackItem = { id: "fbi_1", projectId: "p", previewRunId: "run_pv", serverName: "web", author: { id: "u_ana", name: "Ana Ribeiro" },
  body: "Too close", anchor: { kind: "preview", url: `${WEB}/cart`, selector: "#pay" }, captureSha: null, status: "resolved",
  createdAt: new Date().toISOString(), resolvedAt: new Date().toISOString(), resolvedBy: "u_tom", resolutionNote: "Moved it",
  history: [{ status: "resolved", personId: "u_tom", note: "Moved it", at: new Date().toISOString() }] };

class Client extends ApiClient {
  constructor() { super({}); }
  submits = mock(async (_run: string, _server: string, input: { submissionKey: string }) => ({ id: `fbi_${input.submissionKey}` }) as PreviewFeedbackItem);
  override async previewFeedback() { return { feedback: [saved], nextCursor: null }; }
  override async feedback() { return { feedback: [] as FeedbackItem[], blocking: 0 }; }
  override async validatePreviewServer(runId: string, server: string): Promise<BranchPreviewServerAt> {
    return { kind: "branch", organizationId: "o", organizationSlug: "o", projectId: "p", previewRunId: runId, branch: "feat/checkout", server };
  }
  override submitPreviewFeedback = this.submits as unknown as ApiClient["submitPreviewFeedback"];
}

let disposers: Array<() => void>;
beforeEach(() => {
  named = new Map();
  disposers = [];
});
afterEach(() => { for (const d of disposers) d(); });

function windows(t: Tab, client = new Client()) {
  const changes = mock(() => {});
  const w = new ServerWindows(t as unknown as Window, client, { you: "u_me", name: () => "Tom" }, changes);
  disposers.push(() => w.dispose());
  return { w, client, changes };
}

test("two servers of two Runs, a branch preview's and a task's, are connected to one tab, each in its own window", async () => {
  const me = tab();
  const { w } = windows(me);
  expect(w.open(branchRun, branchRun.servers[0]!)).toBe(true);
  expect(w.open(taskRun, taskRun.servers[0]!)).toBe(true);
  const web = named.get(branchWindowName({ runId: "run_pv", server: "web" }))!;
  const task = named.get("dude-preview-wi_1")!;
  expect(web).toBeDefined();
  expect(task).toBeDefined();
  expect(web).not.toBe(task);
  say(me, { type: "hello", v: 1, url: `${WEB}/cart` }, WEB, web);
  say(me, { type: "hello", v: 1, url: `${TASK}/chat` }, TASK, task);
  await tick();
  expect([...w.connected()].sort()).toEqual(["run_pv/web", "run_wi/web"]);
  const branchSession = told(web).find((m) => m.type === "session")!;
  expect(overlaySessionSchema.safeParse(branchSession).success).toBe(true);
  // The branch window gets its saved feedback as pins, open and resolved; the task's is in task mode.
  expect(branchSession).toMatchObject({ mode: "branch", task: "feat/checkout", drafts: 0 });
  expect(branchSession.type === "session" && branchSession.pins.map((p) => [p.id, p.status, p.replies[0]?.kind])).toEqual([["fbi_1", "resolved", "resolve"]]);
  expect(told(task).find((m) => m.type === "session")).not.toHaveProperty("mode");
  // Each origin is its own window's: the task window's origin is not the branch server's.
  say(me, { type: "hello", v: 1, url: `${TASK}/x` }, TASK, web);
  await tick();
  expect(told(web).filter((m) => m.type === "session").every((m) => m.type === "session" && m.url.startsWith(WEB))).toBe(true);
});

test("a branch window's comment is submitted once at once with its nonce as the key, and answered only to that window", async () => {
  const me = tab();
  const { w, client } = windows(me);
  w.open(branchRun, branchRun.servers[0]!);
  const web = named.get(branchWindowName({ runId: "run_pv", server: "web" }))!;
  say(me, { type: "hello", v: 1, url: `${WEB}/cart` }, WEB, web);
  await tick();
  const draft = { type: "draft", v: 1, nonce: "n1", body: "Hi", anchor: { kind: "preview", url: `${WEB}/cart`, selector: "#pay" } };
  say(me, draft, WEB, web);
  say(me, draft, WEB, web);
  await tick();
  say(me, draft, WEB, web);
  await tick();
  expect(client.submits).toHaveBeenCalledTimes(1);
  expect(client.submits.mock.calls[0]![2]).toMatchObject({ submissionKey: "n1", body: "Hi" });
  expect(told(web).filter((m) => m.type === "saved").length).toBe(2);
  // Another site's page in the same window is not answered.
  say(me, { ...draft, nonce: "n2" }, "https://evil.example.test", web);
  await tick();
  expect(client.submits).toHaveBeenCalledTimes(1);
});

test("Preview on a server whose window is connected to another of my dude tabs claims it; that tab shows it moved and stops answering", async () => {
  const before = tab();
  const now = tab();
  const a = windows(before);
  const b = windows(now);
  a.w.open(branchRun, branchRun.servers[1]!);
  const admin = named.get(branchWindowName({ runId: "run_pv", server: "admin" }))!;
  say(before, { type: "hello", v: 1, url: `${ADMIN}/orders` }, ADMIN, admin);
  await tick();
  expect([...a.w.connected()]).toEqual(["run_pv/admin"]);

  // The other tab's Preview: the same named window is found, not navigated; it is told to come over.
  expect(b.w.open(branchRun, branchRun.servers[1]!)).toBe(true);
  expect(told(admin).at(-1)).toEqual({ type: "claim", v: 1 });
  // The overlay tells its old tab it left and says hello to the new one.
  say(before, { type: "left", v: 1 }, ADMIN, admin);
  say(now, { type: "hello", v: 1, url: `${ADMIN}/orders` }, ADMIN, admin);
  await tick();
  expect([...b.w.connected()]).toEqual(["run_pv/admin"]);
  expect([...a.w.connected()]).toEqual([]);
  expect([...a.w.moved()]).toEqual(["run_pv/admin"]);

  // The losing tab answers nothing more from it: no session, no save.
  const sessionsBefore = told(admin).length;
  say(before, { type: "hello", v: 1, url: `${ADMIN}/orders` }, ADMIN, admin);
  say(before, { type: "draft", v: 1, nonce: "x", body: "b", anchor: { kind: "preview", url: `${ADMIN}/orders`, selector: "#f" } }, ADMIN, admin);
  await tick();
  expect(told(admin).length).toBe(sessionsBefore);
  expect(a.client.submits).not.toHaveBeenCalled();
});

test("a window opened fresh (nothing of that name yet) is navigated, not claimed", () => {
  const me = tab();
  const { w } = windows(me);
  w.open(branchRun, branchRun.servers[0]!);
  const web = named.get(branchWindowName({ runId: "run_pv", server: "web" }))!;
  expect(told(web).some((m) => (m as { type: string }).type === "claim")).toBe(false);
  expect(web.foreign).toBe(true);
});

test("at the cap of live windows a new one is refused, never an open one let go", () => {
  const me = tab();
  const c = new PreviewCommenting({ win: me as unknown as Window, taskId: "wi_cap", helloWaitMs: 1000,
    read: () => ({ origins: new Set([TASK]), items: [], drafts: 0, taskKey: "K", you: null, name: () => "x" }),
    upload: async () => "", createDraft: async () => ({ id: "", drafts: 0 }), onSaved: () => {}, onOpenItem: () => {}, onChange: () => {} });
  c.listen();
  disposers.push(() => c.dispose());
  // Eight live windows linked to this tab (opened from it), each said hello.
  const live = Array.from({ length: MAX_WINDOWS }, () => Object.assign(popup(), { opener: me }));
  for (const p of live) say(me, { type: "hello", v: 1, url: `${TASK}/` }, TASK, p);
  expect(c.windows.length).toBe(MAX_WINDOWS);
  // The task's named window is a ninth: refused, every live one kept.
  expect(c.open(`${TASK}/`, "web")).toBe(false);
  expect(c.windows.length).toBe(MAX_WINDOWS);
  for (const p of live) expect(c.windows).toContain(p as unknown as Window);
  // One closes: now there is room.
  live[2]!.closed = true;
  expect(c.open(`${TASK}/`, "web")).toBe(true);
  expect(c.windows).not.toContain(live[2] as unknown as Window);
});
