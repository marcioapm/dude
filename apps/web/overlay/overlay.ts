/**
 * dude's overlay on a branch preview's pages: a toolbar, picking an
 * element (hover label, ↑ wider, Esc out), the comment box, pins of the
 * task's items on this page, and the panel offering Connect to dude when no
 * dude tab is reachable.
 *
 * It talks only to one dude window, and only by postMessage: the window
 * that opened it (or opened the preview page that opened it), the dude tab
 * its Connect to dude opened, or the dude window that last answered.
 * Posted with dude's origin as the target, and heard only from that origin
 * and that window. It holds no secret and requests nothing; dude's tab
 * does everything that needs the person's sign-in. Everything it
 * draws is in a closed shadow root, so the page's CSS can't reach it and
 * its own can't leak. The page keeps working: nothing covers it but the
 * toolbar until Comment is pressed, and focus is taken only while picking
 * or writing.
 */

import {
  OVERLAY_LIMITS, OVERLAY_OPENER_STEPS, OVERLAY_PROTOCOL, OVERLAY_THEMES, overlayPage, overlayWindowName,
  type DudeToOverlay, type OverlayAnchor, type OverlayPin, type OverlayTheme, type OverlayToDude,
} from "@dude/domain/previewOverlayProtocol";
import { CSS } from "./css.ts";
import { TOKENS_CSS } from "./tokens.gen.ts";
import { anchorFor, foreignFrame, labelFor, resolveOne, selectorFor, componentOf } from "./pick.ts";
import { PictureError, drawPicture, tooBig } from "./picture.ts";

/** How long dude has to answer hello before the overlay says to open the preview from dude. */
export const SESSION_WAIT_MS = 3_000;

/** How long dude has to answer a draft with saved or refused before the box says it didn't. */
export const SAVE_WAIT_MS = 15_000;

/**
 * The heartbeat: while not connected, hello is said again this often, so a
 * dude tab that reloaded (or a preview that did) finds the other again. While
 * connected, hello is said after two beats with nothing from dude and again
 * after three; after four the connection waits.
 */
export const HEARTBEAT_MS = 2_000;

/** Beats waiting with no answer, counted from the drop to waiting (10 s at HEARTBEAT_MS), before the panel offers Connect to dude again. */
const WAITING_BEATS = 5;

// The dude tab Connect to dude opened, by its window name, in this tab's sessionStorage (outlives a reload).
const CONNECT_NAME_KEY = "dudeWindow";

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
  readonly heartbeatMs?: number;
  /** How often pins are moved to follow the page. */
  readonly tickMs?: number;
}

interface PinNodes {
  readonly key: string;
  readonly pin: HTMLElement;
  readonly show: HTMLElement | null;
  readonly pop: HTMLElement | null;
}

interface BoxParts {
  readonly section: HTMLElement;
  readonly picked: HTMLElement;
  readonly attach: HTMLInputElement;
  readonly picLine: HTMLElement;
  readonly picDd: HTMLElement;
  readonly disconnected: HTMLElement;
  readonly problem: HTMLElement;
  readonly foot: HTMLElement;
  readonly keys: HTMLElement;
  readonly sp: HTMLElement;
  readonly cancel: HTMLElement;
  readonly copy: HTMLElement;
  readonly save: HTMLButtonElement;
  picKey: string;
}

/** Sets `parent`'s children to `want`, in order, moving or replacing none that stay. */
function sync(parent: Element, want: ReadonlyArray<Node>): void {
  for (const n of [...parent.childNodes]) if (!want.includes(n)) n.remove();
  want.forEach((n, i) => {
    const at = parent.childNodes[i] ?? null;
    if (at !== n) parent.insertBefore(n, at);
  });
}

const setText = (el: Element, text: string) => { if (el.textContent !== text) el.textContent = text; };

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

