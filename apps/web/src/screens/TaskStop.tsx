/**
 * One Stop per task: the header's Stop and the `…` menu's Stop… open the
 * same dialog, which stops every live Run of the task, any attempt's (conductor and
 * Deliver alike) — Stop here, kept for a resume — or stops it and starts
 * the next attempt, decided by whoever the reader picks (DeciderField,
 * defaulting to who decided this one). Owner or admin; anyone else sees
 * it disabled with whose decision it is.
 */

import { useId, useState, type ReactNode } from "react";
import { DeciderField, LiveWork, liveWords, type LiveItem, type NextDecider } from "@dude/design-system/components";
import { Button, ChoiceList, Dialog, Textarea, Tooltip, type ChoiceOption } from "@dude/design-system/primitives";
import type { RowMenuItem } from "@dude/design-system/primitives";
import { NOT_STARTED_STATUSES, PHASE_DOER, TERMINAL_RUN_STATUSES, isConductor, reviewerCategoryInSentence } from "@dude/domain";
import type { ApiClient, PullRequest, Run, TaskDetail } from "../api/client.ts";
import { errorText } from "../hooks/useSave.tsx";

/** Between its start and its end: what Stop stops. */
export function inProgress(task: Pick<TaskDetail, "status">): boolean {
  return !NOT_STARTED_STATUSES.includes(task.status) && !["done", "aborted", "failed"].includes(task.status);
}

const cap = (s: string) => s.charAt(0).toUpperCase() + s.slice(1);

/** A live Run as LiveWork shows it: who, what it is doing, and whether it is at work. */
function liveItem(run: Run, task: Pick<TaskDetail, "awaitingDecision">): LiveItem {
  const role = run.role ?? "implementer";
  if (isConductor(run)) {
    const deciding = task.awaitingDecision !== null;
    return { id: run.id, role, who: "Conductor", doing: deciding ? "deciding" : run.status === "paused" ? "between turns" : "talking",
      state: run.status === "running" ? "working" : "waiting" };
  }
  const doer = run.phase ? (PHASE_DOER[run.phase] ?? run.phase) : "agent";
  const queued = run.status === "pending" || run.status === "scheduled";
  return { id: run.id, role, who: cap(doer), doing: run.category ? reviewerCategoryInSentence(run.category) : run.phase ?? "",
    state: queued ? "queued" : "working" };
}

/** The task's live Runs: its conductor's first, then the current attempt's, oldest first. */
export function liveItemsOf(task: Pick<TaskDetail, "runs" | "awaitingDecision">): LiveItem[] {
  const live = task.runs.filter((r) => r.kind !== "preview" && !TERMINAL_RUN_STATUSES.includes(r.status));
  return [...live].sort((a, b) => Number(isConductor(b)) - Number(isConductor(a)) || a.createdAt.localeCompare(b.createdAt))
    .map((r) => liveItem(r, task));
}

/** "The conductor and 2 reviewers are working; a fixer is queued." */
export function liveSummary(items: readonly LiveItem[]): string {
  const working = items.filter((i) => i.state !== "queued");
  const queued = items.filter((i) => i.state === "queued").map((i) => ({ ...i, state: undefined }));
  if (items.length === 0) return "Nothing is running now: the delivery waits.";
  const are = (n: number) => (n === 1 ? "is" : "are");
  const first = working.length ? `${cap(liveWords(working))} ${are(working.length)} working` : "";
  const then = queued.length ? `${liveWords(queued)} ${are(queued.length)} queued` : "";
  return `${first && then ? `${first}; ${then}` : cap(first || then)}.`;
}

/** The task's open pull request, and who opened it: a task started from one never closes it. */
export interface OpenPullRequest {
  readonly number: number;
  readonly openedBy: "dude" | "someone";
}

