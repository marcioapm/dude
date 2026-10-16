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

import type { FeedbackCodeAnchor } from "./feedback.ts";
import type { RunDiffFile, RunDiffLine } from "./live.ts";

export type CodeSide = "old" | "new";

export type RelocatedAnchor =
  | { readonly kind: "lines"; readonly path: string; readonly side: CodeSide; readonly startLine: number; readonly endLine: number }
  | { readonly kind: "outdated"; readonly path: string | null; readonly reason: OutdatedReason }
  | { readonly kind: "unchanged"; readonly path: string }
  /** Its file is not in this diff at all: nothing to place. */
  | { readonly kind: "absent" };

export type OutdatedReason = "changed" | "ambiguous" | "renamed" | "removed";

export interface ViewedDiff {
  readonly runId: string;
  readonly checksum: string;
  readonly files: ReadonlyArray<RunDiffFile>;
}

type Anchor = Pick<FeedbackCodeAnchor, "runId" | "repo" | "path" | "side" | "startLine" | "endLine" | "diffChecksum" | "excerpt">;

const onSide = (l: Pick<RunDiffLine, "kind">, side: CodeSide) => (side === "old" ? l.kind !== "+" : l.kind !== "-");
const numberOn = (l: Pick<RunDiffLine, "old" | "new">, side: CodeSide) => (side === "old" ? l.old : l.new);

/**
 * The diff's file an anchor names. With one repository the diff names a
 * file by its path; with several, `<checkout name>/<path>`, where the
 * checkout name is the repository's own when it is a simple lowercase name
 * (lux_name, migration 093). A hashed checkout name is matched only when
 * exactly one repository's file has that path.
 */
export function anchorFile(anchor: Pick<FeedbackCodeAnchor, "repo" | "path">, files: ReadonlyArray<RunDiffFile>): RunDiffFile | undefined {
  const exact = files.find((f) => f.path === anchor.path) ?? files.find((f) => f.path === `${anchor.repo}/${anchor.path}`);
  if (exact) return exact;
  const under = files.filter((f) => {
    const slash = f.path.indexOf("/");
    return slash > 0 && f.path.slice(slash + 1) === anchor.path;
  });
  return under.length === 1 ? under[0] : undefined;
}

/** The anchor's own lines on its side, as written: the text the rule looks for. */
export function anchoredText(anchor: Pick<FeedbackCodeAnchor, "side" | "excerpt">): string[] {
  return anchor.excerpt.filter((l) => onSide(l, anchor.side)).map((l) => l.text);
}

/** Where `anchor` sits in `diff`; see the module comment for the rule. */
export function relocateCodeAnchor(anchor: Anchor, diff: ViewedDiff): RelocatedAnchor {
  const file = anchorFile(anchor, diff.files);
  if (!file) return { kind: "absent" };
  const { side } = anchor;
  // On the very diff it was written on, its lines are where they were.
  if (anchor.runId === diff.runId && anchor.diffChecksum === diff.checksum) {
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
