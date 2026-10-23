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

test("opened with no opener it says to open the preview from dude, and never offers Comment or Save", () => {
  page(CART, false);
  boot();
  expect(tid("ov-no-opener")!.textContent).toContain("Open this preview from dude to comment");
  // Any of the task's links in dude opens it with the comment tool: there is no Comment button to press.
  expect(tid("ov-no-opener")!.textContent).toContain("Open the preview from any of the task's links in dude");
  expect(tid("ov-no-opener")!.textContent).not.toContain("press Comment");
  expect(count('[data-testid="ov-comment"]')).toBe(0);
  expect(count('[data-testid="ov-save"]')).toBe(0);
});

test("an opener that never answers is treated as none after the wait", async () => {
  boot();
  await tick(80);
  expect(tid("ov-no-opener")!.textContent).toContain("Open this preview from dude to comment");
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

/** The overlay saved a draft and shows its toast, Go to dude in it. */
function savedToast(session: DudeToOverlay) {
  boot();
  fromDude(session);
  tid("ov-comment")!.click();
  const pay = sized(win.document.querySelector("button.pay")!);
  hover(pay);
  pay.dispatchEvent(new win.MouseEvent("click", { bubbles: true, cancelable: true }));
  const field = q("textarea") as HTMLTextAreaElement;
  field.value = "Make it green";
  field.dispatchEvent(new win.Event("input", { bubbles: true }));
  return field;
}

test("Go to dude looks up dude's tab by the window name its session gave, not the opener's focus()", async () => {
  const opens: Array<[string, string]> = [];
  let focused = 0;
  opener!.focus = () => { focused++; };
  win.open = (url: string, name: string) => { opens.push([url, name]); return opener; };
  savedToast({ ...themed(), dudeWindow: "dude-tab-abc123" });
  await tick();
  tid("ov-save")!.click();
  const draft = sent().find((m) => m.type === "draft") as Extract<OverlayToDude, { type: "draft" }>;
  fromDude({ type: "saved", v: 1, nonce: draft.nonce, id: "fbk_1", drafts: 1, picture: "attached" });
  tid("ov-go-dude")!.click();
  expect(opens).toEqual([["", "dude-tab-abc123"]]);
  expect(focused).toBe(0);
});

test("from a session with no window name (an older dude), Go to dude falls back to the opener's focus()", async () => {
  const opens: string[] = [];
  let focused = 0;
  opener!.focus = () => { focused++; };
  win.open = (_: string, name: string) => { opens.push(name); return opener; };
  savedToast(themed());
  await tick();
  tid("ov-save")!.click();
  const draft = sent().find((m) => m.type === "draft") as Extract<OverlayToDude, { type: "draft" }>;
  fromDude({ type: "saved", v: 1, nonce: draft.nonce, id: "fbk_1", drafts: 1, picture: "attached" });
  tid("ov-go-dude")!.click();
  expect(opens).toEqual([]);
  expect(focused).toBe(1);
});

test("a name that finds no window opens a blank one: it is closed again and the opener's focus() is tried", async () => {
  let focused = 0;
  let closed = 0;
  opener!.focus = () => { focused++; };
  win.open = () => ({ location: { href: "about:blank" }, close: () => { closed++; } });
  savedToast({ ...themed(), dudeWindow: "dude-tab-gone" });
  await tick();
  tid("ov-save")!.click();
  const draft = sent().find((m) => m.type === "draft") as Extract<OverlayToDude, { type: "draft" }>;
  fromDude({ type: "saved", v: 1, nonce: draft.nonce, id: "fbk_1", drafts: 1, picture: "attached" });
  tid("ov-go-dude")!.click();
  expect([closed, focused]).toEqual([1, 1]);
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
