/**
 * A server's feedback folded under its row: a branch preview's (open and
 * resolved, paged honestly, a failed read with Try again) and a task's
 * (its statuses, your own draft first, the Feedback tab link), and the
 * project's stream moving it: a submit from someone else's window arrives
 * marked New, the row's counts move, "Updated just now".
 */

import { afterEach, beforeEach, expect, test } from "bun:test";
import { EventTypes, type PreviewFeedbackItem } from "@dude/domain";
import { ApiError, type ApiClient } from "../src/api/client.ts";
import { FixtureClient, emit, pagePreviewFeedback } from "../src/fixtures/client.ts";
import { SERVERS_KEY } from "../src/fixtures/scenario.ts";
import { ORG, PROJECT, TASK_ID } from "../src/fixtures/data.ts";
import { ProjectServersScreen } from "../src/screens/ProjectServersScreen.tsx";
import { FOLD_READ_MS } from "../src/screens/ServerFeedback.tsx";
import { resetTabWindows } from "../src/serverWindows.ts";
import { act, click, mount, settle, until } from "./dom.ts";

const unmounts: Array<() => Promise<void>> = [];
beforeEach(() => {
  localStorage.removeItem(SERVERS_KEY);
  resetTabWindows();
});
afterEach(async () => {
  for (const u of unmounts.splice(0)) await u();
  localStorage.removeItem(SERVERS_KEY);
  resetTabWindows();
});

async function page(client: ApiClient = new FixtureClient("a")) {
  const opened: string[] = [];
  const { container, unmount } = await mount(<ProjectServersScreen client={client} projectId={PROJECT.id} projectName="web-console"
    onBoard={() => {}} onTaskFeedback={(id) => opened.push(id)} />);
  unmounts.push(unmount);
  await until(() => container.querySelector('[data-testid="server-group"]'), "the list");
  return { container, opened };
}

const tid = (root: ParentNode, id: string) => root.querySelector<HTMLElement>(`[data-testid="${id}"]`);
const row = (root: ParentNode, key: string) => root.querySelector<HTMLElement>(`[data-key="${key}"]`)!;
const option = (fold: HTMLElement, starts: string) => [...fold.querySelectorAll<HTMLButtonElement>('[data-testid="fold-filter"] button')].find((b) => b.textContent!.startsWith(starts))!;

async function unfold(container: HTMLElement, key: string): Promise<HTMLElement> {
  await click(tid(row(container, key), "server-fold-toggle")!);
  const fold = await until(() => container.querySelector<HTMLElement>('[data-testid="server-fold"]'), "the fold");
  await until(() => (fold.querySelector('[data-testid="fold-item"], .serverFoldEmpty, [data-testid="fold-error"]') ? fold : null), "its feedback");
  return fold;
}

test("a branch preview server's feedback: everyone's, open and resolved, with the resolution as its thread", async () => {
  const { container } = await page();
  const toggle = tid(row(container, "run_pv_checkout/web"), "server-fold-toggle")!;
  expect(toggle.getAttribute("aria-expanded")).toBe("false");
  const fold = await unfold(container, "run_pv_checkout/web");
  expect(toggle.getAttribute("aria-expanded")).toBe("true");
  expect(fold.dataset.kind).toBe("preview");
  expect([...fold.querySelectorAll('[data-testid="fold-filter"] button')].map((b) => b.textContent)).toEqual(["All 3", "Open 2", "Resolved 1"]);
  expect(fold.querySelectorAll('[data-testid="fold-item"]').length).toBe(3);
  expect(fold.textContent).toContain("Ana Ribeiro");
  expect(fold.textContent).toContain("Tom Okafor");
  expect(fold.textContent).toContain("Increased the contrast and spacing around the delivery estimate.");
  await click(option(fold, "Open"));
  expect([...fold.querySelectorAll<HTMLElement>('[data-testid="fold-item"]')].map((i) => i.dataset.status)).toEqual(["open", "open"]);
  await click(option(fold, "Resolved"));
  expect([...fold.querySelectorAll<HTMLElement>('[data-testid="fold-item"]')].map((i) => i.dataset.status)).toEqual(["resolved"]);
  // A preview server has no task: no Feedback tab link, no drafts.
  expect(fold.querySelectorAll('[data-testid="task-feedback-link"], [data-testid="my-draft"]').length).toBe(0);
});

