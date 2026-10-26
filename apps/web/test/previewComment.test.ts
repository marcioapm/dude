/**
 * dude's side of commenting on a preview, without React: the window dude
 * opens, the guards on what it posts, the draft and picture it makes, the
 * pins it answers with, and Show on preview. Messages are MessageEvents
 * with the origin and source the browser would give them.
 */

import { afterEach, beforeEach, expect, mock, test } from "bun:test";
import type { DudeToOverlay, FeedbackItem, OverlayAnchor, RunServer, TaskServers } from "@dude/domain";
import { OVERLAY_LIMITS, overlaySessionSchema } from "@dude/domain";
import { MAX_WINDOWS, PreviewCommenting, checkMessage, commentingOffer, pinsFor, previewOrigins, type CommentingDeps, type CommentingView } from "../src/previewComment.ts";

const WEB = "https://web-shop-1.preview.example.test";
const API = "https://api-shop-1.preview.example.test";
const PNG = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR4nGNgYGBgAAAABQABpfZFQAAAAABJRU5ErkJggg==";
// A 1×1 baseline JPEG.
const JPEG = "data:image/jpeg;base64,/9j/4AAQSkZJRgABAQEASABIAAD/2wBDAP//////////////////////////////////////////////////////////////////////////////////////wgALCAABAAEBAREA/8QAFBABAAAAAAAAAAAAAAAAAAAAAP/aAAgBAQABPxA=";

type Popup = { postMessage: ReturnType<typeof mock>; closed: boolean; focus: () => void };

const server = (name: string, url: string, over: Partial<RunServer> = {}): RunServer => ({
  name, port: 3000, command: ["npm", "start"], workdir: "", env: {}, fromSpec: false, state: "ready", since: "", readySince: null,
  stopReason: null, stoppedEpoch: null, epoch: 1, url, commenting: { available: true }, needsRestart: false, ...over,
});

const servers = (list: RunServer[], kind: "preview" | "agent" = "preview"): TaskServers => ({
  run: { id: "run_p", luxRunId: "lux_p", kind, label: "Branch preview", state: "running", luxState: "running", host: null, startedAt: null, startedBy: null,
    branch: null, commit: null, previewStage: "ready", parksAfterMinutes: null, terminalUrl: null, wakeable: true },
  servers: list, moved: null, recipes: [], preview: null,
});

const item = (id: string, over: Partial<FeedbackItem> & { selector?: string; url?: string } = {}): FeedbackItem => ({
  id, taskId: "tsk_1", attempt: 1, batchId: null, author: { id: "per_ana", name: "Ana Ribeiro" }, body: `words of ${id}`,
  anchor: { kind: "preview", url: over.url ?? `${WEB}/cart`, selector: over.selector ?? "button.pay" }, status: "open", deliveredRunId: null, resolvedCommit: null,
  resolutionPending: false, outdated: false, routedTo: null, submittedAt: new Date().toISOString(), createdAt: new Date().toISOString(),
  updatedAt: new Date().toISOString(), history: [], ...over,
});

let popup: Popup;
let opened: Array<[string, string | undefined, string | undefined]>;
let data: TaskServers;
let items: FeedbackItem[];
let upload: ReturnType<typeof mock>;
let createDraft: ReturnType<typeof mock>;
let views: CommentingView[];
let c: PreviewCommenting;

function boot(over: Partial<CommentingDeps> = {}) {
  c = new PreviewCommenting({
    win: window as unknown as Window, taskId: "tsk_1", helloWaitMs: 40,
    read: () => ({ origins: previewOrigins(data), items, drafts: items.filter((i) => i.status === "draft").length, taskKey: "SHOP-1", you: "per_me", name: () => "Ana" }),
    upload: upload as never, createDraft: createDraft as never, onSaved: () => {}, onOpenItem: () => {}, onChange: (v) => views.push(v),
    ...over,
  });
  c.listen();
}

/** What the page posts, as the browser delivers it to dude's window. */
function post(data: unknown, origin = WEB, source: unknown = popup) {
  window.dispatchEvent(new (window as unknown as { MessageEvent: typeof MessageEvent }).MessageEvent("message", { data, origin, source: source as MessageEventSource }));
}

const told = (): DudeToOverlay[] => popup.postMessage.mock.calls.map((x) => x[0] as DudeToOverlay);
const tick = (ms = 0) => new Promise((r) => setTimeout(r, ms));
const anchor = (over: Partial<OverlayAnchor> = {}): OverlayAnchor => ({ kind: "preview", url: `${WEB}/cart`, selector: "button.pay", viewport: { width: 1280, height: 720 }, ...over });

beforeEach(() => {
  popup = { postMessage: mock(() => {}), closed: false, focus: () => {} };
  opened = [];
  (window as unknown as { open: unknown }).open = (url: string, name?: string, features?: string) => {
    opened.push([url, name, features]);
    return popup;
  };
  data = servers([server("web", WEB), server("api", API, { commenting: { available: true } })]);
  items = [];
  upload = mock(async () => "att_1");
  createDraft = mock(async () => ({ id: "fbk_new", drafts: 1 }));
  views = [];
  boot();
});
afterEach(() => c.dispose());

