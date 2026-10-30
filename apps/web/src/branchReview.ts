/**
 * The task's whole-branch review, as the web app holds it: the snapshot
 * read for one attempt and repository (`GET /v1/tasks/:id/branch-diff`),
 * pinned once shown, and the words and marks the Changes tab draws from it.
 *
 * A snapshot never replaces the one on screen: once a snapshot is shown,
 * every re-read asks for it by id (`snapshot=`), so only `newer` (and the
 * placements) move. Refresh pins `newer.id`.
 */

import { useCallback, useEffect, useRef, useState } from "react";
import {
  isBranchAnchor, relocateCodeAnchor, type FeedbackAnchorInput, type FeedbackItem, type RelocatedAnchor, type TaskBranchDiff,
  type TaskBranchNewer, type TaskBranchSnapshot,
} from "@dude/domain";
import type { ApiClient } from "./api/client.ts";

export const short = (sha: string | null | undefined) => (sha ?? "").slice(0, 7);

export interface BranchRead {
  readonly diff: TaskBranchDiff | null;
  readonly problem: string | null;
  /** The snapshot pinned: what is on screen; null before the first read (or with none). */
  readonly pinned: string | null;
  /** Show `newer`: pins it and reads it. */
  readonly refresh: () => void;
  readonly reload: () => void;
}

/**
 * One attempt's repository's branch, read on mount, when `version` moves
 * (the task page's re-read on its stream, which a captured snapshot
 * triggers) and while a capture is being prepared. With `pin`, the first
 * snapshot read stays the one asked for until `refresh`; without, every
 * read is the latest (the Overview's card).
 */
export function useBranchDiff(client: ApiClient, taskId: string, attempt: number | undefined, repository: string | null, version: number,
    { pin = true }: { pin?: boolean } = {}): BranchRead {
  const [diff, setDiff] = useState<TaskBranchDiff | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const [pinned, setPinned] = useState<string | null>(null);
  const [tick, setTick] = useState(0);
  // Another attempt or repository starts from its latest.
  const scope = `${taskId}\u0000${attempt ?? ""}\u0000${repository ?? ""}`;
  const was = useRef(scope);
  const pinRef = useRef<string | null>(null);
  if (was.current !== scope) {
    was.current = scope;
    pinRef.current = null;
  }
  const reads = useRef(0);
  useEffect(() => {
    const seq = ++reads.current;
    const snapshot = pin ? pinRef.current : null;
    client.taskBranchDiff(taskId, { ...(attempt !== undefined ? { attempt } : {}), ...(repository ? { repository } : {}), ...(snapshot ? { snapshot } : {}) })
      .then((got) => {
        if (seq !== reads.current) return;
        if (pin && !pinRef.current && got.snapshot) pinRef.current = got.snapshot.id;
        setPinned(pin ? pinRef.current : got.snapshot?.id ?? null);
        setDiff((old) => (old && JSON.stringify(old) === JSON.stringify(got) ? old : got));
        setProblem(null);
      }, (err: unknown) => {
        if (seq === reads.current) setProblem(err instanceof Error ? err.message : String(err));
      });
    return () => { if (seq === reads.current) ++reads.current; };
  }, [client, taskId, attempt, repository, version, tick, pin, scope]);
  // Being prepared: asked again until it is captured (the captured event re-reads sooner).
  const preparing = diff?.availability.state === "preparing";
  useEffect(() => {
    if (!preparing) return;
    const t = setTimeout(() => setTick((n) => n + 1), 5000);
    return () => clearTimeout(t);
  }, [preparing, tick]);
  const newerId = diff?.newer?.id ?? null;
  const refresh = useCallback(() => {
    if (!newerId) return;
    pinRef.current = newerId;
    setTick((n) => n + 1);
  }, [newerId]);
  const reload = useCallback(() => setTick((n) => n + 1), []);
  // A read for another scope is not this one's.
  const mine = diff && (attempt === undefined || diff.attempt === attempt) && (!repository || diff.repository === repository) ? diff : null;
  return { diff: mine, problem, pinned, refresh, reload };
}

/** "New changes available · …": what happened between the snapshot shown and the newer one. */
export function newerWords(newer: TaskBranchNewer, targetRef: string): string {
  switch (newer.relation) {
    case "fast-forward":
      return `New commits on the branch — head ${short(newer.fromHeadSha)} → ${short(newer.headSha)}`;
    case "merged-target":
      return `${targetRef} was merged in — base moved ${short(newer.fromMergeBaseSha)} → ${short(newer.mergeBaseSha)}`;
    case "rewritten":
      return "Branch was rewritten (rebase or force-push) — earlier comments are kept, marked outdated where their lines moved";
  }
}

