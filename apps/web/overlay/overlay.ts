/**
 * dude's overlay on a branch preview's pages: a toolbar, picking an
 * element (hover label, ↑ wider, Esc out), the comment box, pins of the
 * task's items on this page, and the panel that says to open the preview
 * from dude when no dude tab opened it.
 *
 * It talks only to the window that opened it, and only by postMessage:
 * posted with dude's origin as the target, and heard only from that
 * origin and that window. It holds no secret and requests nothing; dude's
 * tab does everything that needs the person's sign-in. Everything it
 * draws is in a closed shadow root, so the page's CSS can't reach it and
 * its own can't leak. The page keeps working: nothing covers it but the
 * toolbar until Comment is pressed, and focus is taken only while picking
 * or writing.
 */

import {
  OVERLAY_LIMITS, OVERLAY_PROTOCOL, overlayPage,
  type DudeToOverlay, type OverlayAnchor, type OverlayPin, type OverlayToDude,
} from "@dude/domain/previewOverlayProtocol";
import { CSS } from "./css.ts";
import { anchorFor, foreignFrame, labelFor, resolveOne, selectorFor, componentOf } from "./pick.ts";
import { PictureError, drawPicture, tooBig } from "./picture.ts";

/** How long dude has to answer hello before the overlay says to open the preview from dude. */
export const SESSION_WAIT_MS = 3_000;

/** How long dude has to answer a draft with saved or refused before the box says it didn't. */
export const SAVE_WAIT_MS = 15_000;

export const SAVE_UNANSWERED = "dude didn't answer: its tab may have reloaded or left this task. Copy your words, or try Save again.";

type Connection = "waiting" | "connected" | "none" | "closed";
type Toast = { drafts: number; picture: "attached" | "none" | "failed"; why?: string };
type Picture = { state: "drawing" } | { state: "ready"; url: string } | { state: "failed"; why: string } | { state: "off"; why?: string };

export interface OverlayOptions {
  /** dude's origin, from the tag's data-dude-origin. */
  readonly dudeOrigin: string;
  /** Stand-in for drawPicture (tests: happy-dom has no layout or canvas). */
  readonly draw?: (el: Element, win: Window) => Promise<string>;
  readonly sessionWaitMs?: number;
  readonly saveWaitMs?: number;
}

const GLYPH: Record<string, string> = {
  grip: "M9 6h.01M9 12h.01M9 18h.01M15 6h.01M15 12h.01M15 18h.01",
  pick: "M4 4l7 17 2.5-7.5L21 11z",
  pins: "M12 21s-6-5.6-6-11a6 6 0 1 1 12 0c0 5.4-6 11-6 11zM12 12.5a2.5 2.5 0 1 0 0-5 2.5 2.5 0 0 0 0 5z",
  min: "M5 12h14",
  check: "M5 12.5l4.5 4.5L19 7.5",
  lock: "M7 11V8a5 5 0 0 1 10 0v3M5 11h14v10H5z",
  agent: "M12 3v3M5 9h14v10H5zM9 14h.01M15 14h.01",
  msg: "M4 5h16v11H9l-5 4z",
  dot: "M12 12h.01",
  chev: "M6 9l6 6 6-6",
  link: "M14 4h6v6M20 4l-9 9M18 14v6H4V6h6",
  frame: "M3 5h18v14H3zM3 9h18",
  warn: "M12 4l9 16H3zM12 10v4M12 17h.01",
  route: "M5 12h14M13 6l6 6-6 6",
};

const STATUS: Record<OverlayPin["status"], { label: string; glyph: string }> = {
  draft: { label: "Your draft · only you", glyph: "lock" },
  open: { label: "Open", glyph: "dot" },
  delivered: { label: "With an agent", glyph: "agent" },
  resolved: { label: "Resolved", glyph: "check" },
  replied: { label: "Answered", glyph: "msg" },
};

type Child = Node | string | null | undefined | false;
type Attrs = Record<string, string | boolean | number | undefined | ((e: Event) => void)>;

const SVG = "http://www.w3.org/2000/svg";

// An event's target as an element; the overlay's code runs in the page's realm, where instanceof may not hold.
const asElement = (t: EventTarget | null): Element | null => (t !== null && (t as Node).nodeType === 1 ? (t as Element) : null);

export class Overlay {
  readonly host: HTMLElement;
  readonly #root: ShadowRoot;
  readonly #win: Window;
  readonly #doc: Document;
  readonly #opts: OverlayOptions;
  #connection: Connection = "waiting";
  #task = "";
  #pins: OverlayPin[] = [];
  #drafts = 0;
  #showPins = true;
  #picking = false;
  #hover: Element | null = null;
  // ↑ widened past the element under the pointer: the pointer leaves it be until it moves to another element.
  #widenedFrom: Element | null = null;
  #picked: { el: Element; anchor: OverlayAnchor; frame: string | null } | null = null;
  #picture: Picture = { state: "off" };
  #attach = true;
  #field: HTMLTextAreaElement | null = null;
  #saving: string | null = null;
  #saveTimer = 0;
  // A save went unanswered: Copy text is offered beside Save.
  #unanswered = false;
  #problem: string | null = null;
  #panel: "pins" | null = null;
  #toast: Toast | null = null;
  #popover: { id: string; from?: string } | null = null;
  #focusGone: string | null = null;
  #collapsed = false;
  #page: string | null;
  #timers: number[] = [];
  #stopped = false;
  readonly #firstPage: string | null;
  #at: { right: number; bottom: number } | null = null;
  #raf = 0;

