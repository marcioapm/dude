/**
 * The commenting windows one dude tab holds for a project's Servers page:
 * one PreviewCommenting per target — a task's (its named window, as the
 * task page opens it) or a branch preview server's — so several servers of
 * several Runs are connected to this tab at once. Every check stays the
 * target's own (origin and source, schema, size, nonces and uploads per
 * window); this only creates the targets and says which servers are
 * connected here. Nothing of it is stored or sent anywhere: it is this
 * tab's, in memory.
 */

import type { FeedbackItem, OverlayPin, OverlayTheme, PreviewFeedbackItem, RunningRun, RunningServer } from "@dude/domain";
import { OVERLAY_LIMITS, overlayPage } from "@dude/domain";
import { ApiError, type ApiClient } from "./api/client.ts";
import { PreviewCommenting, arrivedFromPreview, httpsOriginOf, markPreviewArrival, previewArrival, type CommentingState } from "./previewComment.ts";

/** A server a target was opened for, as the page names it. */
export interface ServerRef {
  readonly runId: string;
  readonly server: string;
}

export const serverKey = (r: ServerRef): string => `${r.runId}/${r.server}`;

/** The name a branch preview server's window is opened by: the same in every dude tab, so another tab's is reachable. */
export const branchWindowName = (r: ServerRef): string => `dude-preview-${r.runId}-${r.server}`.replace(/[^A-Za-z0-9_-]/g, "-").slice(0, OVERLAY_LIMITS.windowName);

/** Where an arrival from a branch preview's Connect to dude is kept (previewComment's arrival key). */
export const branchArrival = (r: ServerRef): string => `branch:${serverKey(r)}`;

const LANDING_KEY = "dude-preview-landing";

/** A branch preview's Connect to dude, as this tab keeps it while it is on the project's Servers page. */
export interface ServerLanding extends ServerRef {
  readonly projectId: string;
  readonly branch: string;
  readonly origin: string;
}

/**
 * This tab came from a branch preview's Connect to dude: kept in the tab's
 * sessionStorage, never the URL, with markPreviewArrival's key so the
 * Servers page adopts this tab's own window.opener and nothing else.
 */
export function markServerLanding(win: Pick<Window, "sessionStorage">, landing: ServerLanding): void {
  markPreviewArrival(win, branchArrival(landing));
  try {
    win.sessionStorage.setItem(LANDING_KEY, JSON.stringify(landing));
  } catch {
    memoryLanding = landing;
  }
}

let memoryLanding: ServerLanding | null = null;

/** The landing this tab arrived with on `projectId`'s Servers page, or null. */
export function serverLanding(projectId: string, win: Pick<Window, "sessionStorage"> = window): ServerLanding | null {
  let found: ServerLanding | null = memoryLanding;
  try {
    const raw = win.sessionStorage.getItem(LANDING_KEY);
    found = raw ? (JSON.parse(raw) as ServerLanding) : found;
  } catch {
    // The module's copy.
  }
  if (!found || found.projectId !== projectId || previewArrival(win) !== branchArrival(found)) return null;
  return found;
}

const clip = (s: string, n: number) => (s.length > n ? `${s.slice(0, n - 1)}…` : s);

/**
 * A branch preview server's saved feedback on one page as the overlay pins
 * it: open and resolved, the resolution as the thread's resolve entry,
 * never more than a pin shows.
 */
export function branchPins(items: ReadonlyArray<PreviewFeedbackItem>, url: string, you: string | null,
    name: (personId: string | null | undefined) => string): OverlayPin[] {
  const page = overlayPage(url);
  return items.filter((i) => overlayPage(i.anchor.url) === page && i.anchor.selector)
    .slice(0, OVERLAY_LIMITS.pins)
    .map((i, k) => ({
      id: i.id, n: k + 1, status: i.status, mine: i.author.id === you,
      author: clip(i.author.name.split(" ")[0] ?? "Someone", OVERLAY_LIMITS.name),
      body: clip(i.body, OVERLAY_LIMITS.pinBody),
      selector: clip(i.anchor.selector ?? "", 2_000),
      when: clip(`submitted ${ago(i.createdAt)}`, OVERLAY_LIMITS.name),
      replies: i.history.slice(-OVERLAY_LIMITS.replies).map((h) => ({
        who: clip(name(h.personId), OVERLAY_LIMITS.name), kind: h.status === "resolved" ? "resolve" as const : "reopen" as const,
        body: clip(h.note, OVERLAY_LIMITS.replyBody),
      })),
    }));
}

function ago(at: string): string {
  const min = Math.max(0, Math.round((Date.now() - Date.parse(at)) / 60_000));
  if (min < 1) return "just now";
  if (min < 60) return `${min} min ago`;
  const h = Math.round(min / 60);
  return h < 48 ? `${h} h ago` : `${Math.round(h / 24)} days ago`;
}

