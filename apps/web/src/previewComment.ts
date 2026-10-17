/**
 * Commenting on a branch preview from its task: dude opens the preview in
 * a window it keeps a handle on (named per task, never noopener), and the
 * overlay the page loads (apps/web/overlay) posts to this tab. This tab
 * does what needs the person: uploads the picture, creates the draft,
 * lists the page's pins.
 *
 * A message is taken only from the window dude opened, from the origin of
 * one of this task's preview servers, within OVERLAY_LIMITS.message, and
 * valid by overlayMessageSchema (whose anchor is the API's own preview
 * anchor). Nothing the page says is used but the anchor's fields and the
 * person's words: no id, no attachment, no task comes from it.
 */

import {
  FEEDBACK_LIMITS, OVERLAY_LIMITS, OVERLAY_PROTOCOL, overSize, overlayMessageSchema, overlayPage,
  type DudeToOverlay, type FeedbackItem, type OverlayAnchor, type OverlayPin, type OverlayToDude, type TaskServers,
} from "@dude/domain";

/** Why commenting is not offered, in the words a person reads. */
export const COMMENTING_UNAVAILABLE: Readonly<Record<"public_url" | "lux", string>> = {
  public_url: "Commenting on previews needs auth.public_url",
  lux: "Commenting on previews needs lux v0.1.32 or later",
};

/** How long a window dude opened has to say hello before the notice says the preview is not answering. */
export const HELLO_WAIT_MS = 10_000;

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

const originOf = (url: string | null | undefined): string | null => {
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
  if (offer.kind !== "available") return false;
  const origin = originOf(url);
  return origin !== null && offer.servers.some((s) => originOf(s.url) === origin);
}

export type Checked =
  | { readonly ok: true; readonly msg: OverlayToDude }
  /** Not from the window dude opened at one of the task's origins: not answered at all. */
  | { readonly ok: false; readonly why: "origin" | "source" }
  /** From the right window, refused with words for its box. */
  | { readonly ok: false; readonly why: "size" | "invalid" | "moved"; readonly reason: string };

export const PREVIEW_MOVED = "This preview isn't the one at work any more — open it again from dude.";

/**
 * The guards, in order: origin, source window, size, then the schema and
 * the anchor's own origin. `openedAt` is the origin dude opened the window
 * at: that window, still there but no longer at one of `origins` (the task's
 * read moved to an agent Run, or the task changed), is told so instead of
 * being left unanswered.
 */
export function checkMessage(e: Pick<MessageEvent, "origin" | "source" | "data">, opened: Window | null, origins: ReadonlySet<string>, openedAt: string | null = null): Checked {
  if (!origins.has(e.origin)) {
    const ours = opened !== null && e.source === opened && openedAt !== null && e.origin === openedAt;
    return ours ? { ok: false, why: "moved", reason: PREVIEW_MOVED } : { ok: false, why: "origin" };
  }
  if (opened === null || e.source !== opened) return { ok: false, why: "source" };
  if (overSize(e.data, OVERLAY_LIMITS.message)) return { ok: false, why: "size", reason: "That is too much to send: try without the picture, or a smaller element." };
  const parsed = overlayMessageSchema.safeParse(e.data);
  if (!parsed.success) return { ok: false, why: "invalid", reason: "dude couldn't read what the page sent." };
  const msg = parsed.data;
  const url = msg.type === "draft" ? msg.anchor.url : msg.type === "hello" || msg.type === "navigated" ? msg.url : null;
  // An item is about the page that sent it, never another site's.
  if (url !== null && originOf(url) !== e.origin) return { ok: false, why: "invalid", reason: "The page named another site." };
  return { ok: true, msg };
}

/** A picture's bytes and type, when the data URL holds a PNG or JPEG (checked by its first bytes, not its label). */
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
  if (bytes[0] === 0x89 && bytes[1] === 0x50 && bytes[2] === 0x4e && bytes[3] === 0x47) return { bytes, type: "image/png" };
  if (bytes[0] === 0xff && bytes[1] === 0xd8 && bytes[2] === 0xff) return { bytes, type: "image/jpeg" };
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
      author: i.author?.name.split(" ")[0] ?? "Someone",
      mine: i.author?.id === you,
      body: clip(i.body, OVERLAY_LIMITS.pinBody),
      selector: i.anchor.kind === "preview" ? i.anchor.selector ?? "" : "",
      when: `${i.status === "draft" ? "drafted" : "submitted"} ${ago(i.submittedAt ?? i.createdAt, now)}`,
      replies: i.history.slice(-OVERLAY_LIMITS.replies).map((h) => ({
        who: h.personId ? name(h.personId) : "The agent", kind: h.kind, body: clip(h.body, OVERLAY_LIMITS.replyBody),
      })),
    }));
}

