/**
 * The preview overlay in happy-dom, on a page of its own window: a fake
 * opener stands for dude's tab, messages are MessageEvents with the origin
 * and source the browser would give them. The overlay's root is closed, so
 * the tests hold it by catching attachShadow's return, as nothing in the
 * page can.
 */

import { afterEach, beforeEach, expect, mock, test } from "bun:test";
import type { DudeToOverlay, OverlayPin, OverlayTheme, OverlayToDude } from "@dude/domain/previewOverlayProtocol";
import { OVERLAY_LIMITS } from "@dude/domain/previewOverlayProtocol";
import { Overlay } from "../overlay.ts";
import { PictureError } from "../picture.ts";
import type { RunningRun } from "@dude/domain";
import { ApiClient, ApiError, type BranchPreviewServerAt } from "../../src/api/client.ts";
import { ServerWindows, markServerLanding } from "../../src/serverWindows.ts";

// happy-dom's Window, from the window the repo's preload (packages/design-system/test/dom.ts) made: a fresh one per test.
const Window = (globalThis.window as unknown as { constructor: new (o: object) => object }).constructor;

const DUDE = "https://dude.example.test";
const PAGE = "https://web-wi-1-shop.preview.example.test/cart";

type Opener = { postMessage: ReturnType<typeof mock>; closed: boolean; focus: () => void };

let win: any;
let opener: Opener | null;
let overlay: Overlay;
let root: ShadowRoot;
let requests: string[];
let watchLoads: (node: Node) => void;

function page(html: string, withOpener = true) {
  win = new Window({ url: PAGE, width: 1280, height: 720 });
  win.document.body.innerHTML = html;
  opener = withOpener ? { postMessage: mock(() => {}), closed: false, focus: () => {} } : null;
  Object.defineProperty(win, "opener", { configurable: true, get: () => opener });
  requests = [];
  // Every way a page's script asks the network, recorded: the overlay must use none.
  win.fetch = ((url: RequestInfo | URL) => {
    requests.push(String(url));
    return Promise.reject(new Error("no network in tests"));
  }) as typeof fetch;
  const open = win.XMLHttpRequest.prototype.open;
  win.XMLHttpRequest.prototype.open = function (this: XMLHttpRequest, method: string, url: string | URL) {
    requests.push(String(url));
    return open.call(this, method, url, true);
  } as typeof open;
  Object.defineProperty(win.navigator, "sendBeacon", { configurable: true, value: (url: string) => { requests.push(url); return true; } });
  for (const name of ["EventSource", "WebSocket"]) {
    win[name] = function (url: string | URL) { requests.push(`${name} ${String(url)}`); throw new Error("no network in tests"); };
  }
  // A load named by an element the page holds (an image, a script, a stylesheet, a frame), however it got there.
  const loads = (el: Element) => {
    const name = el.localName;
    const url = name === "link" ? el.getAttribute("href") : ["img", "script", "iframe", "frame", "source", "video", "audio"].includes(name) ? el.getAttribute("src") : null;
    if (url && !url.startsWith("data:") && !url.startsWith("blob:")) requests.push(`${name} ${url}`);
  };
  const setAttribute = win.Element.prototype.setAttribute;
  win.Element.prototype.setAttribute = function (this: Element, name: string, value: string) {
    setAttribute.call(this, name, value);
    // Connected or not: a detached <img> or <script> with a src loads too.
    if ((name === "src" || name === "href") && !this.isConnected) loads(this);
  };
  watchLoads = (node: Node) => new win.MutationObserver((records: MutationRecord[]) => {
    for (const r of records) {
      if (r.type === "attributes") loads(r.target as Element);
      for (const n of r.addedNodes) if (n.nodeType === 1) { loads(n as Element); (n as Element).querySelectorAll("*").forEach(loads); }
    }
  }).observe(node, { subtree: true, childList: true, attributes: true, attributeFilter: ["src", "href"] });
  watchLoads(win.document);
  const attach = win.HTMLElement.prototype.attachShadow;
  win.HTMLElement.prototype.attachShadow = function (this: HTMLElement, init: ShadowRootInit) {
    const r = attach.call(this, init);
    if (this.localName === "dude-preview-overlay") {
      root = r;
      watchLoads(r);
    }
    return r;
  };
}

function boot(opts: Partial<ConstructorParameters<typeof Overlay>[1]> = {}) {
  overlay = new Overlay(win as unknown as globalThis.Window, { dudeOrigin: DUDE, sessionWaitMs: 50, draw: () => Promise.resolve("data:image/png;base64,iVBORw0KGgo="), ...opts });
  overlay.start();
}

/** dude's message to the overlay, as the browser delivers it. */
function fromDude(data: DudeToOverlay, origin = DUDE, source: unknown = opener) {
  win.dispatchEvent(new win.MessageEvent("message", { data, origin, source: source as MessageEventSource }));
}

const sent = (): OverlayToDude[] => (opener?.postMessage.mock.calls ?? []).map((c) => c[0] as OverlayToDude);
const q = (sel: string) => root.querySelector(sel) as HTMLElement | null;
const count = (sel: string) => root.querySelectorAll(sel).length;
const tid = (id: string) => q(`[data-testid="${id}"]`);
const tick = (ms = 0) => new Promise((r) => setTimeout(r, ms));

function connect(pins: OverlayPin[] = [], drafts = 0) {
  fromDude({ type: "session", v: 1, task: "SHOP-1", url: PAGE, pins, drafts, showPins: true });
}

/** happy-dom lays nothing out: an element given the box a browser would. */
function sized(el: Element, x = 100, y = 200, width = 90, height = 30): Element {
  (el as HTMLElement).getBoundingClientRect = () => ({ x, y, left: x, top: y, width, height, right: x + width, bottom: y + height, toJSON: () => ({}) }) as DOMRect;
  return el;
}

function hover(el: Element) {
  el.dispatchEvent(new win.MouseEvent("mouseover", { bubbles: true, composed: true }));
}

const CART = `<main><section class="cart"><form id="checkout"><button class="pay" type="submit">Pay now</button></form></section><p class="other">x</p></main>`;

beforeEach(() => page(CART));
afterEach(async () => {
  overlay?.stop();
  // The load observers report on the next turn.
  await tick(5);
  expect(requests).toEqual([]);
  win.happyDOM?.abort?.();
});

test("it says hello to its opener, posted only to dude's origin, and connects on dude's session", () => {
  boot();
  expect(opener!.postMessage).toHaveBeenCalledWith({ type: "hello", v: 1, url: PAGE }, DUDE);
  expect(tid("ov-conn")!.textContent).toBe("reaching dude…");
  connect();
  expect(tid("ov-conn")!.textContent).toBe("connected to dude");
  expect(count('[data-testid="ov-comment"]')).toBe(1);
});

test("branch mode offers direct submission and the server's saved feedback as pins, no task drafts", () => {
  boot();
  fromDude({ type: "session", v: 1, mode: "branch", task: "feature/cart", url: PAGE, pins: [], drafts: 0, showPins: false });
  expect(count('[data-testid="ov-pins"]')).toBe(1);
  tid("ov-comment")!.click();
  const button = sized(win.document.querySelector("button.pay")!); hover(button);
  button.dispatchEvent(new win.MouseEvent("click", { bubbles: true, composed: true }));
  expect(tid("ov-save")!.textContent).toBe("Submit feedback");
  expect(tid("ov-comment-box")!.textContent).not.toContain("Private draft");
  expect(tid("ov-comment-box")!.textContent).not.toContain("only through the CLI and API");
  expect(tid("ov-comment-box")!.textContent).toContain("Everyone in the project sees it in dude");
});

test("branch mode pins the page's saved feedback, open and resolved, with the resolution, and View in dude goes to dude's tab", () => {
  page(CART);
  boot();
  sized(win.document.querySelector("button.pay")!);
  const pins: OverlayPin[] = [
    { id: "fbi_1", n: 1, status: "open", author: "Ana", mine: false, body: "Too close", selector: "button.pay", when: "submitted 4 min ago", replies: [] },
    { id: "fbi_2", n: 2, status: "resolved", author: "Tom", mine: false, body: "Hard to read", selector: "p.other", when: "submitted 31 min ago",
      replies: [{ who: "Márcio", kind: "resolve", body: "More contrast" }] },
  ];
  sized(win.document.querySelector("p.other")!, 100, 300);
  fromDude({ type: "session", v: 1, mode: "branch", task: "feature/cart", url: PAGE, pins, drafts: 0, showPins: true });
  expect([...root.querySelectorAll('[data-testid="ov-pin"]')].map((p) => (p as HTMLElement).dataset.status)).toEqual(["open", "resolved"]);
  tid("ov-pins")!.click();
  expect(count('[data-testid="ov-row"]')).toBe(2);
  (root.querySelector('[data-testid="ov-pin"][data-id="fbi_2"]') as HTMLElement).click();
  expect(tid("ov-popover")!.textContent).toContain("resolved it");
  expect(tid("ov-popover")!.textContent).toContain("More contrast");
  expect(count('[data-k="open-in-dude"]')).toBe(0);
  tid("ov-view-dude")!.click();
  // View in dude never asks dude to open a task item.
  expect(sent().some((m) => m.type === "open")).toBe(false);
});

test("an oversize or malformed branch session is refused: no pins, nothing connected", () => {
  boot();
  const pin = { id: "fbi_1", n: 1, status: "open", author: "Ana", mine: false, body: "x", selector: "button.pay", when: "now", replies: [] };
  for (const bad of [
    { type: "session", v: 1, mode: "branch", task: "feature/cart", url: PAGE, pins: Array.from({ length: 201 }, (_, i) => ({ ...pin, id: `p${i}`, n: i + 1 })), drafts: 0, showPins: true },
    { type: "session", v: 1, mode: "branch", task: "feature/cart", url: PAGE, pins: [{ ...pin, body: "x".repeat(601) }], drafts: 0, showPins: true },
    { type: "session", v: 1, mode: "branch", task: "feature/cart", url: PAGE, pins: [{ ...pin, extra: 1 }], drafts: 0, showPins: true },
    { type: "session", v: 1, mode: "other", task: "feature/cart", url: PAGE, pins: [], drafts: 0, showPins: true },
  ]) fromDude(bad as unknown as DudeToOverlay);
  expect(tid("ov-conn")!.textContent).toBe("reaching dude…");
  expect(count('[data-testid="ov-pin"]')).toBe(0);
});