test("the preview opens in the task's named window, never noopener, and only at one of the task's preview origins", () => {
  expect(c.open(`${WEB}/`, "web")).toBe(true);
  expect(opened).toEqual([[`${WEB}/`, "dude-preview-tsk_1", undefined]]);
  expect(c.open("https://elsewhere.example.test/", "web")).toBe(false);
  expect(opened.length).toBe(1);
});

test("hello from the window dude opened at the task's origin is answered with the page's pins", async () => {
  items = [item("fbk_a"), item("fbk_b", { url: `${WEB}/other` })];
  c.open(`${WEB}/cart`, "web");
  post({ type: "hello", v: 1, url: `${WEB}/cart` });
  const session = told().find((m) => m.type === "session");
  expect(session).toMatchObject({ type: "session", task: "SHOP-1", url: `${WEB}/cart` });
  expect(session && session.type === "session" && session.pins.map((p) => p.id)).toEqual(["fbk_a"]);
  expect(popup.postMessage.mock.calls[0]![1]).toBe(WEB);
  expect(c.view.state).toBe("open");
});

test("the session carries dude's resolved theme, valid by the protocol's schema, and a theme change is sent as a new session", () => {
  let theme: "light" | "dark" = "dark";
  c.dispose();
  boot({ read: () => ({ origins: previewOrigins(data), items, drafts: 0, taskKey: "SHOP-1", you: "per_me", name: () => "Ana", theme }) });
  items = [item("fbk_a")];
  c.open(`${WEB}/cart`, "web");
  post({ type: "hello", v: 1, url: `${WEB}/cart` });
  const first = told().filter((m) => m.type === "session");
  expect(first.map((m) => m.type === "session" && m.theme)).toEqual(["dark"]);
  expect(overlaySessionSchema.safeParse(first[0]).success).toBe(true);
  theme = "light";
  c.refresh();
  const sessions = told().filter((m) => m.type === "session");
  expect(sessions.map((m) => m.type === "session" && m.theme)).toEqual(["dark", "light"]);
  expect(overlaySessionSchema.safeParse(sessions[1]).success).toBe(true);
  // Without a theme to say (an older caller), the session says none.
  c.dispose();
  popup.postMessage.mockClear();
  boot();
  c.open(`${WEB}/cart`, "web");
  post({ type: "hello", v: 1, url: `${WEB}/cart` });
  expect(told().find((m) => m.type === "session")).not.toHaveProperty("theme");
});

test("the session names this tab's window, valid by the protocol, the same name for the tab's every commenting", () => {
  c.open(`${WEB}/cart`, "web");
  post({ type: "hello", v: 1, url: `${WEB}/cart` });
  const session = told().find((m) => m.type === "session");
  const name = session?.type === "session" ? session.dudeWindow : undefined;
  expect(name).toMatch(/^dude-tab-[a-z0-9]{10}$/);
  expect(window.name).toBe(name!);
  expect(overlaySessionSchema.safeParse(session).success).toBe(true);
  // Another task's commenting in the same tab, or the page reloaded, keeps the name an open preview already holds.
  c.dispose();
  popup.postMessage.mockClear();
  boot();
  c.open(`${WEB}/cart`, "web");
  post({ type: "hello", v: 1, url: `${WEB}/cart` });
  expect(told().find((m) => m.type === "session")).toMatchObject({ dudeWindow: name });
});

test("a tab named something else (by a page script, a link's target) is named anew", () => {
  window.name = "something-else";
  c.dispose();
  boot();
  c.open(`${WEB}/cart`, "web");
  post({ type: "hello", v: 1, url: `${WEB}/cart` });
  const session = told().find((m) => m.type === "session");
  expect(session?.type === "session" && session.dudeWindow).toMatch(/^dude-tab-[a-z0-9]{10}$/);
  expect(window.name).toBe((session as { dudeWindow: string }).dudeWindow);
});

test("a tab whose name the browser won't set sends sessions without a window name, still valid by the protocol", () => {
  const own = Object.getOwnPropertyDescriptor(window, "name");
  const setters: Array<[string, (v: string) => void]> = [["ignores writes", () => {}], ["throws", () => { throw new Error("refused"); }]];
  try {
    for (const [what, set] of setters) {
      Object.defineProperty(window, "name", { configurable: true, get: () => "", set });
      c.dispose();
      popup.postMessage.mockClear();
      boot();
      c.open(`${WEB}/cart`, "web");
      post({ type: "hello", v: 1, url: `${WEB}/cart` });
      const session = told().find((m) => m.type === "session");
      expect([what, session?.type]).toEqual([what, "session"]);
      expect([what, session && "dudeWindow" in session]).toEqual([what, false]);
      expect([what, overlaySessionSchema.safeParse(session).success]).toEqual([what, true]);
    }
  } finally {
    if (own) Object.defineProperty(window, "name", own);
    else delete (window as { name?: string }).name;
  }
});

