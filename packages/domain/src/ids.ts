/**
 * Branded ID types.
 *
 * Every integration-specific ID is stored alongside an internal stable ID
 * (plan §31), so the internal IDs are the ones that appear in the domain.
 */

declare const brand: unique symbol;

type Brand<T, B extends string> = T & { readonly [brand]: B };

export type OrganizationId = Brand<string, "OrganizationId">;
export type UserId = Brand<string, "UserId">;
export type ProjectId = Brand<string, "ProjectId">;
export type EpicId = Brand<string, "EpicId">;
export type WorkItemId = Brand<string, "WorkItemId">;
export type RunId = Brand<string, "RunId">;
export type SessionId = Brand<string, "SessionId">;
export type EventId = Brand<string, "EventId">;
export type WorkerId = Brand<string, "WorkerId">;
export type RuntimeInstanceId = Brand<string, "RuntimeInstanceId">;
export type ArtifactId = Brand<string, "ArtifactId">;
export type QuestionId = Brand<string, "QuestionId">;
export type RepositoryId = Brand<string, "RepositoryId">;
export type WorkflowRunId = Brand<string, "WorkflowRunId">;

/** Prefixes make IDs self-describing in logs, events and URLs. */
export const ID_PREFIXES = {
  organization: "org",
  user: "usr",
  project: "prj",
  epic: "epc",
  workItem: "wi",
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

export function isId<K extends IdKind>(kind: K, value: string): boolean {
  return value.startsWith(`${ID_PREFIXES[kind]}_`);
}