test("a claim from another dude tab tells the old one it left and says hello to the new one; anything with it is ignored", () => {
  boot();
  connect();
  const other = { postMessage: mock(() => {}), closed: false, focus: () => {} };
  // Wrong origin, or extra keys: nothing happens.
  fromDude({ type: "claim", v: 1 } as DudeToOverlay, "https://evil.example.test", other);
  fromDude({ type: "claim", v: 1, url: "x" } as unknown as DudeToOverlay, DUDE, other);
  expect(other.postMessage).not.toHaveBeenCalled();
  fromDude({ type: "claim", v: 1 } as DudeToOverlay, DUDE, other);
  expect(sent().at(-1)).toEqual({ type: "left", v: 1 });
  expect(other.postMessage).toHaveBeenCalledWith({ type: "hello", v: 1, url: PAGE }, DUDE);
  // Now the claiming tab is dude: its session connects, the old one's is not heard.
  fromDude({ type: "session", v: 1, task: "OLD", url: PAGE, pins: [], drafts: 0, showPins: true });
  expect(tid("ov-conn")!.textContent).toBe("reaching dude…");
  fromDude({ type: "session", v: 1, task: "NEW", url: PAGE, pins: [], drafts: 0, showPins: true }, DUDE, other);
  expect(tid("ov-conn")!.textContent).toBe("connected to dude");
});

test("branch retryable outcome preserves the nonce for unchanged content", async () => {
  boot();
  fromDude({ type: "session", v: 1, mode: "branch", task: "feature/cart", url: PAGE, pins: [], drafts: 0, showPins: false });
  tid("ov-comment")!.click();
  const button = sized(win.document.querySelector("button.pay")!); hover(button);
  button.dispatchEvent(new win.MouseEvent("click", { bubbles: true, composed: true }));
  await tick();
  const field = root.querySelector("textarea")!; field.value = "The button overlaps";
  field.dispatchEvent(new win.Event("input", { bubbles: true }));
  tid("ov-save")!.click();
  const first = sent().find((m) => m.type === "draft")!;
  fromDude({ type: "retryable", v: 1, nonce: first.nonce, reason: "Response lost" });
  tid("ov-save")!.click();
  const drafts = sent().filter((m) => m.type === "draft");
  expect(drafts).toHaveLength(2); expect(drafts[1]!.nonce).toBe(first.nonce);
});

test.each([401, 403])("a branch server's owner %i and a later POST refusal, across dude tab reloads, preserve a committed submission's identity", async (status) => {
  const owner: BranchPreviewServerAt = { kind: "branch", organizationId: "org_acme", organizationSlug: "acme",
    projectId: "prj_acme", previewRunId: "run_first", branch: "feature/cart", server: "web" };
  const items = new Map<string, string>();
  const keys: string[] = [];
  let ownerDenied = false;
  const upload = mock(async () => "att_shot");
  class Client extends ApiClient {
    override async validatePreviewServer() {
      if (ownerDenied) throw new ApiError(status, "unauthorized", "Sign in again");
      return owner;
    }
    override async previewFeedback() { return { feedback: [], nextCursor: null }; }
    override uploadPreviewAttachment = upload;
    override async submitPreviewFeedback(_run: string, _server: string, input: Parameters<ApiClient["submitPreviewFeedback"]>[2]) {
      keys.push(input.submissionKey);
      if (keys.length === 2) throw new ApiError(403, "forbidden", "Sign in again");
      items.set(input.submissionKey, "fbi_committed");
      if (keys.length === 1) throw new TypeError("response body aborted after commit");
      return { id: items.get(input.submissionKey)! } as Awaited<ReturnType<ApiClient["submitPreviewFeedback"]>>;
    }
  }
  const origin = new URL(PAGE).origin;
  // The dude tab Connect to dude opened: its opener is the preview, and it arrived from it.
  const dudeWin = new Window({ url: DUDE }) as globalThis.Window;
  const preview = { closed: false, postMessage: (data: DudeToOverlay) => queueMicrotask(() => fromDude(data)) };
  Object.defineProperty(dudeWin, "opener", { configurable: true, get: () => preview });
  markServerLanding(dudeWin, { projectId: "prj_acme", runId: "run_first", server: "web", branch: "feature/cart", origin });
  const run: RunningRun = { id: "run_first", kind: "preview", task: null, branch: "feature/cart", state: "running", startedAt: null, startedBy: null,
    servers: [{ name: "web", port: 3000, url: origin, state: "ready", since: null, readySince: null, feedback: { open: 0, resolved: 0, authors: 0 } }] };
  const start = () => {
    const w = new ServerWindows(dudeWin, new Client({}), { you: null, name: () => "x" }, () => {});
    w.expect(run, "web");
    return () => w.dispose();
  };
  let stop = start();
  opener!.postMessage.mockImplementation((data: OverlayToDude) => queueMicrotask(() => dudeWin.dispatchEvent(
    new win.MessageEvent("message", { data, origin, source: preview }))));
  try {
    boot(); await tick(); await tick(10);
    await writeOnPay("The button overlaps");
    tid("ov-save")!.click(); await tick(10);
    const first = sent().find((m) => m.type === "draft")!;
    expect(first.picture).toBe("data:image/png;base64,iVBORw0KGgo=");
    // The dude tab reloads; its check of the server refuses without a nonce.
    stop(); ownerDenied = true; stop = start();
    opener!.postMessage({ type: "hello", v: 1, url: PAGE }, DUDE); await tick(10);
    expect(tid("ov-problem")!.textContent).toContain("Sign in again");
    expect(keys).toHaveLength(1);
    stop(); ownerDenied = false; stop = start();
    opener!.postMessage({ type: "hello", v: 1, url: PAGE }, DUDE); await tick(10);
    tid("ov-save")!.click(); await tick(10);
    expect(tid("ov-problem")!.textContent).toContain("Sign in again");
    tid("ov-save")!.click(); await tick(10);
    expect(keys).toEqual([first.nonce, first.nonce, first.nonce]);
    expect(items.size).toBe(1);
    expect(sent().filter((m) => m.type === "draft").every((m) => m.picture === first.picture)).toBe(true);
    expect(upload).toHaveBeenCalledTimes(2); // The reloaded tab uploads once, then reuses it.
    expect(count('[data-testid="ov-comment-box"]')).toBe(0);
  } finally { stop(); }
});

test.each(["before", "after"])("branch owner refusal %s POST retryable keeps the original content snapshot", async (order) => {
  boot();
  fromDude({ type: "session", v: 1, mode: "branch", task: "feature/cart", url: PAGE, pins: [], drafts: 0, showPins: false });
  const field = await writeOnPay("Original words");
  tid("ov-save")!.click();
  const first = sent().find((m) => m.type === "draft")!;
  const ownerError = () => fromDude({ type: "refused", v: 1, reason: "Sign in again" });
  if (order === "before") ownerError();
  fromDude({ type: "retryable", v: 1, nonce: first.nonce, reason: "Response lost" });
  if (order === "after") ownerError();
  fromDude({ type: "refused", v: 1, nonce: "foreign", reason: "Foreign refusal" });
  expect(tid("ov-problem")!.textContent).not.toContain("Foreign");
  field.value = "Changed words"; field.dispatchEvent(new win.Event("input", { bubbles: true }));
  ownerError();
  fromDude({ type: "saved", v: 1, nonce: first.nonce, id: "fbi_first", drafts: 0, picture: "attached" });
  expect(tid("ov-comment-box")).not.toBeNull();
  tid("ov-save")!.click();
  const changed = sent().filter((m) => m.type === "draft").at(-1)!;
  expect(changed.nonce).not.toBe(first.nonce);
  ownerError();
  fromDude({ type: "saved", v: 1, nonce: changed.nonce, id: "fbi_changed", drafts: 0, picture: "attached" });
  expect(tid("ov-comment-box")).toBeNull();
});

test("a message from another origin, or from a window other than its opener, is not heard", () => {
  boot();
  fromDude({ type: "session", v: 1, task: "SHOP-1", url: PAGE, pins: [], drafts: 0, showPins: true }, "https://evil.example.test");
  expect(count('[data-testid="ov-comment"]')).toBe(0);
  fromDude({ type: "session", v: 1, task: "SHOP-1", url: PAGE, pins: [], drafts: 0, showPins: true }, DUDE, { postMessage() {} });
  expect(count('[data-testid="ov-comment"]')).toBe(0);
  connect();
  expect(count('[data-testid="ov-comment"]')).toBe(1);
});

test("hovering while picking draws the element's box and a label naming it and its size", () => {
  boot();
  connect();
  tid("ov-comment")!.click();
  expect(tid("ov-hint")!.textContent).toContain("to pick");
  hover(win.document.querySelector("button.pay")!);
  expect(count('[data-testid="ov-highlight"]')).toBe(1);
  const label = tid("ov-label")!.textContent!;
  expect(label).toContain("button.pay");
  expect(label).toMatch(/\d+×\d+/);
});

test("a component the app names is in the label", () => {
  page(`<div data-component="CartPanel" data-source="src/Cart.tsx:12"><button class="pay">Pay</button></div>`);
  boot();
  connect();
  tid("ov-comment")!.click();
  hover(win.document.querySelector("button.pay")!);
  expect(tid("ov-label")!.textContent).toContain("<CartPanel>");
});

