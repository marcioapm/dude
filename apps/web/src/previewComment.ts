/**
 * Commenting on a branch preview from its task: dude opens the preview in
 * a window it keeps a handle on (named per task, never noopener), and the
 * overlay the page loads (apps/web/overlay) posts to this tab. This tab
 * does what needs the person: uploads the picture, creates the draft,
 * lists the page's pins.
 *
 * A task has a few such windows: the one it opened, and those it adopted
 * (a tab opened from one of them, a window it opened before a reload, the
 * preview whose Connect to dude opened this tab). A message is taken only
 * from one of them, from the origin of one of this task's preview servers,
 * within OVERLAY_LIMITS.message, and valid by overlayMessageSchema (whose
 * anchor is the API's own preview anchor). Nothing the page says is used
 * but the anchor's fields and the person's words: no id, no attachment, no
 * task comes from it.
 */

import {
  FEEDBACK_LIMITS, OVERLAY_LIMITS, OVERLAY_PROTOCOL, overSize, overlayMessageSchema, overlayPage, overlayWindowName,
  type DudeToOverlay, type FeedbackItem, type OverlayAnchor, type OverlayPin, type OverlayTheme, type OverlayToDude, type TaskServers,
} from "@dude/domain";

/** Why commenting is not offered, in the words a person reads. */
export const COMMENTING_UNAVAILABLE: Readonly<Record<"public_url" | "lux", string>> = {
  public_url: "Commenting on previews needs auth.public_url",
  lux: "Commenting on previews needs lux v0.1.32 or later",
};

/** The accessible name of a server's Preview while its links open the commenting window. */
export const commentingLinkLabel = (server: string): string => `Preview ${server} (opens the preview window with the comment tool)`;

/** How long a window dude opened has to say hello before the notice says the preview is not answering. */
export const HELLO_WAIT_MS = 10_000;

/**
 * This tab's window name, for the overlay's Go to dude to look up: the
 * existing `dude-tab-…` name (window.name outlives a reload), else a new
 * one; null when the browser refuses it. A duplicated tab copies the name,
 * but the lookup searches only the preview's browsing context group.
 */
export function nameTab(win: Pick<Window, "name">): string | null {
  try {
    if (win.name.startsWith("dude-tab-") && overlayWindowName(win.name)) return win.name;
    const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789";
    // 36 does not divide 256 evenly; the slight bias is harmless for a name that only needs to differ per tab.
    const name = `dude-tab-${Array.from(crypto.getRandomValues(new Uint8Array(10)), (b) => alphabet[b % 36]).join("")}`;
    win.name = name;
    return win.name === name ? name : null;
  } catch {
    return null;
  }
}

/** A preview server comments can be made on now: ready, at an https URL, with the overlay. */
export interface CommentableServer {
  readonly name: string;
  readonly url: string;
  readonly needsRestart: boolean;
}

export type CommentingOffer =
  | { readonly kind: "none" }
  | { readonly kind: "unavailable"; readonly reason: "public_url" | "lux" }
  | { readonly kind: "available"; readonly servers: ReadonlyArray<CommentableServer>; readonly restart: ReadonlyArray<string> };

/** An https URL's origin, or null: the only origins a preview is served at. */
export const httpsOriginOf = (url: string | null | undefined): string | null => {
  if (!url) return null;
  try {
    const u = new URL(url);
    return u.protocol === "https:" ? u.origin : null;
  } catch {
    return null;
  }
};

const originOf = httpsOriginOf;

/**
 * What the task's servers offer for commenting: nothing (no live preview,
 * or not one whose servers carry `commenting`), why not, or its ready
 * servers to open and those needing a restart to carry the overlay.
 */
export function commentingOffer(data: TaskServers | null): CommentingOffer {
  if (!data?.run || data.run.kind !== "preview") return { kind: "none" };
  const marked = data.servers.filter((s) => s.commenting !== undefined);
  if (marked.length === 0) return { kind: "none" };
  const off = marked.find((s) => s.commenting && !s.commenting.available)?.commenting;
  if (off && !off.available) return { kind: "unavailable", reason: off.reason };
  return {
    kind: "available",
    servers: marked.filter((s) => s.state === "ready" && originOf(s.url) !== null)
      .map((s) => ({ name: s.name, url: s.url!, needsRestart: s.needsRestart === true })),
    restart: marked.filter((s) => s.needsRestart === true).map((s) => s.name),
  };
}