test("a message from an origin that is not one of this task's previews is ignored", async () => {
  c.open(`${WEB}/cart`, "web");
  post({ type: "draft", v: 1, nonce: "n", body: "x", anchor: anchor({ url: "https://evil.example.test/cart" }) }, "https://evil.example.test");
  post({ type: "hello", v: 1, url: "https://evil.example.test/" }, "https://evil.example.test");
  await tick();
  expect(createDraft).not.toHaveBeenCalled();
  expect(told()).toEqual([]);
  // The same origin once it is not a preview of this task's any more.
  data = servers([server("web", WEB)], "agent");
  post({ type: "draft", v: 1, nonce: "n", body: "x", anchor: anchor() });
  await tick();
  expect(createDraft).not.toHaveBeenCalled();
});

test("the window dude opened, once its origin is not one of the task's previews any more, is told to open it again", async () => {
  c.open(`${WEB}/cart`, "web");
  data = servers([server("web", WEB)], "agent");
  post({ type: "draft", v: 1, nonce: "n9", body: "x", anchor: anchor() });
  await tick();
  expect(createDraft).not.toHaveBeenCalled();
  expect(told()).toEqual([{ type: "refused", v: 1, nonce: "n9", reason: "This preview isn't the one at work any more — open it again from dude." }]);
  // Another window at that origin still hears nothing.
  const other = { postMessage: mock(() => {}), closed: false };
  post({ type: "draft", v: 1, nonce: "n", body: "x", anchor: anchor() }, WEB, other);
  expect(other.postMessage).not.toHaveBeenCalled();
  expect(told().length).toBe(1);
});

test("a dude tab that reloaded takes back the window it had opened, by that window's opener, and answers it", async () => {
  // A fresh handler, as after a reload: it holds no window.
  const reopened = { ...popup, opener: window };
  post({ type: "hello", v: 1, url: `${WEB}/cart` }, WEB, reopened);
  expect(popup.postMessage.mock.calls.map((x) => (x[0] as DudeToOverlay).type)).toEqual(["session"]);
  expect(popup.postMessage.mock.calls[0]![1]).toBe(WEB);
  // A window someone else opened is not taken.
  c.dispose();
  boot();
  const stranger = { postMessage: mock(() => {}), closed: false, opener: null };
  post({ type: "hello", v: 1, url: `${WEB}/cart` }, WEB, stranger);
  expect(stranger.postMessage).not.toHaveBeenCalled();
});

test("a message from a window other than the one dude opened is ignored", async () => {
  c.open(`${WEB}/cart`, "web");
  const other = { postMessage: mock(() => {}), closed: false };
  post({ type: "draft", v: 1, nonce: "n", body: "x", anchor: anchor() }, WEB, other);
  post({ type: "draft", v: 1, nonce: "n", body: "x", anchor: anchor() }, WEB, null);
  await tick();
  expect(createDraft).not.toHaveBeenCalled();
  expect(told()).toEqual([]);
  expect(other.postMessage).not.toHaveBeenCalled();
});

test("an oversize message is refused without reaching the API", async () => {
  c.open(`${WEB}/cart`, "web");
  post({ type: "draft", v: 1, nonce: "n", body: "x", anchor: anchor(), picture: `data:image/png;base64,${"A".repeat(OVERLAY_LIMITS.message)}` });
  await tick();
  expect(upload).not.toHaveBeenCalled();
  expect(createDraft).not.toHaveBeenCalled();
  expect(told()).toContainEqual(expect.objectContaining({ type: "refused", nonce: "n", reason: expect.stringContaining("too much to send") }));
});

test("an invalid anchor is refused without reaching the API", async () => {
  c.open(`${WEB}/cart`, "web");
  for (const bad of [anchor({ url: "javascript:alert(1)" }), { ...anchor(), attachmentId: "att_someone" }, { ...anchor(), kind: "code" }, anchor({ url: `${API}/cart` })]) {
    post({ type: "draft", v: 1, nonce: "n", body: "x", anchor: bad });
  }
  await tick();
  expect(createDraft).not.toHaveBeenCalled();
  expect(told().filter((m) => m.type === "refused").length).toBe(4);
});

test("a valid draft uploads its picture as the task's attachment and creates the draft naming it", async () => {
  c.open(`${WEB}/cart`, "web");
  post({ type: "draft", v: 1, nonce: "n1", body: "  Make it green  ", anchor: anchor({ text: "Pay" }), picture: PNG });
  await tick(5);
  expect(upload).toHaveBeenCalledTimes(1);
  const pic = upload.mock.calls[0]![0] as { bytes: Uint8Array; type: string; name: string };
  expect(pic.type).toBe("image/png");
  expect([...pic.bytes.slice(0, 4)]).toEqual([0x89, 0x50, 0x4e, 0x47]);
  expect(createDraft).toHaveBeenCalledWith({ ...anchor({ text: "Pay" }), attachmentId: "att_1" }, "Make it green");
  expect(told()).toContainEqual({ type: "saved", v: 1, nonce: "n1", id: "fbk_new", drafts: 1, picture: "attached" });
  expect(c.view.saved).toBe(1);
});

test("a picture whose bytes are not an image is dropped, and the draft is saved without it, saying so", async () => {
  c.open(`${WEB}/cart`, "web");
  post({ type: "draft", v: 1, nonce: "n1", body: "x", anchor: anchor(), picture: "data:image/png;base64,PHN2Zz48L3N2Zz4=" });
  await tick(5);
  expect(upload).not.toHaveBeenCalled();
  expect(createDraft).toHaveBeenCalledWith(anchor(), "x");
  expect(told()).toContainEqual(expect.objectContaining({ type: "saved", picture: "failed" }));
});

