/**
 * People's feedback in the web app: the client's routes, the drafts tray
 * and its batch submit (one key, the same on a retry), the merge hold, a
 * done, failed or aborted task, an earlier attempt, and a colleague who
 * never sees another person's draft. Mounted against the fixture client,
 * whose feedback answers as the API does.
 */

import { afterEach, describe, expect, test } from "bun:test";
import type { FeedbackDraftInput, FeedbackItem, PersistedEvent, PullRequest, Run, TaskStatus } from "@dude/domain";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import { renderToStaticMarkup } from "react-dom/server";
import { act, click, mount, settle, until } from "./dom.ts";
import { FixtureClient } from "../src/fixtures/client.ts";
import { PEOPLE, TASK_ID, YOU } from "../src/fixtures/data.ts";
import { PeopleProvider } from "../src/people.tsx";
import { ApiClient, ApiError, type FeedbackSubmitted, type TaskDetail } from "../src/api/client.ts";
import { TaskScreen, activityLines } from "../src/screens/TaskScreen.tsx";
import { PullRequestActions } from "../src/screens/PullRequestActions.tsx";
import { Submitter, feedbackDestination, submitRefusal } from "../src/feedback.tsx";
import { placeItems } from "../src/diffComments.tsx";
import type { People } from "../src/people.tsx";

const mounted: Array<() => Promise<void>> = [];
afterEach(async () => {
  for (const u of mounted.splice(0)) await u();
  document.body.innerHTML = "";
});

// ---------------------------------------------------------------------------
// The client's routes
// ---------------------------------------------------------------------------

describe("the feedback client", () => {
  const original = globalThis.fetch;
  afterEach(() => { globalThis.fetch = original; });

  test("each method calls its route, with the body the API validates", async () => {
    const calls: Array<[string, string, unknown]> = [];
    globalThis.fetch = (async (input: string | URL | Request, init?: RequestInit) => {
      calls.push([init?.method ?? "GET", String(input), init?.body ? JSON.parse(String(init.body)) : undefined]);
      return new Response(init?.method === "DELETE" ? "" : "{}", { status: init?.method === "DELETE" ? 204 : 200 });
    }) as typeof fetch;
    const c = new ApiClient({ baseUrl: "https://d.test" });
    const draft: FeedbackDraftInput = { body: "Name the person", anchor: { kind: "code", runId: "run_1", path: "a.ts", side: "new", startLine: 3, endLine: 4, diffChecksum: "c1" } };
    await c.feedback("tsk_1");
    await c.feedback("tsk_1", 2);
    await c.createFeedbackDraft("tsk_1", draft);
    await c.updateFeedbackDraft("fbk_1", { body: "x", anchor: { kind: "general" } });
    await c.deleteFeedbackDraft("fbk_1");
    await c.submitFeedback("tsk_1", ["fbk_1", "fbk_2"], "key-1");
    await c.replyFeedback("fbk_1", "Kept");
    await c.resolveFeedback("fbk_1");
    await c.reopenFeedback("fbk_1");
    expect(calls).toEqual([
      ["GET", "https://d.test/v1/tasks/tsk_1/feedback", undefined],
      ["GET", "https://d.test/v1/tasks/tsk_1/feedback?attempt=2", undefined],
      ["POST", "https://d.test/v1/tasks/tsk_1/feedback/drafts", draft],
      ["PATCH", "https://d.test/v1/feedback/fbk_1", { body: "x", anchor: { kind: "general" } }],
      ["DELETE", "https://d.test/v1/feedback/fbk_1", undefined],
      ["POST", "https://d.test/v1/tasks/tsk_1/feedback/submit", { draftIds: ["fbk_1", "fbk_2"], submissionKey: "key-1" }],
      ["POST", "https://d.test/v1/feedback/fbk_1/reply", { body: "Kept" }],
      ["POST", "https://d.test/v1/feedback/fbk_1/resolve", {}],
      ["POST", "https://d.test/v1/feedback/fbk_1/reopen", {}],
    ]);
  });
});