/** The origins a message may come from: this task's preview servers' that carry the overlay. */
export function previewOrigins(data: TaskServers | null): ReadonlySet<string> {
  if (!data?.run || data.run.kind !== "preview") return new Set();
  return new Set(data.servers.filter((s) => s.commenting?.available).flatMap((s) => originOf(s.url) ?? []));
}

/** Whether Show on preview can work for a page: it is on one of the servers comments can be made on now. */
export function showable(offer: CommentingOffer, url: string): boolean {
  return serverOf(offer, url) !== null;
}

/** The server comments can be made on now whose origin `url` is at, or null. */
export function serverOf(offer: CommentingOffer, url: string): CommentableServer | null {
  if (offer.kind !== "available") return null;
  const origin = originOf(url);
  return origin === null ? null : offer.servers.find((s) => originOf(s.url) === origin) ?? null;
}

export type Checked =
  | { readonly ok: true; readonly msg: OverlayToDude }
  /** Not from one of dude's commenting windows at one of the task's origins: not answered at all. */
  | { readonly ok: false; readonly why: "origin" | "source" }
  /** From the right window, refused with words for its box. */
  | { readonly ok: false; readonly why: "size" | "invalid" | "moved"; readonly reason: string };

export const PREVIEW_MOVED = "This preview isn't the one at work any more — open it again from dude.";

/**
 * The guards, in order: origin, source window, size, then the schema and
 * the anchor's own origin. `mine` is whether the source is one of dude's
 * commenting windows, and `openedAt` the origin that window was opened or
 * adopted at: such a window, still there but no longer at one of `origins`
 * (the task's read moved to an agent Run, or the task changed), is told so
 * instead of being left unanswered.
 */
export function checkMessage(e: Pick<MessageEvent, "origin" | "source" | "data">, mine: boolean, origins: ReadonlySet<string>, openedAt: string | null = null): Checked {
  if (!origins.has(e.origin)) {
    const ours = mine && e.source !== null && openedAt !== null && e.origin === openedAt;
    return ours ? { ok: false, why: "moved", reason: PREVIEW_MOVED } : { ok: false, why: "origin" };
  }
  if (!mine || e.source === null) return { ok: false, why: "source" };
  if (overSize(e.data, OVERLAY_LIMITS.message)) return { ok: false, why: "size", reason: "That is too much to send: try without the picture, or a smaller element." };
  const parsed = overlayMessageSchema.safeParse(e.data);
  if (!parsed.success) return { ok: false, why: "invalid", reason: "dude couldn't read what the page sent." };
  const msg = parsed.data;
  const url = msg.type === "draft" ? msg.anchor.url : msg.type === "hello" || msg.type === "navigated" ? msg.url : null;
  // An item is about the page that sent it, never another site's.
  if (url !== null && originOf(url) !== e.origin) return { ok: false, why: "invalid", reason: "The page named another site." };
  return { ok: true, msg };
}

/** A picture's bytes and type, when the data URL holds a PNG or JPEG: its first bytes must be what its label says. */
export function pictureBytes(dataUrl: string): { bytes: Uint8Array; type: "image/png" | "image/jpeg" } | null {
  const m = /^data:image\/(png|jpeg);base64,(.+)$/.exec(dataUrl);
  if (!m) return null;
  let bin: string;
  try {
    bin = atob(m[2]!);
  } catch {
    return null;
  }
  const bytes = Uint8Array.from(bin, (c) => c.charCodeAt(0));
  const png = bytes[0] === 0x89 && bytes[1] === 0x50 && bytes[2] === 0x4e && bytes[3] === 0x47;
  const jpeg = bytes[0] === 0xff && bytes[1] === 0xd8 && bytes[2] === 0xff;
  if (m[1] === "png" && png) return { bytes, type: "image/png" };
  if (m[1] === "jpeg" && jpeg) return { bytes, type: "image/jpeg" };
  return null;
}

