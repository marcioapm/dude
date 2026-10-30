/**
 * The fixtures' task branch (`?branch=…`, fixtures/branchDiff.ts): each
 * state the Changes tab draws is reachable through the client the app
 * uses, answered as the API answers, and run anchors keep working.
 */

import { afterEach, describe, expect, test } from "bun:test";
import type { FeedbackItem } from "@dude/domain";
import { ApiError, ApiClient } from "../src/api/client.ts";
import { FixtureClient } from "../src/fixtures/client.ts";
import { BRANCH_KEY, BRANCH_STATES, SHAS } from "../src/fixtures/branchDiff.ts";
import { TASK_ID } from "../src/fixtures/data.ts";
import { RUN_KEY } from "../src/fixtures/scenario.ts";

afterEach(() => {
  localStorage.removeItem(BRANCH_KEY);
  localStorage.removeItem(RUN_KEY);
});

function client(state: string | null, run: string | null = null): FixtureClient {
  if (state) localStorage.setItem(BRANCH_KEY, state);
  if (run) localStorage.setItem(RUN_KEY, run);
  return new FixtureClient("a");
}

describe("fixtures: the task's branch", () => {
  test("populated: two repositories, files with binary and too-large ones, comments on two files, one outdated", async () => {
    const c = client("populated");
    const d = await c.taskBranchDiff(TASK_ID);
    expect(d).toMatchObject({ attempt: 1, currentAttempt: 1, readOnly: false, repository: "web-console", availability: { state: "ok" },
      newer: null, pullRequest: { number: 482, headSha: SHAS.head } });
    expect(d.repositories.map((r) => r.name)).toEqual(["web-console", "sdk-js"]);
    const files = d.snapshot!.files;
    expect(files.length).toBeGreaterThanOrEqual(3);
    expect(files.find((f) => f.binary)?.path).toBe("public/card-icons.png");
    expect(files.find((f) => f.truncated)?.path).toBe("package-lock.json");
    expect(d.snapshot!.stats).toMatchObject({ files: 5, binaryFiles: 1, truncatedFiles: 1 });
    const byFile = new Map<string, string[]>();
    for (const p of d.placements) {
      const path = "path" in p.at && p.at.path ? p.at.path : "?";
      byFile.set(path, [...(byFile.get(path) ?? []), p.at.kind]);
    }
    expect(byFile.get("src/checkout/PaymentStep.tsx")!.sort()).toEqual(["lines", "lines", "lines", "outdated"]);
    expect(byFile.get("src/checkout/PaymentStep.test.tsx")).toEqual(["lines"]);
    // The same items are the task's feedback, listed as the Feedback tab reads them.
    const listed = (await c.feedback(TASK_ID)).feedback.filter((i) => i.anchor.kind === "code" && i.anchor.scope === "branch");
    expect(listed.length).toBe(5);
    const sdk = await c.taskBranchDiff(TASK_ID, { repository: "sdk-js" });
    expect(sdk).toMatchObject({ repository: "sdk-js", availability: { state: "ok" }, pullRequest: null, placements: [] });
  });

  test("updated: a newer snapshot with main merged in, the base moved; refreshing reads it and the comments follow the shift", async () => {
    const c = client("updated");
    const d = await c.taskBranchDiff(TASK_ID);
    expect(d.snapshot!.id).toBe("tbs_fx_web");
    expect(d.newer).toMatchObject({ id: "tbs_fx_merged", relation: "merged-target", fromMergeBaseSha: SHAS.base, mergeBaseSha: SHAS.targetMerged,
      fromHeadSha: SHAS.head, headSha: SHAS.headMerged, count: 1 });
    const refreshed = await c.taskBranchDiff(TASK_ID, { snapshot: d.newer!.id });
    expect(refreshed).toMatchObject({ newer: null, snapshot: { id: "tbs_fx_merged", relation: "merged-target", previousSnapshotId: "tbs_fx_web" },
      pullRequest: { headSha: SHAS.head } });
    expect(refreshed.snapshot!.headSha).not.toBe(refreshed.pullRequest!.headSha);
    const ana = refreshed.placements.find((p) => p.itemId === "fbi_fx_b1")!;
    expect(ana.at).toEqual({ kind: "lines", path: "src/checkout/PaymentStep.tsx", side: "new", startLine: 11, endLine: 11 });
  });

  test("rebased: the newer snapshot is rewritten; a comment on a rewritten line is outdated", async () => {
    const c = client("rebased");
    const d = await c.taskBranchDiff(TASK_ID);
    expect(d.newer).toMatchObject({ id: "tbs_fx_rebased", relation: "rewritten", headSha: SHAS.headRebased });
    const r = await c.taskBranchDiff(TASK_ID, { snapshot: "tbs_fx_rebased" });
    expect(r.snapshot!.relation).toBe("rewritten");
    expect(r.placements.find((p) => p.itemId === "fbi_fx_b3")!.at).toEqual({ kind: "outdated", path: "src/checkout/PaymentStep.tsx", reason: "changed" });
    expect(r.placements.find((p) => p.itemId === "fbi_fx_b4")!.at.kind).toBe("lines");
  });

  test("empty, unavailable and preparing say so, never a diff", async () => {
    const empty = await client("empty").taskBranchDiff(TASK_ID);
    expect(empty).toMatchObject({ availability: { state: "ok" }, snapshot: { files: [], stats: { files: 0 } } });
    localStorage.removeItem(BRANCH_KEY);
    const unavailable = await client("unavailable").taskBranchDiff(TASK_ID);
    expect(unavailable).toMatchObject({ snapshot: null, availability: { state: "unavailable" }, capture: { state: "unavailable" } });
    expect((unavailable.availability as { reason: string }).reason).toContain("403");
    localStorage.removeItem(BRANCH_KEY);
    expect(await client("preparing").taskBranchDiff(TASK_ID)).toMatchObject({ snapshot: null, availability: { state: "preparing" },
      capture: { state: "pending" } });
  });

  test("older: attempt 1 is read-only with its recorded snapshot; with ?run=restarted the task is on attempt 2 in any state", async () => {
    const c = client("older");
    const now = await c.taskBranchDiff(TASK_ID);
    expect(now).toMatchObject({ attempt: 2, currentAttempt: 2, readOnly: false });
    const old = await c.taskBranchDiff(TASK_ID, { attempt: 1 });
    expect(old).toMatchObject({ attempt: 1, readOnly: true, availability: { state: "ok" }, snapshot: { id: "tbs_fx_attempt1", attempt: 1 },
      pullRequest: null });
    expect((await c.taskBranchDiff(TASK_ID, { attempt: 1, repository: "sdk-js" })).availability.state).toBe("none");
    localStorage.removeItem(BRANCH_KEY);
    const restarted = client(null, "restarted");
    expect(await restarted.taskBranchDiff(TASK_ID, { attempt: 1 })).toMatchObject({ readOnly: true, snapshot: { attempt: 1 } });
  });

  test("a draft on a snapshot is taken, its anchor read from the snapshot; a stale checksum or another attempt is refused", async () => {
    const c = client("populated");
    const d = await c.taskBranchDiff(TASK_ID);
    const draft = await c.createFeedbackDraft(TASK_ID, { body: "Name the event", anchor: { kind: "code", scope: "branch", snapshotId: d.snapshot!.id,
      path: "src/checkout/PaymentStep.tsx", side: "new", startLine: 9, endLine: 9, diffChecksum: d.snapshot!.checksum } });
    expect(draft.anchor).toMatchObject({ scope: "branch", snapshotId: "tbs_fx_web", repo: "web-console", headSha: SHAS.head, baseSha: SHAS.base });
    expect((draft.anchor as { excerpt: unknown[] }).excerpt.length).toBe(1);
    const stale = c.createFeedbackDraft(TASK_ID, { body: "x", anchor: { kind: "code", scope: "branch", snapshotId: "tbs_fx_web",
      path: "src/checkout/PaymentStep.tsx", side: "new", startLine: 9, endLine: 9, diffChecksum: "old" } });
    await expect(stale).rejects.toBeInstanceOf(ApiError);
    // Run anchors in the fixtures are as before: refused, there is no Run diff.
    const run = c.createFeedbackDraft(TASK_ID, { body: "x", anchor: { kind: "code", runId: "run_x", path: "a", side: "new", startLine: 1, endLine: 1 } });
    await expect(run).rejects.toMatchObject({ status: 400 });
    const older = client("older");
    await expect(older.createFeedbackDraft(TASK_ID, { body: "x", anchor: { kind: "code", scope: "branch", snapshotId: "tbs_fx_attempt1",
      path: "src/checkout/PaymentStep.tsx", side: "new", startLine: 9, endLine: 9, diffChecksum: "sum-tbs_fx_attempt1" } })).rejects.toMatchObject({ status: 409 });
  });

  test("unset, the tab is populated with no seeded comments; every state is reachable", async () => {
    const c = client(null);
    const d = await c.taskBranchDiff(TASK_ID);
    expect(d.availability.state).toBe("ok");
    expect(d.placements).toEqual([]);
    expect((await c.feedback(TASK_ID)).feedback.filter((i: FeedbackItem) => i.anchor.kind === "code")).toEqual([]);
    for (const state of BRANCH_STATES) {
      localStorage.setItem(BRANCH_KEY, state);
      expect((await new FixtureClient("a").taskBranchDiff(TASK_ID)).taskId).toBe(TASK_ID);
    }
    await expect(c.taskBranchDiff("wi_other")).rejects.toMatchObject({ status: 404 });
  });

  test("the real client asks the route with each choice", async () => {
    const asked: string[] = [];
    const real = new ApiClient({ apiKey: "k", baseUrl: "http://dude.test" });
    const was = globalThis.fetch;
    globalThis.fetch = (async (url: string) => {
      asked.push(String(url));
      return new Response("{}", { status: 200, headers: { "content-type": "application/json" } });
    }) as unknown as typeof fetch;
    try {
      await real.taskBranchDiff("wi_1");
      await real.taskBranchDiff("wi 2", { attempt: 1, repository: "web-console", snapshot: "tbs_1" });
      await real.taskBranchDiff("wi_1", { files: false });
    } finally {
      globalThis.fetch = was;
    }
    expect(asked.map((u) => u.replace("http://dude.test", ""))).toEqual(["/v1/tasks/wi_1/branch-diff",
      "/v1/tasks/wi%202/branch-diff?attempt=1&repository=web-console&snapshot=tbs_1", "/v1/tasks/wi_1/branch-diff?files=0"]);
  });
});