test("↑ widens the pick to the parent, step by step, never to the page itself", () => {
  boot();
  connect();
  tid("ov-comment")!.click();
  hover(win.document.querySelector("button.pay")!);
  const key = () => win.dispatchEvent(new win.KeyboardEvent("keydown", { key: "ArrowUp", bubbles: true }));
  key();
  expect(tid("ov-label")!.textContent).toContain("#checkout");
  key();
  expect(tid("ov-label")!.textContent).toContain("section.cart");
  key();
  key();
  key();
  expect(tid("ov-label")!.textContent).toContain("main");
  // The pointer still over the button does not undo the widening.
  hover(win.document.querySelector("button.pay")!);
  expect(tid("ov-label")!.textContent).toContain("main");
});

test("↑ widens with focus still on the overlay's Comment button, just pressed", () => {
  boot();
  connect();
  const comment = tid("ov-comment")!;
  comment.focus();
  comment.click();
  hover(win.document.querySelector("button.pay")!);
  const focused = root.activeElement ?? tid("ov-comment")!;
  focused.dispatchEvent(new win.KeyboardEvent("keydown", { key: "ArrowUp", bubbles: true, composed: true }));
  expect(tid("ov-label")!.textContent).toContain("#checkout");
});

test("a frame from another site can't be picked inside: hovering it says so, and a click comments on the frame", () => {
  page(`<main><iframe src="https://status.widgets.test/" title="Status"></iframe></main>`);
  const frame = win.document.querySelector("iframe")!;
  Object.defineProperty(frame, "contentDocument", { configurable: true, get: () => { throw new Error("cross-origin"); } });
  sized(frame, 50, 300, 400, 90);
  boot();
  connect();
  tid("ov-comment")!.click();
  const catcher = tid("ov-frame-catcher")!;
  catcher.dispatchEvent(new win.MouseEvent("mouseover", { bubbles: true }));
  expect(tid("ov-label")!.textContent).toContain("Can't pick inside this frame");
  expect(tid("ov-label")!.textContent).toContain("status.widgets.test");
  tid("ov-frame-catcher")!.click();
  expect(tid("ov-comment-box")!.getAttribute("aria-label")).toBe("Comment on the frame from status.widgets.test");
});

test("Esc backs out one step at a time: the box, then picking", () => {
  boot();
  connect();
  tid("ov-comment")!.click();
  const pay = win.document.querySelector("button.pay")!;
  hover(pay);
  pay.dispatchEvent(new win.MouseEvent("click", { bubbles: true, cancelable: true }));
  expect(count('[data-testid="ov-comment-box"]')).toBe(1);
  const esc = () => win.dispatchEvent(new win.KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }));
  esc();
  expect(count('[data-testid="ov-comment-box"]')).toBe(0);
  expect(count('[data-testid="ov-hint"]')).toBe(1);
  esc();
  expect(count('[data-testid="ov-hint"]')).toBe(0);
  expect(tid("ov-comment")!.getAttribute("aria-pressed")).toBe("false");
});