/** After Refresh: "Now at 3e7a2b9 · base moved 2be91c7 → 8c1d0e4". */
export function refreshedWords(from: Pick<TaskBranchSnapshot, "mergeBaseSha" | "headSha">, to: Pick<TaskBranchSnapshot, "mergeBaseSha" | "headSha" | "relation">): string {
  const moved = from.mergeBaseSha !== to.mergeBaseSha ? ` · base moved ${short(from.mergeBaseSha)} → ${short(to.mergeBaseSha)}` : "";
  const after = to.relation === "rewritten"
    ? "Branch was rewritten: earlier comments are kept, marked outdated where their lines moved."
    : "Comments follow their lines on a simple shift; otherwise they are marked outdated, never moved.";
  return `Now at ${short(to.headSha)}${moved}. ${after}`;
}

/** What Refresh says while it is refused. */
export const REFRESH_REFUSED = "Save or cancel your open comment first. It stays on the snapshot it was written on.";

/** A new comment's anchor on the snapshot shown: its id and checksum say which diff it was written on. */
export function branchAnchorInput(snapshot: Pick<TaskBranchSnapshot, "id" | "repository" | "checksum">,
    range: { path: string; side: "old" | "new"; startLine: number; endLine: number }): FeedbackAnchorInput {
  return { kind: "code", scope: "branch", snapshotId: snapshot.id, repository: snapshot.repository, path: range.path, side: range.side,
    startLine: range.startLine, endLine: range.endLine, diffChecksum: snapshot.checksum };
}

/** A branch item on the snapshot shown: under a line, or at its file's head (outdated, unchanged). */
export interface PlacedComment {
  readonly item: FeedbackItem;
  readonly path: string;
  readonly at: { side: "old" | "new"; startLine: number; endLine: number } | { head: "outdated" | "unchanged" };
}

/**
 * The attempt's branch items on this repository, each where it sits on the
 * snapshot shown: the server's placement when it gave one, else the same
 * rule (`relocateCodeAnchor`) here, for a draft saved since the last read.
 * An item whose file is not in the snapshot is left out.
 */
export function placeBranchItems(items: ReadonlyArray<FeedbackItem>, diff: Pick<TaskBranchDiff, "placements"> & { snapshot: TaskBranchSnapshot }): PlacedComment[] {
  const snap = diff.snapshot;
  const server = new Map(diff.placements.map((p) => [p.itemId, p.at]));
  const viewed = { snapshotId: snap.id, attempt: snap.attempt, repository: snap.repository, checksum: snap.checksum, files: snap.files };
  const out: PlacedComment[] = [];
  for (const item of items) {
    const a = item.anchor;
    if (a.kind !== "code" || !isBranchAnchor(a) || a.repo !== snap.repository) continue;
    const at: RelocatedAnchor = server.get(item.id) ?? relocateCodeAnchor(a, viewed);
    if (at.kind === "absent") continue;
    const path = ("path" in at && at.path) || a.path;
    if (at.kind === "lines" && !item.outdated) out.push({ item, path, at: { side: at.side, startLine: at.startLine, endLine: at.endLine } });
    else out.push({ item, path, at: { head: at.kind === "unchanged" ? "unchanged" : "outdated" } });
  }
  return out;
}

/** Each file's comments in the order the mark visits them: those at the file's head first, then by line. */
export function commentsByFile(placed: ReadonlyArray<PlacedComment>): Map<string, PlacedComment[]> {
  const by = new Map<string, PlacedComment[]>();
  for (const p of placed) by.set(p.path, [...(by.get(p.path) ?? []), p]);
  const line = (p: PlacedComment) => ("head" in p.at ? -1 : p.at.startLine);
  for (const list of by.values()) list.sort((a, b) => line(a) - line(b) || a.item.createdAt.localeCompare(b.item.createdAt));
  return by;
}

/** The mark's tooltip, in words: "2 submitted by Ana Silva, Tom Berg · 1 your draft · 1 outdated". */
export function marksLabel(list: ReadonlyArray<PlacedComment>, you: string | null): string {
  const submitted = list.filter((p) => p.item.status !== "draft");
  const theirs = submitted.filter((p) => p.item.author?.id !== you);
  const yours = submitted.length - theirs.length;
  const drafts = list.length - submitted.length;
  const outdated = list.filter((p) => "head" in p.at && p.at.head === "outdated").length;
  const names = [...new Set(theirs.map((p) => p.item.author?.name ?? "someone"))];
  return [
    theirs.length ? `${theirs.length} submitted by ${names.join(", ")}` : null,
    yours ? `${yours} submitted by you` : null,
    drafts ? `${drafts} your ${drafts === 1 ? "draft" : "drafts"}` : null,
    outdated ? `${outdated} outdated · lines changed since` : null,
  ].filter(Boolean).join(" · ");
}

/** The next comment's index after `at` (none yet: the first), wrapping after the last. */
export const nextComment = (at: number | undefined, count: number): number => (at === undefined ? 0 : (at + 1) % count);
