/**
 * Picking a stopped task back up: the notice that says how it stopped and
 * offers the ways back, and the one dialog they open.
 *
 * In the escalation's place and grammar (EscalationPanel): who stopped
 * what and why, what it left, then Resume… / Try again… / Start over… —
 * the orchestrator says which are open now (GET /v1/tasks/:id/recover) and
 * which fits first. Each opens `PickUpDialog`, whose `ChoiceList` holds all
 * three with what each keeps and throws away under it, so the choice is
 * made with that in view. Only the task's owner picks it back up; anyone
 * else is told whom it waits on.
 */

import { useEffect, useId, useState, type ReactNode } from "react";
import { shortId } from "@dude/design-system";
import { DeciderField, Duration, ROLE_LABEL, pullRequestNumbers, type NextDecider } from "@dude/design-system/components";
import { Button, Callout, CalloutActions, ChoiceList, calloutActionLineId, Dialog, Textarea, type ChoiceOption } from "@dude/design-system/primitives";
import { DECISION_POINT_LABEL, EventTypes, blockedRefusal, isConductor, runDoer, runLabel, type DecisionPoint, type PersistedEvent } from "@dude/domain";
import { refusedBlockers, type ApiClient, type RecoverAction, type RecoveryOptions, type Run, type TaskDetail, type TaskSource } from "../api/client.ts";
import { actorName, humanActor } from "../api/conversation.ts";
import { shortError } from "../escalation.ts";
import { errorText } from "../hooks/useSave.tsx";
import type { People } from "../people.tsx";
import { prsAnd, prsByOpener, type OpenPullRequest } from "./TaskStop.tsx";

/** How the task stopped, as the record has it. */
export interface Stop {
  /** The Run it stopped on (an aborted one, or the one that failed): when and how is its own. For a Stop of the task, its conductor if it had one. */
  run: Run | null;
  /** Who aborted it, or null for a failure or a stop no person made. */
  by: string | null;
  /** In their words, or the failure's. */
  why: string | null;
  /** A person's Stop of the whole task (task.stopped): when, and the decision the conductor was on, if any. */
  task?: { readonly at: string; readonly point: DecisionPoint | null } | undefined;
}

/** Who stopped a Run and why: who aborted it and their reason, or the failure's words. */
export function howRunStopped(run: Run, events: readonly PersistedEvent[], people: People, max?: number): Pick<Stop, "by" | "why"> {
  if (run.status !== "aborted") return { by: null, why: run.error ? shortError(run.error, max) : null };
  const aborted = events.findLast((e) => e.eventType === "run.aborted" && e.runId === run.id);
  return {
    by: aborted ? actorName(humanActor(aborted), people.names) : null,
    why: typeof aborted?.payload.reason === "string" ? aborted.payload.reason : null,
  };
}

/** The decision a conductor was on at `at`: the last one awaited before it that it had not taken. */
function decisionAt(events: readonly PersistedEvent[], at: string): DecisionPoint | null {
  const before = events.filter((e) => e.occurredAt <= at);
  const awaited = before.findLast((e) => e.eventType === EventTypes.ConductorDecisionAwaited);
  if (!awaited || before.some((e) => e.eventType === EventTypes.ConductorDecided && e.cursor > awaited.cursor)) return null;
  const point = awaited.payload.point;
  return typeof point === "string" && point in DECISION_POINT_LABEL ? point as DecisionPoint : null;
}

/**
 * How the task stopped. A person's Stop of the task since the work last
 * went on (a pick-up, or a decision other than Stop): on its conductor, if
 * it stopped one, else its latest phase Run. Otherwise the current
 * attempt's last aborted or failed delivery Run since then, and who stopped
 * it and why; failing that, a conductor a person aborted since then — a
 * conductor ending later than a delivery stop is not what stopped it. None,
 * when it stopped on no Run (a pull request closed, a person stopping a
 * review that got stuck): the notice then says only that it stopped.
 */
