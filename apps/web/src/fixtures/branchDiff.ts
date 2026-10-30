/**
 * The fixtures' task branch snapshots (`GET /v1/tasks/:id/branch-diff`),
 * in each state the Changes tab draws: `?branch=` (remembered as
 * `dude.fixtures.branch`, `off` forgets) with `?fixtures=a` … `e`.
 *
 *   populated  two repositories (web-console: 5 files, one binary, one too
 *              large to show; sdk-js: 1 file), comments on two files of
 *              web-console, one of them outdated
 *   updated    the page's snapshot, and a newer one: main merged in
 *              (merged-target, the merge base moved); `snapshot=` the newer
 *              one shows it, the published PR head still the older head
 *   rebased    the same with the branch rewritten (rewritten)
 *   empty      a snapshot of no change (a successful empty diff)
 *   unavailable GitHub could not be read: why
 *   preparing  nothing captured yet; a capture is asked for
 *   older      the task started over (as `?run=restarted`): attempt 2 is
 *              current, `attempt=1` is attempt 1's recorded snapshot,
 *              read-only. With `?run=restarted` any state has attempt 1's.
 *
 * Unset, the tab answers `populated` without the seeded comments, so the
 * fixtures' other feedback stays as it was. Snapshots of one diff with its
 * lines shifted, not computed git trees. In `updated` and `rebased` the
 * read with no `snapshot` answers the page's snapshot (tbs_fx_web) with
 * `newer` set — as the API answers `snapshot=tbs_fx_web` once a later one
 * is captured — so the notice shows at once; `snapshot=<newer.id>` is the
 * refresh.
 */

import { relocateCodeAnchor, type FeedbackBranchAnchor, type FeedbackExcerptLine, type FeedbackItem, type RunDiffFile, type RunDiffLine,
  type TaskBranchDiff, type TaskBranchSnapshot, type TaskBranchStats } from "@dude/domain";
import type { TaskBranchDiffQuery } from "../api/client.ts";
import { PULL_REQUEST, TASK_ID } from "./data.ts";

export const BRANCH_KEY = "dude.fixtures.branch";
export const BRANCH_STATES = ["populated", "updated", "rebased", "empty", "unavailable", "preparing", "older"] as const;
export type BranchState = (typeof BRANCH_STATES)[number];

/** The state asked for (`?branch=`), or null for the default (populated, no seeded comments). */
export function branchState(): BranchState | null {
  const v = typeof window === "undefined" ? null : localStorage.getItem(BRANCH_KEY);
  return v && (BRANCH_STATES as readonly string[]).includes(v) ? (v as BranchState) : null;
}

const MIN = 60_000;
const at = (minutesAgo: number) => new Date(Date.now() - minutesAgo * MIN).toISOString();
const sha = (seed: string) => (seed.repeat(Math.ceil(40 / seed.length))).slice(0, 40);

/** The SHAs of the story: main, its merge bases, and the branch's heads. */
export const SHAS = {
  target: sha("2be91c7a"), // main at the first captures
  base: sha("2be91c7a"), // merge-base(main, head) before main is merged in
  targetMerged: sha("8c1d0e4f"), // main when it was merged into the branch
  head1: sha("51f0a2d3"), // the branch's first snapshot
  head: sha("7ac42e1b"), // populated: the head shown
  headMerged: sha("3e7a2b9c"), // updated: main merged in
  headRebased: sha("9df31a7e"), // rebased: rewritten onto newer main
  attempt1Head: sha("c0ffee12"),
  sdkHead: sha("a17b9e05"),
} as const;

/** A hunk from unified lines (" ", "+", "-" first), numbered from its starts. */
function hunk(oldStart: number, newStart: number, body: string): { header: string; lines: RunDiffLine[] } {
  let o = oldStart;
  let n = newStart;
  const lines: RunDiffLine[] = body.split("\n").map((raw) => {
    const kind = raw[0] as " " | "+" | "-";
    const text = raw.slice(1);
    if (kind === "+") return { kind, old: null, new: n++, text };
    if (kind === "-") return { kind, old: o++, new: null, text };
    return { kind: " ", old: o++, new: n++, text };
  });
  const olds = lines.filter((l) => l.kind !== "+").length;
  const news = lines.filter((l) => l.kind !== "-").length;
  return { header: `@@ -${oldStart},${olds} +${newStart},${news} @@`, lines };
}

