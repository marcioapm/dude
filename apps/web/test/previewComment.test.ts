/**
 * dude's side of commenting on a preview, without React: the window dude
 * opens, the guards on what it posts, the draft and picture it makes, the
 * pins it answers with, and Show on preview. Messages are MessageEvents
 * with the origin and source the browser would give them.
 */

import { afterEach, beforeEach, expect, mock, test } from "bun:test";
import type { DudeToOverlay, FeedbackItem, OverlayAnchor, RunServer, TaskServers } from "@dude/domain";
import { OVERLAY_LIMITS } from "@dude/domain";
import { PreviewCommenting, commentingOffer, pinsFor, previewOrigins, type CommentingView } from "../src/previewComment.ts";

const WEB = "https://web-shop-1.preview.example.test";
const API = "https://api-shop-1.preview.example.test";
const PNG = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR4nGNgYGBgAAAABQABpfZFQAAAAABJRU5ErkJggg==";

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

function boot() {
  c = new PreviewCommenting({
    win: window as unknown as Window, taskId: "tsk_1", helloWaitMs: 40,
    read: () => ({ origins: previewOrigins(data), items, drafts: items.filter((i) => i.status === "draft").length, taskKey: "SHOP-1", you: "per_me", name: () => "Ana" }),
    upload: upload as never, createDraft: createDraft as never, onSaved: () => {}, onOpenItem: () => {}, onChange: (v) => views.push(v),
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

test("commenting is offered on a live preview's ready servers, or not with the reason, and restart is named", () => {
  expect(commentingOffer(null)).toEqual({ kind: "none" });
  expect(commentingOffer(servers([server("web", WEB)], "agent"))).toEqual({ kind: "none" });
  expect(commentingOffer(servers([server("web", WEB, { commenting: { available: false, reason: "public_url" } })])))
    .toEqual({ kind: "unavailable", reason: "public_url" });
  const offer = commentingOffer(servers([server("web", WEB, { needsRestart: true }), server("api", API, { state: "starting" }), server("plain", "http://x.test")]));
  expect(offer).toEqual({ kind: "available", servers: [{ name: "web", url: WEB, needsRestart: true }], restart: ["web"] });
});