describe("Submitter", () => {
  test("a failed submit is sent again with the same key; other drafts get a new one", async () => {
    const sent: Array<[string[], string]> = [];
    let fail = true;
    let keys = 0;
    const s = new Submitter({
      submitFeedback: async (_t, ids, key) => {
        sent.push([[...ids], key]);
        if (fail) throw new ApiError(502, "error", "the orchestrator did not answer");
        return { batchId: "b", itemIds: [...ids], routedTo: "workflow" } satisfies FeedbackSubmitted;
      },
    }, () => `k${++keys}`);
    await expect(s.submit("t", ["a", "b"])).rejects.toThrow("did not answer");
    fail = false;
    // The same drafts, in any order: the same batch.
    await s.submit("t", ["b", "a"]);
    await s.submit("t", ["c"]);
    expect(sent).toEqual([[["a", "b"], "k1"], [["b", "a"], "k1"], [["c"], "k2"]]);
  });
});

// ---------------------------------------------------------------------------
// The page
// ---------------------------------------------------------------------------

const ANA = PEOPLE.find((p) => p.id === "u_ana")!;
const ME = PEOPLE.find((p) => p.id === YOU)!;

function item(over: Partial<FeedbackItem>): FeedbackItem {
  const now = new Date(Date.now() - 60_000).toISOString();
  return { id: "fbk_x", taskId: TASK_ID, attempt: 1, batchId: "fb_b", author: { id: ANA.id, name: ANA.name }, body: "A word", anchor: { kind: "general" },
    status: "open", deliveredRunId: null, resolvedCommit: null, resolutionPending: false, outdated: false, routedTo: "workflow",
    submittedAt: now, createdAt: now, updatedAt: now, history: [], ...over };
}

/** The fixture client with feedback's writes recorded, the submit's answers scripted, and the task's status set. */
class FeedbackClient extends FixtureClient {
  submits: Array<{ ids: string[]; key: string }> = [];
  failSubmits = 0;
  resolved: string[] = [];
  replied: Array<[string, string]> = [];
  status: TaskStatus | null = null;
  runs: Run[] | null = null;
  prs: PullRequest[] = [];
  constructor() {
    super("d");
  }
  override async getTask(id: string): Promise<TaskDetail> {
    const t = await super.getTask(id);
    return { ...t, ...(this.status ? { status: this.status } : {}), ...(this.runs ? { runs: this.runs } : {}) };
  }
  override listPullRequests() {
    return Promise.resolve({ pullRequests: this.prs });
  }
  override async submitFeedback(_taskId: string, draftIds: ReadonlyArray<string>, submissionKey: string): Promise<FeedbackSubmitted> {
    this.submits.push({ ids: [...draftIds], key: submissionKey });
    if (this.failSubmits-- > 0) throw new ApiError(502, "error", "The orchestrator did not answer.");
    for (const i of this.feedbackItems) {
      if (draftIds.includes(i.id)) Object.assign(i, { status: "open", submittedAt: new Date().toISOString(), batchId: "fb_b1", routedTo: "workflow" });
    }
    return { batchId: "fb_b1", itemIds: [...draftIds], routedTo: "workflow" };
  }
  override async resolveFeedback(id: string) {
    this.resolved.push(id);
    const i = this.feedbackItems.find((x) => x.id === id)!;
    Object.assign(i, { status: "resolved", history: [...i.history, { id: "h1", kind: "resolve", body: "", commit: null, personId: YOU, runId: null, createdAt: new Date().toISOString() }] });
    return { itemId: id, action: "resolve" as const };
  }
  override async replyFeedback(id: string, body: string) {
    this.replied.push([id, body]);
    return { itemId: id, action: "reply" as const };
  }
  override async deleteFeedbackDraft(id: string) {
    this.feedbackItems = this.feedbackItems.filter((i) => i.id !== id);
  }
}

async function taskPage(client: FixtureClient, tab?: "feedback" | "activity") {
  const { container, unmount } = await mount(
    <ToastProvider><TooltipProvider><PeopleProvider client={client}>
      <TaskScreen client={client} taskId={TASK_ID} onOpenRun={() => {}} onBack={() => {}} tab={tab} />
    </PeopleProvider></TooltipProvider></ToastProvider>,
  );
  mounted.push(unmount);
  await until(() => container.querySelector("[data-testid=task-screen]"), "the task page");
  await settle(80);
  return container;
}