export function stopOf(task: TaskDetail, events: readonly PersistedEvent[], people: People): Stop {
  const since = events.findLast((e) => e.eventType === "task.recovered" ||
    (e.eventType === "task.decided" && e.payload.action !== "stop"))?.occurredAt ?? "";
  const stopped = events.findLast((e) => e.eventType === "task.stopped" && e.occurredAt > since);
  if (stopped) {
    const ids = new Set(Array.isArray(stopped.payload.runIds) ? stopped.payload.runIds.filter((id): id is string => typeof id === "string") : []);
    const named = task.runs.filter((r) => ids.has(r.id) && !r.parentRunId);
    const run = named.find(isConductor) ?? named.sort((a, b) => b.createdAt.localeCompare(a.createdAt))[0] ?? null;
    return { run, by: actorName(humanActor(stopped), people.names), why: typeof stopped.payload.note === "string" && stopped.payload.note ? stopped.payload.note : null,
      task: { at: stopped.occurredAt, point: decisionAt(events, stopped.occurredAt) } };
  }
  const delivery = task.runs.filter((r) => !isConductor(r));
  const attempt = Math.max(1, ...delivery.map((r) => r.attempt));
  const ended = delivery.filter((r) => r.attempt === attempt && !r.replacedBy && (r.status === "aborted" || r.status === "failed") && (r.endedAt ?? "") > since)
    .sort((a, b) => (b.endedAt ?? b.createdAt).localeCompare(a.endedAt ?? a.createdAt));
  const conductor = task.runs.filter((r) => isConductor(r) && r.status === "aborted" && (r.endedAt ?? "") > since)
    .sort((a, b) => (b.endedAt ?? b.createdAt).localeCompare(a.endedAt ?? a.createdAt))[0];
  const run = ended[0] ?? conductor ?? null;
  return run ? { run, ...howRunStopped(run, events, people) } : { run: null, by: null, why: null };
}

/** The sentence for how a Run stopped, without when: "Ana aborted the implementer: “…”", "The fixer failed: …". */
export function stoppedSentence(run: Run, by: string | null, why: string | null, ago: ReactNode = null): ReactNode {
  const what = runDoer(run);
  if (run.status === "failed") return <><strong>The {what} failed</strong>{ago}{why ? `: ${why}` : "."}</>;
  return <><strong>{by ?? "Someone"} aborted the {what}</strong>{ago}{why ? <>: “{why}”</> : "."}</>;
}

/** What the conductor was deciding, as a clause: "deciding after review". */
const DECIDING: Record<DecisionPoint, string> = {
  start: "deciding whether to start building",
  after_implement: "deciding after the implementer",
  after_review: "deciding after review",
  after_fix: "deciding after the fix",
  before_pull_request: "deciding whether to open the pull request",
  pull_request_feedback: "deciding on the pull request's feedback",
};

/** The first line: who stopped what, when, and why. */
function stopSentence(stop: Stop): ReactNode {
  const ago = (at: string | null | undefined) => (at ? <> <Duration ms={Math.max(0, Date.now() - Date.parse(at))} format="age" tone="muted" /> ago</> : null);
  if (stop.task) {
    const run = stop.run;
    const doing = run && isConductor(run) ? ` while the conductor was ${stop.task.point ? DECIDING[stop.task.point] : "between turns"}`
      : run ? ` while the ${runDoer(run)} was working` : "";
    return <><strong>{stop.by ?? "Someone"} stopped the task</strong>{ago(stop.task.at)}{doing}{stop.why ? <>: “{stop.why}”</> : "."}</>;
  }
  if (!stop.run) return <><strong>Delivery stopped</strong>.</>;
  return stoppedSentence(stop.run, stop.by, stop.why, ago(stop.run.endedAt));
}

/** When a kept session stops being kept, as a day: "Thu 8 Oct". */
export const keptUntil = (iso: string) =>
  new Date(iso).toLocaleDateString(undefined, { weekday: "short", day: "numeric", month: "short" });

/** What stopped, for a sentence about it: "Ana aborted its implement.", "Its implement failed.", "Its delivery stopped." */
function stoppedWords(stop: Stop, role: string): string {
  if (!stop.run) return "Its delivery stopped.";
  return stop.run.status === "failed" ? `Its ${role} failed.` : `${stop.by ?? "Someone"} aborted its ${role}.`;
}