export function openPullRequestOf(task: Pick<TaskDetail, "source">, prs: readonly PullRequest[]): OpenPullRequest | undefined {
  const pr = prs.find((p) => p.state === "open" || p.state === "draft");
  if (!pr) return undefined;
  const adopted = task.source?.kind === "pr" && task.source.number === pr.number;
  return { number: pr.number, openedBy: adopted ? "someone" : "dude" };
}

/** The `…` menu's Stop…, first: danger, disabled for anyone who may not. */
export function stopMenuItem(onStop: () => void, notYours: string | undefined): RowMenuItem {
  return { id: "stop", label: "Stop…", icon: "stop", tone: "danger", onSelect: onStop,
    description: "Stop it here, or stop it and start a new attempt", disabled: Boolean(notYours), disabledReason: notYours };
}

/** The header's Stop: quiet, danger ink, its glyph; it opens the dialog. Refused, it stays reachable for its reason. */
export function StopButton({ onStop, notYours }: { readonly onStop: () => void; readonly notYours: string | undefined }) {
  const reasonId = useId();
  if (!notYours) {
    return <Button variant="danger" leadingIcon="stop" onClick={onStop} data-testid="task-stop">Stop</Button>;
  }
  return (
    <Tooltip content={notYours} side="bottom">
      {/* A disabled button takes no focus or pointer: the wrapper is what a reader meets, and its reason is the description. */}
      <span className="taskStopRefused" tabIndex={0} role="button" aria-disabled="true" aria-label="Stop" aria-describedby={reasonId}
        data-testid="task-stop" data-refused="">
        <span id={reasonId} className="ds-sr-only">{notYours}</span>
        <Button variant="danger" leadingIcon="stop" disabled tabIndex={-1}>Stop</Button>
      </span>
    </Tooltip>
  );
}

type Way = "stop" | "restart";
type Effect = { what: string; how: "kept" | "new" | "gone"; text: ReactNode };