test("a JPEG picture is uploaded as image/jpeg; one whose label and bytes disagree is not uploaded", async () => {
  c.open(`${WEB}/cart`, "web");
  post({ type: "draft", v: 1, nonce: "j1", body: "x", anchor: anchor(), picture: JPEG });
  await tick(5);
  expect(upload).toHaveBeenCalledTimes(1);
  const pic = upload.mock.calls[0]![0] as { bytes: Uint8Array; type: string; name: string };
  expect(pic.type).toBe("image/jpeg");
  expect([...pic.bytes.slice(0, 3)]).toEqual([0xff, 0xd8, 0xff]);
  expect(pic.name).toBe("preview-cart-element.jpg");
  expect(told()).toContainEqual(expect.objectContaining({ type: "saved", nonce: "j1", picture: "attached" }));
  // JPEG bytes labelled PNG, and PNG bytes labelled JPEG: the draft is saved, the picture refused.
  post({ type: "draft", v: 1, nonce: "j2", body: "x", anchor: anchor(), picture: JPEG.replace("image/jpeg", "image/png") });
  await tick(5);
  post({ type: "draft", v: 1, nonce: "j3", body: "x", anchor: anchor(), picture: PNG.replace("image/png", "image/jpeg") });
  await tick(5);
  expect(upload).toHaveBeenCalledTimes(1);
  expect(told()).toContainEqual(expect.objectContaining({ type: "saved", nonce: "j2", picture: "failed" }));
  expect(told()).toContainEqual(expect.objectContaining({ type: "saved", nonce: "j3", picture: "failed" }));
});

test("an upload refused saves the draft without its picture", async () => {
  upload = mock(async () => { throw new Error("image storage is not configured"); });
  c.dispose();
  boot();
  c.open(`${WEB}/cart`, "web");
  post({ type: "draft", v: 1, nonce: "n1", body: "x", anchor: anchor(), picture: PNG });
  await tick(5);
  expect(createDraft).toHaveBeenCalledWith(anchor(), "x");
  expect(told()).toContainEqual(expect.objectContaining({ type: "saved", picture: "failed" }));
});

test("one save at a time: a second draft while one is in flight is refused, and saves again once it is done", async () => {
  let finish!: () => void;
  upload = mock(() => new Promise<string>((r) => { finish = () => r("att_1"); }));
  c.dispose();
  boot();
  c.open(`${WEB}/cart`, "web");
  post({ type: "draft", v: 1, nonce: "n1", body: "x", anchor: anchor(), picture: PNG });
  post({ type: "draft", v: 1, nonce: "n2", body: "y", anchor: anchor(), picture: PNG });
  await tick();
  expect(upload).toHaveBeenCalledTimes(1);
  expect(told()).toEqual([{ type: "refused", v: 1, nonce: "n2", reason: "Still saving the last one" }]);
  finish();
  await tick(5);
  expect(told().at(-1)).toMatchObject({ type: "saved", nonce: "n1" });
  post({ type: "draft", v: 1, nonce: "n3", body: "z", anchor: anchor() });
  await tick(5);
  expect(told().at(-1)).toMatchObject({ type: "saved", nonce: "n3" });
});

test("at the drafts limit a draft is refused before its picture is uploaded", async () => {
  items = Array.from({ length: 50 }, (_, i) => item(`d${i}`, { status: "draft" }));
  c.open(`${WEB}/cart`, "web");
  post({ type: "draft", v: 1, nonce: "n1", body: "x", anchor: anchor(), picture: PNG });
  await tick(5);
  expect(upload).not.toHaveBeenCalled();
  expect(createDraft).not.toHaveBeenCalled();
  expect(told()).toEqual([{ type: "refused", v: 1, nonce: "n1", reason: "you have 50 drafts on this task: submit or remove some first" }]);
});

test("Show on preview sends highlight to the open page, or opens the item's page and marks it once it says hello", async () => {
  const it = item("fbk_s", { url: `${WEB}/cart` });
  items = [it];
  c.open(`${WEB}/cart`, "web");
  post({ type: "hello", v: 1, url: `${WEB}/cart` });
  c.show(it);
  expect(told().at(-1)).toEqual({ type: "highlight", v: 1, itemId: "fbk_s" });
  // Another page: reopened there, marked after its hello.
  const elsewhere = item("fbk_t", { url: `${WEB}/greetings` });
  items = [it, elsewhere];
  c.show(elsewhere);
  expect(opened.at(-1)![0]).toBe(`${WEB}/greetings`);
  post({ type: "hello", v: 1, url: `${WEB}/greetings` });
  expect(told().slice(-2).map((m) => m.type)).toEqual(["session", "highlight"]);
});

test("Comment with the window already open on that server brings it forward instead of opening it again", () => {
  let focused = 0;
  popup.focus = () => { focused++; };
  expect(c.comment(`${WEB}/`, "web")).toBe(true);
  expect(opened.length).toBe(1);
  post({ type: "hello", v: 1, url: `${WEB}/cart` });
  focused = 0;
  expect(c.comment(`${WEB}/`, "web")).toBe(true);
  expect(opened.length).toBe(1);
  expect(focused).toBe(1);
  // Another server, or a closed window: opened.
  c.comment(`${API}/`, "api");
  expect(opened.length).toBe(2);
  popup.closed = true;
  c.comment(`${API}/`, "api");
  expect(opened.length).toBe(3);
});

