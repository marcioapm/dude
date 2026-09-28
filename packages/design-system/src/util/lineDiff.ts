/**
 * A line diff between two texts, as the hunks `DiffView` draws: what a
 * prompt's version changed from the one before it. Longest common
 * subsequence over lines — prompts are hundreds of lines, not thousands,
 * so the quadratic table is fine — with three lines of context round each
 * change, as `git diff` shows it.
 */

import type { DiffHunk, DiffLine } from "../components/DiffView.tsx";

export interface LineDiff {
  readonly hunks: DiffHunk[];
  readonly additions: number;
  readonly deletions: number;
}

const CONTEXT = 3;

export function lineDiff(before: string, after: string): LineDiff {
  const a = before ? before.split("\n") : [];
  const b = after ? after.split("\n") : [];
  // lcs[i][j]: the longest common subsequence of a[i..] and b[j..].
  const lcs = Array.from({ length: a.length + 1 }, () => new Uint32Array(b.length + 1));
  for (let i = a.length - 1; i >= 0; i--) {
    for (let j = b.length - 1; j >= 0; j--) {
      lcs[i]![j] = a[i] === b[j] ? lcs[i + 1]![j + 1]! + 1 : Math.max(lcs[i + 1]![j]!, lcs[i]![j + 1]!);
    }
  }
  const lines: DiffLine[] = [];
  let i = 0;
  let j = 0;
  while (i < a.length || j < b.length) {
    if (i < a.length && j < b.length && a[i] === b[j]) {
      lines.push({ kind: "context", text: a[i]!, oldNo: ++i, newNo: ++j });
    } else if (i < a.length && (j >= b.length || lcs[i + 1]![j]! >= lcs[i]![j + 1]!)) {
      // A replaced line reads removed, then added, as git shows it.
      lines.push({ kind: "del", text: a[i]!, oldNo: ++i });
    } else {
      lines.push({ kind: "add", text: b[j]!, newNo: ++j });
    }
  }

  // Hunks: each change with its context, joined where the context meets.
  const changed = lines.map((l, n) => (l.kind !== "context" ? n : -1)).filter((n) => n >= 0);
  const hunks: DiffHunk[] = [];
  let start = -1;
  let end = -1;
  const flush = () => {
    if (start < 0) return;
    const body = lines.slice(start, end + 1);
    const first = body[0]!;
    const oldStart = first.oldNo ?? (body.find((l) => l.oldNo)?.oldNo ?? 1);
    const newStart = first.newNo ?? (body.find((l) => l.newNo)?.newNo ?? 1);
    const oldCount = body.filter((l) => l.kind !== "add").length;
    const newCount = body.filter((l) => l.kind !== "del").length;
    hunks.push({ header: `@@ -${oldStart},${oldCount} +${newStart},${newCount} @@`, lines: body });
  };
  for (const n of changed) {
    const from = Math.max(0, n - CONTEXT);
    if (start >= 0 && from <= end + 1) {
      end = Math.min(lines.length - 1, n + CONTEXT);
      continue;
    }
    flush();
    start = from;
    end = Math.min(lines.length - 1, n + CONTEXT);
  }
  flush();
  return {
    hunks,
    additions: lines.filter((l) => l.kind === "add").length,
    deletions: lines.filter((l) => l.kind === "del").length,
  };
}