test("a click while picking picks and never reaches the page", () => {
  boot();
  connect();
  const pressed = mock(() => {});
  const pay = win.document.querySelector("button.pay")!;
  pay.addEventListener("click", pressed);
  tid("ov-comment")!.click();
  hover(pay);
  pay.dispatchEvent(new win.MouseEvent("click", { bubbles: true, cancelable: true }));
  expect(pressed).not.toHaveBeenCalled();
  // Off again, the page has its clicks back.
  win.dispatchEvent(new win.KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
  win.dispatchEvent(new win.KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
  pay.dispatchEvent(new win.MouseEvent("click", { bubbles: true, cancelable: true }));
  expect(pressed).toHaveBeenCalledTimes(1);
});

test("Save draft sends the anchor and the picture to dude alone, and a saved answer says so", async () => {
  boot();
  connect();
  tid("ov-comment")!.click();
  const pay = sized(win.document.querySelector("button.pay")!);
  hover(pay);
  pay.dispatchEvent(new win.MouseEvent("click", { bubbles: true, cancelable: true }));
  await tick();
  const field = q("textarea")!as HTMLTextAreaElement;
  field.value = "Make it green";
  field.dispatchEvent(new win.Event("input", { bubbles: true }));
  tid("ov-save")!.click();
  const draft = sent().find((m) => m.type === "draft");
  expect(draft).toMatchObject({ type: "draft", v: 1, body: "Make it green", picture: "data:image/png;base64,iVBORw0KGgo=",
    anchor: { kind: "preview", url: PAGE, selector: "button.pay", viewport: { width: 1280, height: 720 } } });
  expect(opener!.postMessage.mock.calls.every((c) => c[1] === DUDE)).toBe(true);
  fromDude({ type: "saved", v: 1, nonce: (draft as { nonce: string }).nonce, id: "fbk_1", drafts: 1, picture: "attached" });
  expect(count('[data-testid="ov-comment-box"]')).toBe(0);
  expect(tid("ov-saved")!.textContent).toContain("Saved to your drafts in dude");
});

test("a picture that can't be drawn says why in the box and the draft is saved without one", async () => {
  boot({ draw: () => Promise.reject(new PictureError("it shows an image from another site")) });
  connect();
  tid("ov-comment")!.click();
  const pay = sized(win.document.querySelector("button.pay")!);
  hover(pay);
  pay.dispatchEvent(new win.MouseEvent("click", { bubbles: true, cancelable: true }));
  await tick();
  expect(tid("ov-picture-failed")!.textContent).toContain("it shows an image from another site");
  const field = q("textarea") as HTMLTextAreaElement;
  field.value = "Bigger";
  field.dispatchEvent(new win.Event("input", { bubbles: true }));
  expect((tid("ov-save") as HTMLButtonElement).disabled).toBe(false);
  tid("ov-save")!.click();
  const draft = sent().find((m) => m.type === "draft") as Extract<OverlayToDude, { type: "draft" }>;
  expect(draft.body).toBe("Bigger");
  expect("picture" in draft).toBe(false);
});

test("opened with no opener it offers Connect to dude, and never offers Comment or Save", () => {
  page(CART, false);
  boot();
  expect(tid("ov-no-opener")!.textContent).toContain("Connect to dude to comment: dude opens and finds this preview's task");
  expect(tid("ov-connect")!.localName).toBe("button");
  expect(tid("ov-no-opener")!.textContent).toContain("Hide for this tab");
  expect(count('[data-testid="ov-comment"]')).toBe(0);
  expect(count('[data-testid="ov-save"]')).toBe(0);
});

test("an opener that never answers is treated as none after the wait", async () => {
  boot();
  await tick(80);
  expect(tid("ov-no-opener")!.textContent).toContain("Connect to dude to comment");
  expect(count('[data-testid="ov-comment"]')).toBe(0);
  // A late session still connects: the tab may have been slow.
  connect();
  expect(count('[data-testid="ov-comment"]')).toBe(1);
});

test("dude's tab closing mid-comment keeps the words, offers Copy text and no Save", async () => {
  boot();
  connect();
  tid("ov-comment")!.click();
  const pay = win.document.querySelector("button.pay")!;
  hover(pay);
  pay.dispatchEvent(new win.MouseEvent("click", { bubbles: true, cancelable: true }));
  const field = q("textarea") as HTMLTextAreaElement;
  field.value = "Half a thought";
  opener!.closed = true;
  await tick(1_100);
  expect(count('[data-testid="ov-save"]')).toBe(0);
  expect(count('[data-testid="ov-copy"]')).toBe(1);
  expect((q("textarea") as HTMLTextAreaElement).value).toBe("Half a thought");
  expect(tid("ov-disconnected")!.textContent).toContain("dude's tab was closed");
  // The closed panel offers Connect to dude too.
  expect(count('[data-testid="ov-connect"]')).toBe(1);
});

test("pins only what resolves to exactly one element; the rest are listed as not found here", () => {
  page(`<button class="pay">Pay</button><li class="row">a</li><li class="row">b</li>`);
  boot();
  const pin = (id: string, selector: string, n: number): OverlayPin =>
    ({ id, n, status: "open", author: "Ana", mine: false, body: `about ${selector}`, selector, when: "submitted 2 min ago", replies: [] });
  connect([pin("one", "button.pay", 1), pin("many", "li.row", 2), pin("none", "div.gone", 3), pin("bad", "::nope(", 4)]);
  expect([...root.querySelectorAll('[data-testid="ov-pin"]')].map((p) => p.getAttribute("data-id"))).toEqual(["one"]);
  tid("ov-pins")!.click();
  expect([...root.querySelectorAll('[data-testid="ov-gone"]')].map((p) => p.getAttribute("data-id"))).toEqual(["many", "none", "bad"]);
  // Show pins off: none drawn, and dude is told.
  (tid("ov-show-pins") as HTMLInputElement).click();
  expect(count('[data-testid="ov-pin"]')).toBe(0);
  expect(sent()).toContainEqual({ type: "prefs", v: 1, showPins: false });
});

test("highlight opens the item's popover saying it was shown from dude", () => {
  boot();
  connect([{ id: "fbk_9", n: 1, status: "draft", author: "Márcio", mine: true, body: "Green", selector: "button.pay", when: "drafted now", replies: [] }]);
  fromDude({ type: "highlight", v: 1, itemId: "fbk_9" });
  expect(tid("ov-popover")!.textContent).toContain("Shown from dude · your draft 1");
  expect(count('[data-testid="ov-show"]')).toBe(1);
});

test("a route change in the app tells dude, and the pins wait for that page's", () => {
  boot();
  connect([{ id: "a", n: 1, status: "open", author: "Ana", mine: false, body: "x", selector: "button.pay", when: "", replies: [] }]);
  win.history.pushState({}, "", "/greetings");
  expect(sent().at(-1)).toEqual({ type: "navigated", v: 1, url: "https://web-wi-1-shop.preview.example.test/greetings" });
  expect(count('[data-testid="ov-pin"]')).toBe(0);
  // A late session for the old page is not this page's.
  connect([{ id: "a", n: 1, status: "open", author: "Ana", mine: false, body: "x", selector: "button.pay", when: "", replies: [] }]);
  expect(count('[data-testid="ov-pin"]')).toBe(0);
});

test("a route change before dude's session is told to dude, and the session for the page it is on now connects", () => {
  boot();
  win.history.replaceState({}, "", "/home");
  expect(sent().map((m) => m.type)).toEqual(["hello", "navigated"]);
  expect(sent().at(-1)).toEqual({ type: "navigated", v: 1, url: "https://web-wi-1-shop.preview.example.test/home" });
  // dude's answer to the first hello names /cart: not this page's.
  connect();
  expect(tid("ov-conn")!.textContent).toBe("reaching dude…");
  fromDude({ type: "session", v: 1, task: "SHOP-1", url: "https://web-wi-1-shop.preview.example.test/home", drafts: 0, showPins: true,
    pins: [{ id: "h", n: 1, status: "open", author: "Ana", mine: false, body: "x", selector: "button.pay", when: "", replies: [] }] });
  expect(tid("ov-conn")!.textContent).toBe("connected to dude");
  expect([...root.querySelectorAll('[data-testid="ov-pin"]')].map((p) => p.getAttribute("data-id"))).toEqual(["h"]);
});

/** Comment, then a pick of the Pay button, then words in the box. */
async function writeOnPay(words: string): Promise<HTMLTextAreaElement> {
  tid("ov-comment")!.click();
  const pay = sized(win.document.querySelector("button.pay")!);
  hover(pay);
  pay.dispatchEvent(new win.MouseEvent("click", { bubbles: true, cancelable: true }));
  await tick();
  const field = q("textarea") as HTMLTextAreaElement;
  field.value = words;
  field.dispatchEvent(new win.Event("input", { bubbles: true }));
  return field;
}

test("a save dude never answers is given up on: the reason, Copy text, Save again, and the words kept through a reconnect", async () => {
  boot({ saveWaitMs: 60 });
  connect();
  await writeOnPay("Half a thought");
  tid("ov-save")!.click();
  expect(tid("ov-save")!.textContent).toBe("Saving…");
  await tick(100);
  expect(tid("ov-problem")!.textContent).toContain("dude didn't answer");
  expect(count('[data-testid="ov-copy"]')).toBe(1);
  expect((tid("ov-save") as HTMLButtonElement).disabled).toBe(false);
  expect(tid("ov-save")!.textContent).toBe("Save draft");
  expect((q("textarea") as HTMLTextAreaElement).value).toBe("Half a thought");
  // It said hello again; the dude tab back on the task answers, and the box is as it was.
  expect(sent().at(-1)).toEqual({ type: "hello", v: 1, url: PAGE });
  connect();
  expect(count('[data-testid="ov-comment-box"]')).toBe(1);
  expect((q("textarea") as HTMLTextAreaElement).value).toBe("Half a thought");
  expect(count('[data-testid="ov-problem"]')).toBe(0);
});

test("after the give-up, a late saved from a slow dude still lands: the box closes and says it was saved", async () => {
  boot({ saveWaitMs: 60 });
  connect();
  await writeOnPay("Slow link");
  tid("ov-save")!.click();
  const first = sent().at(-1) as { type: string; nonce: string };
  await tick(100);
  expect(tid("ov-problem")!.textContent).toContain("dude didn't answer");
  fromDude({ type: "saved", v: 1, nonce: first.nonce, id: "fbk_1", drafts: 1, picture: "none" });
  expect(count('[data-testid="ov-comment-box"]')).toBe(0);
  expect(count('[data-testid="ov-saved"]')).toBe(1);
});

test("after the give-up, Save again sends the same save, so dude never saves the words twice", async () => {
  boot({ saveWaitMs: 60 });
  connect();
  await writeOnPay("Slow link");
  tid("ov-save")!.click();
  const first = sent().at(-1) as { type: string; nonce: string };
  expect(first.type).toBe("draft");
  await tick(100);
  connect();
  tid("ov-save")!.click();
  const again = sent().at(-1) as { type: string; nonce: string };
  expect(again).toMatchObject({ type: "draft", nonce: first.nonce });
  fromDude({ type: "saved", v: 1, nonce: first.nonce, id: "fbk_1", drafts: 1, picture: "none" });
  expect(count('[data-testid="ov-comment-box"]')).toBe(0);
  expect(count('[data-testid="ov-saved"]')).toBe(1);
});

test("after the give-up, words changed before Save again are a new save: a new nonce, the new words", async () => {
  boot({ saveWaitMs: 60 });
  connect();
  const field = await writeOnPay("First words");
  tid("ov-save")!.click();
  const first = sent().at(-1) as { nonce: string };
  await tick(100);
  connect();
  field.value = "Completely different words";
  field.dispatchEvent(new win.Event("input", { bubbles: true }));
  tid("ov-save")!.click();
  const again = sent().at(-1) as { type: string; nonce: string; body: string };
  expect(again.type).toBe("draft");
  expect(again.nonce).not.toBe(first.nonce);
  expect(again.body).toBe("Completely different words");
});

test("after the give-up, a late saved for the old words leaves a box holding new words open", async () => {
  boot({ saveWaitMs: 60 });
  connect();
  const field = await writeOnPay("First words");
  tid("ov-save")!.click();
  const first = sent().at(-1) as { nonce: string };
  await tick(100);
  field.value = "Edited meanwhile";
  field.dispatchEvent(new win.Event("input", { bubbles: true }));
  fromDude({ type: "saved", v: 1, nonce: first.nonce, id: "fbk_1", drafts: 1, picture: "none" });
  expect(count('[data-testid="ov-comment-box"]')).toBe(1);
  expect((q("textarea") as HTMLTextAreaElement).value).toBe("Edited meanwhile");
});

test("a late saved that lands while the same save is sent again, after an edit, frees Save at once", async () => {
  boot({ saveWaitMs: 60 });
  connect();
  const field = await writeOnPay("Slow link");
  tid("ov-save")!.click();
  const first = sent().at(-1) as { nonce: string };
  await tick(100);
  connect();
  tid("ov-save")!.click();
  expect((sent().at(-1) as { nonce: string }).nonce).toBe(first.nonce);
  field.value = "Slow link, and more";
  field.dispatchEvent(new win.Event("input", { bubbles: true }));
  fromDude({ type: "saved", v: 1, nonce: first.nonce, id: "fbk_1", drafts: 1, picture: "none" });
  expect(count('[data-testid="ov-comment-box"]')).toBe(1);
  expect(tid("ov-save")!.textContent).toBe("Save draft");
  await tick(100);
  expect(count('[data-testid="ov-problem"]')).toBe(0);
});

test("after the give-up, turning the picture off is a change: a late saved leaves the box, Save sends a new save without one", async () => {
  boot({ saveWaitMs: 60 });
  connect();
  await writeOnPay("With a picture");
  await tick(20);
  tid("ov-save")!.click();
  const first = sent().at(-1) as { nonce: string; picture?: string };
  expect(first.picture).toBeDefined();
  await tick(100);
  connect();
  const attach = tid("ov-attach") as HTMLInputElement;
  attach.checked = false;
  attach.dispatchEvent(new win.Event("change", { bubbles: true }));
  fromDude({ type: "saved", v: 1, nonce: first.nonce, id: "fbk_1", drafts: 1, picture: "attached" });
  expect(count('[data-testid="ov-comment-box"]')).toBe(1);
  tid("ov-save")!.click();
  const again = sent().at(-1) as { type: string; nonce: string; picture?: string };
  expect(again.type).toBe("draft");
  expect(again.nonce).not.toBe(first.nonce);
  expect(again.picture).toBeUndefined();
});

test("after the give-up, a late refused for that save still shows its reason", async () => {
  boot({ saveWaitMs: 60 });
  connect();
  await writeOnPay("Words");
  tid("ov-save")!.click();
  const first = sent().at(-1) as { nonce: string };
  await tick(100);
  fromDude({ type: "refused", v: 1, nonce: first.nonce, reason: "you have 50 drafts on this task" });
  expect(tid("ov-problem")!.textContent).toContain("50 drafts");
  tid("ov-save")!.click();
  expect((sent().at(-1) as { nonce: string }).nonce).not.toBe(first.nonce);
});

test("the Esc that ends a composition in WebKit (keyCode 229, not composing) is the IME's too", async () => {
  boot();
  connect();
  await writeOnPay("にほ");
  win.dispatchEvent(new win.KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true, keyCode: 229 } as KeyboardEventInit));
  expect(count('[data-testid="ov-comment-box"]')).toBe(1);
});

test("Esc while an IME is composing is the IME's: the box and its words stay", async () => {
  boot();
  connect();
  const field = await writeOnPay("にほ");
  field.dispatchEvent(new win.CompositionEvent("compositionstart", { bubbles: true }));
  win.dispatchEvent(new win.KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true, isComposing: true }));
  expect(count('[data-testid="ov-comment-box"]')).toBe(1);
  expect((q("textarea") as HTMLTextAreaElement).value).toBe("にほ");
  // Once the composition is over, Esc is the overlay's again.
  field.dispatchEvent(new win.CompositionEvent("compositionend", { bubbles: true }));
  win.dispatchEvent(new win.KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }));
  expect(count('[data-testid="ov-comment-box"]')).toBe(0);
});

const pinOn = (id: string, selector: string, n = 1): OverlayPin =>
  ({ id, n, status: "open", author: "Ana", mine: false, body: `about ${selector}`, selector, when: "", replies: [] });