test("a hash route is its own page for pins; a plain fragment is not", () => {
  const list = [item("cart", { url: `${WEB}/#/cart` }), item("settings", { url: `${WEB}/#!/settings` }), item("top", { url: `${WEB}/#top` })];
  expect(pinsFor(list, `${WEB}/#/cart`, null, () => "Ana").map((p) => p.id)).toEqual(["cart"]);
  expect(pinsFor(list, `${WEB}/#!/settings`, null, () => "Ana").map((p) => p.id)).toEqual(["settings"]);
  expect(pinsFor(list, `${WEB}/`, null, () => "Ana").map((p) => p.id)).toEqual(["top"]);
});

test("a window that never says hello is the preview not answering; one closed says so", async () => {
  c.open(`${WEB}/cart`, "web");
  await tick(60);
  expect(c.view.state).toBe("silent");
  popup.closed = true;
  c.open(`${WEB}/cart`, "web");
  popup.closed = true;
  await tick(1_100);
  expect(c.view.state).toBe("closed");
});

test("pins carry only the page's preview items that name a selector, each no longer than a pin shows", () => {
  const long = "x".repeat(2_000);
  const list = [
    item("a", { body: long }), item("b", { url: `${WEB}/cart?tab=2#top` }), item("c", { url: `${WEB}/greetings` }),
    { ...item("d"), anchor: { kind: "general" as const } }, item("e", { selector: "" }),
    item("f", { status: "draft", author: { id: "per_me", name: "Me Myself" } }),
  ];
  const pins = pinsFor(list, `${WEB}/cart`, "per_me", () => "Ana");
  expect(pins.map((p) => p.id)).toEqual(["a", "b", "f"]);
  expect(pins[0]!.body.length).toBe(OVERLAY_LIMITS.pinBody);
  expect(pins[2]).toMatchObject({ mine: true, status: "draft", n: 3 });
});

test("a 300-character first name or display name still makes a session the overlay accepts", () => {
  const long = "N".repeat(300);
  const list = [item("a", {
    author: { id: "per_ana", name: `${long} Ribeiro` },
    history: [{ id: "h1", kind: "reply", body: "seen", commit: null, personId: "per_ana", runId: null, createdAt: new Date().toISOString() }],
  })];
  const pins = pinsFor(list, `${WEB}/cart`, null, () => long);
  const session = { type: "session", v: 1, task: "SHOP-1", url: `${WEB}/cart`, pins, drafts: 0, showPins: true };
  expect(overlaySessionSchema.safeParse(session).success).toBe(true);
  expect(pins[0]!.author.length).toBe(OVERLAY_LIMITS.name);
  expect(pins[0]!.replies[0]!.who.length).toBe(OVERLAY_LIMITS.name);
});

test("Stop commenting tells the window to take the overlay off, and the view is idle", () => {
  c.open(`${WEB}/cart`, "web");
  post({ type: "hello", v: 1, url: `${WEB}/cart` });
  c.stop();
  expect(told().at(-1)).toEqual({ type: "stop", v: 1 });
  expect(popup.postMessage.mock.calls.at(-1)![1]).toBe(WEB);
  expect(c.view.state).toBe("idle");
});

test("navigated is answered with the session for the page the window is on now", () => {
  items = [item("cart"), item("greet", { url: `${WEB}/greetings` })];
  c.open(`${WEB}/cart`, "web");
  post({ type: "hello", v: 1, url: `${WEB}/cart` });
  post({ type: "navigated", v: 1, url: `${WEB}/greetings` });
  const session = told().at(-1)!;
  expect(session).toMatchObject({ type: "session", url: `${WEB}/greetings` });
  expect(session.type === "session" && session.pins.map((p) => p.id)).toEqual(["greet"]);
});

test("hello or navigated naming another site than the sender's is refused, not answered with pins", () => {
  items = [item("cart")];
  c.open(`${WEB}/cart`, "web");
  post({ type: "hello", v: 1, url: `${API}/cart` });
  post({ type: "navigated", v: 1, url: "https://evil.example.test/cart" });
  expect(told().map((m) => m.type)).toEqual(["refused", "refused"]);
});

test("Open in dude for an item not in the task's list is not passed on", () => {
  const openItem = mock((_id: string) => {});
  c.dispose();
  boot({ onOpenItem: openItem });
  items = [item("fbk_here")];
  c.open(`${WEB}/cart`, "web");
  post({ type: "open", v: 1, itemId: "fbk_elsewhere" });
  post({ type: "open", v: 1, itemId: "fbk_here" });
  expect(openItem.mock.calls).toEqual([["fbk_here"]]);
});

test("a page's pins are never more than the cap", () => {
  const many = Array.from({ length: OVERLAY_LIMITS.pins + 30 }, (_, i) => item(`p${i}`));
  expect(pinsFor(many, `${WEB}/cart`, null, () => "Ana").length).toBe(OVERLAY_LIMITS.pins);
});