function file(path: string, status: RunDiffFile["status"], hunks: RunDiffFile["hunks"], extra: Partial<RunDiffFile> = {}): RunDiffFile {
  const count = (k: string) => hunks.reduce((s, h) => s + h.lines.filter((l) => l.kind === k).length, 0);
  return { path, status, additions: count("+"), deletions: count("-"), hunks, ...extra };
}

/** PaymentStep.tsx's change, from `start` on the new side (main merged in shifts it by 3). */
const paymentStep = (start: number, rewritten = false) => file("src/checkout/PaymentStep.tsx", "M", [hunk(start, start, ` import { useCheckout } from "./useCheckout";
-import { DetailsAndPayment } from "./DetailsAndPayment";
+import { BillingDetails } from "./BillingDetails";
+import { PaymentMethods } from "./PaymentMethods";
 
 export function PaymentStep({ plan }: { plan: Plan }) {
   const checkout = useCheckout();
-  return <DetailsAndPayment checkout={checkout} />;
+  if (!checkout.billing) return <BillingDetails checkout={checkout} />;
+  const methods = plan.annual ? ["card", "sepa", "invoice"] : ["card", "sepa"];
+  ${rewritten ? "track(\"checkout_step\", { step: \"payment\", plan: plan.id });" : "track(\"checkout_step\", { step: \"payment\" });"}
+  return <PaymentMethods checkout={checkout} methods={methods} />;
 }`)]);

const paymentTest = (start: number) => file("src/checkout/PaymentStep.test.tsx", "A", [hunk(0, start, `+import { render, screen } from "@testing-library/react";
+import { PaymentStep } from "./PaymentStep";
+
+test("an annual plan offers an invoice", () => {
+  render(<PaymentStep plan={{ id: "pro", annual: true }} />);
+  expect(screen.getByRole("radio", { name: "Invoice" })).toBeTruthy();
+});`)]);

const flags = file("src/flags.ts", "M", [hunk(12, 12, ` export const FLAGS = {
+  checkout_v2: { until: "2026-11-06" },
   search_v3: false,`)]);

/** Changed, nothing to show line by line. */
const icons = file("public/card-icons.png", "A", [], { additions: 0, deletions: 0, binary: true });
/** Lines changed, too many to show: the counts are whole. */
const lockfile = file("package-lock.json", "M", [], { additions: 1_284, deletions: 377, truncated: true });

const sdkClient = file("src/checkout.ts", "M", [hunk(40, 40, ` export interface CheckoutSession {
   id: string;
+  step: "billing" | "payment";
   plan: string;`)]);

function stats(files: RunDiffFile[], fileCapReached = false): TaskBranchStats {
  return { files: files.length, additions: files.reduce((s, f) => s + f.additions, 0), deletions: files.reduce((s, f) => s + f.deletions, 0),
    truncatedFiles: files.filter((f) => f.truncated).length, binaryFiles: files.filter((f) => f.binary).length, fileCapReached };
}

function snapshot(id: string, over: Partial<TaskBranchSnapshot> & Pick<TaskBranchSnapshot, "headSha" | "files" | "capturedAt">): TaskBranchSnapshot {
  const files = over.files;
  return {
    id, taskId: TASK_ID, attempt: 1, repositoryId: "repo_wc", repository: "web-console", branchRef: PULL_REQUEST.headBranch,
    targetRef: "main", targetSha: SHAS.target, mergeBaseSha: SHAS.base, checksum: `sum-${id}`, source: "github-compare",
    stats: stats(files), truncated: files.some((f) => f.truncated), status: "complete", reason: null,
    prHeadSha: over.headSha, previousSnapshotId: null, relation: "initial", ...over,
  };
}