const clip = (s: string, n: number) => (s.length > n ? `${s.slice(0, n - 1)}…` : s);

function ago(at: string, now: number): string {
  const min = Math.max(0, Math.round((now - Date.parse(at)) / 60_000));
  if (min < 1) return "just now";
  if (min < 60) return `${min} min ago`;
  const h = Math.round(min / 60);
  return h < 48 ? `${h} h ago` : `${Math.round(h / 24)} days ago`;
}

/**
 * The page's pins: the attempt's preview items on that page (origin and
 * path), submitted ones and the reader's own drafts — the list holds no
 * one else's — numbered in the list's order, never more than a pin shows.
 */
export function pinsFor(items: ReadonlyArray<FeedbackItem>, url: string, you: string | null, name: (personId: string | null | undefined) => string, now = Date.now()): OverlayPin[] {
  const page = overlayPage(url);
  return items.filter((i) => i.anchor.kind === "preview" && overlayPage(i.anchor.url) === page && i.anchor.selector)
    .slice(0, OVERLAY_LIMITS.pins)
    .map((i, k) => ({
      id: i.id,
      n: k + 1,
      status: i.status,
      author: clip(i.author?.name.split(" ")[0] ?? "Someone", OVERLAY_LIMITS.name),
      mine: i.author?.id === you,
      body: clip(i.body, OVERLAY_LIMITS.pinBody),
      selector: i.anchor.kind === "preview" ? i.anchor.selector ?? "" : "",
      when: clip(`${i.status === "draft" ? "drafted" : "submitted"} ${ago(i.submittedAt ?? i.createdAt, now)}`, OVERLAY_LIMITS.name),
      replies: i.history.slice(-OVERLAY_LIMITS.replies).map((h) => ({
        who: clip(h.personId ? name(h.personId) : "The agent", OVERLAY_LIMITS.name), kind: h.kind, body: clip(h.body, OVERLAY_LIMITS.replyBody),
      })),
    }));
}

export type CommentingState = "idle" | "waiting" | "open" | "silent" | "closed";

export interface CommentingView {
  /** The most relevant window's: one open, else one waiting, else the latest. */
  readonly state: CommentingState;
  /** The server whose page that window was opened on. */
  readonly server: string | null;
  /** Drafts saved from the task's windows since each was opened or adopted. */
  readonly saved: number;
}

export interface CommentingDeps {
  /** dude's own window: where messages arrive and from which the preview is opened. */
  readonly win: Window;
  readonly taskId: string;
  /** The latest of what the page shows, read at each message. */
  readonly read: () => {
    origins: ReadonlySet<string>;
    items: ReadonlyArray<FeedbackItem>;
    drafts: number;
    taskKey: string;
    you: string | null;
    name: (personId: string | null | undefined) => string;
    /** dude's resolved theme, which the overlay follows once connected. */
    theme?: OverlayTheme;
  };
  /** Uploads a picture as the task's attachment: its id. */
  readonly upload: (picture: { bytes: Uint8Array; type: string; name: string }) => Promise<string>;
  /** Creates the draft: its id, and the reader's drafts on the task after it. */
  readonly createDraft: (anchor: OverlayAnchor & { attachmentId?: string }, body: string) => Promise<{ id: string; drafts: number }>;
  readonly onSaved: (id: string) => void;
  readonly onOpenItem: (id: string) => void;
  readonly onChange: (view: CommentingView) => void;
  /**
   * Whether this tab came here from a preview's Connect to dude (the
   * preview-connect page): then a hello from this tab's own window.opener,
   * at one of the task's preview origins, adopts that window.
   */
  readonly openerArrived?: () => boolean;
  readonly helloWaitMs?: number;
}

const PINS_KEY = "dude-preview-pins";

const ARRIVAL_KEY = "dude-preview-connect";

/**
 * This tab came from a preview's Connect to dude and was sent to `taskId`:
 * kept in the tab's sessionStorage, never the URL, so it outlives the tab's
 * reload and its moves inside the app but can't be pasted. It only lets the
 * task adopt this tab's own window.opener; the browser decides what that is.
 */
