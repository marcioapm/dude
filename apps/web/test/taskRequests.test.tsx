/**
 * What a task page reads when its stream moves: counted on the fixture
 * client. A session's checkout moving (`run.diff.updated`, many a minute
 * while agents work) re-reads only Pending work, and only that session; a
 * re-read of the page reads the branch's metadata, never its files again.
 */

import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import { act, click, mount, settle, until } from "./dom.ts";
import { FixtureClient, emit } from "../src/fixtures/client.ts";
import { BRANCH_KEY, branchAnchor } from "../src/fixtures/branchDiff.ts";
import { ORG, PROJECT, RUN_ID, TASK_ID } from "../src/fixtures/data.ts";
import type { TaskBranchDiffQuery } from "../src/api/client.ts";
import { PeopleProvider } from "../src/people.tsx";
import { TaskScreen } from "../src/screens/TaskScreen.tsx";
import { RUN_DIFF_EVERY_MS } from "../src/screens/ChangesTab.tsx";

let mounted: Array<() => Promise<void>> = [];
beforeEach(() => localStorage.clear());
afterEach(async () => {
  for (const unmount of mounted) await unmount();
  mounted = [];
  localStorage.clear();
});

interface Counted {
  client: FixtureClient;
  branch: TaskBranchDiffQuery[];
  metrics: Array<number | undefined>;
  runDiffs: string[];
  reset: () => void;
}

/** The fixture client with its branch, metrics and checkout reads counted. */
function counted(state: string, live: readonly string[] = []): Counted {
  localStorage.setItem(BRANCH_KEY, state);
  const client = new FixtureClient("a");
  const out: Counted = { client, branch: [], metrics: [], runDiffs: [], reset: () => {
    out.branch.length = 0; out.metrics.length = 0; out.runDiffs.length = 0;
  } };
  const branch = client.taskBranchDiff.bind(client);
  client.taskBranchDiff = (id, o = {}) => { out.branch.push(o); return branch(id, o); };
  const metrics = client.taskMetrics.bind(client);
  client.taskMetrics = (id, attempt) => { out.metrics.push(attempt); return metrics(id, attempt); };
  const runDiff = client.runDiff.bind(client);
  client.runDiff = (id) => { out.runDiffs.push(id); return runDiff(id === RUN_ID ? id : RUN_ID); };
  if (live.length) {
    const task = client.getTask.bind(client);
    client.getTask = async (id) => {
      const t = await task(id);
      const first = t.runs[0]!;
      return { ...t, runs: [...t.runs, ...live.filter((r) => r !== first.id).map((r, i) => ({ ...first, id: r, createdAt: new Date(Date.parse(first.createdAt) - (i + 1) * 1000).toISOString() }))] };
    };
  }
  return out;
}

async function page(c: Counted, tab?: "changes") {
  const { container, unmount } = await mount(
    <TooltipProvider><ToastProvider><PeopleProvider client={c.client}>
      <TaskScreen client={c.client} taskId={TASK_ID} onOpenRun={() => {}} onBack={() => {}} {...(tab ? { tab } : {})} />
    </PeopleProvider></ToastProvider></TooltipProvider>,
  );
  mounted.push(unmount);
  await until(() => container.querySelector(tab ? "[data-testid=changes-tab]" : "[data-testid=overview-snapshot]"), "the page");
  await settle(200);
  return container;
}

/** One event on the task's stream, from `runId`. */
async function event(eventType: string, payload: Record<string, unknown> = {}, runId: string | null = RUN_ID) {
  await act(async () => {
    emit({ eventType, occurredAt: new Date().toISOString(), organizationId: ORG.id, projectId: PROJECT.id, taskId: TASK_ID, runId,
      sessionId: null, workflowRunId: null, actor: { type: "system", id: "dude" }, source: "control-plane", correlationId: null, causationId: null, payload });
  });
}

const withFiles = (asked: TaskBranchDiffQuery[]) => asked.filter((o) => o.files !== false);