export type CommentingState = "idle" | "waiting" | "open" | "silent" | "closed";

export interface CommentingView {
  readonly state: CommentingState;
  /** The server whose page the window was opened on. */
  readonly server: string | null;
  /** Drafts saved from the window since it was opened. */
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
  };
  /** Uploads a picture as the task's attachment: its id. */
  readonly upload: (picture: { bytes: Uint8Array; type: string; name: string }) => Promise<string>;
  /** Creates the draft: its id, and the reader's drafts on the task after it. */
  readonly createDraft: (anchor: OverlayAnchor & { attachmentId?: string }, body: string) => Promise<{ id: string; drafts: number }>;
  readonly onSaved: (id: string) => void;
  readonly onOpenItem: (id: string) => void;
  readonly onChange: (view: CommentingView) => void;
  readonly helloWaitMs?: number;
}

const PINS_KEY = "dude-preview-pins";

/**
 * One task's commenting window: opens it, hears it, answers it. A plain
 * class (the task page's hook holds one) so the guards are tested without
 * React.
 */
export class PreviewCommenting {
  #win: Window | null = null;
  #url: string | null = null;
  #state: CommentingState = "idle";
  #server: string | null = null;
  #saved = 0;
  #pendingHighlight: string | null = null;
  // One draft from the window at a time.
  #saving = false;
  #timers: number[] = [];
  readonly #d: CommentingDeps;

  constructor(deps: CommentingDeps) {
    this.#d = deps;
  }

  /** Starts hearing messages; the returned function stops. */
  listen(): () => void {
    this.#d.win.addEventListener("message", this.onMessage);
    return () => this.dispose();
  }

  dispose(): void {
    this.#d.win.removeEventListener("message", this.onMessage);
    this.#clearTimers();
  }

  get view(): CommentingView {
    return { state: this.#state, server: this.#server, saved: this.#saved };
  }

  get window(): Window | null {
    return this.#win;
  }

  #emit(): void {
    this.#d.onChange(this.view);
  }

