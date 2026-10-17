/**
 * The preview overlay in happy-dom, on a page of its own window: a fake
 * opener stands for dude's tab, messages are MessageEvents with the origin
 * and source the browser would give them. The overlay's root is closed, so
 * the tests hold it by catching attachShadow's return, as nothing in the
 * page can.
 */

import { afterEach, beforeEach, expect, mock, test } from "bun:test";
import type { DudeToOverlay, OverlayPin, OverlayToDude } from "@dude/domain/previewOverlayProtocol";
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
  const attach = win.HTMLElement.prototype.attachShadow;
  win.HTMLElement.prototype.attachShadow = function (this: HTMLElement, init: ShadowRootInit) {
    const r = attach.call(this, init);
    if (this.localName === "dude-preview-overlay") root = r;
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
afterEach(() => {
  overlay?.stop();
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