test("a long list shows the newest first and pages back with Load more, never cut short", async () => {
  // The real route's order: by id (time-ordered), ascending unless order=desc.
  class Many extends FixtureClient {
    base: PreviewFeedbackItem | null = null;
    extra: PreviewFeedbackItem[] = [];
    // Someone else's resolve/reopen, by item id: its status and thread as the API now returns them.
    changed = new Map<string, Pick<PreviewFeedbackItem, "status" | "history">>();
    override async previewFeedback(runId: string, server: string, params: { status?: "open" | "resolved" | "all"; after?: string; before?: string; order?: "asc" | "desc"; limit?: number } = {}) {
      this.base ??= (await super.previewFeedback("run_pv_checkout", "web", { status: "all" })).feedback.find((i) => i.status === "open")!;
      const b = { ...this.base, status: "open" as const, history: [] };
      const all = Array.from({ length: 45 }, (_, i) => {
        const id = `fbi_m${String(i).padStart(2, "0")}`;
        return { ...b, id, body: `Item ${i}`, createdAt: new Date(Date.UTC(2026, 9, 1, 0, i)).toISOString(), ...this.changed.get(id) };
      });
      void runId; void server;
      return pagePreviewFeedback([...all, ...this.extra], params);
    }
  }
  const client = new Many("a");
  const { container } = await page(client);
  const fold = await unfold(container, "run_pv_checkout/web");
  const bodies = () => [...fold.querySelectorAll('[data-testid="fold-item"], [data-testid="new-item"]')].map((e) => /Item \d+|Newest/.exec(e.textContent ?? "")?.[0]);
  expect(fold.querySelectorAll('[data-testid="fold-item"]').length).toBe(20);
  expect(bodies()[0]).toBe("Item 44");
  expect(bodies()[19]).toBe("Item 25");
  await click(tid(fold, "fold-more")!);
  await until(() => (fold.querySelectorAll('[data-testid="fold-item"]').length === 40 ? true : null), "the second page");
  expect(bodies()[20]).toBe("Item 24");
  // Someone submits while 40 are loaded: it is at the top, New, and the 40 stay.
  client.extra = [{ ...client.base!, id: "fbi_n00", body: "Newest", createdAt: new Date(Date.UTC(2026, 9, 2)).toISOString() }];
  await act(async () => emit({ eventType: EventTypes.PreviewFeedbackSubmitted, occurredAt: new Date().toISOString(), organizationId: ORG.id,
    projectId: PROJECT.id, taskId: null, runId: null, sessionId: null, workflowRunId: null, actor: { type: "person", id: "u_kai" },
    source: "control-plane", correlationId: null, causationId: null, payload: { feedbackId: "fbi_n00", previewRunId: "run_pv_checkout", server: "web", status: "open" } }));
  const fresh = await until(() => tid(fold, "new-item"), "the new item");
  expect(fresh.textContent).toContain("Newest");
  expect(bodies()[0]).toBe("Newest");
  expect(fold.querySelectorAll('[data-testid="fold-item"]').length).toBe(40);
  // Someone resolves Item 24, on the second page: its card, thread and filter move, in place, not New.
  const item = (n: number) => [...fold.querySelectorAll<HTMLElement>('[data-testid="fold-item"], [data-testid="new-item"]')]
    .find((e) => /Item \d+/.exec(e.textContent ?? "")?.[0] === `Item ${n}`) ?? null;
  const listedIn = async (which: string) => { await click(option(fold, which)); const has = item(24) !== null; await click(option(fold, "All")); return has; };
  const at = new Date(Date.UTC(2026, 9, 3)).toISOString();
  const resolved = { status: "resolved" as const, history: [{ status: "resolved" as const, personId: "u_kai", note: "Adjusted spacing", at }] };
  const change = (eventType: string, status: string) => act(async () => event(eventType,
    { payload: { feedbackId: "fbi_m24", previewRunId: "run_pv_checkout", server: "web", status } }));
  client.changed.set("fbi_m24", resolved);
  await change(EventTypes.PreviewFeedbackResolved, "resolved");
  await until(() => (item(24)?.dataset.status === "resolved" ? true : null), "Item 24 resolved");
  expect(item(24)!.dataset.testid).toBe("fold-item");
  expect(item(24)!.textContent).toContain("Adjusted spacing");
  expect(bodies()[21]).toBe("Item 24");
  expect(await listedIn("Open")).toBe(false);
  expect(await listedIn("Resolved")).toBe(true);
  // And reopens it.
  client.changed.set("fbi_m24", { status: "open", history: [...resolved.history, { status: "open", personId: "u_kai", note: "Still tight", at }] });
  await change(EventTypes.PreviewFeedbackReopened, "open");
  await until(() => (item(24)?.dataset.status === "open" ? true : null), "Item 24 reopened");
  expect(item(24)!.textContent).toContain("Still tight");
  expect(await listedIn("Open")).toBe(true);
  expect(await listedIn("Resolved")).toBe(false);
  // Refresh alone reconciles every loaded card, with no event at all.
  client.changed.set("fbi_m24", resolved);
  await click(tid(fold, "fold-refresh")!);
  await until(() => (item(24)?.dataset.status === "resolved" ? true : null), "Item 24 after Refresh");
  expect(fold.querySelectorAll('[data-testid="fold-item"]').length).toBe(40);
  expect(bodies()[0]).toBe("Newest");
  expect(fold.querySelectorAll('[data-testid="new-item"]').length).toBe(1);
  await click(tid(fold, "fold-more")!);
  await until(() => (fold.querySelectorAll('[data-testid="fold-item"]').length === 45 ? true : null), "the last page");
  expect(bodies().at(-1)).toBe("Item 0");
  expect(fold.querySelectorAll('[data-testid="fold-more"]').length).toBe(0);
});