const ACTION_LABEL: Record<RecoverAction, string> = { resume: "Resume…", retry: "Try again…", restart: "Start over…" };
const ACTION_ICON = { resume: "play", retry: "retry", restart: "git-branch" } as const;

/** Each way's line in the notice: what it does, under the conductor or Deliver, to the task's pull requests. */
function wayLine(a: RecoverAction, conducted: boolean, attempt: number, prs: readonly OpenPullRequest[]): string {
  const n = attempt + 1;
  const stay = prs.length ? ` PR ${prsAnd(prs, "stays", "stay")} open.` : "";
  const { adopted, ours } = prsByOpener(prs);
  const closed = ours.length ? `PR ${prsAnd(ours, "is", "are")} closed` : "";
  switch (a) {
    case "resume":
      return conducted
        ? `The same conductor goes on, with its whole conversation and its agents.${stay}`
        : `Deliver and its agents go on where they stopped.${stay}`;
    case "retry":
      return conducted ? "A new conductor on this attempt, briefed with what the stopped one did."
        : "The step that stopped runs again with new agents, on the same branch.";
    case "restart":
      return adopted ? `Attempt ${n}, carrying on PR #${adopted.number}${closed ? `; ${closed}` : ""}.`
        : closed ? `Attempt ${n} on a new branch; ${closed}.` : `Attempt ${n} on a new branch.`;
  }
}

/**
 * The notice on a stopped task. `options` is null until read; a task that
 * cannot be picked back up any way (none offered) shows only how it stopped.
 * The ways back are a list, each button with its line as its description.
 */
export function StoppedNotice({ task, stop, options, owner, you, pullRequests = [], onOpenRun, onChoose }: {
  task: TaskDetail;
  stop: Stop;
  options: RecoveryOptions | null;
  /** The owner's name, for whom it waits on. */
  owner: string | null;
  /** Whether the reader may pick it up: they own it (or nobody does). */
  you: boolean;
  /** Every open pull request of the task, and who opened each. */
  pullRequests?: readonly OpenPullRequest[] | undefined;
  onOpenRun: (runId: string) => void;
  onChoose: (action: RecoverAction) => void;
}) {
  const actions = options?.actions ?? [];
  const head = Object.entries(stop.run?.heads ?? {})[0];
  const lineId = useId();
  const conducted = task.decider === "conductor";
  return (
    <Callout tone={stop.run?.status === "failed" ? "danger" : "attention"} data-testid="stopped" data-status={task.status}>
      <div className="escalation">
        <p>
          {stopSentence(stop)}{" "}
          {stop.run ? (
            <Button size="sm" variant="quiet" trailingIcon="arrow-right" onClick={() => onOpenRun(stop.run!.id)} data-testid="stopped-run">
              Open {runLabel(stop.run)}
            </Button>
          ) : null}
        </p>
        <p className="escalationWaiting">
          {head ? <>It left <span className="ds-mono">{shortId(head[1], 7)}</span> on <span className="ds-mono">{stop.run?.branch ?? "its branch"}</span>. </> : null}
          {options === null ? null : options.keptUntil
            ? <>Its workspace and conversation are kept until {keptUntil(options.keptUntil)}.</>
            : actions.length > 0 ? <>Its workspace and conversation are no longer kept.</> : null}
        </p>
        {actions.length === 0 ? null : you ? (
          <CalloutActions id={lineId} label="Ways to pick it back up" items={actions.map((a, i) => ({
            id: a,
            action: (
              <Button size="sm" variant={i === 0 ? "primary" : "secondary"} leadingIcon={ACTION_ICON[a]}
                onClick={() => onChoose(a)} aria-describedby={calloutActionLineId(lineId, a)} data-testid={`recover-${a}`}>
                {ACTION_LABEL[a]}
              </Button>
            ),
            line: wayLine(a, conducted, options?.attempt ?? 1, pullRequests),
          }))} />
        ) : (
          <p className="escalationWaiting" data-testid="recover-waiting">
            Only {owner ?? "its owner"}, its owner, can pick it back up. Take over the task to do it yourself.
          </p>
        )}
      </div>
    </Callout>
  );
}

