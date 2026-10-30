/**
 * Where a code feedback item sits in a diff other than the one it was
 * written on.
 *
 * A code anchor keeps the lines it was written on (its excerpt) as the
 * Run's diff showed them. In a later diff those lines follow the code only
 * on a simple shift: their text, exact and contiguous, appears exactly once
 * among the diff's lines on the anchor's side. Anything else is outdated,
 * kept with its original excerpt; the item is never attached to lines
 * whose text differs. A diff shows only hunks, so lines outside every hunk
 * cannot be read: an anchor whose own range falls wholly outside them is
 * `unchanged` (not shown by this diff), not outdated and not placed.
 */

import type { FeedbackBranchAnchor, FeedbackCodeAnchor } from "./feedback.ts";
import type { RunDiffFile, RunDiffLine } from "./live.ts";

export type CodeSide = "old" | "new";

export type RelocatedAnchor =
  | { readonly kind: "lines"; readonly path: string; readonly side: CodeSide; readonly startLine: number; readonly endLine: number }
  | { readonly kind: "outdated"; readonly path: string | null; readonly reason: OutdatedReason }
  | { readonly kind: "unchanged"; readonly path: string }
  /** Its file is not in this diff at all: nothing to place. */
  | { readonly kind: "absent" };

export type OutdatedReason = "changed" | "ambiguous" | "renamed" | "removed";

/** A Run's diff, as a session's Changes shows it. */
export interface ViewedRunDiff {
  readonly runId: string;
  readonly checksum: string;
  readonly files: ReadonlyArray<RunDiffFile>;
}

/** A task branch snapshot (`GET /v1/tasks/:id/branch-diff`): one repository of one attempt. */
export interface ViewedBranchDiff {
  readonly snapshotId: string;
  readonly attempt: number;
  /** The snapshot's repository, by name. */
  readonly repository: string;
  readonly checksum: string;
  readonly files: ReadonlyArray<RunDiffFile>;
}

export type ViewedDiff = ViewedRunDiff | ViewedBranchDiff;

type Lines = "repo" | "path" | "side" | "startLine" | "endLine" | "diffChecksum" | "excerpt";
type Anchor = Pick<FeedbackCodeAnchor, "runId" | "scope" | Lines> | Pick<FeedbackBranchAnchor, "scope" | "snapshotId" | "attempt" | Lines>;

/**
 * Whether `diff` is the very one `anchor` was written on, or one its lines
 * may be followed into (`later`). A Run's anchor follows into Run diffs; a
 * branch anchor into the snapshots of its own attempt and repository. Any
 * other pairing places nothing (the item is not on this diff).
 */
function pairing(anchor: Anchor, diff: ViewedDiff): "same" | "later" | null {
  if (anchor.scope === "branch") {
    if (!("snapshotId" in diff)) return null;
    if (anchor.snapshotId === diff.snapshotId && anchor.diffChecksum === diff.checksum) return "same";
    return anchor.attempt === diff.attempt && anchor.repo === diff.repository ? "later" : null;
  }
  if (!("runId" in diff)) return null;
  return anchor.runId === diff.runId && anchor.diffChecksum === diff.checksum ? "same" : "later";
}

const onSide = (l: Pick<RunDiffLine, "kind">, side: CodeSide) => (side === "old" ? l.kind !== "+" : l.kind !== "-");
const numberOn = (l: Pick<RunDiffLine, "old" | "new">, side: CodeSide) => (side === "old" ? l.old : l.new);

// lux's rule for a repository's name (lux.NameRe); any other name is checked out rewritten.
const LUX_NAME = /^[a-z0-9][a-z0-9_-]{0,31}$/;
const HASH_SUFFIX = /^[0-9a-f]{8}$/;

/**
 * Whether `segment` is `repo` as lux.SpecName / lux_name (migration 093)
 * rewrites a name lux refuses: lowercased, other characters as '-',
 * leading '-' and '_' dropped, cut to 23, then '-' and 8 hex digits of
 * SHA-256. The 8 hex digits are matched by form, not recomputed: no
 * synchronous SHA-256 is available here. Migration 094 makes checkout
 * names unique per project, so no two repositories share a directory. The
 * misread left: another repository of the project checked out as the same
 * base plus 8 hex digits, either one literally named that (`web-1234abcd`
 * beside `Web`) or one lux also rewrites to that base (`WEB` beside `Web`),
 * when only that one changed the path.
 */