test("words typed through an IME composition are saved exactly as composed, the box never redrawn under it", async () => {
  boot({ tickMs: 10 });
  connect([pinOn("p", "p.other")]);
  const field = await writeOnPay("");
  field.focus();
  const ev = (type: string, data: string, composing: boolean) =>
    field.dispatchEvent(type.startsWith("composition") ? new win.CompositionEvent(type, { data, bubbles: true }) : new win.InputEvent("input", { data, isComposing: composing, bubbles: true }));
  ev("compositionstart", "", true);
  field.value = "にほ";
  ev("input", "にほ", true);
  await tick(30);
  field.value = "にほん";
  ev("input", "にほん", true);
  field.value = "日本";
  ev("compositionend", "日本", false);
  ev("input", "日本", false);
  // The field is still the same node, in the box, with focus.
  expect(q("textarea")).toBe(field);
  expect(root.activeElement).toBe(field);
  tid("ov-save")!.click();
  expect((sent().find((m) => m.type === "draft") as { body: string }).body).toBe("日本");
});

test("the pin tick moves pins in place: an open disclosure stays open, focus stays in the box, pin nodes are the same nodes", async () => {
  boot({ tickMs: 10 });
  connect([pinOn("p", "p.other")]);
  const pinBefore = tid("ov-pin")!;
  await tick(40);
  expect(tid("ov-pin")).toBe(pinBefore);
  // The page moves the element: the same pin node moves with it.
  sized(win.document.querySelector("p.other")!, 300, 400, 50, 20);
  await tick(40);
  expect(tid("ov-pin")).toBe(pinBefore);
  expect(pinBefore.getAttribute("style")).toBe("left:332px;top:386px");
  // With the box open: the disclosure and focus survive ticks and keystrokes.
  const field = await writeOnPay("Gre");
  field.focus();
  const details = tid("ov-what-dude-sends") as HTMLDetailsElement;
  details.open = true;
  const save = tid("ov-save");
  await tick(40);
  field.value = "Green";
  field.dispatchEvent(new win.InputEvent("input", { data: "en", bubbles: true }));
  await tick(40);
  expect(tid("ov-what-dude-sends")).toBe(details);
  expect(details.open).toBe(true);
  expect(root.activeElement).toBe(field);
  expect(tid("ov-save")).toBe(save);
  expect((save as HTMLButtonElement).disabled).toBe(false);
});

test("the overlay's root is closed and the page's CSS doesn't reach into it", () => {
  const style = win.document.createElement("style");
  style.textContent = "button { color: rgb(255, 0, 0) !important; display: none !important } * { font-size: 40px !important }";
  win.document.head.append(style);
  boot();
  connect();
  const host = win.document.querySelector("dude-preview-overlay") as HTMLElement;
  expect(host.shadowRoot).toBeNull();
  const comment = tid("ov-comment")!;
  const cs = win.getComputedStyle(comment);
  expect(cs.color).not.toBe("rgb(255, 0, 0)");
  expect(cs.display).not.toBe("none");
  // And the overlay's own rules don't leak: the page's buttons keep the page's.
  expect(win.getComputedStyle(win.document.querySelector("button.pay")!).color).toBe("rgb(255, 0, 0)");
});

test("it takes no focus until picking or writing", () => {
  boot();
  connect();
  expect(win.document.activeElement).toBe(win.document.body);
});

test("hash routes are pages: each shows only its own pins, and moving between them is told to dude", async () => {
  page(`<button class="pay">Pay</button><p class="other">x</p>`);
  win.location.href = "https://web-wi-1-shop.preview.example.test/#/cart";
  boot();
  expect(sent()[0]).toEqual({ type: "hello", v: 1, url: "https://web-wi-1-shop.preview.example.test/#/cart" });
  fromDude({ type: "session", v: 1, task: "SHOP-1", url: "https://web-wi-1-shop.preview.example.test/#/cart", drafts: 0, showPins: true, pins: [pinOn("c", "button.pay")] });
  expect([...root.querySelectorAll('[data-testid="ov-pin"]')].map((p) => p.getAttribute("data-id"))).toEqual(["c"]);
  win.location.hash = "#/settings";
  win.dispatchEvent(new win.HashChangeEvent("hashchange"));
  expect(sent().at(-1)).toEqual({ type: "navigated", v: 1, url: "https://web-wi-1-shop.preview.example.test/#/settings" });
  expect(count('[data-testid="ov-pin"]')).toBe(0);
  // The /#/cart session is not this page's.
  fromDude({ type: "session", v: 1, task: "SHOP-1", url: "https://web-wi-1-shop.preview.example.test/#/cart", drafts: 0, showPins: true, pins: [pinOn("c", "button.pay")] });
  expect(count('[data-testid="ov-pin"]')).toBe(0);
  fromDude({ type: "session", v: 1, task: "SHOP-1", url: "https://web-wi-1-shop.preview.example.test/#/settings", drafts: 0, showPins: true, pins: [pinOn("s", "p.other")] });
  expect([...root.querySelectorAll('[data-testid="ov-pin"]')].map((p) => p.getAttribute("data-id"))).toEqual(["s"]);
  // A plain fragment is no route: #top and #bottom are the same page.
  win.location.hash = "#top";
  win.dispatchEvent(new win.HashChangeEvent("hashchange"));
  expect(sent().at(-1)).toEqual({ type: "navigated", v: 1, url: "https://web-wi-1-shop.preview.example.test/#top" });
  const before = sent().length;
  win.location.hash = "#bottom";
  win.dispatchEvent(new win.HashChangeEvent("hashchange"));
  expect(sent().length).toBe(before);
});

test("an app holding history's own pushState from before the overlay still has its route change told, on the tick", async () => {
  const own = win.history.pushState.bind(win.history);
  boot({ tickMs: 10 });
  connect();
  own({}, "", "/greetings");
  await tick(40);
  expect(sent().at(-1)).toEqual({ type: "navigated", v: 1, url: "https://web-wi-1-shop.preview.example.test/greetings" });
});

test("stop from dude takes the overlay off the page", () => {
  boot();
  connect();
  expect(win.document.querySelectorAll("dude-preview-overlay").length).toBe(1);
  fromDude({ type: "stop", v: 1 });
  expect(win.document.querySelectorAll("dude-preview-overlay").length).toBe(0);
});

test("a saved answer for another save is not this one's", async () => {
  boot();
  connect();
  await writeOnPay("Green");
  tid("ov-save")!.click();
  fromDude({ type: "saved", v: 1, nonce: "someone-else", id: "fbk_x", drafts: 4, picture: "none" });
  expect(count('[data-testid="ov-comment-box"]')).toBe(1);
  expect(tid("ov-save")!.textContent).toBe("Saving…");
  expect(count('[data-testid="ov-saved"]')).toBe(0);
});

test("Esc with only a panel showing closes it and the page still hears Esc; picking, the overlay keeps it", () => {
  boot();
  connect();
  const heard: boolean[] = [];
  win.document.addEventListener("keydown", (e: KeyboardEvent) => { if (e.key === "Escape") heard.push(e.defaultPrevented); });
  const esc = () => win.document.body.dispatchEvent(new win.KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }));
  tid("ov-pins")!.click();
  expect(count('[data-testid="ov-pins-panel"]')).toBe(1);
  esc();
  expect(count('[data-testid="ov-pins-panel"]')).toBe(0);
  expect(heard).toEqual([false]);
  tid("ov-comment")!.click();
  esc();
  expect(count('[data-testid="ov-hint"]')).toBe(0);
  expect(heard).toEqual([false]);
});

/** The OS's colour scheme as the page's matchMedia reports it, switchable as a person would switch it. */
function osScheme(start: "light" | "dark" | "none") {
  let scheme = start;
  const lists: Array<{ media: string; listeners: Array<() => void> }> = [];
  win.matchMedia = (media: string) => {
    const l = { media, listeners: [] as Array<() => void> };
    lists.push(l);
    return {
      media,
      get matches() { return scheme !== "none" && media.includes(`prefers-color-scheme: ${scheme}`); },
      addEventListener: (_: string, f: () => void) => l.listeners.push(f),
      removeEventListener: (_: string, f: () => void) => { l.listeners = l.listeners.filter((x) => x !== f); },
    };
  };
  return {
    set(next: "light" | "dark") { scheme = next; for (const l of lists) for (const f of l.listeners) f(); },
    listening: () => lists.reduce((n, l) => n + l.listeners.length, 0),
  };
}

const drawn = () => q("[data-overlay]")!.getAttribute("data-theme");
const themed = (theme?: OverlayTheme): Extract<DudeToOverlay, { type: "session" }> => ({ type: "session", v: 1, task: "SHOP-1", url: PAGE, pins: [], drafts: 0, showPins: true, ...(theme ? { theme } : {}) });

test("before dude answers it follows the OS's colour scheme, live, and dark when the OS says neither", () => {
  const os = osScheme("light");
  boot();
  expect(drawn()).toBe("light");
  os.set("dark");
  expect(drawn()).toBe("dark");
  os.set("light");
  expect(drawn()).toBe("light");
  overlay.stop();
  expect(os.listening()).toBe(0);
  page(CART);
  osScheme("none");
  boot();
  expect(drawn()).toBe("dark");
});

test("once dude's session names a theme it follows dude's, not the OS, and a new session switches it", () => {
  const os = osScheme("light");
  boot();
  fromDude(themed("dark"));
  expect(drawn()).toBe("dark");
  os.set("light");
  expect(drawn()).toBe("dark");
  fromDude(themed("light"));
  expect(drawn()).toBe("light");
});

test("a legacy session resumes following the OS, and malformed sessions leave state unchanged", () => {
  const os = osScheme("light");
  boot();
  fromDude(themed("dark"));
  expect(drawn()).toBe("dark");
  const before = { theme: drawn(), connection: tid("ov-conn")!.textContent, pins: count('[data-testid="ov-pin"]') };
  fromDude({ ...themed(), pins: {}, theme: 7 } as never);
  fromDude({ ...themed(), extra: true } as never);
  expect({ theme: drawn(), connection: tid("ov-conn")!.textContent, pins: count('[data-testid="ov-pin"]') }).toEqual(before);
  fromDude(themed());
  expect(drawn()).toBe("light");
  os.set("dark");
  expect(drawn()).toBe("dark");
});