describe("what the task page reads on its stream", () => {
  test("Overview: a session's checkout moving reads no branch files and no metrics", async () => {
    const c = counted("updated");
    const view = await page(c);
    // The first read: metadata only, the newest's too; one metrics read with one attempt.
    expect(c.branch.length).toBeGreaterThan(0);
    expect(withFiles(c.branch)).toEqual([]);
    expect(c.metrics).toEqual([undefined]);
    c.reset();
    for (let i = 0; i < 5; i++) await event("run.diff.updated", { checksum: `c${i}` });
    await settle(RUN_DIFF_EVERY_MS + 200);
    expect(withFiles(c.branch)).toEqual([]);
    expect(c.branch).toEqual([]);
    expect(c.metrics).toEqual([]);
    expect(view.querySelector("[data-testid=overview-snapshot]")).not.toBeNull();
  });

  test("Changes: a task event re-reads the snapshot shown as metadata, never its files", async () => {
    const c = counted("populated");
    const view = await page(c, "changes");
    expect(withFiles(c.branch).length).toBe(1);
    const shown = view.querySelector<HTMLElement>("[data-testid=changes-tab]")!.dataset["snapshot"];
    c.reset();
    await event("task.updated", {});
    await settle(500);
    expect(c.branch).toEqual([{ attempt: 1, snapshot: shown, files: false }]);
    // The diff and its comments are still drawn from the files read first.
    expect(view.querySelectorAll("[data-testid=diff-section]").length).toBeGreaterThan(0);
    expect(view.querySelector("[data-testid=comment-mark]")).not.toBeNull();
    // A comment saved now shows under its line, placed here, with no read of the files.
    c.reset();
    const section = view.querySelector(`[data-testid=diff-section][data-path="src/checkout/PaymentStep.tsx"]`)!;
    await click(section.querySelector("[data-cside=new][data-cline=\"2\"] [data-testid=gutter-add]")!);
    const editor = await until(() => view.querySelector("textarea"), "the editor");
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(editor, "Inline without a full read.");
      editor.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await click([...view.querySelectorAll("button")].find((b) => b.textContent === "Save draft" || b.textContent === "Save")!);
    await until(() => [...view.querySelectorAll("[data-fb-item]")].find((el) => el.textContent?.includes("Inline without a full read.")), "the new comment inline");
    await settle(500);
    expect(withFiles(c.branch)).toEqual([]);
  });

  test("Changes: a re-read that moves only metadata redraws no hunk; a comment on one file redraws none of another's", async () => {
    const c = counted("populated");
    const view = await page(c, "changes");
    const main = "src/checkout/PaymentStep.tsx";
    const other = "src/checkout/PaymentStep.test.tsx";
    expect(view.querySelector(`[data-testid=diff-section][data-path="${other}"] [data-fb-item]`) !== null).toBe(true);
    // Each hunk drawn asks what goes under each of its lines (a lookup by "path, side, line"): counted per file.
    const drawn = new Map<string, number>();
    const get = Map.prototype.get;
    Map.prototype.get = function (this: Map<unknown, unknown>, key: unknown) {
      if (typeof key === "string") {
        const m = /^(.+)\u0000(old|new)\u0000\d+$/.exec(key);
        // Only the lookups a hunk makes as it draws (LiveDiff asking under its lines), not the page building its index.
        if (m && new Error().stack?.includes("LiveDiff.tsx")) drawn.set(m[1]!, (drawn.get(m[1]!) ?? 0) + 1);
      }
      return get.call(this, key);
    };
    try {
      // The PR's sync time moves on every read (as a poll moves it): the read is new, its snapshot and placements are not.
      await event("task.updated", {});
      await settle(500);
      expect(c.branch.filter((o) => o.files === false).length).toBeGreaterThan(0);
      expect([...drawn.entries()]).toEqual([]);
      // A comment written on one file (elsewhere) redraws that file's hunks only.
      await c.client.createFeedbackDraft(TASK_ID, { body: "On the test file only.", anchor: (() => {
        const a = branchAnchor(1, "tbs_fx_web", other, "new", 4, 4);
        return { kind: "code", scope: "branch", snapshotId: a.snapshotId, repository: a.repo, path: a.path, side: a.side, startLine: 4, endLine: 4, diffChecksum: a.diffChecksum };
      })() });
      // Feedback is re-read on its own events (there is no "feedback.item.created"; submitted is one).
      await event("feedback.submitted", {});
      await until(() => [...view.querySelectorAll("[data-fb-item]")].find((el) => el.textContent?.includes("On the test file only.")), "the new comment");
      await settle(300);
      expect(drawn.get(other) ?? 0).toBeGreaterThan(0);
      expect(drawn.get(main) ?? 0).toBe(0);
    } finally {
      Map.prototype.get = get;
    }
  });

  test("Pending: an event for one session re-reads that session alone, keeping what was read meanwhile", async () => {
    const c = counted("populated", ["run_pending_b"]);
    const view = await page(c, "changes");
    await click([...view.querySelector("[data-testid=changes-scope]")!.querySelectorAll("button")].find((b) => b.textContent === "Pending work")!);
    await until(() => (c.runDiffs.length === 2 ? c.runDiffs : null), "both sessions read");
    await settle(100);
    c.reset();
    // The re-read waits until released: what was read stays shown meanwhile.
    let release: () => void = () => {};
    const gate = new Promise<void>((r) => { release = r; });
    const runDiff = c.client.runDiff;
    c.client.runDiff = async (id) => { const out = runDiff(id); await gate; return out; };
    await event("run.diff.updated", { checksum: "x" }, RUN_ID);
    await event("run.diff.updated", { checksum: "y" }, RUN_ID);
    await settle(RUN_DIFF_EVERY_MS + 200);
    expect(c.runDiffs).toEqual([RUN_ID]);
    // Never back to "Reading…" while it is read again.
    expect(view.textContent).not.toContain("Reading checkout changes");
    await act(async () => release());
    await settle(100);
    expect(c.runDiffs).toEqual([RUN_ID]);
    expect(withFiles(c.branch)).toEqual([]);
  });

  test("Pending: an event for a session being read reads it again after that read, never beside it; the newer answer is shown", async () => {
    const c = counted("populated", ["run_pending_b"]);
    const base = await c.client.runDiff(RUN_ID);
    expect(base.files.length).toBeGreaterThan(1);
    const view = await page(c, "changes");
    await click([...view.querySelector("[data-testid=changes-scope]")!.querySelectorAll("button")].find((b) => b.textContent === "Pending work")!);
    await until(() => (c.runDiffs.length >= 3 ? c.runDiffs : null), "both sessions read");
    await settle(100);
    c.reset();
    // Each read answered by hand.
    const held: Array<(d: typeof base) => void> = [];
    c.client.runDiff = (id) => { c.runDiffs.push(id); return new Promise((r) => held.push(r)); };
    await event("run.diff.updated", { checksum: "x" }, RUN_ID);
    await settle(RUN_DIFF_EVERY_MS + 200);
    expect(c.runDiffs).toEqual([RUN_ID]);
    await event("run.diff.updated", { checksum: "y" }, RUN_ID);
    await settle(RUN_DIFF_EVERY_MS + 200);
    // Asked again while it is read: not read beside it.
    expect(c.runDiffs).toEqual([RUN_ID]);
    await act(async () => held[0]!({ ...base, files: base.files.slice(0, 1) }));
    await settle(100);
    expect(c.runDiffs).toEqual([RUN_ID, RUN_ID]);
    await act(async () => held[1]!({ ...base, files: base.files.slice(0, 2) }));
    await settle(100);
    const row = view.querySelector(`[data-testid=changes-session][data-run="${RUN_ID}"] [data-testid=checkout-summary]`)!;
    expect(row.textContent).toContain("2 files");
    expect(c.runDiffs).toEqual([RUN_ID, RUN_ID]);
  });

  test("Pending: a session joining reads that session alone", async () => {
    const live = ["run_pending_b"];
    const c = counted("populated", live);
    const view = await page(c, "changes");
    await click([...view.querySelector("[data-testid=changes-scope]")!.querySelectorAll("button")].find((b) => b.textContent === "Pending work")!);
    await until(() => (view.querySelectorAll("[data-testid=changes-session]").length === 2 && c.runDiffs.length >= 2 ? c.runDiffs : null), "both sessions read");
    await settle(200);
    c.reset();
    live.push("run_pending_c");
    await event("task.updated", {});
    await until(() => (view.querySelectorAll("[data-testid=changes-session]").length === 3 ? true : null), "the third session");
    await settle(300);
    expect(c.runDiffs).toEqual(["run_pending_c"]);
  });
});
