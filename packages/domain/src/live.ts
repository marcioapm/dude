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
 * included. `GET /v1/runs/:id/diff`.
 */
export interface RunDiff {
  base: string;
  files: RunDiffFile[];
  /** Identifies the diff: a summary with the same checksum is this one. */
  checksum: string;
  /** Left as the container stopped (lux's beforeStop hook), not read live. */
  final: boolean;
  updatedAt: string;
}

/**
 * The `run.diff.updated` payload: which files changed and how much, with
 * no lines — the ledger stays small; the lines are `GET /v1/runs/:id/diff`.
 */
export interface RunDiffSummary {
  checksum: string;
  updatedAt: string;
  final: boolean;
  files: Array<Pick<RunDiffFile, "path" | "status" | "additions" | "deletions">>;
}

/** A cost as its two halves: model tokens and machine time. */
export interface CostSplit {
  totalUsd: number;
  tokensUsd: number;
  machineUsd: number;
  /** Who priced each half, when known (a Run's, a task's). */
  origin?: CostOrigin;
}

/**
 * Who priced a cost's halves: lux's cost plugins, or the agent's harness
 * and dude's machine-rate estimate. `settled`: lux has made every lux
 * figure in it final. A total over several Runs is from lux only if every
 * Run's is.
 */
export interface CostOrigin {
  tokens: "lux" | "agent";
  machine: "lux" | "estimate";
  settled: boolean;
}

/** The split of a tokens cost and a machine cost. */
export function costSplit(tokensUsd: number, machineUsd: number, origin?: CostOrigin): CostSplit {
  return { totalUsd: tokensUsd + machineUsd, tokensUsd, machineUsd, ...(origin ? { origin } : {}) };
}