test("a live read that a newer one supersedes stops paging", async () => {
  // Pages of at most 10, so reading the loaded 20 again takes two requests.
  class Paged extends FixtureClient {
    base: PreviewFeedbackItem | null = null;
    reads = 0;
    hold: Promise<void> | null = null;
    override async previewFeedback(runId: string, server: string, params: { status?: "open" | "resolved" | "all"; after?: string; before?: string; order?: "asc" | "desc"; limit?: number } = {}) {
      this.base ??= (await super.previewFeedback("run_pv_checkout", "web", { status: "all" })).feedback[0]!;
      if (params.order === "desc") this.reads++;
      const held = this.hold;
      this.hold = null;
      if (held) await held;
      const all = Array.from({ length: 45 }, (_, i) => ({ ...this.base!, id: `fbi_p${String(i).padStart(2, "0")}`, body: `Item ${i}`,
        createdAt: new Date(Date.UTC(2026, 9, 1, 0, i)).toISOString() }));
      void runId; void server;
      return pagePreviewFeedback(all, { ...params, limit: Math.min(params.limit ?? 50, 10) });
    }
  }
  const client = new Paged("a");
  const { container } = await page(client);
  const fold = await unfold(container, "run_pv_checkout/web");
  expect(fold.querySelectorAll('[data-testid="fold-item"]').length).toBe(20);
  let release!: () => void;
  client.hold = new Promise<void>((r) => { release = r; });
  // The first Refresh waits on its first page; the second supersedes it and reads both pages.
  await click(tid(fold, "fold-refresh")!);
  await click(tid(fold, "fold-refresh")!);
  await settle(100);
  const reads = client.reads;
  await act(async () => release());
  await settle(100);
  expect(client.reads).toBe(reads);
  expect(fold.querySelectorAll('[data-testid="fold-item"]').length).toBe(20);
});

test("a failed read is an error with Try again, never an empty list", async () => {
  class Down extends FixtureClient {
    down = true;
    override async previewFeedback(runId: string, server: string, params?: { status?: "open" | "resolved" | "all"; after?: string; limit?: number }) {
      if (this.down) throw new ApiError(503, "unavailable", "the database is away");
      return super.previewFeedback(runId, server, params);
    }
  }
  const client = new Down("a");
  const { container } = await page(client);
  const fold = await unfold(container, "run_pv_checkout/web");
  expect(tid(fold, "fold-error")!.textContent).toContain("Couldn't read this server's feedback");
  expect(fold.querySelectorAll(".serverFoldEmpty").length).toBe(0);
  client.down = false;
  await click([...tid(fold, "fold-error")!.querySelectorAll("button")].find((b) => b.textContent?.includes("Try again"))!);
  await until(() => (fold.querySelectorAll('[data-testid="fold-item"]').length === 3 ? true : null), "the list");
  expect(fold.querySelectorAll('[data-testid="fold-error"]').length).toBe(0);
});