function effects(way: Way, decider: NextDecider, next: NextDecider, attempt: number, branch: string, pr: OpenPullRequest | undefined,
  headBranch: string | null): Effect[] {
  const mono = (s: string) => <span className="ds-mono">{s}</span>;
  const n = attempt + 1;
  if (way === "stop") {
    return [
      { what: "Sessions", how: "kept", text: "Every stopped agent's conversation and workspace, kept a while: Resume carries on from them." },
      decider === "conductor"
        ? { what: "Conductor", how: "kept", text: "Stopped with the rest; on Resume the same conductor goes on with its whole conversation." }
        : { what: "Deliver", how: "kept", text: "Parked where it was; on Resume it goes on from there." },
      { what: "Branch", how: "kept", text: mono(branch) },
      ...(pr ? [{ what: "Pull request", how: "kept" as const, text: `#${pr.number} stays open; Resume carries on with it.` }] : []),
    ];
  }
  const where: Effect[] = pr?.openedBy === "someone" && headBranch
    ? [{ what: "Branch", how: "kept", text: <>{mono(headBranch)}, PR #{pr.number}'s: attempt {n} works on it from where attempt {attempt} left it.</> },
      { what: "Pull request", how: "kept", text: `#${pr.number} stays open — attempt ${n} continues on it. Its author opened it; dude never closes it.` }]
    : [{ what: "Branch", how: "new", text: <>A new branch for attempt {n}, from the default branch. Attempt {attempt}'s is kept.</> },
      ...(pr ? [{ what: "Pull request", how: "gone" as const, text: `#${pr.number}, opened by dude on attempt ${attempt}'s branch, is closed. Attempt ${n} opens its own.` }] : [])];
  return [
    ...where,
    { what: "Sessions", how: "new", text: `The whole pipeline again. Attempt ${attempt}'s stay to read.` },
    next === "conductor"
      ? { what: "Conductor", how: "new", text: `A new one, briefed with your note and what attempt ${attempt} did. Attempt ${attempt}'s Chat stays above a divider.` }
      : { what: "Deliver", how: "new", text: `Runs attempt ${n} to the pull request on its own, with your note. Chat stays open to the conductor.` },
  ];
}

/** The Stop dialog: Stop here, or Stop and start the next attempt. */
export function StopDialog({ client, task, attempt, live, pullRequest, onClose, onDone }: {
  client: ApiClient;
  task: TaskDetail;
  /** The current attempt: Stop and start makes the next. */
  attempt: number;
  live: readonly LiveItem[];
  pullRequest?: OpenPullRequest | undefined;
  onClose: () => void;
  onDone: (restarted: boolean) => void;
}) {
  const decider: NextDecider = task.decider === "conductor" ? "conductor" : "deliver";
  const [way, setWay] = useState<Way>("stop");
  const [next, setNext] = useState<NextDecider>(decider);
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<string | null>(null);
  const key = task.key ?? "this task";
  const n = attempt + 1;
  const branch = task.runs.find((r) => r.attempt === attempt && r.branch)?.branch ?? "its branch";
  const adopted = pullRequest?.openedBy === "someone";
  const ways: ChoiceOption<Way>[] = [
    { value: "stop", icon: "stop", label: "Stop here",
      description: `Everything stops: ${decider === "conductor" ? "the conductor, every agent it or Deliver started" : "every agent Deliver started"}. What they were doing is kept for a while so you can Resume.` },
    { value: "restart", icon: "git-branch", label: `Stop and start attempt ${n}`,
      description: adopted ? `A fresh start on PR #${pullRequest.number}'s branch: new sessions, the whole thing again, pushing to the same PR.`
        : "A fresh start from the default branch: new branch, new sessions, the whole thing again." },
  ];
  const go = async () => {
    setBusy(true);
    setProblem(null);
    try {
      await client.stopTask(task.id, way === "stop" ? { note: note.trim() }
        : { andRestart: true, decider: next === "conductor" ? "conductor" : "policy", note: note.trim() });
      onDone(way === "restart");
    } catch (err) {
      setProblem(errorText(err));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} tone="danger" size="md" title={`Stop ${key}?`} footerProblem={problem}
      footer={<>
        <Button variant="quiet" onClick={onClose}>Cancel</Button>
        <Button variant="danger" solid leadingIcon="stop" loading={busy} disabled={busy} onClick={() => void go()} data-testid="stop-confirm">
          {way === "stop" ? "Stop" : `Stop and start attempt ${n}`}
        </Button>
      </>}>
      <div className="pickUp" data-testid="stop-dialog">
        <LiveWork items={live} summary={liveSummary(live)} />
        <ChoiceList<Way> label="How to stop it" value={way} onChange={setWay} options={ways} data-testid="stop-way" />
        <div className="pickUpWhatItDoes">
          {ways.map(({ value: w }) => (
            <div key={w} className="pickUpWay" data-shown={w === way}>
              {w === "restart" ? <DeciderField attempt={n} value={next} onChange={setNext} previous={decider} /> : null}
              <ul className="pickUpEffects" aria-label="What it does">
                {effects(w, decider, next, attempt, branch, pullRequest, task.source?.headBranch ?? null).map((e) => (
                  <li key={e.what} data-how={e.how}>
                    <span className="pickUpWhat">{e.what}</span>
                    <span className="pickUpHow">{e.how === "gone" ? "closed" : e.how}</span>
                    <span>{e.text}</span>
                  </li>
                ))}
              </ul>
            </div>
          ))}
        </div>
        <Textarea label={way === "restart" ? "Why, and what to do differently (optional)" : "Why (optional)"} rows={3} value={note}
          onChange={(e) => setNote(e.target.value)} maxLength={2000} data-testid="stop-note"
          hint={way === "restart" ? "Every agent of the next attempt reads it." : "Shown on the task and in Chat beside your name; Resume and Try again pass it on."} />
      </div>
    </Dialog>
  );
}