test("a session with an invalid theme is rejected", () => {
  osScheme("light");
  boot();
  connect();
  fromDude(themed("sepia" as "light"));
  expect(drawn()).toBe("light");
  expect(tid("ov-conn")!.textContent).toBe("connected to dude");
});

test("a theme from another origin or another window is not heard", () => {
  osScheme("dark");
  boot();
  fromDude(themed("light"), "https://evil.example.test");
  fromDude(themed("light"), DUDE, { postMessage() {} });
  expect(drawn()).toBe("dark");
});

/** The overlay saves a draft from `session` and Go to dude is pressed on its saved toast. */
async function atGoToDude(session: DudeToOverlay) {
  boot();
  fromDude(session);
  tid("ov-comment")!.click();
  const pay = sized(win.document.querySelector("button.pay")!);
  hover(pay);
  pay.dispatchEvent(new win.MouseEvent("click", { bubbles: true, cancelable: true }));
  const field = q("textarea") as HTMLTextAreaElement;
  field.value = "Make it green";
  field.dispatchEvent(new win.Event("input", { bubbles: true }));
  await tick();
  tid("ov-save")!.click();
  const draft = sent().find((m) => m.type === "draft") as Extract<OverlayToDude, { type: "draft" }>;
  fromDude({ type: "saved", v: 1, nonce: draft.nonce, id: "fbk_1", drafts: 1, picture: "attached" });
  tid("ov-go-dude")!.click();
}

/** Counts the opener's focus() calls. */
function countFocus(): () => number {
  let focused = 0;
  opener!.focus = () => { focused++; };
  return () => focused;
}

/** window.open finding dude's tab (the opener) by name; returns the [url, name] it was called with. */
function openFindsOpener(): Array<[string, string]> {
  const opens: Array<[string, string]> = [];
  win.open = (url: string, name: string) => { opens.push([url, name]); return opener; };
  return opens;
}

test("Go to dude looks up dude's tab by the window name its session gave, not the opener's focus()", async () => {
  const opens = openFindsOpener();
  const focused = countFocus();
  await atGoToDude({ ...themed(), dudeWindow: "dude-tab-abc123" });
  expect(opens).toEqual([["", "dude-tab-abc123"]]);
  expect(focused()).toBe(0);
});

test("from a session with no window name (an older dude), Go to dude falls back to the opener's focus()", async () => {
  const opens = openFindsOpener();
  const focused = countFocus();
  await atGoToDude(themed());
  expect(opens).toEqual([]);
  expect(focused()).toBe(1);
});

test("a name that finds no window opens a blank one: it is closed again and the opener's focus() is tried", async () => {
  let closed = 0;
  const focused = countFocus();
  win.open = () => ({ location: { href: "about:blank" }, close: () => { closed++; } });
  await atGoToDude({ ...themed(), dudeWindow: "dude-tab-gone" });
  expect([closed, focused()]).toEqual([1, 1]);
});

test("a name held by another origin's window, not the opener, leaves that window open and tries the opener's focus()", async () => {
  let closed = 0;
  const focused = countFocus();
  win.open = () => ({ get location(): never { throw new Error("cross-origin"); }, close: () => { closed++; } });
  await atGoToDude({ ...themed(), dudeWindow: "dude-tab-taken" });
  expect([closed, focused()]).toEqual([0, 1]);
});

test("a pin's Open in dude tells dude which item and brings dude's tab forward by its window name", () => {
  const opens = openFindsOpener();
  const focused = countFocus();
  boot();
  fromDude({ ...themed(), pins: [{ id: "fbk_9", n: 1, status: "draft", author: "Márcio", mine: true, body: "Green", selector: "button.pay", when: "drafted now", replies: [] }], dudeWindow: "dude-tab-abc123" });
  fromDude({ type: "highlight", v: 1, itemId: "fbk_9" });
  q('[data-testid="ov-popover"] [data-k="open-in-dude"]')!.click();
  expect(sent()).toContainEqual({ type: "open", v: 1, itemId: "fbk_9" });
  expect(opens).toEqual([["", "dude-tab-abc123"]]);
  expect(focused()).toBe(0);
});

test("a session whose window name is not one dude gives is refused whole, before any state changes", () => {
  osScheme("light");
  boot();
  for (const dudeWindow of ["_blank", "dude tab", "x".repeat(OVERLAY_LIMITS.windowName + 1), 7]) {
    fromDude({ ...themed("dark"), dudeWindow } as never);
    expect(tid("ov-conn")!.textContent).toBe("reaching dude…");
    expect(drawn()).toBe("light");
  }
  fromDude({ ...themed("dark"), dudeWindow: "dude-tab-ok" });
  expect(tid("ov-conn")!.textContent).toBe("connected to dude");
});

// The heartbeat's interval, given a delay no other timer uses, is taken out of the window's timers and beaten by hand.
// Every function registered at that delay is kept and beaten, so a second heartbeat would show as a second hello.
const HB = 777;
let beats: Array<() => void> = [];
function heartbeatByHand(): () => void {
  beats = [];
  const real = win.setInterval.bind(win);
  win.setInterval = (fn: () => void, ms: number) => {
    if (ms !== HB) return real(fn, ms);
    beats.push(fn);
    return 0;
  };
  return () => { for (const fn of beats) fn(); };
}
const hellos = () => sent().filter((m) => m.type === "hello").length;
// Quiet beats after which a connected overlay drops to waiting.
const QUIET_TO_WAITING = 4;
const dropToWaiting = (beat: () => void) => { for (let i = 0; i < QUIET_TO_WAITING; i++) beat(); };

test("dude's tab reloading: hello after two quiet beats and again after three, reaching dude after four and hello each beat, and the reloaded tab's session reconnects with the box's words kept", async () => {
  const beat = heartbeatByHand();
  boot({ heartbeatMs: HB });
  expect(beats.length).toBe(1);
  connect();
  await writeOnPay("Half a thought");
  expect(hellos()).toBe(1);
  beat();
  expect(hellos()).toBe(1);
  // A message from dude (here a session, as any answer is) restarts the count.
  connect();
  beat();
  beat();
  expect(hellos()).toBe(2);
  expect(tid("ov-conn")!.textContent).toBe("connected to dude");
  // A third quiet beat: dude may only be slow, so hello again, still connected.
  beat();
  expect(hellos()).toBe(3);
  expect(tid("ov-conn")!.textContent).toBe("connected to dude");
  expect(count('[data-testid="ov-comment"]')).toBe(1);
  beat();
  expect(tid("ov-conn")!.textContent).toBe("reaching dude…");
  expect(hellos()).toBe(4);
  expect(count('[data-testid="ov-save"]')).toBe(0);
  expect(tid("ov-disconnected")!.textContent).toContain("Reaching dude again");
  beat();
  expect(hellos()).toBe(5);
  // The reloaded tab is a new document in the same window: the browser gives the same opener as source.
  connect();
  expect(tid("ov-conn")!.textContent).toBe("connected to dude");
  expect((q("textarea") as HTMLTextAreaElement).value).toBe("Half a thought");
  expect((tid("ov-save") as HTMLButtonElement).disabled).toBe(false);
});

test("picking stops when the connection drops to waiting: no hover label, Comment gone, and a click reaches the page", () => {
  const beat = heartbeatByHand();
  boot({ heartbeatMs: HB });
  connect();
  tid("ov-comment")!.click();
  const pay = sized(win.document.querySelector("button.pay")!);
  hover(pay);
  expect(count('[data-testid="ov-label"]')).toBe(1);
  for (let i = 0; i < 3; i++) beat();
  expect(tid("ov-conn")!.textContent).toBe("connected to dude");
  beat();
  expect(tid("ov-conn")!.textContent).toBe("reaching dude…");
  expect(count('[data-testid="ov-hint"]')).toBe(0);
  expect(count('[data-testid="ov-label"]')).toBe(0);
  hover(pay);
  expect(count('[data-testid="ov-label"]')).toBe(0);
  const click = new win.MouseEvent("click", { bubbles: true, cancelable: true });
  pay.dispatchEvent(click);
  expect(click.defaultPrevented).toBe(false);
  expect(count('[data-testid="ov-comment-box"]')).toBe(0);
});

test("dude's tab closing while the connection waits says it was closed and offers Connect, the words kept", async () => {
  const beat = heartbeatByHand();
  boot({ heartbeatMs: HB, sessionWaitMs: 60_000 });
  connect();
  await writeOnPay("Half a thought");
  dropToWaiting(beat);
  expect(tid("ov-conn")!.textContent).toBe("reaching dude…");
  opener!.closed = true;
  await tick(1_100);
  expect(tid("ov-conn")!.textContent).toBe("dude's tab closed");
  expect(tid("ov-disconnected")!.textContent).toContain("dude's tab was closed");
  expect(count('[data-testid="ov-connect"]')).toBe(1);
  expect((q("textarea") as HTMLTextAreaElement).value).toBe("Half a thought");
});