/** What a target reads at each message: the page's latest. */
export interface WindowsContext {
  readonly you: string | null;
  readonly name: (personId: string | null | undefined) => string;
  readonly theme?: OverlayTheme;
}

interface Target {
  commenting: PreviewCommenting;
  readonly run: RunningRun;
  // A branch server's saved feedback, or a task's items, for pins: read when its window opens and on events.
  items: ReadonlyArray<PreviewFeedbackItem> | ReadonlyArray<FeedbackItem>;
  states: Map<string, CommentingState>;
}

/** Connected to this tab: a window open, waiting for its hello, or not answering yet. */
const LIVE: ReadonlySet<CommentingState> = new Set(["open", "waiting", "silent"]);

export class ServerWindows {
  readonly #targets = new Map<string, Target>();
  readonly #win: Window;
  readonly #client: ApiClient;
  #ctx: WindowsContext;
  readonly #onChange: () => void;
  // Servers whose window another of this person's dude tabs took over: shown as moved.
  readonly #moved = new Set<string>();
  readonly #claimed = new Set<string>();

  constructor(win: Window, client: ApiClient, ctx: WindowsContext, onChange: () => void) {
    this.#win = win;
    this.#client = client;
    this.#ctx = ctx;
    this.#onChange = onChange;
  }

  set context(ctx: WindowsContext) {
    this.#ctx = ctx;
  }