export function markPreviewArrival(win: Pick<Window, "sessionStorage">, taskId: string): void {
  try {
    win.sessionStorage.setItem(ARRIVAL_KEY, taskId);
    arrivedFor = null;
  } catch {
    // No storage: the module's copy, which lasts until this tab reloads.
    arrivedFor = taskId;
  }
}

let arrivedFor: string | null = null;

/** Whether this tab arrived at `taskId` from a preview's Connect to dude. */
export function arrivedFromPreview(win: Pick<Window, "sessionStorage">, taskId: string): boolean {
  try {
    return win.sessionStorage.getItem(ARRIVAL_KEY) === taskId;
  } catch {
    return arrivedFor === taskId;
  }
}

/** The windows one task keeps: the named one and those adopted beside it. */
export const MAX_WINDOWS = 8;

/** How far up a window's openers dude looks for itself, as the overlay looks for dude. */
const OPENER_STEPS = 5;

/**
 * One commenting window. Each keeps its own saves answered: a nonce is the
 * overlay's in that window, and a window opened or adopted anew starts with
 * none remembered, so nothing one page saved is answered to another.
 */
interface Pane {
  readonly win: Window;
  url: string | null;
  server: string | null;
  state: CommentingState;
  saved: number;
  pendingHighlight: string | null;
  // The one save in flight (one draft from the window at a time), and the ones answered:
  // a nonce sent again (the overlay gave up waiting) is not saved twice.
  inFlight: string | null;
  readonly answered: Map<string, Extract<DudeToOverlay, { type: "saved" }>>;
  helloTimer: number;
  // When it was last opened, adopted or heard from: the view and the ways in prefer the latest.
  touched: number;
}

const closedOrGone = (w: Window): boolean => {
  try {
    return w.closed;
  } catch {
    return true;
  }
};

/**
 * One task's commenting windows: opens the task's named one, adopts those
 * that reach this tab, hears them, answers each. A plain class (the task
 * page's hook holds one) so the guards are tested without React.
 */
export class PreviewCommenting {
  #panes: Pane[] = [];
  // The window open() opened by the task's name, once it has.
  #named: Window | null = null;
  #clock = 0;
  #watch = 0;
  readonly #d: CommentingDeps;
  // This tab's window name, sent with each session for the overlay's Go to dude.
  readonly #tab: string | null;

  constructor(deps: CommentingDeps) {
    this.#d = deps;
    this.#tab = nameTab(deps.win);
  }

  /** Starts hearing messages; the returned function stops. */
  listen(): () => void {
    this.#d.win.addEventListener("message", this.onMessage);
    return () => this.dispose();
  }

  dispose(): void {
    this.#d.win.removeEventListener("message", this.onMessage);
    this.#d.win.clearInterval(this.#watch);
    this.#watch = 0;
    for (const p of this.#panes) this.#d.win.clearTimeout(p.helloTimer);
  }

  get view(): CommentingView {
    const latest = (fits: (p: Pane) => boolean) => this.#panes.filter(fits).sort((a, b) => b.touched - a.touched)[0];
    const pick = latest((p) => p.state === "open") ?? latest((p) => p.state === "waiting") ?? latest((p) => p.state !== "idle");
    return { state: pick?.state ?? "idle", server: pick?.server ?? null, saved: this.#panes.reduce((n, p) => n + p.saved, 0) };
  }

  /** The windows held now, the named one among them. */
  get windows(): ReadonlyArray<Window> {
    return this.#panes.map((p) => p.win);
  }

  #emit(): void {
    this.#d.onChange(this.view);
  }

  #paneOf(source: unknown): Pane | null {
    return source ? this.#panes.find((p) => p.win === source) ?? null : null;
  }

  /** A window to hold: at the cap, the oldest closed one makes room, else the oldest the task's name doesn't hold. */
  #add(win: Window, url: string | null, server: string | null): Pane {
    if (this.#panes.length >= MAX_WINDOWS) {
      const byAge = [...this.#panes].sort((a, b) => a.touched - b.touched);
      this.#drop(byAge.find((p) => p.state === "closed" || closedOrGone(p.win)) ?? byAge.find((p) => p.win !== this.#named) ?? byAge[0]!);
    }
    const pane: Pane = { win, url, server, state: "waiting", saved: 0, pendingHighlight: null, inFlight: null, answered: new Map(), helloTimer: 0, touched: ++this.#clock };
    this.#panes.push(pane);
    this.#watchClosed();
    return pane;
  }

  #drop(pane: Pane): void {
    this.#d.win.clearTimeout(pane.helloTimer);
    this.#panes = this.#panes.filter((p) => p !== pane);
  }