  constructor(win: Window, opts: OverlayOptions) {
    this.#win = win;
    this.#doc = win.document;
    this.#opts = opts;
    this.#page = overlayPage(win.location.href);
    this.#firstPage = this.#page;
    this.host = this.#doc.createElement("dude-preview-overlay");
    this.host.setAttribute("style", "all:initial;position:fixed;inset:0;pointer-events:none;z-index:2147483647");
    this.#root = this.host.attachShadow({ mode: "closed" });
    this.#collapsed = this.#session("hidden") === "1";
  }

  start(): void {
    (this.#doc.body ?? this.#doc.documentElement).appendChild(this.host);
    this.#win.addEventListener("message", this.#onMessage);
    for (const t of ["mouseover", "mousemove", "focusin"]) this.#win.addEventListener(t, this.#onHover, true);
    for (const t of ["click", "mousedown", "mouseup", "pointerdown", "pointerup", "touchstart", "touchend", "auxclick", "dblclick", "contextmenu"]) {
      this.#win.addEventListener(t, this.#onPress, true);
    }
    this.#win.addEventListener("keydown", this.#onKey, true);
    this.#win.addEventListener("scroll", this.#later, true);
    this.#win.addEventListener("resize", this.#later);
    this.#win.addEventListener("popstate", this.#onRoute);
    this.#wrapHistory();
    if (!this.#opener()) {
      this.#connection = "none";
    } else {
      this.#post({ type: "hello", v: OVERLAY_PROTOCOL, url: this.#win.location.href });
      this.#timers.push(this.#win.setTimeout(() => {
        if (this.#connection === "waiting") this.#lost("none");
      }, this.#opts.sessionWaitMs ?? SESSION_WAIT_MS));
      this.#timers.push(this.#win.setInterval(() => {
        if (this.#connection === "connected" && !this.#opener()) this.#lost("closed");
      }, 1_000));
    }
    // Pins follow what the page draws, which may move without a scroll.
    this.#timers.push(this.#win.setInterval(() => this.#pins.length > 0 && this.#showPins && this.#render(), 1_500));
    this.#render();
  }

  stop(): void {
    this.#stopped = true;
    this.#win.clearTimeout(this.#saveTimer);
    for (const t of this.#timers) { this.#win.clearTimeout(t); this.#win.clearInterval(t); }
    this.#win.removeEventListener("message", this.#onMessage);
    for (const t of ["mouseover", "mousemove", "focusin"]) this.#win.removeEventListener(t, this.#onHover, true);
    for (const t of ["click", "mousedown", "mouseup", "pointerdown", "pointerup", "touchstart", "touchend", "auxclick", "dblclick", "contextmenu"]) {
      this.#win.removeEventListener(t, this.#onPress, true);
    }
    this.#win.removeEventListener("keydown", this.#onKey, true);
    this.#win.removeEventListener("scroll", this.#later, true);
    this.#win.removeEventListener("resize", this.#later);
    this.#win.removeEventListener("popstate", this.#onRoute);
    this.host.remove();
  }

  // ------------------------------------------------------------------ dude

  #opener(): Window | null {
    try {
      const o = this.#win.opener as Window | null;
      return o && !o.closed ? o : null;
    } catch {
      return null;
    }
  }

  #post(msg: OverlayToDude): boolean {
    const o = this.#opener();
    if (!o) return false;
    o.postMessage(msg, this.#opts.dudeOrigin);
    return true;
  }

  #lost(to: "none" | "closed"): void {
    this.#connection = to;
    this.#picking = false;
    this.#hover = null;
    this.#saveDone();
    this.#render();
  }

  readonly #onMessage = (e: MessageEvent): void => {
    if (e.origin !== this.#opts.dudeOrigin || e.source !== this.#opener() || e.source === null) return;
    const m = e.data as DudeToOverlay;
    if (!m || typeof m !== "object" || m.v !== OVERLAY_PROTOCOL) return;
    switch (m.type) {
      case "session":
        if (overlayPage(m.url) !== this.#page) return;
        this.#connection = "connected";
        this.#task = String(m.task).slice(0, 40);
        this.#pins = Array.isArray(m.pins) ? m.pins.slice(0, OVERLAY_LIMITS.pins) : [];
        this.#drafts = Number(m.drafts) || 0;
        this.#showPins = m.showPins !== false;
        if (this.#unanswered) {
          // dude's tab is back: the box keeps its words, and Save is for it again.
          this.#unanswered = false;
          this.#problem = null;
        }
        break;
      case "saved":
        if (m.nonce !== this.#saving) return;
        this.#saveDone();
        this.#drafts = Number(m.drafts) || this.#drafts;
        this.#toast = { drafts: this.#drafts, picture: m.picture, ...(this.#picture.state === "failed" ? { why: this.#picture.why } : {}) };
        this.#closeBox(false);
        break;
      case "refused":
        if (m.nonce !== undefined && m.nonce !== this.#saving) return;
        this.#saveDone();
        this.#problem = String(m.reason).slice(0, 500);
        break;
      case "highlight":
        this.#highlight(String(m.itemId));
        break;
      case "stop":
        this.stop();
        return;
      default:
        return;
    }
    this.#render();
  };

  // ------------------------------------------------------------------ the page

  #wrapHistory(): void {
    const h = this.#win.history;
    for (const name of ["pushState", "replaceState"] as const) {
      const orig = h[name];
      const self = this;
      h[name] = function (this: History, ...args: Parameters<History["pushState"]>) {
        const out = orig.apply(this, args);
        self.#onRoute();
        return out;
      };
    }
  }

  readonly #onRoute = (): void => {
    if (this.#stopped) return;
    const page = overlayPage(this.#win.location.href);
    if (page === this.#page) return;
    this.#page = page;
    this.#pins = [];
    this.#popover = null;
    // Before a session, too: dude's answer to the first hello names a page this one has left.
    if (this.#connection === "connected" || this.#connection === "waiting") this.#post({ type: "navigated", v: OVERLAY_PROTOCOL, url: this.#win.location.href });
    this.#render();
  };

  #ours(e: Event): boolean {
    return e.target === this.host || e.composedPath().includes(this.host);
  }

  readonly #onHover = (e: Event): void => {
    if (!this.#picking || this.#picked || this.#ours(e)) return;
    const t = asElement(e.target);
    if (!t || t === this.#doc.documentElement || t === this.#doc.body) return;
    if (this.#widenedFrom && (t === this.#widenedFrom || this.#widenedFrom.contains(t) && this.#hover?.contains(t))) return;
    this.#widenedFrom = null;
    if (t !== this.#hover) {
      this.#hover = t;
      this.#render();
    }
  };

  readonly #onPress = (e: Event): void => {
    if (!this.#picking || this.#ours(e)) return;
    // The page under the pick never hears it: no link followed, no button pressed.
    e.preventDefault();
    e.stopImmediatePropagation();
    if (e.type === "click" && !this.#picked) {
      const t = asElement(e.target);
      if (t && t !== this.#hover && !(this.#hover?.contains(t))) this.#hover = t;
      if (this.#hover) this.#pick(this.#hover);
    }
  };

  readonly #onKey = (e: KeyboardEvent): void => {
    if (this.#stopped) return;
    const ours = this.#ours(e);
    if (e.key === "Escape") {
      if (this.#back()) {
        e.preventDefault();
        e.stopImmediatePropagation();
      }
      return;
    }
    if (!this.#picking || this.#picked) return;
    // ↑ works with focus on the overlay's own buttons too (Comment was just pressed); Enter there is that button.
    if (e.key === "ArrowUp" && this.#hover) {
      e.preventDefault();
      e.stopImmediatePropagation();
      this.widen();
    } else if (e.key === "Enter" && this.#hover && !ours) {
      e.preventDefault();
      e.stopImmediatePropagation();
      this.#pick(this.#hover);
    }
  };

  /** One step back, as Esc takes it: the box, then picking, then a popover or panel. Whether there was one. */
  #back(): boolean {
    if (this.#picked) {
      this.#closeBox(true);
    } else if (this.#picking) {
      this.#setPicking(false);
    } else if (this.#popover) {
      this.#popover = null;
    } else if (this.#panel) {
      this.#panel = null;
    } else if (this.#toast) {
      this.#toast = null;
    } else return false;
    this.#render();
    return true;
  }

  /** ↑: the hovered element's parent, short of the page itself. */
  widen(): void {
    const up = this.#hover?.parentElement;
    if (!up || up === this.#doc.body || up === this.#doc.documentElement) return;
    this.#widenedFrom ??= this.#hover;
    this.#hover = up;
    this.#render();
  }

  #setPicking(on: boolean): void {
    this.#picking = on && this.#connection === "connected";
    this.#hover = null;
    this.#widenedFrom = null;
    if (this.#picking) {
      this.#panel = null;
      this.#popover = null;
      this.#toast = null;
    }
    this.#render();
    if (!on) this.#focusKey("comment");
  }

  #pick(el: Element): void {
    const frame = foreignFrame(el, this.#win);
    this.#picked = { el, anchor: anchorFor(el, this.#win), frame };
    this.#problem = null;
    this.#field = this.#doc.createElement("textarea");
    this.#field.setAttribute("aria-label", "Comment");
    this.#field.setAttribute("placeholder", "What should change here…");
    this.#field.setAttribute("maxlength", String(OVERLAY_LIMITS.body));
    this.#field.rows = 4;
    this.#field.addEventListener("keydown", (e) => {
      if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
        e.preventDefault();
        this.#save();
      }
    });
    this.#field.addEventListener("input", () => this.#render());
    const big = tooBig(el, this.#win);
    this.#attach = !big && !frame;
    this.#picture = frame ? { state: "off", why: "a frame from another site can't be drawn" }
      : big ? { state: "off", why: "it is larger than the window" } : { state: "drawing" };
    if (this.#attach) this.#drawFor(el);
    this.#render();
    this.#field.focus();
  }

  #drawFor(el: Element): void {
    this.#picture = { state: "drawing" };
    const draw = this.#opts.draw ?? drawPicture;
    draw(el, this.#win).then((url) => {
      if (this.#picked?.el !== el) return;
      this.#picture = { state: "ready", url };
      this.#render();
    }, (err: unknown) => {
      if (this.#picked?.el !== el) return;
      this.#picture = { state: "failed", why: err instanceof PictureError ? err.message : "the browser couldn't draw it" };
      this.#render();
    });
  }

  #closeBox(backToPicking: boolean): void {
    this.#picked = null;
    this.#field = null;
    this.#saveDone();
    this.#problem = null;
    this.#picture = { state: "off" };
    this.#hover = null;
    this.#picking = backToPicking && this.#connection === "connected";
    this.#render();
    this.#focusKey("comment");
  }

  #save(): void {
    if (!this.#picked || !this.#field || this.#saving) return;
    const body = this.#field.value.trim();
    if (!body) return;
    if (this.#connection !== "connected") return;
    if (this.#picture.state === "drawing") {
      this.#problem = "Still drawing the picture: a moment.";
      this.#render();
      return;
    }
    const nonce = `d${Date.now().toString(36)}${Math.random().toString(36).slice(2, 8)}`;
    const msg: OverlayToDude = { type: "draft", v: OVERLAY_PROTOCOL, nonce, body: body.slice(0, OVERLAY_LIMITS.body), anchor: this.#picked.anchor };
    if (this.#attach && this.#picture.state === "ready") msg.picture = this.#picture.url;
    if (!this.#post(msg)) {
      this.#lost("closed");
      return;
    }
    this.#saving = nonce;
    this.#problem = null;
    this.#saveTimer = this.#win.setTimeout(() => this.#saveUnanswered(nonce), this.#opts.saveWaitMs ?? SAVE_WAIT_MS);
    this.#render();
  }

  #saveDone(): void {
    this.#saving = null;
    this.#unanswered = false;
    this.#win.clearTimeout(this.#saveTimer);
  }

  /**
   * No saved or refused in time: dude's tab reloaded or left the task, and
   * the opener is another document now. The box keeps the words, offers
   * Copy text, and says hello again so a dude tab on this task can answer.
   */
  #saveUnanswered(nonce: string): void {
    if (this.#saving !== nonce || this.#stopped) return;
    this.#saving = null;
    this.#unanswered = true;
    this.#problem = SAVE_UNANSWERED;
    this.#post({ type: "hello", v: OVERLAY_PROTOCOL, url: this.#win.location.href });
    this.#render();
  }

  async #copy(): Promise<void> {
    const text = this.#field?.value ?? "";
    try {
      await this.#win.navigator.clipboard.writeText(text);
      this.#problem = "Copied.";
    } catch {
      this.#field?.select();
      this.#problem = "Select the words and copy them.";
    }
    this.#render();
  }

  #highlight(id: string): void {
    const pin = this.#pins.find((p) => p.id === id);
    if (!pin) return;
    this.#picking = false;
    this.#panel = null;
    const el = resolveOne(this.#doc, pin.selector);
    if (el) {
      el.scrollIntoView?.({ block: "center", behavior: "smooth" });
      this.#popover = { id, from: `Shown from dude · ${pin.mine ? "your draft" : `${pin.author}'s comment`} ${pin.n}` };
    } else {
      this.#popover = null;
      this.#panel = "pins";
      this.#focusGone = id;
    }
    this.#collapsed = false;
    this.#render();
  }

  readonly #later = (): void => {
    if (this.#raf) return;
    this.#raf = this.#win.requestAnimationFrame?.(() => {
      this.#raf = 0;
      this.#render();
    }) ?? 0;
    if (!this.#raf) this.#render();
  };

  #session(key: string, value?: string): string | null {
    try {
      const k = `dude-overlay-${key}`;
      if (value !== undefined) this.#win.sessionStorage.setItem(k, value);
      return this.#win.sessionStorage.getItem(k);
    } catch {
      return null;
    }
  }

  // ------------------------------------------------------------------ drawing

  #h(tag: string, attrs: Attrs = {}, ...children: Child[]): HTMLElement {
    const el = this.#doc.createElement(tag);
    for (const [k, v] of Object.entries(attrs)) {
      if (v === undefined || v === false) continue;
      if (typeof v === "function") el.addEventListener(k.slice(2).toLowerCase(), v);
      else if (k === "style") el.setAttribute("style", String(v));
      else el.setAttribute(k, v === true ? "" : String(v));
    }
    for (const c of children) if (c !== null && c !== undefined && c !== false) el.append(c);
    return el;
  }

  #g(name: string, size = 14): SVGElement {
    const svg = this.#doc.createElementNS(SVG, "svg");
    for (const [k, v] of Object.entries({ width: size, height: size, viewBox: "0 0 24 24", fill: "none", stroke: "currentColor", "stroke-width": 2,
      "stroke-linecap": "round", "stroke-linejoin": "round", "aria-hidden": "true" })) svg.setAttribute(k, String(v));
    const path = this.#doc.createElementNS(SVG, "path");
    path.setAttribute("d", GLYPH[name] ?? "");
    svg.append(path);
    return svg;
  }

  #focusKey(key: string): void {
    (this.#root.querySelector(`[data-k="${key}"]`) as HTMLElement | null)?.focus?.();
  }

  #render(): void {
    if (this.#stopped) return;
    const active = this.#root.activeElement as HTMLElement | null;
    const activeKey = active?.getAttribute?.("data-k") ?? null;
    const fieldFocused = active !== null && active === this.#field;
    const sel = fieldFocused ? [this.#field!.selectionStart, this.#field!.selectionEnd] : null;
    const narrow = this.#win.innerWidth <= 520;
    const root = this.#h("div", { class: `root${narrow ? " narrow" : ""}`, "data-overlay": "" });
    for (const layer of this.#layers(narrow)) if (layer) root.append(layer);
    this.#root.replaceChildren(this.#h("style", {}, CSS), root);
    if (fieldFocused && this.#field) {
      this.#field.focus();
      if (sel) this.#field.setSelectionRange(sel[0] ?? 0, sel[1] ?? 0);
    } else if (activeKey) this.#focusKey(activeKey);
  }

  #layers(narrow: boolean): Child[] {
    const out: Child[] = [];
    if (this.#showPins && this.#connection === "connected" && !this.#picking && !this.#picked) out.push(...this.#pinLayer());
    if (this.#picking && !this.#picked) out.push(...this.#frameCatchers());
    if (this.#picking && !this.#picked && this.#hover) out.push(...this.#hoverLayer(this.#hover));
    if (this.#picked) out.push(...this.#boxLayer(narrow));
    out.push(this.#collapsed && !this.#picking && !this.#picked ? this.#pill() : this.#bar(narrow));
    return out;
  }

  #rect(el: Element): DOMRect {
    return el.getBoundingClientRect();
  }

  #hoverLayer(el: Element): Child[] {
    const r = this.#rect(el);
    const frame = foreignFrame(el, this.#win);
    const box = this.#h("div", { class: `hl${frame ? " warn" : ""}`, "data-testid": "ov-highlight",
      style: `left:${r.left - 2}px;top:${r.top - 2}px;width:${r.width + 4}px;height:${r.height + 4}px` });
    const top = r.top > 30 ? r.top - 28 : r.bottom + 6;
    const label = frame
      ? this.#h("div", { class: "label warn", "data-testid": "ov-label", style: `left:${Math.max(4, r.left - 2)}px;top:${top}px` }, this.#g("frame", 12),
        this.#h("span", {}, this.#h("b", {}, "Can't pick inside this frame"), ` — it comes from ${frame.replace(/^https?:\/\//, "")}, which dude can't read. Click to comment on the frame as a whole.`))
      : this.#h("div", { class: "label", "data-testid": "ov-label", style: `left:${Math.max(4, r.left - 2)}px;top:${top}px` },
        this.#h("span", { class: "mono" }, labelFor(selectorFor(el), componentOf(el))),
        this.#h("span", { class: "dim" }, `${Math.round(r.width)}×${Math.round(r.height)}`));
    const out: Child[] = [box, label];
    return out;
  }

  /**
   * While picking, a catcher over each frame from another site: the page
   * hears no pointer inside such a frame, so the catcher is where hovering
   * says it can't be read and a click picks the frame as a whole.
   */
  #frameCatchers(): Child[] {
    const out: Child[] = [];
    for (const f of this.#doc.querySelectorAll("iframe, frame")) {
      if (!foreignFrame(f, this.#win)) continue;
      const r = this.#rect(f);
      if (r.width < 1 || r.height < 1) continue;
      const over = () => {
        if (this.#hover !== f) {
          this.#hover = f;
          this.#widenedFrom = null;
          this.#render();
        }
      };
      out.push(this.#h("div", { class: "shield", "data-testid": "ov-frame-catcher",
        style: `inset:auto;left:${r.left}px;top:${r.top}px;width:${r.width}px;height:${r.height}px`,
        onmouseover: over, onmousemove: over,
        onclick: (e: Event) => { e.preventDefault(); this.#pick(f); } }));
    }
    return out;
  }

  #pinLayer(): Child[] {
    const out: Child[] = [];
    for (const p of this.#pins) {
      const el = resolveOne(this.#doc, p.selector);
      if (!el) continue;
      const r = this.#rect(el);
      const active = this.#popover?.id === p.id;
      if (active && this.#popover?.from) {
        out.push(this.#h("div", { class: "hl show", "data-testid": "ov-show", style: `left:${r.left - 3}px;top:${r.top - 3}px;width:${r.width + 6}px;height:${r.height + 6}px` }));
      }
      const st = STATUS[p.status] ?? STATUS.open;
      out.push(this.#h("button", { type: "button", class: `pin ${p.status}${active ? " active" : ""}`, "data-testid": "ov-pin", "data-id": p.id, "data-k": `pin-${p.id}`,
        "data-status": p.status, "aria-expanded": active ? "true" : "false",
        "aria-label": `${p.n}: ${p.mine ? "your draft" : `${p.author}'s comment`}, ${st.label}`,
        style: `left:${Math.max(4, r.right - 18)}px;top:${Math.max(4, r.top - 14)}px`,
        onclick: () => { this.#popover = active ? null : { id: p.id }; this.#render(); } }, String(p.n), this.#g(st.glyph, 11)));
      if (active) out.push(this.#popoverFor(p, r));
    }
    return out;
  }

  #popoverFor(p: OverlayPin, r: DOMRect): HTMLElement {
    const st = STATUS[p.status] ?? STATUS.open;
    const pos = this.#beside(r, 340, 300);
    return this.#h("section", { class: "pop", role: "dialog", "aria-label": `Comment ${p.n}`, "data-testid": "ov-popover", style: `left:${pos.left}px;top:${pos.top}px` },
      this.#popover?.from ? this.#h("p", { class: "from" }, this.#popover.from) : null,
      this.#h("header", { class: "head" }, this.#h("b", {}, p.mine ? "You" : p.author), this.#h("span", { class: "dim" }, p.when), this.#h("span", { class: "sp" }),
        this.#h("span", { class: "st" }, this.#g(st.glyph, 11), ` ${st.label}`)),
      this.#h("p", { class: "body" }, p.body),
      ...p.replies.map((x) => this.#h("div", { class: "entry" }, this.#h("b", {}, x.who), this.#h("span", { class: "dim" },
        x.kind === "resolve" ? " resolved it" : x.kind === "reopen" ? " reopened it" : " answered"), x.body ? this.#h("p", {}, x.body) : null)),
      this.#h("footer", { class: "foot" }, this.#h("span", { class: "dim" }, p.status === "draft" ? "Edit or delete it in dude" : "Reply or resolve it in dude"),
        this.#h("span", { class: "sp" }),
        this.#h("button", { type: "button", "data-k": "open-in-dude", onclick: () => this.#openInDude(p.id) }, this.#g("link", 12), "Open in dude")));
  }

  #openInDude(id: string): void {
    this.#post({ type: "open", v: OVERLAY_PROTOCOL, itemId: id });
    try { this.#opener()?.focus(); } catch { /* another origin's window may refuse */ }
  }

  #beside(r: DOMRect, w: number, h: number): { left: number; top: number } {
    const W = this.#win.innerWidth, H = this.#win.innerHeight;
    const top = Math.max(8, Math.min(r.top - 40, H - h - 8));
    if (r.right + 14 + w <= W - 8) return { left: r.right + 14, top };
    if (r.left - 14 - w >= 8) return { left: r.left - 14 - w, top };
    return { left: Math.max(8, Math.min(r.left, W - w - 8)), top: Math.max(8, Math.min(r.bottom + 14, H - h - 8)) };
  }

  #boxLayer(narrow: boolean): Child[] {
    const { el, anchor, frame } = this.#picked!;
    const r = this.#rect(el);
    const out: Child[] = [this.#h("div", { class: "hl picked", "data-testid": "ov-picked", style: `left:${r.left - 2}px;top:${r.top - 2}px;width:${r.width + 4}px;height:${r.height + 4}px` })];
    const connected = this.#connection === "connected";
    const name = frame ? `the frame from ${frame.replace(/^https?:\/\//, "")}` : labelFor(anchor.selector ?? "", anchor.component);
    const pos = this.#beside(r, 380, 480);
    const pic = this.#picture;
    const picLine = pic.state === "failed"
      ? this.#h("p", { class: "note warn", "data-testid": "ov-picture-failed", role: "status" }, this.#g("warn", 12), ` Couldn't draw a picture: ${pic.why}. It saves without one.`)
      : pic.state === "off" && pic.why ? this.#h("p", { class: "note" }, `No picture: ${pic.why}.`) : null;
    const field = this.#field!;
    const canSave = connected && !this.#saving && field.value.trim() !== "";
    const facts = this.#h("dl", {},
      this.#h("dt", {}, "Page"), this.#h("dd", { class: "mono" }, anchor.url.replace(/^https?:\/\//, "")),
      this.#h("dt", {}, "Window"), this.#h("dd", {}, `${anchor.viewport?.width}×${anchor.viewport?.height}`),
      this.#h("dt", {}, "Element"), this.#h("dd", { class: "mono" }, anchor.selector ?? ""),
      this.#h("dt", {}, "Box"), this.#h("dd", {}, anchor.rect ? `${anchor.rect.width}×${anchor.rect.height} at ${anchor.rect.x}, ${anchor.rect.y}` : ""),
      anchor.text ? this.#h("dt", {}, "Text") : null, anchor.text ? this.#h("dd", {}, `“${anchor.text.slice(0, 200)}${anchor.text.length > 200 ? "…" : ""}”`) : null,
      this.#h("dt", {}, "Component"), this.#h("dd", {}, anchor.component?.name
        ? `${anchor.component.name}${anchor.component.file ? ` ${anchor.component.file}${anchor.component.line ? `:${anchor.component.line}` : ""}` : ""}` : this.#h("span", { class: "dim" }, "The app doesn't say")),
      this.#h("dt", {}, "HTML"), this.#h("dd", { class: "dim" }, `the element's outer HTML, ${anchor.html?.length ?? 0} characters`),
      this.#h("dt", {}, "Picture"), this.#h("dd", {}, this.#attach && pic.state === "ready" ? this.#h("img", { class: "thumb", src: pic.url, alt: "The picture that would be attached" })
        : this.#h("span", { class: "dim" }, this.#attach && pic.state === "drawing" ? "drawing…" : "none")));
    out.push(this.#h("section", { class: `box${narrow ? " sheet" : ""}`, role: "dialog", "aria-label": `Comment on ${name}`, "data-testid": "ov-comment-box",
      style: `left:${pos.left}px;top:${pos.top}px` },
      this.#h("header", { class: "bh" }, this.#h("span", {}, "Comment on ", this.#h("b", { class: "mono" }, name)),
        this.#h("span", { class: "dim" }, this.#g("lock", 11), " Private draft until you submit in dude")),
      field,
      this.#h("label", {}, this.#h("input", { type: "checkbox", "data-testid": "ov-attach", "data-k": "attach", checked: this.#attach,
        disabled: pic.state === "off" || pic.state === "failed",
        onchange: (e: Event) => {
          this.#attach = (e.target as HTMLInputElement).checked;
          if (this.#attach && pic.state === "off") this.#drawFor(el);
          this.#render();
        } }), this.#h("span", {}, "Attach a picture of this element ", this.#h("span", { class: "dim" }, "(drawn from the page)"))),
      picLine,
      this.#h("details", { "data-testid": "ov-what-dude-sends" }, this.#h("summary", { "data-k": "sends" }, "What dude will send"), facts,
        this.#h("p", { class: "dim" }, "Nothing leaves this page until you save. Then it is a draft only you see in dude.")),
      connected ? null : this.#h("p", { class: "note warn", role: "alert", "data-testid": "ov-disconnected" }, this.#g("warn", 12),
        this.#connection === "closed" ? " dude's tab was closed: this can't be saved from here. Copy your words, then open the task again."
          : " This preview wasn't opened from dude: this can't be saved from here. Copy your words."),
      this.#problem ? this.#h("p", { class: "note warn", role: "alert", "data-testid": "ov-problem" }, this.#problem) : null,
      this.#h("footer", { class: "foot" },
        narrow || !connected ? null : this.#h("span", { class: "dim" }, this.#h("kbd", {}, "⌘"), this.#h("kbd", {}, "↵"), " save · ", this.#h("kbd", {}, "Esc"), " cancel"),
        this.#h("span", { class: "sp" }),
        connected
          ? this.#h("button", { type: "button", "data-k": "cancel", onclick: () => this.#closeBox(false) }, "Cancel")
          : null,
        !connected || this.#unanswered
          ? this.#h("button", { type: "button", "data-k": "copy", "data-testid": "ov-copy", onclick: () => void this.#copy() }, "Copy text") : null,
        connected ? this.#h("button", { type: "button", class: "primary", "data-k": "save", "data-testid": "ov-save", disabled: !canSave, onclick: () => this.#save() },
          this.#saving ? "Saving…" : "Save draft") : null)));
    return out;
  }

  #pill(): HTMLElement {
    return this.#h("button", { type: "button", class: "pill", "data-testid": "ov-pill", "data-k": "pill",
      "aria-label": `dude: comment on this preview${this.#drafts ? `, ${this.#drafts} drafts` : ""}`,
      onclick: () => { this.#collapsed = false; this.#session("hidden", "0"); this.#render(); this.#focusKey("collapse"); } },
      this.#h("span", { class: "mark" }, "d"), this.#drafts ? this.#h("span", {}, String(this.#drafts)) : null);
  }

  #bar(narrow: boolean): HTMLElement {
    const connected = this.#connection === "connected";
    const pinsHere = this.#pins.length;
    const conn = connected ? "connected to dude" : this.#connection === "waiting" ? "reaching dude…" : this.#connection === "closed" ? "dude's tab closed" : "not opened from dude";
    const bar = this.#h("div", { class: "bar", role: "toolbar", "aria-label": "dude feedback", "data-testid": "ov-toolbar", "data-connection": this.#connection },
      this.#panelFor(),
      this.#h("div", { class: "row" },
        narrow ? null : this.#h("span", { class: "grip", title: "Drag to move", "aria-hidden": "true", onpointerdown: (e: Event) => this.#drag(e as PointerEvent) }, this.#g("grip")),
        this.#h("span", { class: "who" }, this.#h("span", { class: "mark" }, "d"),
          this.#h("span", { class: "whoText" }, this.#h("b", {}, this.#task || "dude"), this.#h("span", { class: `conn${connected ? "" : " off"}`, "data-testid": "ov-conn" }, conn))),
        connected ? this.#h("button", { type: "button", class: this.#picking ? "on" : "primary", "aria-pressed": this.#picking ? "true" : "false",
          "data-testid": "ov-comment", "data-k": "comment", onclick: () => this.#setPicking(!this.#picking) },
          this.#g(this.#picking ? "check" : "pick"), this.#picking ? "Done" : "Comment") : null,
        connected ? this.#h("button", { type: "button", class: this.#panel === "pins" ? "on" : "", "aria-expanded": this.#panel === "pins" ? "true" : "false",
          "data-testid": "ov-pins", "data-k": "pins", title: this.#showPins ? "Comments on this page" : "Pins are hidden",
          onclick: () => { this.#panel = this.#panel === "pins" ? null : "pins"; this.#focusGone = null; this.#render(); } },
          this.#g("pins"), narrow ? String(pinsHere) : `${pinsHere} on this page`, this.#g("chev", 12)) : null,
        connected && this.#drafts > 0 && !narrow ? this.#h("span", { class: "drafts", title: "Saved to your drafts in dude: only you see them until you submit there", "data-testid": "ov-drafts" },
          this.#g("lock", 12), ` ${this.#drafts} ${this.#drafts === 1 ? "draft" : "drafts"} in dude`) : null,
        this.#h("button", { type: "button", class: "icon", "aria-label": "Collapse to a dot", title: "Collapse", "data-k": "collapse",
          onclick: () => { this.#collapsed = true; this.#setPicking(false); this.#render(); this.#focusKey("pill"); } }, this.#g("min"))),
      this.#picking ? this.#h("div", { class: "hint", "data-testid": "ov-hint" }, narrow ? this.#h("span", {}, "Tap to pick")
        : this.#h("span", {}, this.#h("b", {}, "Click"), " to pick · ", this.#h("kbd", {}, "↑"), " wider · ", this.#h("kbd", {}, "Esc"), " stop")) : null,
      connected && !this.#picking && this.#page !== this.#firstPage ? this.#h("div", { class: "hint" }, this.#routeNote()) : null);
    if (!narrow && this.#at) bar.setAttribute("style", `right:${this.#at.right}px;bottom:${this.#at.bottom}px`);
    return bar;
  }

  #routeNote(): HTMLElement {
    const path = (() => { try { return new URL(this.#win.location.href).pathname; } catch { return ""; } })();
    return this.#h("span", { "data-testid": "ov-route" }, this.#g("route", 12), " Now on ", this.#h("b", { class: "mono" }, path),
      this.#pins.length === 0 ? " · no comments here yet" : ` · ${this.#pins.length} here`);
  }

  #drag(e: PointerEvent): void {
    const startX = e.clientX, startY = e.clientY;
    const from = this.#at ?? { right: 20, bottom: 20 };
    const move = (m: PointerEvent) => {
      const W = this.#win.innerWidth, H = this.#win.innerHeight;
      this.#at = { right: Math.min(W - 80, Math.max(4, from.right - (m.clientX - startX))), bottom: Math.min(H - 40, Math.max(4, from.bottom - (m.clientY - startY))) };
      this.#render();
    };
    const up = () => { this.#win.removeEventListener("pointermove", move); this.#win.removeEventListener("pointerup", up); };
    this.#win.addEventListener("pointermove", move);
    this.#win.addEventListener("pointerup", up);
  }

  #panelFor(): HTMLElement | null {
    if (this.#connection === "none" || this.#connection === "closed") return this.#noOpener();
    if (this.#toast && !this.#picking) return this.#toastFor(this.#toast);
    if (this.#panel === "pins") return this.#pinsPanel();
    return null;
  }

  #noOpener(): HTMLElement {
    const closed = this.#connection === "closed";
    return this.#h("div", { class: "panel", "data-testid": "ov-no-opener" },
      this.#h("div", { class: "ph" }, this.#h("b", {}, closed ? "dude's tab was closed" : "Open this preview from dude to comment")),
      this.#h("p", { class: "pp" }, closed
        ? "Your saved drafts are kept in dude. To pick more, open the task again and press Comment on preview: this window is reused."
        : "Comments on a preview are made from its task, so dude knows who you are and which task they belong to. On the task's Servers tab, press Comment."),
      this.#h("div", { class: "pf" },
        this.#h("a", { class: "btn primary", href: this.#opts.dudeOrigin, target: "_blank", rel: "noopener", "data-k": "open-dude" }, this.#g("link", 12), "Open dude"),
        this.#h("button", { type: "button", "data-k": "hide", onclick: () => { this.#collapsed = true; this.#session("hidden", "1"); this.#render(); this.#focusKey("pill"); } }, "Hide for this tab")));
  }

  #toastFor(t: Toast): HTMLElement {
    const pic = t.picture === "attached" ? "with its picture" : t.picture === "failed" ? "without its picture: dude couldn't keep it" : t.why ? `without a picture: ${t.why}` : "with no picture";
    return this.#h("div", { class: "toast", role: "status", "data-testid": "ov-saved" },
      this.#h("span", { class: "okc" }, this.#g("check")),
      this.#h("span", { class: "tt" }, this.#h("b", {}, "Saved to your drafts in dude"),
        this.#h("span", { class: "dim" }, `${t.drafts} ${t.drafts === 1 ? "draft" : "drafts"} on ${this.#task} · ${pic} · only you see them until you submit there`)),
      this.#h("button", { type: "button", "data-k": "go-dude", onclick: () => { try { this.#opener()?.focus(); } catch { /* refused */ } } }, this.#g("link", 12), "Go to dude"));
  }

  #pinsPanel(): HTMLElement {
    const found = this.#pins.filter((p) => resolveOne(this.#doc, p.selector));
    const gone = this.#pins.filter((p) => !resolveOne(this.#doc, p.selector));
    const row = (p: OverlayPin, isGone: boolean) => this.#h("li", { class: isGone && this.#focusGone === p.id ? "focus" : undefined, "data-testid": isGone ? "ov-gone" : "ov-row", "data-id": p.id },
      this.#h("span", { class: `pin inline ${isGone ? "gone" : p.status}` }, String(p.n)),
      this.#h("span", { class: "lt" },
        isGone ? this.#h("span", { class: "warnLine" }, this.#g("warn", 12), " Couldn't find this on the page — it may have changed.") : null,
        this.#h("span", {}, this.#h("b", {}, p.mine ? "You" : p.author), ` ${p.body.slice(0, 160)}`),
        isGone ? this.#h("span", { class: "mono dim" }, `was ${p.selector}`) : this.#h("span", { class: "dim" }, STATUS[p.status]?.label ?? "")),
      isGone ? this.#h("button", { type: "button", "data-k": `open-${p.id}`, onclick: () => this.#openInDude(p.id) }, this.#g("link", 12), "Open in dude")
        : this.#h("button", { type: "button", "data-k": `show-${p.id}`, onclick: () => { this.#panel = null; this.#popover = { id: p.id }; this.#render(); } }, "Show"));
    return this.#h("div", { class: "panel", "data-testid": "ov-pins-panel" },
      this.#h("div", { class: "ph" }, this.#h("b", {}, "On this page"),
        this.#h("label", {}, this.#h("input", { type: "checkbox", "data-testid": "ov-show-pins", "data-k": "show-pins", checked: this.#showPins,
          onchange: (e: Event) => {
            this.#showPins = (e.target as HTMLInputElement).checked;
            this.#post({ type: "prefs", v: OVERLAY_PROTOCOL, showPins: this.#showPins });
            this.#render();
          } }), "Show pins")),
      found.length === 0 && gone.length === 0 ? this.#h("p", { class: "pp" }, "No comments on this page yet.") : null,
      found.length > 0 ? this.#h("ul", {}, ...found.map((p) => row(p, false))) : null,
      gone.length > 0 ? this.#h("div", { class: "ph" }, this.#h("b", {}, "Not found on this page")) : null,
      gone.length > 0 ? this.#h("ul", {}, ...gone.map((p) => row(p, true))) : null);
  }
}
