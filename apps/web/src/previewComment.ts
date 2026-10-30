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
  FEEDBACK_LIMITS, OVERLAY_LIMITS, OVERLAY_OPENER_STEPS, OVERLAY_PROTOCOL, overSize, overlayMessageSchema, overlayPage, overlayWindowName,
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
    servers: marked.filter((s) => s.state === "ready" && httpsOriginOf(s.url) !== null)
      .map((s) => ({ name: s.name, url: s.url!, needsRestart: s.needsRestart === true })),
    restart: marked.filter((s) => s.needsRestart === true).map((s) => s.name),
  };
}

/** The origins a message may come from: this task's preview servers' that carry the overlay. */
export function previewOrigins(data: TaskServers | null): ReadonlySet<string> {
  if (!data?.run || data.run.kind !== "preview") return new Set();
  return new Set(data.servers.filter((s) => s.commenting?.available).flatMap((s) => httpsOriginOf(s.url) ?? []));
}

/** Whether Show on preview can work for a page: it is on one of the servers comments can be made on now. */
export function showable(offer: CommentingOffer, url: string): boolean {
  return serverOf(offer, url) !== null;
}

/** The server comments can be made on now whose origin `url` is at, or null. */
export function serverOf(offer: CommentingOffer, url: string): CommentableServer | null {
  if (offer.kind !== "available") return null;
  const origin = httpsOriginOf(url);
  return origin === null ? null : offer.servers.find((s) => httpsOriginOf(s.url) === origin) ?? null;
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
  if (url !== null && httpsOriginOf(url) !== e.origin) return { ok: false, why: "invalid", reason: "The page named another site." };
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

export type CommentingState = "idle" | "waiting" | "open" | "silent" | "closed" | "moved";

export interface CommentingView {
  /** The most relevant window's: one open, else one waiting, else the latest. */
  readonly state: CommentingState;
  /** The server whose page that window was opened on. */
  readonly server: string | null;
  /** The page that window is on, as it last said (or was opened at). */
  readonly url?: string | null;
  /** Drafts saved from the task's windows since each was opened or adopted. */
  readonly saved: number;
  /** That window was claimed from another of this person's dude tabs. */
  readonly claimed?: boolean;
}

export interface CommentingDeps {
  /** dude's own window: where messages arrive and from which the preview is opened. */
  readonly win: Window;
  readonly taskId: string;
  /** The window opened by name for this target; `dude-preview-<taskId>` by default. */
  readonly windowName?: string;
  /**
   * `branch`: a branch preview's server, no task. A comment is submitted at
   * once (`submit`), never a draft; the session says `mode: "branch"`.
   */
  readonly mode?: "task" | "branch";
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
    /** A page's pins when they are not task items (a branch preview's saved feedback). */
    pins?: (url: string) => OverlayPin[];
  };
  /** Uploads a picture as the task's attachment: its id. */
  readonly upload: (picture: { bytes: Uint8Array; type: string; name: string }) => Promise<string>;
  /** Creates the draft: its id, and the reader's drafts on the task after it. */
  readonly createDraft: (anchor: OverlayAnchor & { attachmentId?: string }, body: string) => Promise<{ id: string; drafts: number }>;
  /**
   * Branch mode: submits the comment with `key` as its submission key (a
   * retry with the same key is the same item). A thrown error with
   * `definite: true` is the API's refusal; any other may have landed.
   */
  readonly submit?: (anchor: OverlayAnchor & { attachmentId?: string }, body: string, key: string) => Promise<{ id: string }>;
  /**
   * Branch mode: the server is still the one this target names (the dude
   * tab's check, never the page's word). Throws, `definite: true`, when not.
   */
  readonly verify?: () => Promise<void>;
  /**
   * Preview first looks for this target's window already open in this
   * browser (another of this person's dude tabs may hold it) and claims it.
   */
  readonly claimable?: boolean;
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
  return previewArrival(win) === taskId;
}

/** What this tab arrived at from a preview's Connect to dude, or null. */
export function previewArrival(win: Pick<Window, "sessionStorage">): string | null {
  try {
    return win.sessionStorage.getItem(ARRIVAL_KEY);
  } catch {
    return arrivedFor;
  }
}

/** The windows one task keeps: the named one and those adopted beside it. */
export const MAX_WINDOWS = 8;

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
  readonly answered: Map<string, DudeToOverlay>;
  // Branch mode: a picture uploaded for a nonce, reused when the same save is sent again.
  readonly uploads: Map<string, { picture: string; id: string }>;
  helloTimer: number;
  // When it was last opened, adopted or heard from: the view and the ways in prefer the latest.
  touched: number;
  // Reached by name in another of this person's dude tabs and claimed from it.
  claimed: boolean;
}