test("a server whose commenting is not available gives no origin a draft is taken from", async () => {
  data = servers([server("web", WEB, { commenting: { available: false, reason: "lux" } })]);
  expect(previewOrigins(data).size).toBe(0);
  data = servers([server("web", WEB), server("api", API, { commenting: { available: false, reason: "lux" } })]);
  expect([...previewOrigins(data)]).toEqual([WEB]);
  c.open(`${WEB}/cart`, "web");
  // A window at api's origin, posting as the opened one would: not heard.
  post({ type: "draft", v: 1, nonce: "n", body: "x", anchor: anchor({ url: `${API}/cart` }) }, API);
  await tick();
  expect(createDraft).not.toHaveBeenCalled();
});

test("commenting is offered on a live preview's ready servers, or not with the reason, and restart is named", () => {
  expect(commentingOffer(null)).toEqual({ kind: "none" });
  expect(commentingOffer(servers([server("web", WEB)], "agent"))).toEqual({ kind: "none" });
  expect(commentingOffer(servers([server("web", WEB, { commenting: { available: false, reason: "public_url" } })])))
    .toEqual({ kind: "unavailable", reason: "public_url" });
  const offer = commentingOffer(servers([server("web", WEB, { needsRestart: true }), server("api", API, { state: "starting" }), server("plain", "http://x.test")]));
  expect(offer).toEqual({ kind: "available", servers: [{ name: "web", url: WEB, needsRestart: true }], restart: ["web"] });
});

test("a draft sent again with its nonce is saved once: in flight it is answered when it lands, after that with the same draft", async () => {
  let finish!: () => void;
  upload = mock(() => new Promise<string>((r) => { finish = () => r("att_1"); }));
  c.dispose();
  boot();
  c.open(`${WEB}/cart`, "web");
  post({ type: "draft", v: 1, nonce: "n1", body: "x", anchor: anchor(), picture: PNG });
  post({ type: "draft", v: 1, nonce: "n1", body: "x", anchor: anchor(), picture: PNG });
  await tick();
  expect(told()).toEqual([]);
  finish();
  await tick(5);
  post({ type: "draft", v: 1, nonce: "n1", body: "x", anchor: anchor(), picture: PNG });
  await tick(5);
  expect(createDraft).toHaveBeenCalledTimes(1);
  expect(upload).toHaveBeenCalledTimes(1);
  const saved = told().filter((m) => m.type === "saved");
  expect(saved).toHaveLength(2);
  expect(saved.map((m) => m.type === "saved" && m.id)).toEqual(["fbk_new", "fbk_new"]);
});

test("a window taken back after a reload is brought forward by Comment on its own server, not another's", () => {
  let focused = 0;
  const reopened = { ...popup, opener: window, focus: () => { focused++; } };
  post({ type: "hello", v: 1, url: `${WEB}/cart` }, WEB, reopened);
  expect(c.comment(`${WEB}/`, "web")).toBe(true);
  expect(opened.length).toBe(0);
  expect(focused).toBe(1);
  c.comment(`${API}/`, "api");
  expect(opened.length).toBe(1);
});

test("what one window saved is not answered to the next: a new window starts with nothing remembered", async () => {
  c.open(`${WEB}/cart`, "web");
  post({ type: "draft", v: 1, nonce: "n1", body: "x", anchor: anchor() });
  await tick(5);
  const next = { postMessage: mock(() => {}), closed: false, focus: () => {} };
  (window as unknown as { open: unknown }).open = () => next;
  c.open(`${WEB}/cart`, "web");
  post({ type: "draft", v: 1, nonce: "n1", body: "x", anchor: anchor() }, WEB, next);
  await tick(5);
  expect(createDraft).toHaveBeenCalledTimes(2);
});

test("a window taken back after a reload starts with nothing remembered: its old nonces are saved again", async () => {
  c.open(`${WEB}/cart`, "web");
  post({ type: "draft", v: 1, nonce: "n1", body: "x", anchor: anchor() });
  await tick(5);
  popup.closed = true;
  const reopened = { postMessage: mock(() => {}), closed: false, focus: () => {}, opener: window };
  post({ type: "hello", v: 1, url: `${WEB}/cart` }, WEB, reopened);
  post({ type: "draft", v: 1, nonce: "n1", body: "x", anchor: anchor() }, WEB, reopened);
  await tick(5);
  expect(createDraft).toHaveBeenCalledTimes(2);
});

test("checkMessage refuses a source that is not one of dude's windows, and tells only one of them that its origin moved", () => {
  const origins = new Set([WEB]);
  const hello = { type: "hello", v: 1, url: `${WEB}/cart` };
  expect(checkMessage({ origin: WEB, source: popup as unknown as Window, data: hello }, false, origins)).toEqual({ ok: false, why: "source" });
  expect(checkMessage({ origin: WEB, source: null, data: hello }, true, origins)).toEqual({ ok: false, why: "source" });
  expect(checkMessage({ origin: WEB, source: popup as unknown as Window, data: hello }, true, origins)).toMatchObject({ ok: true });
  expect(checkMessage({ origin: API, source: popup as unknown as Window, data: hello }, false, origins, API)).toEqual({ ok: false, why: "origin" });
  expect(checkMessage({ origin: API, source: popup as unknown as Window, data: hello }, true, origins, API)).toMatchObject({ ok: false, why: "moved" });
});