test("a task's server: its statuses, your own draft first as Draft · only you, and the task's Feedback tab", async () => {
  const { container, opened } = await page();
  const fold = await unfold(container, "run_web142/web");
  expect(fold.dataset.kind).toBe("task");
  const draft = tid(fold, "my-draft")!;
  // Drafts come before every submitted item.
  const order = [...fold.querySelectorAll('[data-testid="my-draft"], [data-testid="fold-item"]')].map((e) => (e as HTMLElement).dataset.testid);
  expect(order[0]).toBe("my-draft");
  expect(draft.textContent).toContain("Placeholder disappears too early");
  expect([...fold.querySelectorAll('[data-testid="fold-filter"] button')].map((b) => b.textContent)).toEqual(["All 4", "Open 2", "Resolved 2"]);
  await click(option(fold, "Open"));
  expect([...fold.querySelectorAll<HTMLElement>('[data-testid="fold-item"]')].map((i) => i.dataset.status).sort()).toEqual(["delivered", "open"]);
  await click(option(fold, "Resolved"));
  expect([...fold.querySelectorAll<HTMLElement>('[data-testid="fold-item"]')].map((i) => i.dataset.status).sort()).toEqual(["replied", "resolved"]);
  const link = tid(fold, "task-feedback-link")!;
  expect(link.textContent).toBe("WEB-142 Feedback tab");
  await click(link);
  expect(opened).toEqual([TASK_ID]);
});

test("someone else's submit arrives without a reload: at the top, marked New, the row's counts moved, Updated just now", async () => {
  localStorage.setItem(SERVERS_KEY, "live");
  const { container } = await page();
  const fold = await unfold(container, "run_pv_checkout/web");
  expect(fold.querySelectorAll('[data-testid="new-item"]').length).toBe(0);
  // The fixture's Kai submits a moment after the fold's first read, and the project stream says so.
  const fresh = await until(() => tid(fold, "new-item"), "Kai's comment", 120);
  expect(fresh.textContent).toContain("Total jumps when the delivery estimate loads");
  expect(fold.querySelector('[data-testid="new-item"], [data-testid="fold-item"]')).toBe(fresh);
  expect(tid(fold, "live-note")!.textContent).toBe("Updated just now");
  await until(() => (tid(row(container, "run_pv_checkout/web"), "server-fold-toggle")!.textContent === "3 open · 1 resolved" ? true : null), "the counts");
  expect(tid(row(container, "run_pv_checkout/web"), "commenting-count")!.textContent).toContain("3 people commenting");
});

test("another server's events leave this fold as it is", async () => {
  const client = new Counting("a");
  const { container } = await page(client);
  const fold = await unfold(container, "run_pv_checkout/web");
  const before = client.preview;
  const timers = foldTimers();
  await act(async () => emit({ eventType: EventTypes.PreviewFeedbackSubmitted, occurredAt: new Date().toISOString(), organizationId: ORG.id,
    projectId: PROJECT.id, taskId: null, runId: null, sessionId: null, workflowRunId: null, actor: { type: "person", id: "u_kai" },
    source: "control-plane", correlationId: null, causationId: null, payload: { feedbackId: "x", previewRunId: "run_pv_checkout", server: "admin", status: "open" } }));
  // A whole debounce window passes on its own: the sibling server's event read nothing.
  await timers.advance();
  expect(client.preview).toBe(before);
  expect(tid(fold, "live-note")).toBeNull();
  // The same fold's matching event, in a window of its own, is the positive control: the listener really is live.
  await act(async () => event(EventTypes.PreviewFeedbackSubmitted, { payload: { feedbackId: "y", previewRunId: "run_pv_checkout", server: "web", status: "open" } }));
  await timers.advance();
  await until(() => tid(fold, "live-note"), "the matching server event");
  expect(client.preview - before).toBe(1);
  expect(tid(fold, "live-note")!.textContent).toBe("Updated just now");
});

/**
 * The folds' debounce timers (FOLD_READ_MS), held by the test until `advance` runs them; every
 * other timer runs as usual. Restored after each test.
 */