/**
 * Every commenting window a dude tab holds, by whichever PreviewCommenting
 * holds it: a window is answered by one of them only, so two (a task page's
 * and the Servers page's) never both save one draft. Keyed by the dude tab's
 * own window.
 */
const heldByTab = new WeakMap<Window, Map<Window, PreviewCommenting>>();
const heldIn = (tab: Window): Map<Window, PreviewCommenting> => {
  let m = heldByTab.get(tab);
  if (!m) heldByTab.set(tab, (m = new Map()));
  return m;
};

const closedOrGone = (w: Window): boolean => {
  try {
    return w.closed;
  } catch {
    return true;
  }
};

/** A window showing another origin's page: its location can't be read. A blank one just opened can. */
const foreignPage = (w: Window): boolean => {
  if (closedOrGone(w)) return false;
  try {
    void w.location.href;
    return false;
  } catch {
    return true;
  }
};

const focusWin = (w: Window): void => {
  try {
    if (!closedOrGone(w)) w.focus();
  } catch {
    // Focus is the browser's to refuse.
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
    for (const c of this.#claims.values()) this.#d.win.clearTimeout(c.timer);
    this.#claims.clear();
    for (const p of this.#panes) {
      this.#d.win.clearTimeout(p.helloTimer);
      if (this.#tabHeld.get(p.win) === this) this.#tabHeld.delete(p.win);
    }
  }

  get #name(): string {
    return this.#d.windowName ?? `dude-preview-${this.#d.taskId}`;
  }

  // The windows this dude tab holds, across its commenting targets.
  get #tabHeld(): Map<Window, PreviewCommenting> {
    return heldIn(this.#d.win);
  }

  get #branch(): boolean {
    return this.#d.mode === "branch";
  }

  get view(): CommentingView {
    const pick = this.#latest((p) => p.state === "open") ?? this.#latest((p) => p.state === "waiting") ?? this.#latest((p) => p.state !== "idle");
    return { state: pick?.state ?? "idle", server: pick?.server ?? null, url: pick?.url ?? null, saved: this.#panes.reduce((n, p) => n + p.saved, 0),
      ...(pick?.claimed ? { claimed: true } : {}) };
  }

  /** Every window held now: its server, its page and its state. */
  get panes(): ReadonlyArray<{ readonly server: string | null; readonly url: string | null; readonly state: CommentingState }> {
    return this.#panes.map((p) => ({ server: p.server, url: p.url, state: p.state }));
  }

  // The window last opened, adopted or heard from among those that `fits`.
  #latest(fits: (p: Pane) => boolean): Pane | undefined {
    return this.#panes.filter(fits).sort((a, b) => b.touched - a.touched)[0];
  }

  // For tests: the windows held now, the named one among them.
  get windows(): ReadonlyArray<Window> {
    return this.#panes.map((p) => p.win);
  }

  #emit(): void {
    this.#d.onChange(this.view);
  }

  #paneOf(source: unknown): Pane | null {
    return source ? this.#panes.find((p) => p.win === source) ?? null : null;
  }

  /**
   * A window to hold. Another commenting target of this tab that held it
   * lets it go: the newest opener answers it. At the cap only a closed,
   * stopped or moved window makes room; a live one is never let go, and the
   * window is not held (null).
   */
  #add(win: Window, url: string | null, server: string | null): Pane | null {
    if (this.#panes.length >= MAX_WINDOWS) {
      const free = [...this.#panes].sort((a, b) => a.touched - b.touched)
        .find((p) => p.state === "closed" || p.state === "idle" || p.state === "moved" || closedOrGone(p.win));
      if (!free) return null;
      this.#drop(free);
    }
    const before = this.#tabHeld.get(win);
    if (before && before !== this) before.#release(win);
    const pane: Pane = { win, url, server, state: "waiting", saved: 0, pendingHighlight: null, inFlight: null, answered: new Map(),
      uploads: new Map(), helloTimer: 0, touched: ++this.#clock, claimed: false };
    this.#panes.push(pane);
    this.#tabHeld.set(win, this);
    this.#watchClosed();
    return pane;
  }

  #drop(pane: Pane): void {
    this.#d.win.clearTimeout(pane.helloTimer);
    this.#panes = this.#panes.filter((p) => p !== pane);
    if (this.#tabHeld.get(pane.win) === this) this.#tabHeld.delete(pane.win);
  }

  // Another target of this tab took the window: it is that one's now.
  #release(win: Window): void {
    const pane = this.#paneOf(win);
    if (!pane) return;
    this.#drop(pane);
    if (this.#named === win) this.#named = null;
    this.#emit();
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
    const origin = httpsOriginOf(url);
    if (origin === null || !this.#d.read().origins.has(origin)) return false;
    if (this.#d.claimable) {
      const claimed = this.#claim(origin, server);
      if (claimed !== null) return claimed;
    }
    // Without noopener: the overlay talks to its opener, and only to it.
    const w = this.#d.win.open(url, this.#name);
    if (!w) return false;
    const claim = this.#claims.get(w);
    if (claim) {
      this.#d.win.clearTimeout(claim.timer);
      this.#claims.delete(w);
    }
    // The name gave another window (the last one was closed): what the last one saved is not this one's.
    const before = this.#named && this.#named !== w ? this.#paneOf(this.#named) : null;
    if (before) this.#drop(before);
    const pane = this.#paneOf(w) ?? this.#add(w, url, server);
    if (!pane) return false;
    this.#named = w;
    pane.url = url;
    pane.server = server;
    pane.pendingHighlight = highlight;
    pane.state = "waiting";
    pane.claimed = false;
    pane.touched = ++this.#clock;
    this.#waitForHello(pane);
    this.#focus(pane);
    this.#emit();
    return true;
  }

  #waitForHello(pane: Pane): void {
    this.#d.win.clearTimeout(pane.helloTimer);
    pane.helloTimer = this.#d.win.setTimeout(() => {
      if (pane.state === "waiting") {
        pane.state = "silent";
        this.#emit();
      }
    }, this.#d.helloWaitMs ?? HELLO_WAIT_MS);
  }

  /**
   * Reconnect-if-mine: the window of this name, reached from the click with
   * window.open("", name). Only a window of this browser answers to it, so a
   * page there already (its location unreadable: another origin) is one this
   * person opened, connected to another of their dude tabs or none. It is
   * told `claim` at the server's origin and held here, its page kept; the
   * tab that had it is told `left` by the overlay. Null: no such window
   * (the probe opened a blank one), so open() goes on to navigate it.
   */
  #claim(origin: string, server: string | null): boolean | null {
    const w = this.#d.win.open("", this.#name);
    if (!w) return false;
    if (!foreignPage(w)) return null;
    const held = this.#paneOf(w);
    if (held && held.state !== "moved" && held.state !== "closed") {
      // This target's own window, on another of its servers, or one that stopped answering (it left the server): navigated.
      if (held.state === "silent" || (held.url !== null && httpsOriginOf(held.url) !== origin)) return null;
      this.#focus(held);
      return true;
    }
    // Held live by another target of this tab (the task page's, or a page that named its window so): left to it, brought forward.
    const holder = this.#tabHeld.get(w);
    if (holder && holder !== this && holder.#holdsLive(w)) {
      focusWin(w);
      return true;
    }
    const pending = this.#claims.get(w);
    // Claimed before and never answered from the server's origin: the window left it, so it is navigated back.
    if (pending?.unanswered) return null;
    if (pending) this.#d.win.clearTimeout(pending.timer);
    const claim = { origin, server, unanswered: false, timer: 0 };
    claim.timer = this.#d.win.setTimeout(() => { claim.unanswered = true; }, this.#d.helloWaitMs ?? HELLO_WAIT_MS);
    this.#claims.set(w, claim);
    w.postMessage({ type: "claim", v: OVERLAY_PROTOCOL } satisfies DudeToOverlay, origin);
    focusWin(w);
    return true;
  }

  // Windows told `claim` and not yet heard from: one is held only once a hello from its claim's origin arrives.
  readonly #claims = new Map<Window, { origin: string; server: string | null; unanswered: boolean; timer: number }>();

  #holdsLive(w: Window): boolean {
    const p = this.#paneOf(w);
    return p !== null && (p.state === "open" || p.state === "waiting" || p.state === "silent") && !closedOrGone(p.win);
  }

  /** The latest window held, in one of `states` and not closed, that `fits`. */
  #live(fits: (p: Pane) => boolean, ...states: CommentingState[]): Pane | null {
    return this.#latest((p) => states.includes(p.state) && !closedOrGone(p.win) && fits(p)) ?? null;
  }

  /**
   * The Feedback tab's way in (or the closed notice's Comment on preview) on `server`: a window
   * already open on that server brought forward, so the page and a
   * half-written comment stay; the named window opened at `url` otherwise.
   */
  comment(url: string, server: string): boolean {
    // By origin: two servers (or two Runs' servers) may share a name, and an adopted window knows its page, not its server.
    const origin = httpsOriginOf(url);
    const at = this.#live((p) => origin !== null && httpsOriginOf(p.url) === origin, "open", "waiting");
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
    focusWin(pane.win);
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
    const origin = httpsOriginOf(item.anchor.url);
    const server = this.#latest((p) => p.server !== null && httpsOriginOf(p.url) === origin)?.server ?? null;
    return this.open(item.anchor.url, server, item.id);
  }

  /** Each open page's pins and dude's theme again, after the list or the theme changed (a save, a submit, an agent's reply, the person's theme). */
  refresh(): void {
    // A branch server the dude tab no longer vouches for gets no pins.
    if (this.#branch && !this.#valid) return;
    for (const p of this.#panes) if (p.state === "open" && p.url) this.#post(p, this.#session(p.url));
  }

  #post(pane: Pane, msg: DudeToOverlay): void {
    const origin = httpsOriginOf(pane.url);
    if (closedOrGone(pane.win) || !origin) return;
    pane.win.postMessage(msg, origin);
  }

  #refuse(pane: Pane, reason: string, nonce: string | undefined): void {
    this.#post(pane, { type: "refused", v: OVERLAY_PROTOCOL, ...(nonce !== undefined ? { nonce } : {}), reason });
  }

  #session(url: string, withPins = true): DudeToOverlay {
    const s = this.#d.read();
    const pins = !withPins ? [] : s.pins ? s.pins(url).slice(0, OVERLAY_LIMITS.pins) : pinsFor(s.items, url, s.you, s.name);
    return { type: "session", v: OVERLAY_PROTOCOL, task: s.taskKey.slice(0, 100), url, pins, drafts: this.#branch ? 0 : s.drafts,
      showPins: this.#showPins(), ...(this.#branch ? { mode: "branch" as const } : {}),
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
    let held = this.#paneOf(e.source);
    const origins = this.#d.read().origins;
    const claim = e.source ? this.#claims.get(e.source as Window) : undefined;
    // A window this target told `claim`, now speaking from the server's origin it was claimed at.
    const claimed = claim !== undefined && claim.origin === e.origin;
    const linked = held === null && origins.has(e.origin) && (claimed || this.#linked(e.source as Window | null));
    const checked = checkMessage(e, held !== null || linked || claimed, origins, httpsOriginOf(held?.url));
    if (!checked.ok) {
      if ("reason" in checked && held) {
        const nonce = (e.data as { nonce?: unknown } | null)?.nonce;
        this.#refuse(held, checked.reason, typeof nonce === "string" && nonce.length <= 100 ? nonce : undefined);
      }
      return;
    }
    const msg = checked.msg;
    if (msg.type === "left") {
      // Claimed by another of this person's dude tabs: shown as moved, answered no more.
      if (held && held.state !== "moved") {
        held.state = "moved";
        this.#d.win.clearTimeout(held.helloTimer);
        if (this.#tabHeld.get(held.win) === this) this.#tabHeld.delete(held.win);
        this.#emit();
      }
      return;
    }
    const hello = msg.type === "hello" || msg.type === "navigated";
    if (claimed && hello) {
      this.#d.win.clearTimeout(claim.timer);
      this.#claims.delete(e.source as Window);
      if (held?.state === "moved") {
        this.#drop(held);
        held = null;
      }
    }
    if (held?.state === "moved") return;
    const p = held ?? (hello ? this.#adopt(e.source as Window, msg.url, claimed ? { server: claim.server, moved: msg.type === "hello" && msg.moved === true } : null) : null);
    if (!p) return;
    switch (msg.type) {
      case "hello":
      case "navigated":
        p.url = msg.url;
        p.touched = ++this.#clock;
        if (this.#branch) {
          this.#verified(p, msg.url);
          return;
        }
        this.#connected(p, msg.url);
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
        void (this.#branch ? this.#submit(p, msg) : this.#save(p, msg)).finally(() => { p.inFlight = null; });
      }
    }
  };

  #connected(p: Pane, url: string): void {
    if (p.state !== "open") {
      p.state = "open";
      this.#d.win.clearTimeout(p.helloTimer);
      this.#emit();
    }
    this.#post(p, this.#session(url));
    if (p.pendingHighlight) {
      this.#post(p, { type: "highlight", v: OVERLAY_PROTOCOL, itemId: p.pendingHighlight });
      p.pendingHighlight = null;
    }
  }

  // Branch mode: the dude tab asks the API whether the server is still this target's, at most every 30 s.
  #checkedAt = 0;
  #valid = false;
  #checking: Promise<void> | null = null;
  #verified(p: Pane, url: string): void {
    const verify = this.#d.verify;
    if (!verify) return this.#connected(p, url);
    if (this.#checking) {
      void this.#checking.then(() => { if (this.#valid && p.url === url) this.#connected(p, url); });
      return;
    }
    if (Date.now() < this.#checkedAt) {
      if (this.#valid) this.#connected(p, url);
      return;
    }
    this.#checkedAt = Date.now() + 30_000;
    this.#checking = verify().then(() => {
      this.#valid = true;
      if (p.url === url) this.#connected(p, url);
    }, (err: unknown) => {
      const was = this.#valid;
      this.#valid = false;
      // A transport failure leaves the window waiting; only the API's refusal is told to the page.
      if ((err as { definite?: boolean } | null)?.definite) {
        // Every page of this server that was sent pins loses them: an ended preview's feedback is shown no more.
        const reason = err instanceof Error ? err.message : "This preview ended.";
        for (const q of this.#panes) {
          if (q !== p && (q.state !== "open" || !q.url)) continue;
          if (was && q.url) this.#post(q, this.#session(q.url, false));
          this.#refuse(q, reason, undefined);
        }
      }
    }).finally(() => { this.#checking = null; });
  }

  /** Branch mode: the server is checked again now (its Run ended or its servers changed), for every window on a page. */
  recheck(): void {
    if (!this.#branch) return;
    this.#checkedAt = 0;
    for (const p of this.#panes) if (p.state === "open" && p.url) this.#verified(p, p.url);
  }

  /** Branch mode: submitted at once with the overlay's nonce as its key, so a resend is the same item. */
  async #submit(pane: Pane, msg: Extract<OverlayToDude, { type: "draft" }>): Promise<void> {
    const submit = this.#d.submit;
    if (!submit || !this.#valid) {
      this.#post(pane, { type: "retryable", v: OVERLAY_PROTOCOL, nonce: msg.nonce, reason: "dude hasn't confirmed this preview yet: try again in a moment." });
      return;
    }
    try {
      let attachmentId: string | undefined;
      if (msg.picture) {
        const image = pictureBytes(msg.picture);
        // A requested screenshot is never silently dropped from a submission.
        if (!image) throw Object.assign(new Error("The screenshot is not a PNG or JPEG image."), { definite: true });
        const kept = pane.uploads.get(msg.nonce);
        attachmentId = kept?.picture === msg.picture ? kept.id : await this.#d.upload({ ...image, name: "preview" });
        pane.uploads.set(msg.nonce, { picture: msg.picture, id: attachmentId });
        if (pane.uploads.size > 50) pane.uploads.delete(pane.uploads.keys().next().value!);
      }
      const item = await submit({ ...msg.anchor, ...(attachmentId ? { attachmentId } : {}) }, msg.body, msg.nonce);
      pane.saved++;
      const saved: DudeToOverlay = { type: "saved", v: OVERLAY_PROTOCOL, nonce: msg.nonce, id: item.id, drafts: 0, picture: attachmentId ? "attached" : "none" };
      pane.answered.set(msg.nonce, saved);
      if (pane.answered.size > 50) pane.answered.delete(pane.answered.keys().next().value!);
      this.#post(pane, saved);
      this.#d.onSaved(item.id);
      this.#emit();
    } catch (err) {
      const reason = err instanceof Error ? err.message : "Feedback submission failed";
      const definite = (err as { definite?: boolean } | null)?.definite === true;
      this.#post(pane, { type: definite ? "refused" : "retryable", v: OVERLAY_PROTOCOL, nonce: msg.nonce, reason });
    }
  }

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
   * hello or navigated has passed checkMessage. At the cap it takes only the
   * slot of the oldest window that is closed or stopped (`idle`): a live
   * window is never let go for a newcomer.
   */
  #adopt(source: Window, url: string, claim: { server: string | null; moved: boolean } | null = null): Pane | null {
    // Held by another target of this tab: that one answers it.
    const holder = this.#tabHeld.get(source);
    if (holder && holder !== this && holder.#holdsLive(source)) return null;
    const pane = this.#add(source, url, claim?.server ?? null);
    if (pane && claim) {
      this.#named = source;
      // Moved from another dude tab only when the overlay says it told one `left`: a claim the overlay took from
      // this same tab (a reload of it) it ignores, and the window's opener can't tell (window.open by name re-parents it).
      pane.claimed = claim.moved;
    }
    return pane;
  }

  #reachesMe(source: Window): boolean {
    let at: Window = source;
    for (let step = 0; step < OVERLAY_OPENER_STEPS; step++) {
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