const WEB_FILES = [paymentStep(1), paymentTest(1), flags, icons, lockfile];

/**
 * Every snapshot of the fixtures, by id, on the task's current attempt
 * (`current`); attempt 1's own when the current one is later.
 */
export function snapshots(current: number): Record<string, TaskBranchSnapshot> {
  const all = [
  snapshot("tbs_fx_first", { headSha: SHAS.head1, capturedAt: at(180), files: [paymentStep(1, true), flags] }),
  snapshot("tbs_fx_web", { headSha: SHAS.head, capturedAt: at(42), files: WEB_FILES, previousSnapshotId: "tbs_fx_first", relation: "fast-forward" }),
  snapshot("tbs_fx_merged", { headSha: SHAS.headMerged, targetSha: SHAS.targetMerged, mergeBaseSha: SHAS.targetMerged, capturedAt: at(6),
    files: [paymentStep(4), paymentTest(1), flags, icons, lockfile], previousSnapshotId: "tbs_fx_web", relation: "merged-target" }),
  snapshot("tbs_fx_rebased", { headSha: SHAS.headRebased, targetSha: SHAS.targetMerged, mergeBaseSha: SHAS.targetMerged, capturedAt: at(6),
    files: [paymentStep(4, true), paymentTest(1), flags, icons, lockfile], previousSnapshotId: "tbs_fx_web", relation: "rewritten" }),
  snapshot("tbs_fx_empty", { headSha: SHAS.base, capturedAt: at(30), files: [] }),
  snapshot("tbs_fx_sdk", { repositoryId: "repo_sdk", repository: "sdk-js", headSha: SHAS.sdkHead, capturedAt: at(40), files: [sdkClient] }),
  ];
  const own = all.map((s) => ({ ...s, attempt: current }));
  const earlier = current > 1 ? [snapshot("tbs_fx_attempt1", { attempt: 1, branchRef: "dude/tsk_01j9x4kq/attempt-1", headSha: SHAS.attempt1Head,
    capturedAt: at(3 * 24 * 60), files: [paymentStep(1, true)] })] : [];
  return Object.fromEntries([...own, ...earlier].map((s) => [s.id, s]));
}

const REPOSITORIES = [
  { id: "repo_wc", name: "web-console", access: "write" as const },
  { id: "repo_sdk", name: "sdk-js", access: "write" as const },
];

/** The lines of a file of a snapshot, on a side, as the API quotes them (excerptOf). */
function excerpt(f: RunDiffFile, side: "old" | "new", start: number, end: number): FeedbackExcerptLine[] {
  const n = (l: RunDiffLine) => (side === "old" ? l.old : l.new);
  for (const h of f.hunks) {
    const first = h.lines.findIndex((l) => n(l) === start);
    const last = h.lines.findIndex((l) => n(l) === end);
    if (first >= 0 && last >= first) return h.lines.slice(first, last + 1).map((l) => ({ ...l }));
  }
  throw new Error(`fixtures: no ${side} ${start}-${end} in ${f.path}`);
}

/** A branch anchor on `snapshotId`'s `path`, as the API keeps it. */
export function branchAnchor(current: number, snapshotId: string, path: string, side: "old" | "new", start: number, end: number): FeedbackBranchAnchor {
  const s = snapshots(current)[snapshotId]!;
  const f = s.files.find((x) => x.path === path)!;
  return { kind: "code", scope: "branch", snapshotId, attempt: s.attempt, repo: s.repository, path, side, startLine: start, endLine: end,
    baseSha: s.mergeBaseSha, headSha: s.headSha, targetRef: s.targetRef, diffChecksum: s.checksum, excerpt: excerpt(f, side, start, end) };
}

/**
 * The comments seeded with `?branch=populated` (and updated, rebased): on
 * PaymentStep.tsx, Ana's and Tom's submitted ones, your draft, and Lin's
 * written on the first snapshot on a line since changed (outdated); on
 * PaymentStep.test.tsx one of Ana's.
 */