  dispose(): void {
    for (const t of this.#targets.values()) t.commenting.dispose();
    this.#targets.clear();
  }

  /** The servers connected to this tab now. */
  connected(): ReadonlySet<string> {
    const out = new Set<string>();
    for (const [key, t] of this.#targets) {
      for (const [server, state] of t.states) if (LIVE.has(state)) out.add(t.run.kind === "task" ? `${t.run.id}/${server}` : key);
    }
    return out;
  }

  /** The servers whose window another of this person's dude tabs took. */
  moved(): ReadonlySet<string> {
    return this.#moved;
  }

  /** The servers whose window this tab claimed from another of this person's dude tabs, latest last. */
  claimed(): ReadonlyArray<string> {
    return [...this.#claimed];
  }

  /** What this tab knows of the Runs it has windows for, only their connected servers: listed when the read fails. */
  connectedRuns(): RunningRun[] {
    const on = this.connected();
    const runs = new Map<string, RunningRun>();
    for (const t of this.#targets.values()) {
      const servers = t.run.servers.filter((s) => on.has(`${t.run.id}/${s.name}`));
      if (servers.length === 0) continue;
      const had = runs.get(t.run.id);
      runs.set(t.run.id, { ...t.run, servers: [...(had?.servers ?? []), ...servers.filter((s) => !had?.servers.includes(s))] });
    }
    return [...runs.values()];
  }

  /**
   * Preview on a row: the server's window connected to this tab — focused
   * when this tab holds it, claimed when another of this person's dude tabs
   * does, opened otherwise. False: not a server comments can be made on, or
   * the browser refused the window, or every window this target holds is live.
   */
  open(run: RunningRun, server: RunningServer): boolean {
    if (!server.url || httpsOriginOf(server.url) === null) return false;
    const t = this.#target(run, server.name);
    this.#moved.delete(serverKey({ runId: run.id, server: server.name }));
    return t.commenting.comment(server.url, server.name);
  }

  /** The target for an arrival from a branch preview's Connect to dude: made now, so the opener's next hello is adopted. */
  expect(run: RunningRun, server: string): void {
    this.#target(run, server);
  }

  /** Pins follow the lists: a submit, a resolve or a reopen read again. */
  async reload(ref: ServerRef): Promise<void> {
    const key = serverKey(ref);
    const t = this.#targets.get(key) ?? [...this.#targets.values()].find((x) => x.run.id === ref.runId);
    if (!t) return;
    await this.#load(t, ref.server);
    t.commenting.refresh();
  }

  #target(run: RunningRun, server: string): Target {
    const key = run.kind === "task" ? `task:${run.task?.id ?? run.id}` : serverKey({ runId: run.id, server });
    const found = this.#targets.get(key);
    if (found) return found;
    const ref = { runId: run.id, server };
    const branch = run.kind === "preview";
    const origins = new Set(run.servers.filter((s) => (branch ? s.name === server : s.commenting?.available !== false))
      .flatMap((s) => httpsOriginOf(s.url) ?? []));
    const scoped = this.#client;
    const target: Target = { run, items: [], states: new Map(), commenting: undefined as unknown as PreviewCommenting };
    const taskId = run.task?.id ?? run.id;
    const commenting = new PreviewCommenting({
      win: this.#win,
      taskId,
      mode: branch ? "branch" : "task",
      claimable: true,
      ...(branch ? { windowName: branchWindowName(ref) } : {}),
      read: () => ({
        origins,
        items: branch ? [] : target.items as FeedbackItem[],
        drafts: branch ? 0 : (target.items as FeedbackItem[]).filter((i) => i.status === "draft" && i.author?.id === this.#ctx.you).length,
        taskKey: branch ? (run.branch ?? "") : (run.task?.key ?? ""),
        you: this.#ctx.you,
        name: this.#ctx.name,
        ...(this.#ctx.theme ? { theme: this.#ctx.theme } : {}),
        ...(branch ? { pins: (url: string) => branchPins(target.items as PreviewFeedbackItem[], url, this.#ctx.you, this.#ctx.name) } : {}),
      }),
      upload: async ({ bytes, type, name }) => {
        if (branch) return scoped.uploadPreviewAttachment(ref.runId, ref.server, { bytes, type });
        const blob = new Blob([bytes as BlobPart], { type });
        return (await scoped.uploadAttachment(taskId, { name, original: blob, delivered: blob })).id;
      },
      createDraft: async (anchor, body) => {
        const item = await scoped.createFeedbackDraft(taskId, { body, anchor });
        await this.#load(target, server);
        return { id: item.id, drafts: (target.items as FeedbackItem[]).filter((i) => i.status === "draft").length };
      },
      submit: async (anchor, body, key) => {
        try {
          const item = await scoped.submitPreviewFeedback(ref.runId, ref.server, { body, anchor, submissionKey: key });
          void this.#load(target, server).then(() => commenting.refresh());
          return item;
        } catch (err) {
          throw definite(err);
        }
      },
      verify: async () => {
        const origin = [...origins][0] ?? "";
        try {
          const found = await scoped.validatePreviewServer(ref.runId, ref.server, origin);
          if (found.kind !== "branch" || found.previewRunId !== ref.runId || found.server !== ref.server) {
            throw new ApiError(404, "not_found", "This preview ended. Open the new preview and connect again.");
          }
        } catch (err) {
          throw definite(err);
        }
        await this.#load(target, server);
      },
      onSaved: () => undefined,
      onOpenItem: () => undefined,
      onChange: (view) => {
        const name = view.server ?? server;
        const at = serverKey({ runId: run.id, server: name });
        target.states.set(name, view.state);
        if (view.state === "moved") {
          this.#moved.add(at);
          this.#claimed.delete(at);
        }
        if (view.state === "open" && view.claimed) {
          this.#claimed.delete(at);
          this.#claimed.add(at);
        }
        this.#onChange();
      },
      openerArrived: () => arrivedFromPreview(this.#win, branchArrival(ref)),
    });
    target.commenting = commenting;
    commenting.listen();
    this.#targets.set(key, target);
    void this.#load(target, server);
    return target;
  }

  async #load(t: Target, server: string): Promise<void> {
    try {
      if (t.run.kind === "preview") {
        const all: PreviewFeedbackItem[] = [];
        let after: string | undefined;
        // Bounded: the overlay pins at most OVERLAY_LIMITS.pins a page; four pages of 50 cover them.
        for (let page = 0; page < 4; page++) {
          const got = await this.#client.previewFeedback(t.run.id, server, { status: "all", limit: 50, ...(after ? { after } : {}) });
          all.push(...got.feedback);
          if (!got.nextCursor) break;
          after = got.nextCursor;
        }
        t.items = all;
      } else if (t.run.task) {
        t.items = (await this.#client.feedback(t.run.task.id)).feedback;
      }
    } catch {
      // Pins are a nicety beside commenting: kept as they were.
    }
  }
}

/** The one ServerWindows of this dude tab: its connections outlive the Servers page being left and opened again. */
let tabs: { win: Window; client: ApiClient; windows: ServerWindows; listeners: Set<() => void> } | null = null;

export function tabWindows(win: Window, client: ApiClient, ctx: WindowsContext): { windows: ServerWindows; subscribe: (fn: () => void) => () => void } {
  if (!tabs || tabs.win !== win || tabs.client !== client) {
    tabs?.windows.dispose();
    const listeners = new Set<() => void>();
    tabs = { win, client, listeners, windows: new ServerWindows(win, client, ctx, () => { for (const fn of listeners) fn(); }) };
  }
  const t = tabs;
  t.windows.context = ctx;
  return { windows: t.windows, subscribe: (fn) => { t.listeners.add(fn); return () => t.listeners.delete(fn); } };
}

/** For tests: forget the tab's windows. */
export function resetTabWindows(): void {
  tabs?.windows.dispose();
  tabs = null;
}

/** The API's refusal (a 4xx) is definite; anything else may have landed and is retryable. */
function definite(err: unknown): Error {
  const e = err instanceof Error ? err : new Error(String(err));
  if (err instanceof ApiError && err.status >= 400 && err.status < 500) Object.assign(e, { definite: true });
  return e;
}