const typeInto = async (el: HTMLTextAreaElement, text: string) => act(async () => {
  Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(el, text);
  el.dispatchEvent(new Event("input", { bubbles: true }));
});
const button = (scope: ParentNode, name: string) =>
  [...scope.querySelectorAll<HTMLButtonElement>("button")].find((b) => (b.getAttribute("aria-label") ?? b.textContent ?? "").trim() === name);

describe("general feedback, the tray and the submit", () => {
  test("a draft written on the tab shows in the tray; Submit sends exactly the drafts with one key, and a retry the same key", async () => {
    const client = new FeedbackClient();
    // Someone else's draft is the API's to keep from the reader: listed to its author alone.
    client.feedbackItems.push(item({ id: "fbk_ana_draft", status: "draft", author: { id: ANA.id, name: ANA.name }, body: "Ana's private words", submittedAt: null }));
    client.failSubmits = 1;
    const page = await taskPage(client, "feedback");
    expect(page.textContent).not.toContain("Ana's private words");
    expect(page.querySelectorAll("[data-testid=fb-tray]").length).toBe(0);

    await click(await until(() => page.querySelector("[data-testid=general-open]"), "the general line"));
    const field = await until(() => page.querySelector<HTMLTextAreaElement>("[data-testid=fb-editor] textarea"), "the editor");
    await typeInto(field, "Add a test for the 2-minute cap");
    await act(async () => void field.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", ctrlKey: true, bubbles: true })));
    const tray = await until(() => page.querySelector<HTMLElement>("[data-testid=fb-tray]"), "the tray");
    expect(tray.getAttribute("data-count")).toBe("1");
    expect(tray.textContent).toContain("1 draft · only you see them until you submit");
    expect(page.querySelector("[data-testid=feedback-drafts]")!.textContent).toContain("Add a test for the 2-minute cap");
    const mine = client.feedbackItems.filter((i) => i.author?.id === YOU).map((i) => i.id);

    await click(button(tray, "Submit 1…")!);
    const confirm = await until(() => document.querySelector<HTMLButtonElement>("[data-testid=fb-confirm]"), "the confirmation");
    expect(document.querySelector("[data-testid=fb-confirm-body]")!.textContent).toContain("1 general — as one batch.");
    await click(confirm);
    await until(() => document.querySelector("[data-testid=fb-submit-problem]"), "the failure");
    expect(document.querySelector("[data-testid=fb-submit-problem]")!.textContent).toBe("The orchestrator did not answer.");
    await click(document.querySelector("[data-testid=fb-confirm]")!);
    await until(() => (page.querySelectorAll("[data-testid=fb-tray]").length === 0 ? true : null), "the tray to go");
    expect(client.submits.length).toBe(2);
    expect(client.submits[0]!.ids).toEqual(mine);
    expect(client.submits[1]).toEqual(client.submits[0]!);
    expect(client.submits[0]!.key.length).toBeGreaterThan(8);
    // Submitted: on the tab as an item that needs handling, and Ana's draft never.
    const needs = await until(() => page.querySelector("[data-testid=feedback-needs]"), "needs handling");
    expect(needs.textContent).toContain("Add a test for the 2-minute cap");
    expect(page.textContent).not.toContain("Ana's private words");
  });

  test("the tray lists the drafts, deletes one, and is gone with none", async () => {
    const client = new FeedbackClient();
    client.feedbackItems.push(item({ id: "d1", status: "draft", author: { id: ME.id, name: ME.name }, body: "First thought", submittedAt: null, batchId: null }),
      item({ id: "d2", status: "draft", author: { id: ME.id, name: ME.name }, body: "Second thought", submittedAt: null, batchId: null }));
    const page = await taskPage(client);
    const tray = await until(() => page.querySelector<HTMLElement>("[data-testid=fb-tray]"), "the tray");
    expect(tray.getAttribute("data-count")).toBe("2");
    await click(button(tray, "Review")!);
    const rows = [...page.querySelectorAll<HTMLElement>("[data-testid=fb-tray-row]")];
    expect(rows.map((r) => r.getAttribute("data-id"))).toEqual(["d1", "d2"]);
    await click(button(rows[0]!, "Delete")!);
    await until(() => (page.querySelector("[data-testid=fb-tray]")?.getAttribute("data-count") === "1" ? true : null), "one left");
    await click(button(page.querySelector("[data-testid=fb-tray-row]")!, "Delete")!);
    await until(() => (page.querySelectorAll("[data-testid=fb-tray]").length === 0 ? true : null), "no tray");
  });

  test("Resolve on a card acts on that item and the tab moves it", async () => {
    const client = new FeedbackClient();
    client.feedbackItems.push(item({ id: "o1", body: "Rename it" }));
    const page = await taskPage(client, "feedback");
    const needs = await until(() => page.querySelector<HTMLElement>("[data-testid=feedback-needs] [data-testid=fb-item]"), "the item");
    await click(button(needs, "Resolve")!);
    expect(client.resolved).toEqual(["o1"]);
    await until(() => (page.querySelectorAll("[data-testid=feedback-needs] [data-testid=fb-item]").length === 0 ? true : null), "it to leave Needs handling");
    await click(page.querySelector("[data-testid=feedback-show-settled]")!);
    const settled = await until(() => page.querySelector("[data-testid=feedback-settled]"), "resolved or answered");
    expect(settled.textContent).toContain("Rename it");
    expect(settled.querySelector("[data-testid=fb-entry]")!.textContent).toContain("Márcio resolved it");
  });
});

describe("the merge hold", () => {
  test("the header's chip counts what the API says holds the merge, and opens the Feedback tab", async () => {
    const client = new FeedbackClient();
    client.feedbackItems.push(item({ id: "a" }), item({ id: "b", status: "delivered" }), item({ id: "c", status: "resolved", resolutionPending: true }),
      item({ id: "d", status: "replied" }), item({ id: "e", status: "resolved" }));
    const page = await taskPage(client);
    const chip = await until(() => page.querySelector<HTMLElement>("[data-testid=merge-hold]"), "the chip");
    expect(chip.textContent).toBe(" 3 feedback items hold the merge");
    const tab = [...page.querySelectorAll<HTMLElement>("[role=tab]")].find((t) => t.textContent?.startsWith("Feedback"))!;
    expect(tab.textContent).toBe("Feedback3");
    await click(chip);
    await until(() => page.querySelector("[data-testid=feedback-tab]"), "the Feedback tab");
  });

  test("no chip when nothing holds it", async () => {
    const client = new FeedbackClient();
    client.feedbackItems.push(item({ id: "d", status: "replied" }));
    const page = await taskPage(client);
    await settle(80);
    expect(page.querySelectorAll("[data-testid=merge-hold]").length).toBe(0);
  });

  test("Merge is off with the count while feedback holds it, and on once nothing does", () => {
    const pr = { id: "pr_1", taskId: "t", runId: null, repositoryId: "r", repositoryName: "web", number: 41, url: "https://github.com/a/web/pull/41",
      headBranch: "h", baseBranch: "main", title: "T", state: "open", checks: [], checkState: "passing", review: "approved", reviews: [], mergeable: "clean",
      behindBy: 0, unresolvedThreads: 0, display: "ready", createdAt: "", updatedAt: "" } as unknown as PullRequest;
    const render = (n: number) => renderToStaticMarkup(
      <TooltipProvider><PullRequestActions client={{} as ApiClient} pr={pr} defaultMethod="squash" onChanged={() => {}} feedbackHolds={n}>
        {({ merge, note }) => <div>{merge}{note}</div>}
      </PullRequestActions></TooltipProvider>);
    const held = render(2);
    expect(held).toMatch(/<button[^>]*disabled=""[^>]*data-testid="pr-merge"/);
    expect(held).toContain("2 feedback items hold the merge.");
    const free = render(0);
    expect(free).not.toMatch(/<button[^>]*disabled=""[^>]*data-testid="pr-merge"/);
    expect(free).not.toContain("hold the merge");
  });
});

describe("a task that takes no feedback", () => {
  test("done: no way to write, and the tray's Submit is off with the backend's reason", async () => {
    const client = new FeedbackClient();
    client.status = "done";
    client.feedbackItems.push(item({ id: "d1", status: "draft", author: { id: ME.id, name: ME.name }, submittedAt: null, batchId: null }), item({ id: "o1" }));
    const page = await taskPage(client, "feedback");
    const done = await until(() => page.querySelector("[data-testid=feedback-done]"), "the done notice");
    expect(done.textContent).toContain("This task is done.");
    expect(page.querySelectorAll("[data-testid=general-open]").length).toBe(0);
    // Submitted items are only to read.
    expect(page.querySelectorAll("[data-testid=feedback-needs] footer").length).toBe(0);
    expect(page.querySelector<HTMLButtonElement>("[data-testid=fb-submit-refused] button")!.disabled).toBe(true);
    expect(submitRefusal("done")).toBe("This task is done: feedback on it goes in a follow-up task. Your drafts are kept.");
  });

  test("failed or aborted: recover first; drafts can still be written", async () => {
    for (const status of ["failed", "aborted"] as const) {
      const client = new FeedbackClient();
      client.status = status;
      const page = await taskPage(client, "feedback");
      const note = await until(() => page.querySelector("[data-testid=feedback-recover]"), "the recover notice");
      expect(note.textContent).toContain("Recover it first, then submit");
      expect(page.querySelectorAll("[data-testid=general-open]").length).toBe(1);
      expect(submitRefusal(status)).toBe(`This task is ${status}: recover it first, then submit. Your drafts are kept.`);
      for (const u of mounted.splice(0)) await u();
    }
  });
});

describe("an earlier attempt", () => {
  test("its feedback is read for it, and only to read", async () => {
    const client = new FeedbackClient();
    const base = await client.getTask(TASK_ID);
    const first = base.runs[0]!;
    client.runs = [...base.runs, { ...first, id: "run_second", attempt: 2, createdAt: new Date().toISOString() }];
    client.feedbackItems.push(item({ id: "old", attempt: 1, body: "On the first attempt" }), item({ id: "new", attempt: 2, body: "On the second" }));
    const { container, unmount } = await mount(
      <ToastProvider><TooltipProvider><PeopleProvider client={client}>
        <TaskScreen client={client} taskId={TASK_ID} onOpenRun={() => {}} onBack={() => {}} tab="feedback" attempt={1} />
      </PeopleProvider></TooltipProvider></ToastProvider>);
    mounted.push(unmount);
    const earlier = await until(() => container.querySelector("[data-testid=feedback-earlier]"), "the earlier note");
    expect(earlier.textContent).toContain("Attempt 1 was set aside");
    const tab = container.querySelector("[data-testid=feedback-tab]")!;
    expect(tab.textContent).toContain("On the first attempt");
    expect(tab.textContent).not.toContain("On the second");
    expect(tab.querySelectorAll("footer").length).toBe(0);
    expect(tab.querySelectorAll("[data-testid=general-open]").length).toBe(0);
    // The current attempt's hold is not this attempt's.
    expect(container.querySelectorAll("[data-testid=merge-hold]").length).toBe(0);
  });
});

describe("where a batch goes", () => {
  const run = (over: Partial<Run>): Run => ({ id: "r", attempt: 1, status: "running", phase: "fix", kind: "agent", createdAt: "2026-10-01T00:00:00Z", ...over } as Run);
  test("the conductor when it decides; else the implementer or fixer at work; else the delivery", () => {
    expect(feedbackDestination({ decider: "conductor", status: "running", runs: [] }, 1, 0).who).toBe("the conductor");
    expect(feedbackDestination({ decider: "policy", status: "running", runs: [run({})] }, 1, 0).who).toBe("the fixer at work");
    expect(feedbackDestination({ decider: "policy", status: "running", runs: [run({ phase: "implement" })] }, 1, 0).who).toBe("the implementer at work");
    // A reviewer is never steered, nor an earlier attempt's agent.
    expect(feedbackDestination({ decider: "policy", status: "running", runs: [run({ phase: "review" })] }, 1, 0).who).toBe("Deliver's next fix");
    expect(feedbackDestination({ decider: "policy", status: "running", runs: [run({ attempt: 1 })] }, 2, 0).who).toBe("Deliver's next fix");
    expect(feedbackDestination({ decider: "policy", status: "queued", runs: [] }, 1, 0).who).toBe("a new delivery");
    expect(feedbackDestination({ decider: "policy", status: "queued", runs: [] }, 1, 2).line).toBe("They wait for the task's blockers.");
  });
});

describe("Activity", () => {
  const people = { you: YOU, me: null, all: PEOPLE, byId: new Map(PEOPLE.map((p) => [p.id, p])), names: new Map(PEOPLE.map((p) => [p.id, p.name])),
    refresh: async () => people, seen: () => false } as unknown as People;
  const ev = (eventType: string, payload: Record<string, unknown>, actor: PersistedEvent["actor"], runId: string | null = null): PersistedEvent => ({
    eventId: `${eventType}${JSON.stringify(payload)}`, cursor: 1, eventType, occurredAt: new Date().toISOString(), organizationId: "o", projectId: "p",
    taskId: TASK_ID, runId, sessionId: null, actor, source: "orchestrator", correlationId: null, causationId: null, payload, schemaVersion: 1,
  } as unknown as PersistedEvent);
  test("says who did what to how many items, never their words", () => {
    const fix = { id: "run_fix", attempt: 1, phase: "fix", role: "implementer", kind: "agent", status: "running", createdAt: "" } as unknown as Run;
    const lines = activityLines([
      ev("feedback.submitted", { itemIds: ["a", "b"], routedTo: "steer" }, { type: "person", id: ANA.id }),
      ev("feedback.delivered", { itemIds: ["a", "b"] }, { type: "system", id: "dude" }, "run_fix"),
      ev("feedback.replied", { itemId: "a", by: "agent" }, { type: "agent", id: "run_fix" }, "run_fix"),
      ev("feedback.resolved", { itemId: "b", by: "agent", commit: "9c1e4a7d0e", pending: true }, { type: "agent", id: "run_fix" }, "run_fix"),
      ev("feedback.reopened", { itemId: "a", routedTo: "workflow" }, { type: "person", id: YOU }),
    ], people, [fix], [], 1, "WC-1");
    const words = lines.filter((l) => l.kind === "feedback").map((l) => renderToStaticMarkup(<>{l.text}</>).replace(/<[^>]+>/g, ""));
    expect(words).toEqual([
      "Márcio Martins reopened 1 feedback item · to Deliver",
      "The fixer resolved 1 feedback item in 9c1e4a7 · counts once that commit is published",
      "The fixer answered 1 feedback item · it no longer holds the merge",
      "2 feedback items were given to the fixer",
      "Ana Ribeiro submitted 2 feedback items · to the agent at work",
    ]);
  });
});

describe("placing items on a diff", () => {
  test("on its lines, shifted, or at its file's head; a file not in the diff draws nothing", () => {
    const anchor = { kind: "code" as const, runId: "run_a", repo: "web", path: "a.ts", side: "new" as const, startLine: 2, endLine: 2, baseSha: "b",
      headSha: null, diffChecksum: "c1", excerpt: [{ kind: "+" as const, old: null, new: 2, text: "x()" }] };
    const file = (lines: Array<{ kind: " " | "+" | "-"; old: number | null; new: number | null; text: string }>) =>
      ({ path: "a.ts", status: "M" as const, additions: 1, deletions: 0, hunks: [{ header: "@@", lines }] });
    const items = [item({ id: "i", anchor })];
    expect(placeItems(items, "run_a", { checksum: "c1", files: [file([{ kind: "+", old: null, new: 2, text: "x()" }])] }).map((p) => p.at))
      .toEqual([{ path: "a.ts", side: "new", endLine: 2 }]);
    expect(placeItems(items, "run_b", { checksum: "c2", files: [file([{ kind: "+", old: null, new: 7, text: "x()" }])] }).map((p) => p.at))
      .toEqual([{ path: "a.ts", side: "new", endLine: 7 }]);
    expect(placeItems(items, "run_b", { checksum: "c2", files: [file([{ kind: "+", old: null, new: 2, text: "y()" }])] }).map((p) => p.at))
      .toEqual([{ path: "a.ts", head: "outdated" }]);
    expect(placeItems(items, "run_b", { checksum: "c2", files: [file([{ kind: "+", old: null, new: 90, text: "z()" }])] }).map((p) => p.at))
      .toEqual([{ path: "a.ts", head: "unchanged" }]);
    expect(placeItems(items, "run_b", { checksum: "c2", files: [] })).toEqual([]);
  });
});

describe("commenting in a Run's Changes", () => {
  test("a draft edited from another Run's Changes keeps its own Run's lines and checksum", async () => {
    const { useTaskFeedbackPage } = await import("../src/screens/FeedbackSection.tsx");
    const people = { you: YOU, me: null, all: PEOPLE, byId: new Map(PEOPLE.map((p) => [p.id, p])), names: new Map(PEOPLE.map((p) => [p.id, p.name])),
      refresh: async () => people, seen: () => false } as unknown as People;
    const diffWith = (checksum: string, at: number) => ({ base: "b", checksum, final: false, updatedAt: "", files: [{ path: "src/app.ts", status: "M" as const,
      additions: 1, deletions: 0, hunks: [{ header: "@@", lines: [{ kind: "+" as const, old: null, new: at, text: "x()" }] }] }] });
    // run_impl's diff has x() at line 2 (as written, checksum impl_c1); run_fix's has it at line 7.
    const implDiff = diffWith("impl_c1", 2);
    const fixDiff = diffWith("fix_c9", 7);
    const patched: unknown[] = [];
    class Recording extends FeedbackClient {
      override async updateFeedbackDraft(_id: string, input: FeedbackDraftInput) {
        patched.push(input.anchor);
        return this.feedbackItems[0]!;
      }
    }
    const client = new Recording();
    // The fixtures have no diffs: run_impl's is read from here.
    (client as unknown as { runDiff: (runId: string) => Promise<unknown> }).runDiff = async (runId) => {
      if (runId !== "run_impl") throw new ApiError(404, "not_found", "no diff");
      return implDiff;
    };
    const draft = item({ id: "d_impl", status: "draft", author: { id: ME.id, name: ME.name }, submittedAt: null, batchId: null, anchor: {
      kind: "code", runId: "run_impl", repo: "web", path: "src/app.ts", side: "new", startLine: 2, endLine: 2, baseSha: "b", headSha: null,
      diffChecksum: "impl_c1", excerpt: [{ kind: "+", old: null, new: 2, text: "x()" }] } });
    client.feedbackItems.push(draft);
    let fb: ReturnType<typeof useTaskFeedbackPage> | null = null;
    function Probe() {
      fb = useTaskFeedbackPage({ client, taskId: TASK_ID, task: null, current: 1, version: 0, people, onShowInDiff: () => {}, onShowOnTab: () => {} });
      return null;
    }
    const { unmount } = await mount(<Probe />);
    mounted.push(unmount);
    await act(async () => { await fb!.onDiff.saveDraft(draft, "Edited", { runId: "run_fix", diff: fixDiff }); });
    expect(patched).toEqual([{ kind: "code", runId: "run_impl", path: "src/app.ts", side: "new", startLine: 2, endLine: 2, diffChecksum: "impl_c1" }]);
  });

  test("a range picked in the gutter becomes a draft on those lines with the diff's checksum; items are drawn shifted or at the file's head", async () => {
    const { ChangesPanel } = await import("../src/screens/ChangesPanel.tsx");
    const { FeedbackProvider } = await import("../src/feedback.tsx");
    const diff = { base: "4be19c2", checksum: "c2", final: false, updatedAt: new Date().toISOString(), files: [{
      path: "src/app.ts", status: "M" as const, additions: 3, deletions: 0, hunks: [{ header: "@@ -1,2 +1,5 @@", lines: [
        { kind: "+" as const, old: null, new: 1, text: "// header" },
        { kind: " " as const, old: 1, new: 2, text: "const a = 1;" },
        { kind: "+" as const, old: null, new: 3, text: "const b = 3;" },
        { kind: "+" as const, old: null, new: 4, text: "const c = 4;" },
        { kind: " " as const, old: 2, new: 5, text: "export { a };" },
      ] }] }] };
    const code = (id: string, start: number, text: string) => item({ id, body: `on ${id}`, anchor: { kind: "code", runId: "run_impl", repo: "web", path: "src/app.ts",
      side: "new", startLine: start, endLine: start, baseSha: "b", headSha: null, diffChecksum: "c1", excerpt: [{ kind: "+", old: null, new: start, text }] } });
    // Written on an older diff: "const b = 3;" was line 2 and is now 3 (a shift); "const z" is gone (outdated).
    const items = [code("shifted", 2, "const b = 3;"), code("gone", 3, "const z = 9;")];
    const created: Array<[unknown, string]> = [];
    const client = { runDiff: async () => diff } as unknown as ApiClient;
    const people = { you: YOU, me: null, all: PEOPLE, byId: new Map(PEOPLE.map((p) => [p.id, p])), names: new Map(PEOPLE.map((p) => [p.id, p.name])),
      refresh: async () => people, seen: () => false } as unknown as People;
    const run = { id: "run_impl", attempt: 1, phase: "implement", kind: "agent", status: "running", role: "implementer", createdAt: "" } as unknown as Run;
    const ok = async () => true;
    const fb = { taskId: TASK_ID, items, current: 1, closed: null, readOnly: false, full: false, runs: new Map([[run.id, run]]), people, focus: null,
      createDraft: async (anchor: unknown, body: string) => { created.push([anchor, body]); }, saveDraft: ok, deleteDraft: ok, reply: ok, resolve: ok, reopen: ok, openOnTab: () => {} };
    const { container, unmount } = await mount(
      <TooltipProvider><FeedbackProvider value={fb}>
        <div style={{ height: 600 }}><ChangesPanel client={client} runId="run_impl" role="implementer" events={[]} checksum="c2" live={false}
          selected={null} onSelectedChange={() => {}} toolbarIn={null} /></div>
      </FeedbackProvider></TooltipProvider>);
    mounted.push(unmount);
    await until(() => container.querySelector("[data-testid=diffs]"), "the diff");
    // Shifted: under its new line, 3.
    const after3 = container.querySelector('[data-testid=after-line][data-side=new][data-line="3"]')!;
    expect(after3.querySelector("[data-fb-item=shifted]")).not.toBeNull();
    // Gone: at the file's head, with its original line and why.
    const head = container.querySelector("[data-testid=file-head]")!;
    expect(head.querySelector("[data-fb-item=gone]")!.textContent).toContain("The lines it was on have changed");
    expect(head.textContent).toContain("const z = 9;");
    expect(container.querySelector('[data-testid=diff-file] [aria-label="2 comments"], [aria-label="2 comments"]')).not.toBeNull();

    const plus = (n: number) => button(container, `Comment on line ${n}`)!;
    await act(async () => void plus(3).dispatchEvent(new MouseEvent("mousedown", { bubbles: true, cancelable: true, button: 0 })));
    await act(async () => void container.querySelector('[data-cside=new][data-cline="4"]')!.dispatchEvent(new MouseEvent("mouseover", { bubbles: true })));
    await act(async () => void window.dispatchEvent(new MouseEvent("mouseup", { bubbles: true })));
    const editor = await until(() => container.querySelector<HTMLTextAreaElement>("[data-testid=fb-editor] textarea"), "the editor");
    expect(container.querySelector("[data-testid=fb-editor]")!.textContent).toContain("Comment on lines 3–4");
    await typeInto(editor, "Name these");
    await act(async () => void editor.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", metaKey: true, bubbles: true })));
    await settle(30);
    expect(created).toEqual([[{ kind: "code", runId: "run_impl", path: "src/app.ts", side: "new", startLine: 3, endLine: 4, diffChecksum: "c2" }, "Name these"]]);
    expect(container.querySelectorAll("[data-testid=fb-editor]").length).toBe(0);
  });
});
