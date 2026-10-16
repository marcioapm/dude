/**
 * People's feedback on a task, as the web app holds it: the attempt's
 * items and the reader's drafts (`GET /v1/tasks/:id/feedback`, read again
 * whenever the task page is and after each of the reader's own actions),
 * submitting a batch with a key that makes a retry the same batch, and
 * where a batch submitted now would go.
 *
 * The API decides who sees what: a draft is listed to its author alone, so
 * nothing here hides or adds anyone's drafts.
 */

import { createContext, useCallback, useContext, useEffect, useRef, useState, type ReactNode } from "react";
import { AgentAvatar, PersonAvatar, type FeedbackCardItem, type FeedbackThreadEntry } from "@dude/design-system/components";
import { firstName } from "@dude/design-system";
import {
  FEEDBACK_LIMITS, NOT_STARTED_STATUSES, anchorFile, isConductor, relocateCodeAnchor, runDoer, runLabel,
  type FeedbackAnchorInput, type FeedbackCodeAnchor, type FeedbackItem, type FeedbackList, type RunDiff, type TaskStatus,
} from "@dude/domain";
import type { ApiClient, FeedbackSubmitted, Run, TaskDetail } from "./api/client.ts";
import type { People } from "./people.tsx";

const EMPTY_FEEDBACK: FeedbackList = { feedback: [], blocking: 0 };
const LIVE_RUN: ReadonlySet<string> = new Set(["pending", "scheduled", "starting", "running", "paused"]);

/**
 * The attempt's feedback, read on mount, when `version` changes (the task
 * page's re-read on its event stream) and after the reader's own changes.
 * A slower, older read never replaces a newer one.
 */
export function useTaskFeedback(client: ApiClient, taskId: string, attempt: number | undefined, version: number) {
  const [list, setList] = useState<FeedbackList | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const reads = useRef(0);
  const read = useCallback(async () => {
    const seq = ++reads.current;
    try {
      const got = await client.feedback(taskId, attempt);
      if (seq === reads.current) {
        // The same answer keeps its identity: what draws it is not redrawn for a read that changed nothing.
        setList((was) => (was && JSON.stringify(was) === JSON.stringify(got) ? was : got));
        setProblem(null);
      }
    } catch (err) {
      if (seq === reads.current) setProblem(err instanceof Error ? err.message : String(err));
    }
  }, [client, taskId, attempt]);
  useEffect(() => {
    void read();
  }, [read, version]);
  return { list: list ?? EMPTY_FEEDBACK, loaded: list !== null, problem, reload: read };
}

/**
 * Submitting drafts as one batch. The key is made for a set of drafts and
 * kept until that set is taken: a submit that failed (or whose answer was
 * lost) is sent again with the same key, which the API answers with the
 * same batch rather than a second one. Other drafts get a new key.
 */
export class Submitter {
  #pending: { ids: string; key: string } | null = null;
  constructor(private readonly client: Pick<ApiClient, "submitFeedback">, private readonly newKey: () => string = () => crypto.randomUUID()) {}