test("a dude tab that stops answering (left the task) offers Connect after five waiting beats, still saying hello, the words kept", async () => {
  const beat = heartbeatByHand();
  boot({ heartbeatMs: HB, sessionWaitMs: 60_000 });
  connect();
  await writeOnPay("Half a thought");
  dropToWaiting(beat);
  for (let i = 0; i < 4; i++) beat();
  expect(count('[data-testid="ov-connect"]')).toBe(0);
  beat();
  expect(tid("ov-conn")!.textContent).toBe("reaching dude…");
  expect(count('[data-testid="ov-connect"]')).toBe(1);
  expect(tid("ov-disconnected")!.textContent).toContain("may have reloaded, been closed, or left this task");
  for (let i = 0; i < 22; i++) beat();
  expect(count('[data-testid="ov-connect"]')).toBe(1);
  // The start's hello, three connected-quiet ones, then one for each of the 27 beats waiting.
  expect(hellos()).toBe(1 + 3 + 27);
  // Drawn again now (a route change there and back), it still offers Connect.
  const back = win.location.pathname;
  win.history.pushState({}, "", "/other");
  win.history.pushState({}, "", back);
  expect(count('[data-testid="ov-connect"]')).toBe(1);
  expect((q("textarea") as HTMLTextAreaElement).value).toBe("Half a thought");
  // dude answering again connects, and the panel goes.
  connect();
  expect(tid("ov-conn")!.textContent).toBe("connected to dude");
  expect(count('[data-testid="ov-no-opener"]')).toBe(0);
  // The next quiet stretch starts its count over: Reaching dude again, not Connect.
  dropToWaiting(beat);
  expect(tid("ov-disconnected")!.textContent).toContain("Reaching dude again");
  expect(count('[data-testid="ov-connect"]')).toBe(0);
});

test("not connected, it says hello once a beat and never twice in one: a hello between beats spares the next beat's", async () => {
  page(CART);
  const beat = heartbeatByHand();
  boot({ heartbeatMs: HB, sessionWaitMs: 10 });
  expect(hellos()).toBe(1);
  // The hello at start is this beat's.
  beat();
  expect(hellos()).toBe(1);
  await tick(30);
  expect(tid("ov-conn")!.textContent).toBe("not opened from dude");
  for (let i = 0; i < 5; i++) beat();
  expect(hellos()).toBe(6);
  // Connected and answering, nothing extra is said.
  connect();
  for (let i = 0; i < 5; i++) { beat(); connect(); }
  expect(hellos()).toBe(6);
  // A save given up on between beats says hello: the next beat, which would have said one, does not.
  overlay.stop();
  page(CART);
  const beat2 = heartbeatByHand();
  boot({ heartbeatMs: HB, saveWaitMs: 20 });
  connect();
  await writeOnPay("Words");
  beat2();
  tid("ov-save")!.click();
  await tick(50);
  expect(hellos()).toBe(2);
  beat2();
  expect(hellos()).toBe(2);
  beat2();
  expect(hellos()).toBe(3);
});

type FakeWin = { postMessage: ReturnType<typeof mock>; closed: boolean; focus: () => void; opener?: unknown; location?: unknown };
const fakeWin = (extra: Partial<FakeWin> = {}): FakeWin => ({ postMessage: mock(() => {}), closed: false, focus: () => {}, ...extra });
const posted = (w: FakeWin) => w.postMessage.mock.calls.map((c) => [(c[0] as OverlayToDude).type, c[1]]);
const sessionFor = (): DudeToOverlay => ({ type: "session", v: 1, task: "SHOP-1", url: PAGE, pins: [], drafts: 0, showPins: true });
// window.open replaced: each call's arguments recorded, answered by `open` (a new fake window by default).
function recordOpens(open: () => FakeWin = fakeWin): unknown[][] {
  const opens: unknown[][] = [];
  win.open = (...args: unknown[]) => { opens.push(args); return open(); };
  return opens;
}

test("Connect to dude opens dude's preview-connect page with an opener, says hello to that tab each beat, and only its session connects", () => {
  page(CART, false);
  const beat = heartbeatByHand();
  const dudeTab = fakeWin();
  const opens = recordOpens(() => dudeTab);
  boot({ heartbeatMs: HB });
  beat();
  expect(opens).toEqual([]);
  tid("ov-connect")!.click();
  // No third argument: no noopener, no noreferrer, so the new tab's window.opener is this page.
  expect(opens).toEqual([[`${DUDE}/#/preview-connect`, "_blank"]]);
  expect(tid("ov-connect-note")!.getAttribute("data-connect")).toBe("opened");
  expect(posted(dudeTab)).toEqual([]);
  beat();
  beat();
  expect(posted(dudeTab)).toEqual([["hello", DUDE], ["hello", DUDE]]);
  // Another window at dude's origin, and the right window from another origin, are not heard.
  fromDude(sessionFor(), DUDE, fakeWin());
  fromDude(sessionFor(), "https://evil.example.test", dudeTab);
  expect(tid("ov-conn")!.textContent).toBe("reaching dude…");
  expect(count('[data-testid="ov-comment"]')).toBe(0);
  fromDude(sessionFor(), DUDE, dudeTab);
  expect(tid("ov-conn")!.textContent).toBe("connected to dude");
  expect(count('[data-testid="ov-no-opener"]')).toBe(0);
  expect(count('[data-testid="ov-comment"]')).toBe(1);
});

test("a dude tab Connect opened that is closed before it answers puts the panel back to its first words", async () => {
  page(CART, false);
  const dudeTab = fakeWin();
  win.open = () => dudeTab;
  boot();
  tid("ov-connect")!.click();
  expect(tid("ov-connect-note")!.getAttribute("data-connect")).toBe("opened");
  dudeTab.closed = true;
  await tick(1_100);
  expect(count('[data-testid="ov-connect-note"]')).toBe(0);
  expect(tid("ov-conn")!.textContent).toBe("not opened from dude");
  expect(tid("ov-no-opener")!.textContent).toContain("Connect to dude to comment");
});

test("a dude tab Connect opened that is closed after it connected says dude's tab was closed", async () => {
  page(CART, false);
  const dudeTab = fakeWin();
  win.open = () => dudeTab;
  boot();
  tid("ov-connect")!.click();
  fromDude(sessionFor(), DUDE, dudeTab);
  await writeOnPay("Half a thought");
  dudeTab.closed = true;
  await tick(1_100);
  expect(tid("ov-conn")!.textContent).toBe("dude's tab closed");
  expect(tid("ov-disconnected")!.textContent).toContain("dude's tab was closed");
});

test("after Connect and a session, four quiet beats say Reaching dude again, not Connect's looking note", async () => {
  page(CART, false);
  const beat = heartbeatByHand();
  const dudeTab = fakeWin();
  win.open = () => dudeTab;
  boot({ heartbeatMs: HB });
  tid("ov-connect")!.click();
  fromDude(sessionFor(), DUDE, dudeTab);
  await writeOnPay("Half a thought");
  dropToWaiting(beat);
  expect(tid("ov-conn")!.textContent).toBe("reaching dude…");
  expect(count('[data-testid="ov-no-opener"]')).toBe(0);
  expect(count('[data-testid="ov-connect-note"]')).toBe(0);
  expect(tid("ov-disconnected")!.textContent).toContain("Reaching dude again");
});

test("reloaded after Connect to dude, Connect opens preview-connect in the dude tab it opened before, by the window name that tab's session gave", () => {
  page(CART, false);
  const dudeTab = fakeWin();
  const opens = recordOpens(() => dudeTab);
  boot();
  tid("ov-connect")!.click();
  fromDude({ ...sessionFor(), dudeWindow: "dude-tab-abc123" } as DudeToOverlay, DUDE, dudeTab);
  expect(tid("ov-conn")!.textContent).toBe("connected to dude");
  // The reload: a new document in the same tab, which keeps the tab's sessionStorage and has no opener.
  const kept = win.sessionStorage.getItem("dude-overlay-dudeWindow");
  overlay.stop();
  page(CART, false);
  if (kept !== null) win.sessionStorage.setItem("dude-overlay-dudeWindow", kept);
  const reopens = recordOpens(() => dudeTab);
  boot();
  tid("ov-connect")!.click();
  expect(reopens).toEqual([[`${DUDE}/#/preview-connect`, "dude-tab-abc123"]]);
  fromDude(sessionFor(), DUDE, dudeTab);
  expect(tid("ov-conn")!.textContent).toBe("connected to dude");
});

test("the stored dude tab name is used once: Connect pressed again with no session between opens a new tab", () => {
  page(CART, false);
  win.sessionStorage.setItem("dude-overlay-dudeWindow", "dude-tab-abc123");
  const opens = recordOpens();
  boot();
  tid("ov-connect")!.click();
  tid("ov-connect")!.click();
  expect(opens).toEqual([[`${DUDE}/#/preview-connect`, "dude-tab-abc123"], [`${DUDE}/#/preview-connect`, "_blank"]]);
});

test("a page opened from a preview page drops the dude tab name its storage inherited; one opened by another origin keeps it", async () => {
  page(CART, false);
  win.sessionStorage.setItem("dude-overlay-dudeWindow", "dude-tab-abc123");
  // The preview page that opened this one: same origin, and itself opened by no one, so Connect is offered.
  opener = fakeWin({ location: { origin: new URL(PAGE).origin }, opener: null }) as unknown as Opener;
  const opens = recordOpens();
  boot();
  tid("ov-connect")!.click();
  expect(opens).toEqual([[`${DUDE}/#/preview-connect`, "_blank"]]);
  // A page another origin opened, reloaded after Connect: that opener is not one of this preview's pages.
  overlay.stop();
  page(CART);
  win.sessionStorage.setItem("dude-overlay-dudeWindow", "dude-tab-abc123");
  const reopens = recordOpens();
  boot();
  await tick(80);
  tid("ov-connect")!.click();
  expect(reopens).toEqual([[`${DUDE}/#/preview-connect`, "dude-tab-abc123"]]);
});

test("Connect to dude opens a new tab when the stored name is not one dude gives, or came from a dude tab this page didn't open", () => {
  page(CART, false);
  win.sessionStorage.setItem("dude-overlay-dudeWindow", "_self");
  const opens = recordOpens();
  boot();
  tid("ov-connect")!.click();
  expect(opens).toEqual([[`${DUDE}/#/preview-connect`, "_blank"]]);
  // A dude tab that opened this preview (reached as its opener) has its own opener: its name is not kept.
  overlay.stop();
  page(CART);
  boot();
  fromDude({ ...sessionFor(), dudeWindow: "dude-tab-opener" } as DudeToOverlay);
  expect(tid("ov-conn")!.textContent).toBe("connected to dude");
  expect(win.sessionStorage.getItem("dude-overlay-dudeWindow") ?? "").toBe("");
});