export function branchComments(people: ReadonlyArray<{ id: string; name: string }>, current: number): FeedbackItem[] {
  const who = (id: string) => {
    const p = people.find((x) => x.id === id)!;
    return { id: p.id, name: p.name };
  };
  const item = (id: string, author: string, body: string, anchor: FeedbackBranchAnchor, submitted: boolean, minutesAgo: number): FeedbackItem => ({
    id, taskId: TASK_ID, attempt: current, batchId: submitted ? "fbt_fx_branch" : null, author: who(author), body, anchor,
    status: submitted ? "open" : "draft", deliveredRunId: null, resolvedCommit: null, resolutionPending: false, outdated: false,
    routedTo: submitted ? "workflow" : null, submittedAt: submitted ? at(minutesAgo) : null, createdAt: at(minutesAgo), updatedAt: at(minutesAgo),
    history: [],
  });
  const items = [
    item("fbi_fx_b1", "u_ana", "The invoice option is annual-only: say so on the radio, not only in the docs.",
      branchAnchor(current, "tbs_fx_web", "src/checkout/PaymentStep.tsx", "new", 8, 8), true, 30),
    item("fbi_fx_b2", "u_tom", "Billing first, then payment: keep the step names the funnel already reports.",
      branchAnchor(current, "tbs_fx_web", "src/checkout/PaymentStep.tsx", "new", 2, 3), true, 28),
    item("fbi_fx_b3", "u_marcio", "Would a missing plan id break `track` here?",
      branchAnchor(current, "tbs_fx_web", "src/checkout/PaymentStep.tsx", "new", 9, 9), false, 12),
    item("fbi_fx_b4", "u_lin", "Send the plan id with the step event.",
      branchAnchor(current, "tbs_fx_first", "src/checkout/PaymentStep.tsx", "new", 9, 9), true, 150),
    item("fbi_fx_b5", "u_ana", "Also cover a monthly plan: no invoice offered.",
      branchAnchor(current, "tbs_fx_web", "src/checkout/PaymentStep.test.tsx", "new", 4, 6), true, 25),
  ];
  if (current > 1) {
    for (const [id, line, submitted, body] of [
      ["fbi_fx_old1", 8, true, "Earlier attempt: keep annual invoice wording."],
      ["fbi_fx_old2", 9, false, "Earlier draft: preserve the plan event."],
    ] as const) {
      items.push({ ...item(id, submitted ? "u_ana" : "u_marcio", body,
        branchAnchor(current, "tbs_fx_attempt1", "src/checkout/PaymentStep.tsx", "new", line, line), submitted, 300), attempt: 1 });
    }
    items.push({ ...items.find((i) => i.id === "fbi_fx_old1")!, id: "fbi_fx_old3", body: "Earlier comment: these lines have changed.",
      anchor: { ...branchAnchor(current, "tbs_fx_attempt1", "src/checkout/PaymentStep.tsx", "new", 8, 8),
        snapshotId: "tbs_fx_attempt1_previous", diffChecksum: "older-checksum" }, outdated: true });
  }
  return items;
}

/** The newest snapshot of web-console's attempt 2 in each state; the page's (latest unless refreshed) is tbs_fx_web. */
const NEWEST: Partial<Record<BranchState, string>> = { updated: "tbs_fx_merged", rebased: "tbs_fx_rebased" };

function refused(status: number, code: string, message: string): never {
  throw Object.assign(new Error(message), { status, code });
}

/**
 * `GET /v1/tasks/:id/branch-diff` in `state`, the task's current attempt
 * being `currentAttempt`: as the API would answer `query`, placing `items`
 * (the reader's visible feedback) on the snapshot returned. Errors carry
 * the API's status and code for the client to raise as an ApiError.
 */
