/**
 * Live work: what a running agent's checkout holds now, and what work
 * costs. Shared by the API and the web app; the orchestrator writes the
 * same shapes (orchestrator/internal/phases/diff.go).
 */

/** One line of a hunk: context, added or removed, with its numbers on each side. */
export interface RunDiffLine {
  kind: " " | "+" | "-";
  old: number | null;
  new: number | null;
  text: string;
}

export interface RunDiffHunk {
  header: string;
  lines: RunDiffLine[];
}

export interface RunDiffFile {
  path: string;
  /** Modified, added (untracked files too), deleted, renamed. */
  status: "M" | "A" | "D" | "R";
  additions: number;
  deletions: number;
  hunks: RunDiffHunk[];
  /** Lines were left out of a very large file; the counts are whole. */
  truncated?: boolean;
  binary?: boolean;
}

/**
 * A Run's checkout against the commit it started from, uncommitted work
 * included. `GET /v1/runs/:id/diff`, and the `run.diff.updated` payload.
 */
export interface RunDiff {
  base: string;
  files: RunDiffFile[];
  updatedAt: string;
}

/** A cost as its two halves: model tokens and machine time. */
export interface CostSplit {
  totalUsd: number;
  tokensUsd: number;
  machineUsd: number;
}

/** The split of a tokens cost and a machine cost. */
export function costSplit(tokensUsd: number, machineUsd: number): CostSplit {
  return { totalUsd: tokensUsd + machineUsd, tokensUsd, machineUsd };
}
