/**
 * Prefixed, sortable identifiers.
 *
 * Every integration-specific ID is stored alongside an internal stable ID
 * (plan §31), so the internal IDs are the ones that appear in the domain.
 */

/** Prefixes make IDs self-describing in logs, events and URLs. */
export const ID_PREFIXES = {
  organization: "org",
  user: "usr",
  project: "prj",
  epic: "epc",
  task: "wi",
  run: "run",
  session: "ses",
  event: "evt",
  worker: "wrk",
  runtimeInstance: "rti",
  artifact: "art",
  question: "qst",
  repository: "repo",
  workflowRun: "wfr",
  workflowSignal: "sig",
  apiKey: "key",
  person: "per",
  directive: "dir",
  pullRequest: "pr",
  forgeCredential: "forge",
  finding: "find",
  promptVersion: "pv",
  memory: "mem",
} as const;

export type IdKind = keyof typeof ID_PREFIXES;

/**
 * Generate a prefixed, lexicographically sortable ID.
 *
 * The timestamp prefix means IDs sort by creation order, which keeps
 * index locality good for the append-heavy tables (events above all).
 */
export function newId<K extends IdKind>(kind: K): string {
  const ts = Date.now().toString(36).padStart(9, "0");
  const rand = crypto.randomUUID().replaceAll("-", "").slice(0, 16);
  return `${ID_PREFIXES[kind]}_${ts}${rand}`;
}