  /** One check a second, for every window held: one closed says so. */
  #watchClosed(): void {
    if (this.#watch) return;
    this.#watch = this.#d.win.setInterval(() => {
      let changed = false;
      for (const p of this.#panes) {
        if ((p.state === "open" || p.state === "waiting" || p.state === "silent") && closedOrGone(p.win)) {
          p.state = "closed";
          this.#d.win.clearTimeout(p.helloTimer);
          changed = true;
        }
      }
      if (changed) this.#emit();
    }, 1_000);
  }

  /**
   * Opens `url` in the task's named window (the same one each time) and
   * waits for its overlay. Refused for a URL not at one of the task's
   * preview origins.
   */
  open(url: string, server: string | null, highlight: string | null = null): boolean {
    const origin = originOf(url);
    if (origin === null || !this.#d.read().origins.has(origin)) return false;
    // Without noopener: the overlay talks to its opener, and only to it.
    const w = this.#d.win.open(url, `dude-preview-${this.#d.taskId}`);
    if (!w) return false;
    // The name gave another window (the last one was closed): what the last one saved is not this one's.
    const before = this.#named && this.#named !== w ? this.#paneOf(this.#named) : null;
    if (before) this.#drop(before);
    this.#named = w;
    const pane = this.#paneOf(w) ?? this.#add(w, url, server);
    pane.url = url;
    pane.server = server;
    pane.pendingHighlight = highlight;
    pane.state = "waiting";
    pane.touched = ++this.#clock;
    this.#d.win.clearTimeout(pane.helloTimer);
    pane.helloTimer = this.#d.win.setTimeout(() => {
      if (pane.state === "waiting") {
        pane.state = "silent";
        this.#emit();
      }
    }, this.#d.helloWaitMs ?? HELLO_WAIT_MS);
    this.#focus(pane);
    this.#emit();
    return true;
  }

  /** The latest window held, in one of `states` and not closed, that `fits`. */
  #live(fits: (p: Pane) => boolean, ...states: CommentingState[]): Pane | null {
    return this.#panes.filter((p) => states.includes(p.state) && !closedOrGone(p.win) && fits(p)).sort((a, b) => b.touched - a.touched)[0] ?? null;
  }

  /**
   * The Feedback tab's way in (or the closed notice's Comment on preview) on `server`: a window
   * already open on that server brought forward, so the page and a
   * half-written comment stay; the named window opened at `url` otherwise.
   */
  comment(url: string, server: string): boolean {
    // By origin, not only server name: an adopted window knows its page, not its server.
    const at = this.#live((p) => p.server === server || p.server === null && originOf(p.url) !== null && originOf(p.url) === originOf(url), "open", "waiting");
    if (at) {
      this.#focus(at);
      return true;
    }
    return this.open(url, server);
  }

  /**
   * A feedback card's Page link: a window already on that page (origin and
   * path) brought forward, so a half-written comment stays; the named
   * window navigated to `url` otherwise.
   */
  visit(url: string, server: string): boolean {
    const page = overlayPage(url);
    const at = page === null ? null : this.#live((p) => p.url !== null && overlayPage(p.url) === page, "open", "waiting");
    if (at) {
      this.#focus(at);
      return true;
    }
    return this.open(url, server);
  }

  /** Brings the most relevant window forward, if it is still open. */
  focus(): void {
    const at = this.#live(() => true, "open", "waiting", "silent");
    if (at) this.#focus(at);
  }

  #focus(pane: Pane): void {
    try {
      if (!closedOrGone(pane.win)) pane.win.focus();
    } catch {
      // Focus is the browser's to refuse.
    }
  }

  /** Takes the overlay off every window's page; the windows stay and the drafts stay. */
  stop(): void {
    for (const p of this.#panes) {
      this.#post(p, { type: "stop", v: OVERLAY_PROTOCOL });
      this.#d.win.clearTimeout(p.helloTimer);
      p.state = "idle";
    }
    this.#emit();
  }

  /** The closed and silent notices dismissed: closed windows are let go, silent ones are idle. */
  dismiss(): void {
    let changed = false;
    for (const p of [...this.#panes]) {
      if (p.state === "closed") {
        this.#drop(p);
        changed = true;
      } else if (p.state === "silent") {
        p.state = "idle";
        changed = true;
      }
    }
    if (changed) this.#emit();
  }

  /**
   * Show on preview: marks an item on a window open on its page, or opens
   * its page and marks it once the overlay is there.
   */
  show(item: FeedbackItem): boolean {
    if (item.anchor.kind !== "preview") return false;
    const page = overlayPage(item.anchor.url);
    // Only "open": a highlight needs an overlay that has said hello.
    const at = this.#live((p) => overlayPage(p.url ?? "") === page, "open");
    if (at) {
      this.#post(at, { type: "highlight", v: OVERLAY_PROTOCOL, itemId: item.id });
      this.#focus(at);
      return true;
    }
    const origin = originOf(item.anchor.url);
    const server = this.#panes.filter((p) => p.server !== null && originOf(p.url) === origin).sort((a, b) => b.touched - a.touched)[0]?.server ?? null;
    return this.open(item.anchor.url, server, item.id);
  }

  /** Each open page's pins and dude's theme again, after the list or the theme changed (a save, a submit, an agent's reply, the person's theme). */
  refresh(): void {
    for (const p of this.#panes) if (p.state === "open" && p.url) this.#post(p, this.#session(p.url));
  }

  #post(pane: Pane, msg: DudeToOverlay): void {
    const origin = originOf(pane.url);
    if (closedOrGone(pane.win) || !origin) return;
    pane.win.postMessage(msg, origin);
  }

  #refuse(pane: Pane, reason: string, nonce: string | undefined): void {
    this.#post(pane, { type: "refused", v: OVERLAY_PROTOCOL, ...(nonce !== undefined ? { nonce } : {}), reason });
  }

  #session(url: string): DudeToOverlay {
    const s = this.#d.read();
    return { type: "session", v: OVERLAY_PROTOCOL, task: s.taskKey, url, pins: pinsFor(s.items, url, s.you, s.name), drafts: s.drafts, showPins: this.#showPins(),
      ...(s.theme ? { theme: s.theme } : {}), ...(this.#tab ? { dudeWindow: this.#tab } : {}) };
  }

  #showPins(value?: boolean): boolean {
    try {
      if (value !== undefined) this.#d.win.localStorage.setItem(PINS_KEY, value ? "1" : "0");
      return this.#d.win.localStorage.getItem(PINS_KEY) !== "0";
    } catch {
      return value ?? true;
    }
  }

  readonly onMessage = (e: MessageEvent): void => {
    const held = this.#paneOf(e.source);
    const origins = this.#d.read().origins;
    const linked = held === null && origins.has(e.origin) && this.#linked(e.source as Window | null);
    const checked = checkMessage(e, held !== null || linked, origins, originOf(held?.url));
    if (!checked.ok) {
      if ("reason" in checked && held) {
        const nonce = (e.data as { nonce?: unknown } | null)?.nonce;
        this.#refuse(held, checked.reason, typeof nonce === "string" && nonce.length <= 100 ? nonce : undefined);
      }
      return;
    }
    const msg = checked.msg;
    const p = held ?? (msg.type === "hello" || msg.type === "navigated" ? this.#adopt(e.source as Window, msg.url) : null);
    if (!p) return;
    switch (msg.type) {
      case "hello":
      case "navigated":
        p.url = msg.url;
        p.touched = ++this.#clock;
        if (p.state !== "open") {
          p.state = "open";
          this.#d.win.clearTimeout(p.helloTimer);
          this.#emit();
        }
        this.#post(p, this.#session(msg.url));
        if (p.pendingHighlight) {
          this.#post(p, { type: "highlight", v: OVERLAY_PROTOCOL, itemId: p.pendingHighlight });
          p.pendingHighlight = null;
        }
        return;
      case "prefs":
        this.#showPins(msg.showPins);
        return;
      case "open":
        if (this.#d.read().items.some((i) => i.id === msg.itemId)) this.#d.onOpenItem(msg.itemId);
        return;
      case "draft": {
        const done = p.answered.get(msg.nonce);
        if (done) {
          this.#post(p, done);
          return;
        }
        if (p.inFlight !== null) {
          // The same save sent again is answered when it lands; another one waits its turn.
          if (msg.nonce !== p.inFlight) this.#refuse(p, "Still saving the last one", msg.nonce);
          return;
        }
        p.inFlight = msg.nonce;
        void this.#save(p, msg).finally(() => { p.inFlight = null; });
      }
    }
  };

  /**
   * Whether a window this tab holds no handle on is linked to this tab, as
   * the browser says (a page can't forge `opener`):
   * - this tab is up its chain of openers: a window this tab opened before
   *   a reload, or a tab opened from one (target=_blank, ctrl-click);
   * - or it is this tab's own opener, and this tab came from its Connect to
   *   dude (`openerArrived`).
   */
  #linked(source: Window | null): boolean {
    return source !== null && (this.#reachesMe(source) || this.#isMyOpener(source));
  }

  /**
   * A linked window taken as one of the task's commenting windows, once its
   * hello or navigated has passed checkMessage. At the cap it takes only a
   * closed window's slot: a live window is never let go for a newcomer.
   */
  #adopt(source: Window, url: string): Pane | null {
    if (this.#panes.length >= MAX_WINDOWS && !this.#panes.some((p) => p.state === "closed" || closedOrGone(p.win))) return null;
    return this.#add(source, url, null);
  }

  #reachesMe(source: Window): boolean {
    let at: Window = source;
    for (let step = 0; step < OPENER_STEPS; step++) {
      let next: unknown;
      try {
        next = at.opener;
      } catch {
        return false;
      }
      if (!next) return false;
      if (next === this.#d.win) return true;
      at = next as Window;
    }
    return false;
  }

  #isMyOpener(source: Window): boolean {
    if (!this.#d.openerArrived?.()) return false;
    try {
      return this.#d.win.opener === source;
    } catch {
      return false;
    }
  }

  async #save(pane: Pane, msg: Extract<OverlayToDude, { type: "draft" }>): Promise<void> {
    // The API's own limit, checked before any upload: a refused draft leaves no picture behind.
    const drafts = this.#d.read().drafts;
    if (drafts >= FEEDBACK_LIMITS.drafts) {
      this.#refuse(pane, `you have ${drafts} drafts on this task: submit or remove some first`, msg.nonce);
      return;
    }
    let attachmentId: string | undefined;
    let picture: "attached" | "none" | "failed" = "none";
    if (msg.picture) {
      const decoded = pictureBytes(msg.picture);
      if (decoded) {
        try {
          const path = (() => { try { return new URL(msg.anchor.url).pathname.replace(/[^\w-]+/g, "-").replace(/^-|-$/g, ""); } catch { return ""; } })();
          attachmentId = await this.#d.upload({ ...decoded, name: `preview-${path || "page"}-element.${decoded.type === "image/png" ? "png" : "jpg"}` });
          picture = "attached";
        } catch {
          picture = "failed";
        }
      } else picture = "failed";
    }
    try {
      const anchor: OverlayAnchor & { attachmentId?: string } = { ...msg.anchor, ...(attachmentId ? { attachmentId } : {}) };
      const created = await this.#d.createDraft(anchor, msg.body);
      pane.saved++;
      const saved = { type: "saved", v: OVERLAY_PROTOCOL, nonce: msg.nonce, id: created.id, drafts: created.drafts, picture } as const;
      pane.answered.set(msg.nonce, saved);
      if (pane.answered.size > 50) pane.answered.delete(pane.answered.keys().next().value!);
      this.#post(pane, saved);
      this.#d.onSaved(created.id);
      this.#emit();
    } catch (err) {
      this.#refuse(pane, err instanceof Error ? err.message : String(err), msg.nonce);
    }
  }
}