export function branchDiffFor(state: BranchState | null, currentAttempt: number, query: TaskBranchDiffQuery,
    items: ReadonlyArray<FeedbackItem>): TaskBranchDiff {
  const current = currentAttempt;
  const SNAPSHOTS = snapshots(current);
  const attempt = query.attempt ?? (query.snapshot ? SNAPSHOTS[query.snapshot]?.attempt : undefined) ?? current;
  if (attempt < 1 || attempt > current) refused(404, "not_found", `attempt ${attempt} of this task does not exist`);
  const readOnly = attempt < current;
  const repo = REPOSITORIES.find((r) => r.name === (query.repository ?? "web-console") || r.id === query.repository);
  if (!repo) refused(400, "bad_request", `${query.repository} is not a repository of this task`);
  // The snapshots of this attempt's repository, oldest first (attempt 1 recorded web-console only).
  const lineageOf = (repoId: string): string[] => readOnly ? (attempt === 1 && repoId === "repo_wc" && SNAPSHOTS.tbs_fx_attempt1 ? ["tbs_fx_attempt1"] : [])
    : repoId === "repo_sdk" ? ["tbs_fx_sdk"]
    : state === "empty" ? ["tbs_fx_empty"]
    : ["tbs_fx_first", "tbs_fx_web", ...(NEWEST[state ?? "populated"] ? [NEWEST[state ?? "populated"]!] : [])];
  const repositories = REPOSITORIES.map((r) => ({ ...r, snapshots: lineageOf(r.id).length }));
  const pullRequest = readOnly || repo.id !== "repo_wc" ? null
    : { number: PULL_REQUEST.number, url: PULL_REQUEST.url, state: "open" as const, headSha: SHAS.head, syncedAt: at(3) };
  const base = { taskId: TASK_ID, attempt, currentAttempt: current, readOnly, repository: repo.name, repositories, pullRequest };
  const empty = { snapshot: null, newer: null, capture: null, placements: [] };

  if (!readOnly && state === "unavailable") {
    const reason = "GitHub refused the read (403): Resource not accessible by integration";
    return { ...base, ...empty, availability: { state: "unavailable", reason }, capture: { state: "unavailable", reason, requestedAt: at(4) } };
  }
  if (!readOnly && state === "preparing") {
    return { ...base, ...empty, availability: { state: "preparing" }, capture: { state: "pending", reason: null, requestedAt: at(0) } };
  }
  const lineage = lineageOf(repo.id);
  if (query.snapshot && !lineage.includes(query.snapshot)) {
    refused(404, "not_found", `snapshot ${query.snapshot} is not one of this task's`);
  }
  if (lineage.length === 0) {
    return { ...base, ...empty, availability: { state: "none", reason: `Attempt ${attempt} recorded no snapshot of ${repo.name}.` } };
  }
  // The page reads the latest it was shown: tbs_fx_web until it asks for the newer one.
  const newestId = lineage.at(-1)!;
  const shownId = query.snapshot ?? (NEWEST[state ?? "populated"] && repo.id === "repo_wc" && !readOnly ? "tbs_fx_web" : newestId);
  const shown = SNAPSHOTS[shownId]!;
  const newest = SNAPSHOTS[newestId]!;
  const newer = shownId === newestId ? null : {
    id: newest.id, relation: newest.relation === "rewritten" ? "rewritten" as const : shown.mergeBaseSha === newest.mergeBaseSha ? "fast-forward" as const : "merged-target" as const,
    fromMergeBaseSha: shown.mergeBaseSha, fromHeadSha: shown.headSha, mergeBaseSha: newest.mergeBaseSha, headSha: newest.headSha,
    capturedAt: newest.capturedAt, count: lineage.length - 1 - lineage.indexOf(shownId),
  };
  const viewed = { snapshotId: shown.id, attempt: shown.attempt, repository: shown.repository, checksum: shown.checksum, files: shown.files };
  // As the API: the attempt's branch items on this repository, each placed on the snapshot shown.
  const placements = items.flatMap((i) => (i.anchor.kind === "code" && i.anchor.scope === "branch" && i.attempt === shown.attempt
    && i.anchor.repo === shown.repository ? [{ itemId: i.id, snapshotId: i.anchor.snapshotId, at: relocateCodeAnchor(i.anchor, viewed) }] : []));
  return { ...base, availability: { state: "ok" }, snapshot: shown, newer, capture: null, placements };
}