type Win = { postMessage: ReturnType<typeof mock>; closed: boolean; focus: () => void; opener?: unknown };
const win = (opener: unknown = null): Win => ({ postMessage: mock(() => {}), closed: false, focus: () => {}, opener });
const types = (w: Win) => w.postMessage.mock.calls.map((x) => (x[0] as DudeToOverlay).type);

test("two windows of one task, the named one and a tab opened from it, are both answered, and each draft is saved to its own window", async () => {
  c.open(`${WEB}/cart`, "web");
  post({ type: "hello", v: 1, url: `${WEB}/cart` });
  // A target=_blank link on the preview: its opener is the named window, whose opener is this tab.
  const second = win({ ...popup, opener: window });
  post({ type: "hello", v: 1, url: `${WEB}/greetings` }, WEB, second);
  expect(types(second)).toEqual(["session"]);
  expect(second.postMessage.mock.calls[0]![1]).toBe(WEB);
  expect(c.windows.length).toBe(2);
  post({ type: "draft", v: 1, nonce: "a", body: "first", anchor: anchor() });
  post({ type: "draft", v: 1, nonce: "b", body: "second", anchor: anchor({ url: `${WEB}/greetings` }) }, WEB, second);
  await tick(5);
  expect(createDraft).toHaveBeenCalledTimes(2);
  expect(told().filter((m) => m.type === "saved").map((m) => m.type === "saved" && m.nonce)).toEqual(["a"]);
  expect(second.postMessage.mock.calls.map((x) => x[0] as DudeToOverlay).filter((m) => m.type === "saved").map((m) => m.type === "saved" && m.nonce)).toEqual(["b"]);
  expect(c.view).toMatchObject({ state: "open", saved: 2 });
});

test("a window reachable only through two openers is adopted; one whose openers never reach this tab, or from another origin, is not", () => {
  const middle = win(window);
  const far = win(middle);
  post({ type: "hello", v: 1, url: `${WEB}/cart` }, WEB, far);
  expect(types(far)).toEqual(["session"]);
  expect(c.windows).toEqual([far as unknown as Window]);
  // Openers that end in another tab.
  const strangers = win(win(win({ postMessage() {}, closed: false })));
  post({ type: "hello", v: 1, url: `${WEB}/cart` }, WEB, strangers);
  expect(types(strangers)).toEqual([]);
  // Linked to this tab, but at an origin that is no preview of this task's.
  const elsewhere = win(window);
  post({ type: "hello", v: 1, url: "https://evil.example.test/" }, "https://evil.example.test", elsewhere);
  expect(types(elsewhere)).toEqual([]);
  // A hello naming another origin than the one the browser says it came from.
  const lying = win(window);
  post({ type: "hello", v: 1, url: `${API}/` }, WEB, lying);
  expect(types(lying)).toEqual([]);
  // An opener chain too long: this tab is six steps up.
  let deep: unknown = window;
  for (let i = 0; i < 6; i++) deep = win(deep);
  post({ type: "hello", v: 1, url: `${WEB}/cart` }, WEB, deep);
  expect(types(deep as Win)).toEqual([]);
  // An opener that can't be read.
  const sealed = win();
  Object.defineProperty(sealed, "opener", { get: () => { throw new Error("blocked"); } });
  post({ type: "hello", v: 1, url: `${WEB}/cart` }, WEB, sealed);
  expect(types(sealed)).toEqual([]);
  expect(c.windows.length).toBe(1);
});

test("a draft never adopts a window, even one linked to this tab", async () => {
  const linked = win(window);
  post({ type: "draft", v: 1, nonce: "n", body: "x", anchor: anchor() }, WEB, linked);
  await tick(5);
  expect(createDraft).not.toHaveBeenCalled();
  expect(types(linked)).toEqual([]);
  expect(c.windows.length).toBe(0);
});

test("this tab's own opener is adopted only when the tab came from Connect to dude", () => {
  const preview = win();
  const own = Object.getOwnPropertyDescriptor(window, "opener");
  Object.defineProperty(window, "opener", { configurable: true, get: () => preview });
  try {
    post({ type: "hello", v: 1, url: `${WEB}/cart` }, WEB, preview);
    expect(types(preview)).toEqual([]);
    let arrived = true;
    c.dispose();
    boot({ openerArrived: () => arrived });
    post({ type: "hello", v: 1, url: `${WEB}/cart` }, WEB, preview);
    expect(types(preview)).toEqual(["session"]);
    // Another window, not this tab's opener, still is not.
    const other = win();
    post({ type: "hello", v: 1, url: `${WEB}/cart` }, WEB, other);
    expect(types(other)).toEqual([]);
    arrived = false;
    c.dispose();
    preview.postMessage.mockClear();
    boot({ openerArrived: () => arrived });
    post({ type: "hello", v: 1, url: `${WEB}/cart` }, WEB, preview);
    expect(types(preview)).toEqual([]);
  } finally {
    if (own) Object.defineProperty(window, "opener", own);
    else delete (window as { opener?: unknown }).opener;
  }
});