test("after Connect to dude, ten seconds with no session says dude didn't find the task or didn't answer, and offers Connect again", () => {
  page(CART, false);
  const beat = heartbeatByHand();
  win.open = () => fakeWin();
  boot({ heartbeatMs: HB });
  tid("ov-connect")!.click();
  for (let i = 0; i < 4; i++) beat();
  expect(tid("ov-connect-note")!.textContent).toContain("is looking for this preview's task");
  beat();
  const note = tid("ov-connect-note")!.textContent!;
  expect(note).toContain("dude didn't find this preview's task, or didn't answer");
  expect(note).toContain("press Connect to dude again");
  expect(note).not.toContain("is looking");
  expect(tid("ov-no-opener")!.textContent).toContain("Connect to dude to comment");
  expect(count('[data-testid="ov-connect"]')).toBe(1);
  // Pressed again, the note starts over.
  tid("ov-connect")!.click();
  expect(tid("ov-connect-note")!.textContent).toContain("is looking for this preview's task");
});

test("a blocked pop-up is said in the panel, and Connect to dude can be pressed again", () => {
  page(CART, false);
  let next: FakeWin | null = null;
  win.open = () => next;
  boot();
  tid("ov-connect")!.click();
  expect(tid("ov-connect-note")!.textContent).toContain("The browser blocked dude's tab from opening");
  expect(tid("ov-conn")!.textContent).toBe("not opened from dude");
  next = fakeWin();
  tid("ov-connect")!.click();
  expect(tid("ov-connect-note")!.getAttribute("data-connect")).toBe("opened");
});

test("a page opened from a preview page reaches dude through its openers: hello goes to dude, and only dude's reply is heard", () => {
  page(CART, false);
  const dudeTab = fakeWin();
  // The page that opened this one: the same preview, so readable, and opened by dude.
  const previewPage = fakeWin({ location: { origin: new URL(PAGE).origin }, opener: dudeTab });
  opener = previewPage as unknown as Opener;
  boot();
  expect(posted(dudeTab)).toEqual([["hello", DUDE]]);
  expect(posted(previewPage)).toEqual([]);
  fromDude(sessionFor(), DUDE, previewPage);
  expect(tid("ov-conn")!.textContent).toBe("reaching dude…");
  fromDude(sessionFor(), DUDE, dudeTab);
  expect(tid("ov-conn")!.textContent).toBe("connected to dude");
});

test("once dude answered through the chain, the page stays connected to it after the preview page between them closes", async () => {
  page(CART, false);
  const beat = heartbeatByHand();
  const dudeTab = fakeWin();
  const previewPage = fakeWin({ location: { origin: new URL(PAGE).origin }, opener: dudeTab });
  opener = previewPage as unknown as Opener;
  boot({ heartbeatMs: HB });
  fromDude(sessionFor(), DUDE, dudeTab);
  previewPage.closed = true;
  await tick(1_100);
  expect(tid("ov-conn")!.textContent).toBe("connected to dude");
  expect(count('[data-testid="ov-disconnected"]')).toBe(0);
  // dude's tab is still the one hello goes to and the one heard.
  beat();
  beat();
  expect(posted(dudeTab).at(-1)).toEqual(["hello", DUDE]);
  fromDude(sessionFor(), DUDE, dudeTab);
  expect(tid("ov-conn")!.textContent).toBe("connected to dude");
});

test("dude reached through the openers, with the page between them closed, stays pinned through a slow reload and reconnects", async () => {
  page(CART, false);
  const beat = heartbeatByHand();
  const dudeTab = fakeWin();
  const previewPage = fakeWin({ location: { origin: new URL(PAGE).origin }, opener: dudeTab });
  opener = previewPage as unknown as Opener;
  // A long session wait, so the page stays waiting rather than dropping to "not opened from dude" mid-test.
  boot({ heartbeatMs: HB, sessionWaitMs: 60_000 });
  fromDude(sessionFor(), DUDE, dudeTab);
  previewPage.closed = true;
  // With no chain left to fall back to, the pin outlasts the waiting beats: dude is still said hello to, not taken for closed.
  dropToWaiting(beat);
  for (let i = 0; i < 6; i++) beat();
  await tick(1_100);
  expect(tid("ov-conn")!.textContent).toBe("reaching dude…");
  const before = posted(dudeTab).length;
  beat();
  expect(posted(dudeTab).slice(before)).toEqual([["hello", DUDE]]);
  fromDude(sessionFor(), DUDE, dudeTab);
  expect(tid("ov-conn")!.textContent).toBe("connected to dude");
});

test("a dude window that answered and then stops answering is let go after the waiting beats: the dude up the openers is heard instead", () => {
  page(CART, false);
  const beat = heartbeatByHand();
  const first = fakeWin();
  const second = fakeWin();
  let up: FakeWin = first;
  opener = { postMessage: mock(() => {}), closed: false, focus: () => {}, location: { origin: new URL(PAGE).origin }, get opener() { return up; } } as unknown as Opener;
  boot({ heartbeatMs: HB, sessionWaitMs: 60_000 });
  fromDude(sessionFor(), DUDE, first);
  expect(tid("ov-conn")!.textContent).toBe("connected to dude");
  // The openers now lead to another dude window; the one that answered stays open and says nothing more.
  up = second;
  dropToWaiting(beat);
  for (let i = 0; i < 4; i++) beat();
  fromDude(sessionFor(), DUDE, second);
  expect(tid("ov-conn")!.textContent).toBe("reaching dude…");
  beat();
  expect(posted(second).at(-1)).toEqual(["hello", DUDE]);
  fromDude(sessionFor(), DUDE, second);
  expect(tid("ov-conn")!.textContent).toBe("connected to dude");
});

test("a dude window that stops answering, with no other dude up the openers, is kept: still said hello to, and Connect offered", () => {
  page(CART, false);
  const beat = heartbeatByHand();
  const dudeTab = fakeWin();
  win.open = () => dudeTab;
  boot({ heartbeatMs: HB });
  tid("ov-connect")!.click();
  fromDude(sessionFor(), DUDE, dudeTab);
  dropToWaiting(beat);
  for (let i = 0; i < 6; i++) beat();
  expect(tid("ov-conn")!.textContent).toBe("reaching dude…");
  expect(count('[data-testid="ov-connect"]')).toBe(1);
  expect(posted(dudeTab).at(-1)).toEqual(["hello", DUDE]);
  fromDude(sessionFor(), DUDE, dudeTab);
  expect(tid("ov-conn")!.textContent).toBe("connected to dude");
});

// A page another site opened: its opener is live, of another origin, and never answers, so Connect is offered.
async function connectUnderForeignOpener(): Promise<FakeWin> {
  const dudeTab = fakeWin();
  win.open = () => dudeTab;
  boot({ heartbeatMs: HB });
  await tick(80);
  tid("ov-connect")!.click();
  return dudeTab;
}

test("with a foreign opener up the chain, the tab Connect opened that goes quiet after connecting stays the one said hello to, and its session reconnects", async () => {
  const beat = heartbeatByHand();
  const dudeTab = await connectUnderForeignOpener();
  fromDude(sessionFor(), DUDE, dudeTab);
  expect(tid("ov-conn")!.textContent).toBe("connected to dude");
  const toForeign = posted(opener as unknown as FakeWin).length;
  dropToWaiting(beat);
  for (let i = 0; i < 6; i++) beat();
  expect(posted(dudeTab).at(-1)).toEqual(["hello", DUDE]);
  expect(posted(opener as unknown as FakeWin).length).toBe(toForeign);
  fromDude(sessionFor(), DUDE, dudeTab);
  expect(tid("ov-conn")!.textContent).toBe("connected to dude");
});

test("with a foreign opener up the chain, the tab Connect opened that answers only after the waiting beats still connects", async () => {
  const beat = heartbeatByHand();
  const dudeTab = await connectUnderForeignOpener();
  for (let i = 0; i < 6; i++) beat();
  expect(count('[data-testid="ov-connect"]')).toBe(1);
  expect(posted(dudeTab).at(-1)).toEqual(["hello", DUDE]);
  fromDude(sessionFor(), DUDE, dudeTab);
  expect(tid("ov-conn")!.textContent).toBe("connected to dude");
});

test("the walk up the openers reaches dude at exactly five steps, stops after five same-origin pages, and at an opener that can't be read past", () => {
  page(CART, false);
  const origin = new URL(PAGE).origin;
  const nearTab = fakeWin();
  let near: FakeWin = nearTab;
  for (let i = 0; i < 4; i++) near = fakeWin({ location: { origin }, opener: near });
  opener = near as unknown as Opener;
  boot();
  // dude is five steps up: reached.
  expect(posted(nearTab)).toEqual([["hello", DUDE]]);
  overlay.stop();
  page(CART, false);
  const dudeTab = fakeWin();
  let top: FakeWin = dudeTab;
  for (let i = 0; i < 5; i++) top = fakeWin({ location: { origin }, opener: top });
  opener = top as unknown as Opener;
  boot();
  // dude is six steps up: too far, so nothing is said to anyone.
  expect(posted(dudeTab)).toEqual([]);
  expect(tid("ov-conn")!.textContent).toBe("not opened from dude");
  overlay.stop();
  page(CART, false);
  const throws = fakeWin({ location: { origin } });
  Object.defineProperty(throws, "opener", { get: () => { throw new Error("blocked"); } });
  opener = throws as unknown as Opener;
  boot();
  expect(tid("ov-conn")!.textContent).toBe("not opened from dude");
});