function foldTimers() {
  const held = new Map<number, () => void>();
  const [real, realClear] = [globalThis.setTimeout, globalThis.clearTimeout];
  let id = 1_000_000_000;
  globalThis.setTimeout = ((fn: () => void, ms?: number, ...rest: unknown[]) => {
    if (ms !== FOLD_READ_MS) return real(fn, ms, ...rest);
    held.set(++id, fn);
    return id;
  }) as typeof setTimeout;
  globalThis.clearTimeout = ((t?: number) => { if (!held.delete(t as number)) realClear(t); }) as typeof clearTimeout;
  restores.push(() => { globalThis.setTimeout = real; globalThis.clearTimeout = realClear; });
  return {
    /** Past the debounce window: run what is held, then let the reads it starts settle. */
    async advance() {
      const due = [...held.values()];
      held.clear();
      await act(async () => { for (const fn of due) fn(); });
      await settle(60);
    },
  };
}
const restores: Array<() => void> = [];
afterEach(() => { for (const r of restores.splice(0)) r(); });

/** A preview_feedback.* or feedback.* event on the project's stream, as the backend sends it. */
const event = (eventType: string, over: { taskId?: string | null; payload?: Record<string, unknown> } = {}) => emit({ eventType,
  occurredAt: new Date().toISOString(), organizationId: ORG.id, projectId: PROJECT.id, taskId: over.taskId ?? null, runId: null, sessionId: null,
  workflowRunId: null, actor: { type: "person", id: "u_kai" }, source: "control-plane", correlationId: null, causationId: null, payload: over.payload ?? {} });

class Counting extends FixtureClient {
  preview = 0;
  task = 0;
  override async previewFeedback(...a: Parameters<FixtureClient["previewFeedback"]>) { this.preview++; return super.previewFeedback(...a); }
  override async serverFeedback(...a: Parameters<FixtureClient["serverFeedback"]>) { this.task++; return super.serverFeedback(...a); }
}

test("a task fold reacts only to its own task's feedback events", async () => {
  const client = new Counting("a");
  const { container } = await page(client);
  const fold = await unfold(container, "run_web142/web");
  const before = client.task;
  const timers = foldTimers();
  await act(async () => event(EventTypes.FeedbackSubmitted, { taskId: "task_other" }));
  // A whole debounce window passes on its own: another task's event read nothing.
  await timers.advance();
  expect(client.task).toBe(before);
  expect(tid(fold, "live-note")).toBeNull();
  // A matching event, in a window of its own, is the positive control: its read is the only one.
  await act(async () => event(EventTypes.FeedbackSubmitted, { taskId: TASK_ID }));
  await timers.advance();
  await until(() => tid(fold, "live-note"), "the matching task event");
  expect(client.task - before).toBe(1);
  expect(tid(fold, "live-note")!.textContent).toBe("Updated just now");
});

test("a burst of ten events reads each open fold once or twice, not ten times; a stream that comes back reads it once", async () => {
  const client = new Counting("a");
  const { container } = await page(client);
  const branch = await unfold(container, "run_pv_checkout/web");
  await unfold(container, "run_web142/web");
  await settle(350);
  const [p0, t0] = [client.preview, client.task];
  await act(async () => {
    for (let i = 0; i < 10; i++) {
      event(EventTypes.PreviewFeedbackSubmitted, { payload: { feedbackId: `x${i}`, previewRunId: "run_pv_checkout", server: "web", status: "open" } });
      event(EventTypes.FeedbackDelivered, { taskId: TASK_ID });
      await new Promise((r) => setTimeout(r, 20));
    }
  });
  await until(() => tid(branch, "live-note"), "the branch fold's re-read");
  await settle(400);
  expect(client.preview - p0).toBeGreaterThanOrEqual(1);
  expect(client.preview - p0).toBeLessThanOrEqual(2);
  expect(client.task - t0).toBeGreaterThanOrEqual(1);
  expect(client.task - t0).toBeLessThanOrEqual(2);
  // The stream drops and comes back: each fold reads once for what it may have missed.
  const [p1, t1] = [client.preview, client.task];
  await act(async () => { window.dispatchEvent(new Event("offline")); });
  await act(async () => { window.dispatchEvent(new Event("online")); });
  await until(() => (client.preview > p1 && client.task > t1 ? true : null), "the reads after the stream came back");
  await settle(400);
  expect(client.preview - p1).toBe(1);
  expect(client.task - t1).toBe(1);
});