  async submit(taskId: string, draftIds: ReadonlyArray<string>): Promise<FeedbackSubmitted> {
    const ids = [...draftIds].sort().join(",");
    if (this.#pending?.ids !== ids) this.#pending = { ids, key: this.newKey() };
    const out = await this.client.submitFeedback(taskId, draftIds, this.#pending.key);
    this.#pending = null;
    return out;
  }
}

/** Where a batch submitted now would go, from what the task says of itself. */
export interface Destination {
  /** After "Goes to": "the fixer at work". */
  readonly who: string;
  /** The confirmation's sentence and what follows it. */
  readonly line: string;
  readonly detail: string;
}

/**
 * The orchestrator's routing (routeFeedbackTx) as the task shows it: the
 * conductor when it decides; else the current attempt's implementer or
 * fixer at work; else the delivery — starting one for a task not started,
 * waiting for its blockers when it has open ones.
 */
export function feedbackDestination(task: Pick<TaskDetail, "decider" | "status" | "runs">, current: number, openBlockers: number): Destination {
  if (task.decider === "conductor" && !NOT_STARTED_STATUSES.includes(task.status)) {
    return { who: "the conductor", line: "They go to the conductor.",
      detail: "It decides what to do with them, with you, in Chat. Each stays open until an agent resolves or answers it." };
  }
  // Live as the orchestrator's workingRunSQL counts it (liveRun): a paused or starting one hears it when it goes on.
  const working = task.runs.filter((r) => r.attempt === current && (r.phase === "implement" || r.phase === "fix") && LIVE_RUN.has(r.status))
    .sort((a, b) => b.createdAt.localeCompare(a.createdAt))[0];
  if (working) {
    const doer = runDoer(working);
    return { who: `the ${doer} at work`, line: `The ${doer} at work gets them at its next step.`,
      detail: "Nothing is interrupted: it reads them before its next tool call, and resolves or answers each." };
  }
  if (NOT_STARTED_STATUSES.includes(task.status)) {
    return openBlockers > 0
      ? { who: "the task's first agent", line: "They wait for the task's blockers.",
        detail: "Submitting starts its delivery, which waits for what it is blocked by; its first agent is given them." }
      : { who: "a new delivery", line: "Nothing is running: they start the task's delivery.", detail: "Its first agent is given them." };
  }
  return { who: "Deliver's next fix", line: "Deliver's next fix takes them up.",
    detail: "Nothing is at work on the code now: the delivery gives them to the next fixer it starts." };
}

/** Why a submit would be refused now, in the orchestrator's words (refuseEnded), or null. */
export function submitRefusal(status: TaskStatus): string | null {
  if (status === "done") return "This task is done: feedback on it goes in a follow-up task. Your drafts are kept.";
  if (status === "failed" || status === "aborted") return `This task is ${status}: recover it first, then submit. Your drafts are kept.`;
  return null;
}

/** What a batch was routed to, in words: on its items' cards and in Activity. */
export const ROUTED_WORDS: Readonly<Record<string, string>> = { conductor: "to the conductor", steer: "to the agent at work", workflow: "to Deliver" };

/** An item as the card draws it. */
export function cardItem(item: FeedbackItem, people: People, runs: ReadonlyMap<string, Run>): FeedbackCardItem {
  const known = item.author ? people.byId.get(item.author.id) : undefined;
  const delivered = item.deliveredRunId ? runs.get(item.deliveredRunId) : undefined;
  return {
    id: item.id, status: item.status, resolutionPending: item.resolutionPending, resolvedCommit: item.resolvedCommit,
    body: item.body, anchor: item.anchor, author: item.author ? { ...(known ?? {}), id: item.author.id, name: item.author.name } : null,
    at: item.submittedAt ?? item.createdAt, routed: item.routedTo ? ROUTED_WORDS[item.routedTo] : undefined, withWhom: delivered ? runLabel(delivered) : undefined,
  };
}

/** An item's history as a thread: a person by their face and first name, an agent by its role. */
export function threadOf(item: FeedbackItem, people: People, runs: ReadonlyMap<string, Run>): FeedbackThreadEntry[] {
  return item.history.map((h) => {
    const entry = { id: h.id, kind: h.kind, body: h.body, commit: h.commit, at: h.createdAt };
    if (h.personId) {
      const p = people.byId.get(h.personId);
      const name = p?.name ?? people.names.get(h.personId) ?? "Someone";
      return { ...entry, who: firstName(name), face: <PersonAvatar person={{ ...(p ?? {}), id: h.personId, name }} size={20} /> };
    }
    const run = h.runId ? runs.get(h.runId) : undefined;
    return { ...entry, who: `The ${run ? runDoer(run) : "agent"}`, face: <AgentAvatar role={run?.role ?? "implementer"} size="sm" /> };
  });
}

/**
 * A code draft's anchor to send when it is written again: on its lines in
 * `diff` (a simple shift, or the diff it was written on) with that diff's
 * checksum; otherwise as kept, whose checksum the API refuses once the
 * diff has moved — never re-quoted at numbers whose text changed.
 */
export function draftAnchorInput(anchor: FeedbackCodeAnchor, diff: Pick<RunDiff, "checksum" | "files"> | null): FeedbackAnchorInput {
  const kept = { kind: "code" as const, runId: anchor.runId, path: anchor.path, side: anchor.side, startLine: anchor.startLine,
    endLine: anchor.endLine, diffChecksum: anchor.diffChecksum };
  if (!diff) return kept;
  const at = relocateCodeAnchor(anchor, { runId: anchor.runId, checksum: diff.checksum, files: diff.files });
  if (at.kind === "lines") return { ...kept, path: at.path, side: at.side, startLine: at.startLine, endLine: at.endLine, diffChecksum: diff.checksum };
  const file = anchorFile(anchor, diff.files);
  return file ? { ...kept, path: file.path } : kept;
}

/** The task's runs by id, for names in threads and statuses. */
export const runsOf = (task: Pick<TaskDetail, "runs"> | null): ReadonlyMap<string, Run> => new Map((task?.runs ?? []).map((r) => [r.id, r]));

/** A Run whose diff can take a new comment: an agent's, of the current attempt, not the conductor's. */
export const commentableRun = (run: Run | undefined, current: number): boolean =>
  run !== undefined && run.kind !== "preview" && !isConductor(run) && run.attempt === current;

export interface ViewedRunDiff {
  readonly runId: string;
  readonly diff: Pick<RunDiff, "checksum" | "files">;
}

export const FEEDBACK_FULL = `You have ${FEEDBACK_LIMITS.drafts} drafts on this task, the most kept. Submit or delete some to write more.`;

/**
 * What a Run's Changes needs to show and take comments: given by the task
 * page, so its open session's diff draws the attempt's items and the
 * reader's drafts without reading them again.
 */
export interface FeedbackOnDiff {
  readonly taskId: string;
  readonly items: ReadonlyArray<FeedbackItem>;
  readonly current: number;
  /** New comments refused: why (a done task, an earlier attempt); null when they are taken. */
  readonly closed: string | null;
  /** Actions on submitted items refused: an earlier attempt's, a done task's. */
  readonly readOnly: boolean;
  readonly full: boolean;
  readonly runs: ReadonlyMap<string, Run>;
  readonly people: People;
  readonly createDraft: (anchor: FeedbackAnchorInput, body: string) => Promise<unknown>;
  /** `viewed`: the Run's Changes the edit was made on, if any (not necessarily the draft's own Run). */
  readonly saveDraft: (item: FeedbackItem, body: string, viewed: ViewedRunDiff | null) => Promise<unknown>;
  readonly deleteDraft: (id: string) => Promise<unknown>;
  readonly reply: (id: string, body: string) => Promise<unknown>;
  readonly resolve: (id: string) => Promise<unknown>;
  readonly reopen: (id: string) => Promise<unknown>;
  /** An item to bring into view in its Run's Changes, once (a jump from the tray or the tab). */
  readonly focus: { readonly itemId: string; readonly runId: string; readonly n: number } | null;
  readonly openOnTab: (itemId: string) => void;
}

const FeedbackContext = createContext<FeedbackOnDiff | null>(null);
// The jump alone, apart: the open session (memoised) changes view for it and re-renders for nothing else.
const FocusContext = createContext<FeedbackOnDiff["focus"]>(null);
export const useFeedbackOnDiff = () => useContext(FeedbackContext);
export const useFeedbackFocus = () => useContext(FocusContext);
export function FeedbackProvider({ value, children }: { readonly value: FeedbackOnDiff | null; readonly children: ReactNode }) {
  return <FeedbackContext.Provider value={value}><FocusContext.Provider value={value?.focus ?? null}>{children}</FocusContext.Provider></FeedbackContext.Provider>;
}