function isHashedCheckout(segment: string, repo: string): boolean {
  if (LUX_NAME.test(repo)) return false;
  let mapped = "";
  for (const c of repo) mapped += /^[A-Z]$/.test(c) ? c.toLowerCase() : /^[a-z0-9_-]$/.test(c) ? c : "-";
  const base = mapped.replace(/^[-_]+/, "").slice(0, 23);
  const suffix = base === "" ? segment : segment.startsWith(`${base}-`) ? segment.slice(base.length + 1) : null;
  return suffix !== null && HASH_SUFFIX.test(suffix);
}

/**
 * The diff's file an anchor names. With one repository the diff names a
 * file by its path; with several, `<checkout name>/<path>`: the
 * repository's own name when lux takes it, else its hashed rewrite.
 */
export function anchorFile(anchor: Pick<FeedbackCodeAnchor, "repo" | "path">, files: ReadonlyArray<RunDiffFile>): RunDiffFile | undefined {
  const exact = files.find((f) => f.path === anchor.path) ?? files.find((f) => f.path === `${anchor.repo}/${anchor.path}`);
  if (exact) return exact;
  const under = files.filter((f) => {
    const slash = f.path.indexOf("/");
    return slash > 0 && f.path.slice(slash + 1) === anchor.path && isHashedCheckout(f.path.slice(0, slash), anchor.repo);
  });
  return under.length === 1 ? under[0] : undefined;
}

/** The anchor's own lines on its side, as written: the text the rule looks for. */
export function anchoredText(anchor: Pick<FeedbackCodeAnchor, "side" | "excerpt">): string[] {
  return anchor.excerpt.filter((l) => onSide(l, anchor.side)).map((l) => l.text);
}

/**
 * Where `anchor` sits in `diff`; see the module comment for the rule. A
 * branch anchor (`scope: "branch"`) is keyed by its snapshot as a Run's is
 * by its Run: on its own snapshot (id and checksum) it is where it was; on
 * another snapshot of the same attempt and repository its lines follow by
 * the same rule; on anything else it is absent.
 */
export function relocateCodeAnchor(anchor: Anchor, diff: ViewedDiff): RelocatedAnchor {
  const pair = pairing(anchor, diff);
  if (!pair) return { kind: "absent" };
  const file = anchorFile(anchor, diff.files);
  if (!file) return { kind: "absent" };
  const { side } = anchor;
  // On the very diff it was written on, its lines are where they were.
  if (pair === "same") {
    return { kind: "lines", path: file.path, side, startLine: anchor.startLine, endLine: anchor.endLine };
  }
  if (file.status === "R") return { kind: "outdated", path: file.path, reason: "renamed" };
  if (file.status === "D") return { kind: "outdated", path: file.path, reason: "removed" };
  const wanted = anchoredText(anchor);
  if (wanted.length === 0) return { kind: "outdated", path: file.path, reason: "changed" };

  const found: Array<{ start: number; end: number }> = [];
  let overlaps = false;
  for (const hunk of file.hunks) {
    // One side of a hunk is a contiguous run of that side's lines.
    const lines = hunk.lines.filter((l) => onSide(l, side));
    const first = lines[0] ? numberOn(lines[0], side) : null;
    const last = lines.at(-1) ? numberOn(lines.at(-1)!, side) : null;
    if (first !== null && last !== null && anchor.startLine <= last && anchor.endLine >= first) overlaps = true;
    for (let i = 0; i + wanted.length <= lines.length; i++) {
      if (wanted.every((text, k) => lines[i + k]!.text === text)) {
        found.push({ start: numberOn(lines[i]!, side)!, end: numberOn(lines[i + wanted.length - 1]!, side)! });
      }
    }
  }
  if (found.length === 1) return { kind: "lines", path: file.path, side, startLine: found[0]!.start, endLine: found[0]!.end };
  if (found.length > 1) return { kind: "outdated", path: file.path, reason: "ambiguous" };
  if (!overlaps) return { kind: "unchanged", path: file.path };
  return { kind: "outdated", path: file.path, reason: "changed" };
}