/** Kept, new or gone: what one way does to each thing the task has. */
type Effect = { what: string; how: "kept" | "new" | "gone"; text: ReactNode };

function effects(action: RecoverAction, role: string, branch: string, head: string | null, attempt: number, source: TaskSource | null,
  conducted: boolean, next: NextDecider, prs: readonly OpenPullRequest[]): Effect[] {
  const at = head ? <span className="ds-mono">{head}</span> : "what was pushed";
  const prKept: Effect[] = prs.length ? [{ what: "Pull request", how: "kept", text: `${prsAnd(prs, "stays", "stay")} open.` }] : [];
  switch (action) {
    case "resume":
      if (conducted) {
        return [
          { what: "Conductor", how: "kept", text: "The same conductor goes on, with its whole conversation. Your note is its next message." },
          { what: "Sessions", how: "kept", text: "The agents it had at work go on where they stopped, with their unpushed work." },
          { what: "Branch", how: "kept", text: <><span className="ds-mono">{branch}</span></> },
          ...prKept,
        ];
      }
      return [
        { what: "Conversation", how: "kept", text: `The same ${role} goes on: everything it read and decided is still in its context. Your note is its next message.` },
        { what: "Workspace", how: "kept", text: "Its checkout as it stopped, with anything it had not pushed." },
        { what: "Branch", how: "kept", text: <><span className="ds-mono">{branch}</span></> },
        { what: "Session", how: "kept", text: "The same session: its transcript carries on below where it stopped." },
      ];
    case "retry":
      if (conducted) {
        return [
          { what: "Conductor", how: "new", text: "A new conductor on this attempt, briefed with what the stopped one did, its decisions and your note." },
          { what: "Sessions", how: "new", text: "New agents from what was pushed; the stopped ones stay to read." },
          { what: "Branch", how: "kept", text: <><span className="ds-mono">{branch}</span>, from {at}</> },
          ...prKept,
        ];
      }
      return [
        { what: "Conversation", how: "new", text: `A fresh ${role}, told the task, the decisions so far, and your note.` },
        { what: "Workspace", how: "gone", text: <>It starts from {at}; what the last one had not pushed is not carried over.</> },
        { what: "Branch", how: "kept", text: <><span className="ds-mono">{branch}</span>, and any pull request on it</> },
        { what: "Session", how: "new", text: "A new session in this attempt; the stopped one stays to read." },
      ];
    case "restart": {
      const who: Effect = next === "conductor"
        ? { what: "Conductor", how: "new", text: `A new one, briefed with your note and what attempt ${attempt} did. Attempt ${attempt}'s Chat stays above a divider.` }
        : { what: "Deliver", how: "new", text: `Runs attempt ${attempt + 1} to the pull request on its own, with your note.` };
      // Existing work is started over where it is: its branch, and its pull request if it had one.
      if (source) {
        return [
          { what: "Conversation", how: "new", text: "Fresh agents, told the task as it is now — edit it first if what it asks for was the problem." },
          { what: "Workspace", how: "new", text: <>A clean checkout of <span className="ds-mono">{source.headBranch}</span> as GitHub has it.</> },
          { what: "Branch", how: "kept", text: source.kind === "pr"
            ? <><span className="ds-mono">{source.headBranch}</span>, PR #{source.number}'s: attempt {attempt + 1} works on it from where attempt {attempt} left it.</>
            : <><span className="ds-mono">{source.headBranch}</span>, into <span className="ds-mono">{source.baseBranch}</span>. A pull request dude opened from it is closed.</> },
          ...(source.kind === "pr" ? [{ what: "Pull request", how: "kept" as const, text: `#${source.number} stays open — attempt ${attempt + 1} continues on it. dude never closes it.` }] : []),
          { what: "Session", how: "new", text: `Attempt ${attempt + 1}, from review again. Attempt ${attempt}'s sessions stay, marked as such.` },
          who,
        ];
      }
      return [
        { what: "Conversation", how: "new", text: "Fresh agents, told the task as it is now — edit it first if what it asks for was the problem." },
        { what: "Workspace", how: "new", text: "A clean checkout of the default branch." },
        { what: "Branch", how: "new", text: <>A new branch for attempt {attempt + 1}. Attempt {attempt}'s is kept.</> },
        ...(prs.length ? [{ what: "Pull request", how: "gone" as const, text: `${pullRequestNumbers(prs)}, opened by dude on attempt ${attempt}'s branch, ${prs.length === 1 ? "is" : "are"} closed. Attempt ${attempt + 1} opens its own.` }] : []),
        { what: "Session", how: "new", text: `Attempt ${attempt + 1}, the whole pipeline again. Attempt ${attempt}'s sessions stay, marked as such.` },
        who,
      ];
    }
  }
}

const GO: Record<RecoverAction, (role: string, attempt: number) => string> = {
  resume: (role) => `Resume ${role}`,
  retry: () => "Try again",
  restart: (_, attempt) => `Start attempt ${attempt + 1}`,
};

/** The one dialog every way back opens, on the way it was opened with. */
export function PickUpDialog({ client, task, stop, options, initial, pullRequests = [], onEdit, onClose, onDone }: {
  client: ApiClient;
  task: TaskDetail;
  stop: Stop;
  options: RecoveryOptions;
  initial: RecoverAction;
  /** Every open pull request of the task, and who opened each. */
  pullRequests?: readonly OpenPullRequest[] | undefined;
  /** Edit the task before starting over. */
  onEdit: () => void;
  onClose: () => void;
  onDone: (action: RecoverAction) => void;
}) {
  const conducted = task.decider === "conductor";
  const previous: NextDecider = conducted ? "conductor" : "deliver";
  const [action, setAction] = useState<RecoverAction>(initial);
  const [next, setNext] = useState<NextDecider>(previous);
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<string | null>(null);
  // Start over refused as blocked: a person may start it over anyway.
  const [refused, setRefused] = useState(false);
  useEffect(() => setAction(initial), [initial]);
  // Who it was — the implementer, the reviewer — not what it did: "Resume the implementer".
  const role = conducted ? "conductor" : stop.run?.role ? ROLE_LABEL[stop.run.role].toLowerCase() : "agent";
  const head = Object.values(stop.run?.heads ?? {}).map((sha) => shortId(sha, 7))[0] ?? null;
  const branch = stop.run?.branch ?? task.runs.find((r) => r.branch)?.branch ?? "its branch";
  const open = new Set(options.actions);
  const source = task.source ?? null;
  const reason = (a: RecoverAction): string | undefined => {
    if (open.has(a)) return undefined;
    if (a === "resume") return "No longer kept: lux keeps a stopped session for a while, then lets it go.";
    return "Not for how this one stopped: start over instead.";
  };
  const n = options.attempt + 1;
  const choices: ChoiceOption<RecoverAction>[] = [
    { value: "resume", icon: "play", label: conducted ? "Resume the conductor and its agents" : `Resume the ${role}`,
      description: conducted ? "Everything goes on where it stopped, with its conversation and unpushed work."
        : "The same agent picks up where it stopped, with its conversation and unpushed work.", disabledReason: reason("resume") },
    { value: "retry", icon: "retry", label: conducted ? "Try again with a new conductor" : `Try again with a new ${role}`,
      description: conducted ? "Same branch and attempt, from what was pushed. Nothing the stopped agents were thinking comes along."
        : "Same branch, from what was pushed. Nothing it was thinking comes along.", disabledReason: reason("retry") },
    { value: "restart", icon: "git-branch", label: `Start over as attempt ${n}`,
      description: source?.kind === "pr" ? `The whole thing again on PR #${source.number}'s branch, pushing to the same PR.`
        : source ? "The same branch, reviewed again from the start." : "A new branch from the default branch, and the whole thing again.",
      disabledReason: reason("restart") },
  ];
  const go = async (override = false) => {
    setBusy(true);
    setProblem(null);
    try {
      await client.recover(task.id, action, note.trim(), {
        ...(override ? { override: true as const } : {}),
        ...(action === "restart" ? { decider: next === "conductor" ? "conductor" as const : "policy" as const } : {}),
      });
      onDone(action);
    } catch (err) {
      const blocked = refusedBlockers(err);
      setRefused(blocked !== null && action === "restart");
      setProblem(blocked && blocked.length > 0 ? blockedRefusal(task.key ?? "This task", blocked) : errorText(err));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} size="md" title={`Pick ${task.key ?? "this task"} back up`}
      description={`${stoppedWords(stop, role)} The task goes back to running.`}
      footerProblem={problem}
      footer={
        <>
          <Button variant="quiet" onClick={onClose}>Cancel</Button>
          {refused && action === "restart" ? (
            <Button variant="secondary" disabled={busy} onClick={() => void go(true)} data-testid="recover-anyway">Start over anyway</Button>
          ) : null}
          <Button variant="primary" loading={busy} disabled={busy || !open.has(action)} onClick={() => void go()} data-testid="recover-confirm">
            {GO[action](role, options.attempt)}
          </Button>
        </>
      }>
      <div className="pickUp">
        <ChoiceList label="How to pick it back up" value={action} onChange={setAction} options={choices} data-testid="recover-choice" />
        {/* Every way's account in the same place, only the chosen one shown:
            the place is as tall as the longest, so choosing never moves the
            dialog's edges or the note under it. */}
        <div className="pickUpWhatItDoes">
          {choices.map(({ value: a }) => (
            <div key={a} className="pickUpWay" data-shown={a === action}>
              {a === "restart" ? <DeciderField attempt={n} value={next} onChange={setNext} previous={previous} /> : null}
              <ul className="pickUpEffects" aria-label="What it does">
                {effects(a, role, branch, head, options.attempt, source, conducted, next, pullRequests).map((e) => (
                  <li key={e.what} data-how={e.how}>
                    <span className="pickUpWhat">{e.what}</span>
                    <span className="pickUpHow">{e.how === "gone" && e.what === "Pull request" ? "closed" : e.how}</span>
                    <span>{e.text}</span>
                  </li>
                ))}
              </ul>
              {a === "restart" ? (
                <Callout tone="info">
                  What the task asks for can change before attempt {options.attempt + 1} starts.{" "}
                  <Button size="sm" variant="quiet" leadingIcon="edit" onClick={onEdit}>Edit the task first</Button>
                </Callout>
              ) : null}
            </div>
          ))}
        </div>
        <Textarea label={action === "resume" ? `Tell the ${role} (optional)` : "A note for the agents (optional)"} rows={3}
          value={note} onChange={(e) => setNote(e.target.value)} data-testid="recover-note"
          hint={action === "resume" ? "Its next message, from you. Empty, it is told to go on where it left off." : "Kept with the task: every agent from here on is told it."} />
      </div>
    </Dialog>
  );
}

/**
 * Reads how the task can be picked back up: again when one of its Runs ends
 * or is taken back up, and — just after it stops, until a resume is open —
 * a few seconds later, as the orchestrator keeps the stopped Runs a moment
 * after they stop, and nothing on the page says when.
 */
export function useRecoveryOptions(client: ApiClient, task: TaskDetail | null): RecoveryOptions | null {
  const [options, setOptions] = useState<RecoveryOptions | null>(null);
  const [tick, setTick] = useState(0);
  const stopped = task?.status === "aborted" || task?.status === "failed";
  const taskId = task?.id;
  const runs = task?.runs.map((r) => `${r.id}:${r.status}:${r.endedAt ?? ""}`).join(",") ?? "";
  const waiting = stopped && options !== null && !options.keptUntil && tick < 5;
  useEffect(() => {
    if (!waiting) return;
    const timer = setTimeout(() => setTick((t) => t + 1), 2000);
    return () => clearTimeout(timer);
  }, [waiting, options]);
  useEffect(() => {
    if (!stopped || !taskId) {
      setOptions(null);
      return;
    }
    let live = true;
    client.recoveryOptions(taskId).then((o) => live && setOptions(o), () => live && setOptions(null));
    return () => {
      live = false;
    };
  }, [client, taskId, stopped, runs, tick]);
  return options;
}