const withoutScheme = (url: string) => url.replace(/^https?:\/\//, "");

const live = (w: Window | null | undefined): w is Window => {
  try {
    return !!w && !w.closed;
  } catch {
    return false;
  }
};

// overlaySessionSchema's rules, by hand: the overlay bundles no zod.
const SESSION_KEYS = new Set(["type", "v", "task", "url", "pins", "drafts", "showPins", "theme", "dudeWindow"]);
const PIN_KEYS = new Set(["id", "n", "status", "author", "mine", "body", "selector", "when", "replies"]);
const REPLY_KEYS = new Set(["who", "kind", "body"]);
const PIN_STATUSES = new Set<unknown>(["draft", "open", "delivered", "resolved", "replied"]);
const REPLY_KINDS = new Set<unknown>(["reply", "resolve", "reopen"]);

/** A plain object with no key outside `keys`, as zod's `.strict()`. */
function strictObject(value: unknown, keys: ReadonlySet<string>): value is Record<string, unknown> {
  if (value === null || typeof value !== "object") return false;
  const proto = Object.getPrototypeOf(value);
  return (proto === Object.prototype || proto === null) && Object.keys(value).every((key) => keys.has(key));
}

function boundedString(value: unknown, max: number): value is string {
  return typeof value === "string" && value.length <= max;
}

function wholeNumber(value: unknown, min: number): value is number {
  return typeof value === "number" && Number.isInteger(value) && value >= min;
}

function validReply(value: unknown): boolean {
  return strictObject(value, REPLY_KEYS) && boundedString(value.who, OVERLAY_LIMITS.name) && REPLY_KINDS.has(value.kind)
    && boundedString(value.body, OVERLAY_LIMITS.replyBody);
}

function validPin(value: unknown): value is OverlayPin {
  return strictObject(value, PIN_KEYS) && boundedString(value.id, 100) && value.id.length > 0 && wholeNumber(value.n, 1)
    && PIN_STATUSES.has(value.status) && boundedString(value.author, OVERLAY_LIMITS.name) && typeof value.mine === "boolean"
    && boundedString(value.body, OVERLAY_LIMITS.pinBody) && boundedString(value.selector, 2_000) && boundedString(value.when, OVERLAY_LIMITS.name)
    && Array.isArray(value.replies) && value.replies.length <= OVERLAY_LIMITS.replies && value.replies.every(validReply);
}

function validSession(value: unknown): value is Extract<DudeToOverlay, { type: "session" }> {
  if (!strictObject(value, SESSION_KEYS) || value.type !== "session" || value.v !== OVERLAY_PROTOCOL
    || !boundedString(value.task, 100) || !boundedString(value.url, 2_048)
    || !Array.isArray(value.pins) || value.pins.length > OVERLAY_LIMITS.pins
    || !wholeNumber(value.drafts, 0) || typeof value.showPins !== "boolean"
    || (value.theme !== undefined && !OVERLAY_THEMES.includes(value.theme as OverlayTheme))
    || (value.dudeWindow !== undefined && !overlayWindowName(value.dudeWindow))) return false;
  try {
    const url = new URL(value.url);
    if (url.protocol !== "http:" && url.protocol !== "https:") return false;
  } catch {
    return false;
  }
  return value.pins.every(validPin);
}

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
  // The save given up on after SAVE_WAIT_MS: dude may only have been slow, so its answer still counts.
  #late: string | null = null;
  // What that save sent (words and whether it had a picture): only the very same save may reuse its nonce.
  #lateSent: string | null = null;
  #sent: string | null = null;
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
  // A hello went out since the last beat: the beat says none, so there is never more than one a beat.
  #saidHello = false;
  // Beats in a row with nothing from dude while connected.
  #quietBeats = 0;
  // Beats in a row spent waiting with nothing from dude: at WAITING_BEATS the panel offers Connect again.
  #waitBeats = 0;
  // The dude tab Connect to dude opened, or the dude window that last answered with a session; and what the click came to.
  #connectWin: Window | null = null;
  #connect: "opened" | "blocked" | null = null;
  // The tab this page's Connect to dude opened: only its window name is kept for a reload.
  #connectOpened: Window | null = null;
  #stopped = false;
  readonly #firstPage: string | null;
  #at: { right: number; bottom: number } | null = null;
  #raf = 0;
  // dude's resolved theme once a session names it; until then (or from an older dude) the OS's.
  #dudeTheme: OverlayTheme | null = null;
  // dude's tab's window name once a session names it: Go to dude looks it up.
  #dudeWindow: string | null = null;
  #osDark: MediaQueryList | null = null;
  // The shadow tree's layers, made once: a render replaces a layer's children, never the tree.
  readonly #top: HTMLElement;
  readonly #lay: Record<"pins" | "catchers" | "hover" | "box" | "bar", HTMLElement>;
  // The pins drawn now, by id, moved in place as the page moves.
  #pinNodes = new Map<string, PinNodes>();
  // The comment box, built once per pick and updated in place: the textarea is never moved while it is in use.
  #box: BoxParts | null = null;

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
    // A page opened from a preview page was given a copy of that page's sessionStorage: the dude tab name in it is the opener's.
    let opener: Window | null = null;
    try {
      opener = win.opener as Window | null;
    } catch {
      opener = null;
    }
    if (live(opener) && this.#sameOrigin(opener)) this.#session(CONNECT_NAME_KEY, "");
    const layer = () => this.#h("div", { class: "layer" });
    this.#lay = { pins: layer(), catchers: layer(), hover: layer(), box: layer(), bar: layer() };
    this.#top = this.#h("div", { class: "root", "data-overlay": "" }, this.#lay.pins, this.#lay.catchers, this.#lay.hover, this.#lay.box, this.#lay.bar);
    this.#root.append(this.#h("style", {}, TOKENS_CSS, CSS), this.#top);
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
    this.#win.addEventListener("hashchange", this.#onRoute);
    this.#wrapHistory();
    try {
      this.#osDark = this.#win.matchMedia?.("(prefers-color-scheme: dark)") ?? null;
    } catch {
      this.#osDark = null;
    }
    this.#osDark?.addEventListener?.("change", this.#onOsTheme);
    this.#applyTheme();
    if (!this.#dude()) {
      this.#connection = "none";
    } else {
      this.#hello();
      this.#timers.push(this.#win.setTimeout(() => {
        if (this.#connection === "waiting") this.#lost("none");
      }, this.#opts.sessionWaitMs ?? SESSION_WAIT_MS));
    }
    this.#timers.push(this.#win.setInterval(() => {
      if (this.#dude()) return;
      // The tab Connect to dude opened was closed before it answered: back to the panel's first words.
      if (this.#connection === "waiting" && this.#connect === "opened") {
        this.#connect = null;
        this.#lost("none");
      } else if (this.#connection === "connected" || this.#connection === "waiting") this.#lost("closed");
    }, 1_000));
    this.#timers.push(this.#win.setInterval(this.#heartbeat, this.#opts.heartbeatMs ?? HEARTBEAT_MS));
    // Pins follow what the page draws, which may move without a scroll: moved in place, never redrawn.
    // The route is checked too, for an app that kept history's own pushState from before the overlay wrapped it.
    this.#timers.push(this.#win.setInterval(() => { this.#onRoute(); this.#placePins(); }, this.#opts.tickMs ?? 1_500));
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
    this.#win.removeEventListener("hashchange", this.#onRoute);
    this.#osDark?.removeEventListener?.("change", this.#onOsTheme);
    this.host.remove();
  }

  /** The theme drawn now: dude's once it said one, else the OS's; dark when neither is known, as dude's own default. */
  get theme(): OverlayTheme {
    if (this.#dudeTheme) return this.#dudeTheme;
    try {
      if (this.#win.matchMedia?.("(prefers-color-scheme: light)").matches) return "light";
    } catch {
      // No media queries: dude's default.
    }
    return "dark";
  }

  #applyTheme(): void {
    if (this.#top.getAttribute("data-theme") !== this.theme) this.#top.setAttribute("data-theme", this.theme);
  }

  readonly #onOsTheme = (): void => {
    if (!this.#stopped) this.#applyTheme();
  };

  // ------------------------------------------------------------------ dude

  /**
   * The dude window to talk to: the tab Connect to dude opened or the one
   * that last answered, while it is open; else the
   * first opener up the chain that is not one of this preview's own pages
   * (a link with target=_blank opened from a connected page has that page as
   * its opener). Not same-origin does not prove it is dude: messages are
   * posted with dude's origin as the target, and heard only from dude's
   * origin and this very window, so anything else hears nothing and says
   * nothing that is heard.
   */
  #dude(): Window | null {
    if (live(this.#connectWin)) return this.#connectWin;
    return this.#chain();
  }

  // The first opener up the chain that is not one of this preview's own pages.
  #chain(): Window | null {
    let at: Window = this.#win;
    for (let step = 0; step < OVERLAY_OPENER_STEPS; step++) {
      let next: Window | null;
      try {
        next = at.opener as Window | null;
      } catch {
        return null;
      }
      if (!live(next)) return null;
      if (!this.#sameOrigin(next)) return next;
      at = next;
    }
    return null;
  }

  #sameOrigin(w: Window): boolean {
    try {
      return w.location.origin === this.#win.location.origin;
    } catch {
      // Another origin's window throws on location.origin: not a page of this preview.
      return false;
    }
  }

  #post(msg: OverlayToDude): boolean {
    const o = this.#dude();
    if (!o) return false;
    o.postMessage(msg, this.#opts.dudeOrigin);
    return true;
  }

  // `between`: a hello sent between beats (at start, after a save went unanswered), which spares the next beat's own.
  // A beat's own hello passes false, so it never spares the beat after it.
  #hello(between = true): void {
    if (this.#post({ type: "hello", v: OVERLAY_PROTOCOL, url: this.#win.location.href }) && between) this.#saidHello = true;
  }

  /**
   * One beat. Not connected: hello again. Connected: after two quiet beats,
   * hello (dude answers every hello with a session, so a live tab just
   * answers it, and the toolbar never flickers), and hello again after a
   * third, for a dude slow to answer; a fourth quiet beat means the tab is
   * gone or reloading, and the connection waits, still saying hello, for
   * whichever tab answers next. So dude has about two beats (4 s) to answer
   * before the toolbar says so. The box and its words stay throughout.
   */
  readonly #heartbeat = (): void => {
    if (this.#stopped || !this.#dude()) return;
    const said = this.#saidHello;
    this.#saidHello = false;
    if (this.#connection === "waiting" && ++this.#waitBeats === WAITING_BEATS) {
      // The pinned dude window went unanswered this long: the window up the openers, if there is one, is talked to instead.
      // The tab this page's Connect opened stays pinned: it is proven to be dude, and the chain's window is not.
      // With no chain the pin stays too, so it is still said hello to and not taken for closed.
      if (this.#connectWin !== this.#connectOpened && this.#chain()) this.#connectWin = null;
      this.#render();
    }
    if (this.#connection === "connected") {
      this.#quietBeats++;
      if (this.#quietBeats < 2) return;
      if (this.#quietBeats >= 4) {
        this.#connection = "waiting";
        this.#waitBeats = 0;
        this.#picking = false;
        this.#hover = null;
        this.#render();
      }
    }
    if (!said) this.#hello(false);
  };

  #lost(to: "none" | "closed"): void {
    this.#connection = to;
    this.#picking = false;
    this.#hover = null;
    this.#saveDone();
    this.#render();
  }

  readonly #onMessage = (e: MessageEvent): void => {
    if (e.origin !== this.#opts.dudeOrigin || e.source !== this.#dude() || e.source === null) return;
    const m = e.data as DudeToOverlay;
    if (!m || typeof m !== "object" || m.v !== OVERLAY_PROTOCOL) return;
    this.#quietBeats = 0;
    this.#waitBeats = 0;
    switch (m.type) {
      case "session":
        if (!validSession(m) || overlayPage(m.url) !== this.#page) return;
        // The window that answered is dude's: kept, so a preview page between them closing doesn't lose it.
        this.#connectWin = e.source as Window;
        this.#connection = "connected";
        this.#connect = null;
        this.#task = String(m.task).slice(0, 40);
        this.#pins = Array.isArray(m.pins) ? m.pins.slice(0, OVERLAY_LIMITS.pins) : [];
        this.#drafts = Number(m.drafts) || 0;
        this.#showPins = m.showPins !== false;
        this.#dudeTheme = m.theme ?? null;
        this.#dudeWindow = m.dudeWindow ?? null;
        // Kept for this tab's next document: a reload's Connect to dude goes back to the tab Connect opened.
        // A dude tab reached otherwise didn't come from this page, so its name is not one to send preview-connect to.
        this.#session(CONNECT_NAME_KEY, e.source === this.#connectOpened ? m.dudeWindow ?? "" : "");
        this.#applyTheme();
        if (this.#unanswered) {
          // dude's tab is back: the box keeps its words, and Save is for it again.
          this.#unanswered = false;
          this.#problem = null;
        }
        break;
      case "saved":
        if (m.nonce !== this.#saving && (this.#late === null || m.nonce !== this.#late)) return;
        if (m.nonce === this.#late && this.#lateSent !== this.#current()) {
          // The words or picture changed after the give-up: that save landed, this box is a new one.
          this.#saveDone();
          this.#drafts = Number(m.drafts) || this.#drafts;
          this.#problem = null;
          break;
        }
        this.#saveDone();
        this.#drafts = Number(m.drafts) || this.#drafts;
        this.#toast = { drafts: this.#drafts, picture: m.picture, ...(this.#picture.state === "failed" ? { why: this.#picture.why } : {}) };
        this.#closeBox(false);
        break;
      case "refused":
        if (m.nonce !== undefined && m.nonce !== this.#saving && m.nonce !== this.#late) return;
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
      // Esc while composing cancels the composition: it is the IME's, and the words stay.
      if (e.isComposing || e.keyCode === 229) return;
      // Picking or writing, Esc is the overlay's alone; otherwise it closes a toast, popover or panel and the page still hears it.
      const owned = this.#picking || this.#picked !== null;
      if (this.#back() && owned) {
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
    // Only the box's own state follows the words; the textarea is left alone, so an IME composition runs undisturbed.
    this.#field.addEventListener("input", (e) => { if (!(e as InputEvent).isComposing) this.#syncBox(); });
    this.#field.addEventListener("compositionend", () => this.#syncBox());
    this.#box = null;
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
    const draw = this.#opts.draw ?? ((target: Element, win: Window) => {
      const style = win.getComputedStyle(this.#top);
      return drawPicture(target, win, { outline: style.getPropertyValue("--ds-color-accent").trim(), background: style.getPropertyValue("--ds-color-surface").trim() });
    });
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
    // After a give-up, Save again of the very same words and picture is the same save: dude answers its nonce once.
    const same = this.#late !== null && this.#lateSent === this.#current();
    if (!same) {
      this.#late = null;
      this.#lateSent = null;
    }
    const nonce = same ? this.#late! : `d${Date.now().toString(36)}${Math.random().toString(36).slice(2, 8)}`;
    const msg: OverlayToDude = { type: "draft", v: OVERLAY_PROTOCOL, nonce, body: body.slice(0, OVERLAY_LIMITS.body), anchor: this.#picked.anchor };
    if (this.#attach && this.#picture.state === "ready") msg.picture = this.#picture.url;
    if (!this.#post(msg)) {
      this.#lost("closed");
      return;
    }
    this.#saving = nonce;
    this.#sent = this.#current();
    this.#problem = null;
    this.#saveTimer = this.#win.setTimeout(() => this.#saveUnanswered(nonce), this.#opts.saveWaitMs ?? SAVE_WAIT_MS);
    this.#render();
  }

  /** The box's words and whether a picture goes with them: what a save sends. */
  #current(): string {
    const body = (this.#field?.value ?? "").trim().slice(0, OVERLAY_LIMITS.body);
    return `${this.#attach && this.#picture.state === "ready" ? "pic" : "nopic"}|${this.#picked?.anchor.selector ?? ""}|${body}`;
  }

  #saveDone(): void {
    this.#saving = null;
    this.#late = null;
    this.#lateSent = null;
    this.#sent = null;
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
    this.#late = nonce;
    this.#lateSent = this.#sent;
    this.#unanswered = true;
    this.#problem = SAVE_UNANSWERED;
    this.#hello();
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
      this.#place();
    }) ?? 0;
    if (!this.#raf) this.#place();
  };

  /** After a scroll or resize: what follows the page's boxes moves; nothing else is touched. */
  #place(): void {
    if (this.#stopped) return;
    this.#placePins();
    if (this.#picking && !this.#picked) {
      sync(this.#lay.catchers, this.#frameCatchers());
      sync(this.#lay.hover, this.#hover ? this.#hoverLayer(this.#hover) : []);
    }
    if (this.#picked) this.#syncBox();
  }

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
    const narrow = this.#win.innerWidth <= 520;
    this.#top.className = `root${narrow ? " narrow" : ""}`;
    this.#syncPins();
    const picking = this.#picking && !this.#picked;
    sync(this.#lay.catchers, picking ? this.#frameCatchers() : []);
    sync(this.#lay.hover, picking && this.#hover ? this.#hoverLayer(this.#hover) : []);
    this.#syncBox();
    sync(this.#lay.bar, [this.#collapsed && !this.#picking && !this.#picked ? this.#pill() : this.#bar(narrow)]);
    if (active && !active.isConnected && activeKey) this.#focusKey(activeKey);
  }

  #rect(el: Element): DOMRect {
    return el.getBoundingClientRect();
  }

  #hoverLayer(el: Element): HTMLElement[] {
    const r = this.#rect(el);
    const frame = foreignFrame(el, this.#win);
    const box = this.#h("div", { class: `hl${frame ? " warn" : ""}`, "data-testid": "ov-highlight",
      style: `left:${r.left - 2}px;top:${r.top - 2}px;width:${r.width + 4}px;height:${r.height + 4}px` });
    const top = r.top > 30 ? r.top - 28 : r.bottom + 6;
    const label = frame
      ? this.#h("div", { class: "label warn", "data-testid": "ov-label", style: `left:${Math.max(4, r.left - 2)}px;top:${top}px` }, this.#g("frame", 12),
        this.#h("span", {}, this.#h("b", {}, "Can't pick inside this frame"), ` — it comes from ${withoutScheme(frame)}, which dude can't read. Click to comment on the frame as a whole.`))
      : this.#h("div", { class: "label", "data-testid": "ov-label", style: `left:${Math.max(4, r.left - 2)}px;top:${top}px` },
        this.#h("span", { class: "mono" }, labelFor(selectorFor(el), componentOf(el))),
        this.#h("span", { class: "dim" }, `${Math.round(r.width)}×${Math.round(r.height)}`));
    return [box, label];
  }

  /**
   * While picking, a catcher over each frame from another site: the page
   * hears no pointer inside such a frame, so the catcher is where hovering
   * says it can't be read and a click picks the frame as a whole.
   */
  #frameCatchers(): HTMLElement[] {
    const out: HTMLElement[] = [];
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

  #pinsShown(): boolean {
    return this.#showPins && this.#connection === "connected" && !this.#picking && !this.#picked;
  }

  /** The pins layer to match the pins, the popover and what resolves now: a pin whose data is unchanged keeps its nodes. */
  #syncPins(): void {
    const keep = new Map<string, PinNodes>();
    const want: Node[] = [];
    if (this.#pinsShown()) {
      const placed: Array<[PinNodes, DOMRect]> = [];
      for (const p of this.#pins) {
        const el = resolveOne(this.#doc, p.selector);
        if (!el) continue;
        const active = this.#popover?.id === p.id;
        const key = `${JSON.stringify(p)}|${active ? `+${this.#popover?.from ?? ""}` : "-"}`;
        const old = this.#pinNodes.get(p.id);
        const n = old && old.key === key ? old : this.#pinNode(p, active, key);
        placed.push([n, this.#rect(el)]);
        keep.set(p.id, n);
        if (n.show) want.push(n.show);
        want.push(n.pin);
        if (n.pop) want.push(n.pop);
      }
      for (const [n, r] of placed) this.#placePin(n, r);
    }
    this.#pinNodes = keep;
    sync(this.#lay.pins, want);
  }

  /** The tick: pins moved to where their elements are now; nodes are made or dropped only when one appears or goes. */
  #placePins(): void {
    if (this.#stopped || !this.#pinsShown()) return;
    let changed = false;
    // Every read before any write, so the page is laid out once, not once per pin.
    const moves: Array<[PinNodes, DOMRect]> = [];
    for (const p of this.#pins) {
      const el = resolveOne(this.#doc, p.selector);
      const n = this.#pinNodes.get(p.id);
      if (!el !== !n) changed = true;
      else if (el && n) moves.push([n, this.#rect(el)]);
    }
    for (const [n, r] of moves) this.#placePin(n, r);
    if (changed) this.#syncPins();
  }

  #placePin(n: PinNodes, r: DOMRect): void {
    const at = (node: HTMLElement, style: string) => { if (node.getAttribute("style") !== style) node.setAttribute("style", style); };
    at(n.pin, `left:${Math.max(4, r.right - 18)}px;top:${Math.max(4, r.top - 14)}px`);
    if (n.show) at(n.show, `left:${r.left - 3}px;top:${r.top - 3}px;width:${r.width + 6}px;height:${r.height + 6}px`);
    if (n.pop) {
      const pos = this.#beside(r, 340, 300);
      at(n.pop, `left:${pos.left}px;top:${pos.top}px`);
    }
  }

  #pinNode(p: OverlayPin, active: boolean, key: string): PinNodes {
    const st = STATUS[p.status] ?? STATUS.open;
    const pin = this.#h("button", { type: "button", class: `pin ${p.status}${active ? " active" : ""}`, "data-testid": "ov-pin", "data-id": p.id, "data-k": `pin-${p.id}`,
      "data-status": p.status, "aria-expanded": active ? "true" : "false",
      "aria-label": `${p.n}: ${p.mine ? "your draft" : `${p.author}'s comment`}, ${st.label}`,
      onclick: () => { this.#popover = active ? null : { id: p.id }; this.#render(); } }, String(p.n), this.#g(st.glyph, 11));
    const show = active && this.#popover?.from ? this.#h("div", { class: "hl show", "data-testid": "ov-show" }) : null;
    return { key, pin, show, pop: active ? this.#popoverFor(p) : null };
  }

  #popoverFor(p: OverlayPin): HTMLElement {
    const st = STATUS[p.status] ?? STATUS.open;
    return this.#h("section", { class: "pop", role: "dialog", "aria-label": `Comment ${p.n}`, "data-testid": "ov-popover" },
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
    this.#goToDude();
  }

  /**
   * Brings dude's tab forward; called inside a click. Chrome ignores a
   * popup's `opener.focus()`, but `window.open("", name)` with the click's
   * activation brings forward the window holding that name without
   * navigating it. When that is not the opener, or there is no name, the
   * opener's focus(), which some browsers honour.
   */
  #goToDude(): void {
    const opener = this.#dude();
    if (!opener) return;
    if (this.#dudeWindow) {
      try {
        const found = this.#win.open("", this.#dudeWindow);
        if (found === opener) return;
        // No window held the name, so a blank one was opened: this page's own origin, readable, and closed again.
        // Another window holding the name is another origin's and throws here: it is left alone.
        if (found && found.location.href === "about:blank") found.close();
      } catch {
        // Refused: the opener's focus() below.
      }
    }
    try { opener.focus(); } catch { /* another origin's window may refuse */ }
  }

  /**
   * Connect to dude, inside the click: opens dude's preview-connect page,
   * deliberately with an opener (no noopener), so that tab can hear this
   * page's hello, find the task whose preview this is, and adopt this
   * window. In a new tab, or, after this tab reloaded, in the dude tab an
   * earlier Connect opened (its opener is still this tab), found by the
   * window name its session gave. The heartbeat then says hello to it
   * until it answers.
   */
  #connectToDude(): void {
    const stored = this.#session(CONNECT_NAME_KEY);
    // Used once: a session from that tab writes it back, and a retry with none between gets a fresh tab.
    this.#session(CONNECT_NAME_KEY, "");
    let w: Window | null = null;
    try {
      w = this.#win.open(`${this.#opts.dudeOrigin}/#/preview-connect`, overlayWindowName(stored) ? stored : "_blank");
    } catch {
      w = null;
    }
    if (!w) {
      this.#connect = "blocked";
    } else {
      this.#connectWin = w;
      this.#connectOpened = w;
      this.#connect = "opened";
      this.#connection = "waiting";
      this.#waitBeats = 0;
    }
    this.#render();
  }

  #beside(r: DOMRect, w: number, h: number): { left: number; top: number } {
    const W = this.#win.innerWidth, H = this.#win.innerHeight;
    const top = Math.max(8, Math.min(r.top - 40, H - h - 8));
    if (r.right + 14 + w <= W - 8) return { left: r.right + 14, top };
    if (r.left - 14 - w >= 8) return { left: r.left - 14 - w, top };
    return { left: Math.max(8, Math.min(r.left, W - w - 8)), top: Math.max(8, Math.min(r.bottom + 14, H - h - 8)) };
  }

  /** The comment box, built once per pick. */
  #buildBox(): BoxParts {
    const { el, anchor, frame } = this.#picked!;
    const name = frame ? `the frame from ${withoutScheme(frame)}` : labelFor(anchor.selector ?? "", anchor.component);
    const picDd = this.#h("dd", {});
    const facts = this.#h("dl", {},
      this.#h("dt", {}, "Page"), this.#h("dd", { class: "mono" }, withoutScheme(anchor.url)),
      this.#h("dt", {}, "Window"), this.#h("dd", {}, `${anchor.viewport?.width}×${anchor.viewport?.height}`),
      this.#h("dt", {}, "Element"), this.#h("dd", { class: "mono" }, anchor.selector ?? ""),
      this.#h("dt", {}, "Box"), this.#h("dd", {}, anchor.rect ? `${anchor.rect.width}×${anchor.rect.height} at ${anchor.rect.x}, ${anchor.rect.y}` : ""),
      anchor.text ? this.#h("dt", {}, "Text") : null, anchor.text ? this.#h("dd", {}, `“${anchor.text.slice(0, 200)}${anchor.text.length > 200 ? "…" : ""}”`) : null,
      this.#h("dt", {}, "Component"), this.#h("dd", {}, anchor.component?.name
        ? `${anchor.component.name}${anchor.component.file ? ` ${anchor.component.file}${anchor.component.line ? `:${anchor.component.line}` : ""}` : ""}` : this.#h("span", { class: "dim" }, "The app doesn't say")),
      this.#h("dt", {}, "HTML"), this.#h("dd", { class: "dim" }, `the element's outer HTML, ${anchor.html?.length ?? 0} characters`),
      this.#h("dt", {}, "Picture"), picDd);
    const attach = this.#h("input", { type: "checkbox", "data-testid": "ov-attach", "data-k": "attach",
      onchange: (e: Event) => {
        this.#attach = (e.target as HTMLInputElement).checked;
        if (this.#attach && this.#picture.state === "off") this.#drawFor(el);
        this.#syncBox();
      } }) as HTMLInputElement;
    const picLine = this.#h("p", { class: "note" });
    const disconnected = this.#h("p", { class: "note warn", role: "alert", "data-testid": "ov-disconnected" }, this.#g("warn", 12), this.#h("span", {}));
    const problem = this.#h("p", { class: "note warn", role: "alert", "data-testid": "ov-problem" });
    const keys = this.#h("span", { class: "dim" }, this.#h("kbd", {}, "⌘"), this.#h("kbd", {}, "↵"), " save · ", this.#h("kbd", {}, "Esc"), " cancel");
    const sp = this.#h("span", { class: "sp" });
    const cancel = this.#h("button", { type: "button", "data-k": "cancel", onclick: () => this.#closeBox(false) }, "Cancel");
    const copy = this.#h("button", { type: "button", "data-k": "copy", "data-testid": "ov-copy", onclick: () => void this.#copy() }, "Copy text");
    const save = this.#h("button", { type: "button", class: "primary", "data-k": "save", "data-testid": "ov-save", onclick: () => this.#save() }) as HTMLButtonElement;
    const foot = this.#h("footer", { class: "foot" });
    const section = this.#h("section", { class: "box", role: "dialog", "aria-label": `Comment on ${name}`, "data-testid": "ov-comment-box" },
      this.#h("header", { class: "bh" }, this.#h("span", {}, "Comment on ", this.#h("b", { class: "mono" }, name)),
        this.#h("span", { class: "dim" }, this.#g("lock", 11), " Private draft until you submit in dude")),
      this.#field!,
      this.#h("label", {}, attach, this.#h("span", {}, "Attach a picture of this element ", this.#h("span", { class: "dim" }, "(drawn from the page)"))),
      this.#h("details", { "data-testid": "ov-what-dude-sends" }, this.#h("summary", { "data-k": "sends" }, "What dude will send"), facts,
        this.#h("p", { class: "dim" }, "Nothing leaves this page until you save. Then it is a draft only you see in dude.")),
      foot);
    const picked = this.#h("div", { class: "hl picked", "data-testid": "ov-picked" });
    return { section, picked, attach, picLine, picDd, disconnected, problem, foot, keys, sp, cancel, copy, save, picKey: "" };
  }

  /** The box to match the state: positions, words and buttons set in place; the textarea and the open disclosure are left as they are. */
  #syncBox(): void {
    if (!this.#picked || !this.#field) {
      this.#box = null;
      sync(this.#lay.box, []);
      return;
    }
    const b = (this.#box ??= this.#buildBox());
    const narrow = this.#win.innerWidth <= 520;
    const connected = this.#connection === "connected";
    const r = this.#rect(this.#picked.el);
    const pos = this.#beside(r, 380, 480);
    b.picked.setAttribute("style", `left:${r.left - 2}px;top:${r.top - 2}px;width:${r.width + 4}px;height:${r.height + 4}px`);
    b.section.setAttribute("style", `left:${pos.left}px;top:${pos.top}px`);
    b.section.className = `box${narrow ? " sheet" : ""}`;
    const pic = this.#picture;
    b.attach.checked = this.#attach;
    b.attach.disabled = pic.state === "off" || pic.state === "failed";
    const picKey = `${pic.state}|${pic.state === "ready" ? pic.url.length : ""}|${this.#attach}`;
    if (picKey !== b.picKey) {
      b.picKey = picKey;
      b.picDd.replaceChildren(this.#attach && pic.state === "ready" ? this.#h("img", { class: "thumb", src: pic.url, alt: "The picture that would be attached" })
        : this.#h("span", { class: "dim" }, this.#attach && pic.state === "drawing" ? "drawing…" : "none"));
      if (pic.state === "failed") {
        b.picLine.className = "note warn";
        b.picLine.setAttribute("data-testid", "ov-picture-failed");
        b.picLine.setAttribute("role", "status");
        b.picLine.replaceChildren(this.#g("warn", 12), ` Couldn't draw a picture: ${pic.why}. It saves without one.`);
      } else {
        b.picLine.className = "note";
        b.picLine.removeAttribute("data-testid");
        b.picLine.removeAttribute("role");
        b.picLine.textContent = pic.state === "off" && pic.why ? `No picture: ${pic.why}.` : "";
      }
    }
    const lineShown = pic.state === "failed" || pic.state === "off" && !!pic.why;
    const after = b.section.querySelector("label");
    if (lineShown && b.picLine.parentNode !== b.section) after?.after(b.picLine);
    else if (!lineShown) b.picLine.remove();
    setText(b.disconnected.lastElementChild!, this.#connection === "closed" ? " dude's tab was closed: this can't be saved from here. Copy your words, then open the task again."
      : this.#connection === "waiting" ? this.#unheard() ? " dude isn't answering: its tab may have reloaded, been closed, or left this task. Connect to dude to reach it again; your words stay here."
        : " Reaching dude again: its tab may be reloading. Save comes back when it answers."
      : " This preview wasn't opened from dude: this can't be saved from here. Copy your words.");
    this.#toggle(b.section, b.disconnected, !connected, b.foot);
    setText(b.problem, this.#problem ?? "");
    this.#toggle(b.section, b.problem, this.#problem !== null, b.foot);
    setText(b.save, this.#saving ? "Saving…" : "Save draft");
    b.save.disabled = !(connected && !this.#saving && this.#field.value.trim() !== "");
    sync(b.foot, [narrow || !connected ? null : b.keys, b.sp, connected ? b.cancel : null, !connected || this.#unanswered ? b.copy : null, connected ? b.save : null]
      .filter((n): n is HTMLElement => n !== null));
    sync(this.#lay.box, [b.picked, b.section]);
  }

  /** `node` in `parent` just before `before` when `on`, out of it otherwise; left where it is when already right. */
  #toggle(parent: Element, node: Element, on: boolean, before: Element): void {
    if (!on) node.remove();
    else if (node.parentNode !== parent) parent.insertBefore(node, before);
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
    const path = (() => { try { const u = new URL(this.#win.location.href); return u.pathname + (/^#!?\//.test(u.hash) ? u.hash : ""); } catch { return ""; } })();
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

  // Waiting for WAITING_BEATS with no answer from dude.
  #unheard(): boolean {
    return this.#connection === "waiting" && this.#waitBeats >= WAITING_BEATS;
  }

  #panelFor(): HTMLElement | null {
    // After Connect to dude, until a session, and once waiting has gone unanswered: the panel, so dude can be tried again.
    if (this.#connection === "none" || this.#connection === "closed" || this.#connection === "waiting" && (this.#connect !== null || this.#unheard())) return this.#noOpener();
    if (this.#toast && !this.#picking) return this.#toastFor(this.#toast);
    if (this.#panel === "pins") return this.#pinsPanel();
    return null;
  }

  #noOpener(): HTMLElement {
    const closed = this.#connection === "closed";
    const unheard = this.#unheard() && this.#connect === null;
    const note = this.#connect === "blocked" ? "The browser blocked dude's tab from opening: allow pop-ups for this site, or press Connect to dude again."
      : this.#connect === "opened" && this.#unheard() ? "dude didn't find this preview's task, or didn't answer: its tab says which. To try again, press Connect to dude again."
      : this.#connect === "opened" && this.#connection === "waiting" ? "dude opened in a new tab and is looking for this preview's task. This connects once it finds it."
      : null;
    return this.#h("div", { class: "panel", "data-testid": "ov-no-opener" },
      this.#h("div", { class: "ph" }, this.#h("b", {}, closed ? "dude's tab was closed" : unheard ? "dude isn't answering" : "Connect to dude to comment")),
      this.#h("p", { class: "pp" }, closed
        ? "Your saved drafts are kept in dude. Connect to dude to pick more: dude opens and finds this preview's task."
        : unheard ? "dude's tab may have reloaded, been closed, or left this task. Connect to dude to reach it again: dude opens and finds this preview's task."
        : "Connect to dude to comment: dude opens and finds this preview's task. Comments are made through your dude tab, so dude knows who you are and which task they belong to."),
      note ? this.#h("p", { class: "pp", role: "status", "data-testid": "ov-connect-note", "data-connect": this.#connect ?? "" }, note) : null,
      this.#h("div", { class: "pf" },
        this.#h("button", { type: "button", class: "primary", "data-k": "connect-dude", "data-testid": "ov-connect", onclick: () => this.#connectToDude() }, this.#g("link", 12), "Connect to dude"),
        this.#h("button", { type: "button", "data-k": "hide", onclick: () => { this.#collapsed = true; this.#session("hidden", "1"); this.#render(); this.#focusKey("pill"); } }, "Hide for this tab")));
  }

  #toastFor(t: Toast): HTMLElement {
    const pic = t.picture === "attached" ? "with its picture" : t.picture === "failed" ? "without its picture: dude couldn't keep it" : t.why ? `without a picture: ${t.why}` : "with no picture";
    return this.#h("div", { class: "toast", role: "status", "data-testid": "ov-saved" },
      this.#h("span", { class: "okc" }, this.#g("check")),
      this.#h("span", { class: "tt" }, this.#h("b", {}, "Saved to your drafts in dude"),
        this.#h("span", { class: "dim" }, `${t.drafts} ${t.drafts === 1 ? "draft" : "drafts"} on ${this.#task} · ${pic} · only you see them until you submit there`)),
      this.#h("button", { type: "button", "data-k": "go-dude", "data-testid": "ov-go-dude", onclick: () => this.#goToDude() }, this.#g("link", 12), "Go to dude"));
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