test("no more than MAX_WINDOWS are held: a closed one makes room, and a ninth live window is not adopted", () => {
  c.open(`${WEB}/cart`, "web");
  post({ type: "hello", v: 1, url: `${WEB}/cart` });
  const adopted = Array.from({ length: MAX_WINDOWS - 1 }, () => win(window));
  for (const w of adopted) post({ type: "hello", v: 1, url: `${WEB}/cart` }, WEB, w);
  expect(c.windows.length).toBe(MAX_WINDOWS);
  adopted[3]!.closed = true;
  const next = win(window);
  post({ type: "hello", v: 1, url: `${WEB}/cart` }, WEB, next);
  expect(c.windows.length).toBe(MAX_WINDOWS);
  expect(c.windows).not.toContain(adopted[3] as unknown as Window);
  expect(c.windows).toContain(next as unknown as Window);
  // None closed: a ninth live window is not adopted, and every live one stays.
  const last = win(window);
  post({ type: "hello", v: 1, url: `${WEB}/cart` }, WEB, last);
  expect(c.windows.length).toBe(MAX_WINDOWS);
  expect(types(last)).toEqual([]);
  expect(c.windows).toContain(adopted[0] as unknown as Window);
  expect(c.windows).toContain(popup as unknown as Window);
});

test("a hello dude can't read (another protocol version, the wrong shape, too big) adopts no window", () => {
  const unreadable = [
    { type: "hello", v: 99, url: `${WEB}/cart` },
    { type: "hello", v: 1, url: `${WEB}/cart`, extra: { nested: true } },
    { type: "navigated", v: 1, url: `${WEB}/${"x".repeat(OVERLAY_LIMITS.message)}` },
  ];
  const linked = Array.from({ length: 12 }, () => win(window));
  linked.forEach((w, i) => post(unreadable[i % unreadable.length], WEB, w));
  expect(c.windows.length).toBe(0);
  expect(c.view.state).toBe("idle");
  expect(linked.flatMap(types)).toEqual([]);
});

test("past MAX_WINDOWS live linked windows, hellos don't evict and re-adopt, so a save sent again is not created twice", async () => {
  let land!: () => void;
  createDraft = mock(() => new Promise<{ id: string; drafts: number }>((r) => { land = () => r({ id: "fbk_new", drafts: 1 }); }));
  c.dispose();
  boot();
  const linked = Array.from({ length: MAX_WINDOWS + 1 }, () => win(window));
  for (const w of linked) post({ type: "hello", v: 1, url: `${WEB}/cart` }, WEB, w);
  const first = linked[0]!;
  post({ type: "draft", v: 1, nonce: "n", body: "x", anchor: anchor() }, WEB, first);
  await tick();
  land();
  await tick(5);
  const sessionsBefore = linked.map((w) => types(w).filter((t) => t === "session").length);
  for (let round = 0; round < 3; round++) for (const w of linked) post({ type: "hello", v: 1, url: `${WEB}/cart` }, WEB, w);
  post({ type: "draft", v: 1, nonce: "n", body: "x", anchor: anchor() }, WEB, first);
  await tick(5);
  expect(createDraft).toHaveBeenCalledTimes(1);
  // The ninth is never held, so never answered; the eight held are each answered once per hello.
  expect(types(linked[MAX_WINDOWS]!)).toEqual([]);
  expect(linked.map((w, i) => types(w).filter((t) => t === "session").length - sessionsBefore[i]!)).toEqual([...Array(MAX_WINDOWS).fill(3), 0]);
});

test("refresh sends the session to every open window, each for its own page", () => {
  items = [item("cart"), item("greet", { url: `${WEB}/greetings` })];
  c.open(`${WEB}/cart`, "web");
  post({ type: "hello", v: 1, url: `${WEB}/cart` });
  const second = win(window);
  post({ type: "hello", v: 1, url: `${WEB}/greetings` }, WEB, second);
  const closed = win(window);
  post({ type: "hello", v: 1, url: `${WEB}/cart` }, WEB, closed);
  closed.closed = true;
  popup.postMessage.mockClear();
  second.postMessage.mockClear();
  closed.postMessage.mockClear();
  c.refresh();
  const only = (w: Win) => w.postMessage.mock.calls.map((x) => x[0] as DudeToOverlay).map((m) => m.type === "session" ? m.pins.map((p) => p.id) : m.type);
  expect([only(popup as Win), only(second), only(closed)]).toEqual([[["cart"]], [["greet"]], []]);
});

test("Show on preview and a Page link go to the adopted window already on that page, not the named one", () => {
  const it = item("fbk_g", { url: `${WEB}/greetings` });
  items = [it];
  c.open(`${WEB}/cart`, "web");
  post({ type: "hello", v: 1, url: `${WEB}/cart` });
  let focused = 0;
  const second = { ...win(window), focus: () => { focused++; } };
  post({ type: "hello", v: 1, url: `${WEB}/greetings` }, WEB, second);
  expect(c.show(it)).toBe(true);
  expect(types(second).at(-1)).toBe("highlight");
  expect(opened.length).toBe(1);
  expect(c.visit(`${WEB}/greetings?x=1`, "web")).toBe(true);
  expect(opened.length).toBe(1);
  expect(focused).toBe(2);
});