  #clearTimers(): void {
    for (const t of this.#timers) {
      this.#d.win.clearTimeout(t);
      this.#d.win.clearInterval(t);
    }
    this.#timers = [];
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
    if (w !== this.#win) this.#saved = 0;
    this.#win = w;
    this.#url = url;
    this.#server = server;
    this.#pendingHighlight = highlight;
    this.#state = "waiting";
    this.#clearTimers();
    this.#timers.push(this.#d.win.setTimeout(() => {
      if (this.#state === "waiting") {
        this.#state = "silent";
        this.#emit();
      }
    }, this.#d.helloWaitMs ?? HELLO_WAIT_MS));
    this.#timers.push(this.#d.win.setInterval(() => {
      if (this.#win?.closed && this.#state !== "closed") {
        this.#state = "closed";
        this.#clearTimers();
        this.#emit();
      }
    }, 1_000));
    try {
      w.focus();
    } catch {
      // Focus is the browser's to refuse.
    }
    this.#emit();
    return true;
  }

  /** Brings the window forward, if it is still open. */
  focus(): void {
    try {
      if (this.#win && !this.#win.closed) this.#win.focus();
    } catch {
      // Focus is the browser's to refuse.
    }
  }

  /** Takes the overlay off the page; the window stays and the drafts stay. */
  stop(): void {
    this.#post({ type: "stop", v: OVERLAY_PROTOCOL });
    this.#clearTimers();
    this.#state = "idle";
    this.#emit();
  }

  dismiss(): void {
    if (this.#state === "closed" || this.#state === "silent") {
      this.#state = "idle";
      this.#emit();
    }
  }

  /**
   * Show on preview: marks an item on the open page, or opens its page
   * and marks it once the overlay is there.
   */
  show(item: FeedbackItem): boolean {
    if (item.anchor.kind !== "preview") return false;
    if (this.#state === "open" && this.#win && !this.#win.closed && overlayPage(this.#url ?? "") === overlayPage(item.anchor.url)) {
      this.#post({ type: "highlight", v: OVERLAY_PROTOCOL, itemId: item.id });
      this.focus();
      return true;
    }
    return this.open(item.anchor.url, this.#server, item.id);
  }

  /** The page's pins again, after the list changed (a save, a submit, an agent's reply). */
  refresh(): void {
    if (this.#state === "open" && this.#url) this.#post(this.#session(this.#url));
  }

  #post(msg: DudeToOverlay): void {
    const w = this.#win;
    const origin = originOf(this.#url);
    if (!w || w.closed || !origin) return;
    w.postMessage(msg, origin);
  }

  #session(url: string): DudeToOverlay {
    const s = this.#d.read();
    return { type: "session", v: OVERLAY_PROTOCOL, task: s.taskKey, url, pins: pinsFor(s.items, url, s.you, s.name), drafts: s.drafts, showPins: this.#showPins() };
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
    this.#adopt(e);
    const checked = checkMessage(e, this.#win, this.#d.read().origins, originOf(this.#url));
    if (!checked.ok) {
      if ("reason" in checked) {
        const nonce = (e.data as { nonce?: unknown } | null)?.nonce;
        this.#post({ type: "refused", v: OVERLAY_PROTOCOL, ...(typeof nonce === "string" && nonce.length <= 100 ? { nonce } : {}), reason: checked.reason });
      }
      return;
    }
    const msg = checked.msg;
    switch (msg.type) {
      case "hello":
      case "navigated":
        this.#url = msg.url;
        if (this.#state !== "open") {
          this.#state = "open";
          this.#clearTimers();
          this.#timers.push(this.#d.win.setInterval(() => {
            if (this.#win?.closed && this.#state !== "closed") {
              this.#state = "closed";
              this.#clearTimers();
              this.#emit();
            }
          }, 1_000));
          this.#emit();
        }
        this.#post(this.#session(msg.url));
        if (this.#pendingHighlight) {
          this.#post({ type: "highlight", v: OVERLAY_PROTOCOL, itemId: this.#pendingHighlight });
          this.#pendingHighlight = null;
        }
        return;
      case "prefs":
        this.#showPins(msg.showPins);
        return;
      case "open":
        if (this.#d.read().items.some((i) => i.id === msg.itemId)) this.#d.onOpenItem(msg.itemId);
        return;
      case "draft":
        if (this.#saving) {
          this.#post({ type: "refused", v: OVERLAY_PROTOCOL, nonce: msg.nonce, reason: "Still saving the last one" });
          return;
        }
        this.#saving = true;
        void this.#save(msg).finally(() => { this.#saving = false; });
    }
  };

  /**
   * A tab that reloaded, or came back to the task, holds no handle on the
   * window it opened. A hello from one of this task's preview origins whose
   * sender's opener is this very window (the browser says so; a page can't
   * forge it) is that window: it is taken back, so the overlay reconnects
   * with its half-written comment in place.
   */
  #adopt(e: MessageEvent): void {
    if (this.#win !== null && !this.#win.closed) return;
    const m = e.data as { type?: unknown; url?: unknown } | null;
    if (!m || typeof m !== "object" || m.type !== "hello" && m.type !== "navigated" || typeof m.url !== "string") return;
    if (!this.#d.read().origins.has(e.origin) || originOf(m.url) !== e.origin) return;
    const source = e.source as Window | null;
    let opener: unknown;
    try {
      opener = source?.opener;
    } catch {
      return;
    }
    if (!source || opener !== this.#d.win) return;
    this.#win = source;
    this.#url = m.url;
    this.#saved = 0;
  }

  async #save(msg: Extract<OverlayToDude, { type: "draft" }>): Promise<void> {
    // The API's own limit, checked before any upload: a refused draft leaves no picture behind.
    const drafts = this.#d.read().drafts;
    if (drafts >= FEEDBACK_LIMITS.drafts) {
      this.#post({ type: "refused", v: OVERLAY_PROTOCOL, nonce: msg.nonce, reason: `you have ${drafts} drafts on this task: submit or remove some first` });
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
      this.#saved++;
      this.#post({ type: "saved", v: OVERLAY_PROTOCOL, nonce: msg.nonce, id: created.id, drafts: created.drafts, picture });
      this.#d.onSaved(created.id);
      this.#emit();
    } catch (err) {
      this.#post({ type: "refused", v: OVERLAY_PROTOCOL, nonce: msg.nonce, reason: err instanceof Error ? err.message : String(err) });
    }
  }
}
