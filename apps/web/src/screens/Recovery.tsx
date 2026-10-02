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

import { useEffect, useState, type ReactNode } from "react";
import { shortId } from "@dude/design-system";
import { Duration, ROLE_LABEL } from "@dude/design-system/components";
import { Button, Callout, ChoiceList, Dialog, Textarea, type ChoiceOption } from "@dude/design-system/primitives";
import { runLabel, type PersistedEvent } from "@dude/domain";
import type { ApiClient, RecoverAction, RecoveryOptions, Run, TaskDetail } from "../api/client.ts";
import { actorName, humanActor } from "../api/conversation.ts";
import { shortError } from "../escalation.ts";
import { errorText } from "../hooks/useSave.tsx";
import type { People } from "../people.tsx";

/** How the task stopped, as the record has it. */
export interface Stop {
  /** The Run it stopped on (an aborted one, or the one that failed): when and how is its own. */
  run: Run | null;
  /** Who aborted it, or null for a failure or a stop no person made. */
  by: string | null;
  /** In their words, or the failure's. */
  why: string | null;
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

/**
 * How the task stopped: the current attempt's last aborted or failed Run
 * since the work last went on past one (a pick-up, or a decision other
 * than Stop), and who stopped it and why. None, when it stopped on no Run
 * (a pull request closed, a person stopping a review that got stuck): the
 * notice then says only that it stopped. Delivery's Runs only: a task's
 * conductor is no part of it, and recovery never takes one up.
 */
export function stopOf(task: TaskDetail, events: readonly PersistedEvent[], people: People): Stop {
  const delivery = task.runs.filter((r) => r.role !== "conductor");
  const attempt = Math.max(1, ...delivery.map((r) => r.attempt));
  const since = events.findLast((e) => e.eventType === "task.recovered" ||
    (e.eventType === "task.decided" && e.payload.action !== "stop"))?.occurredAt ?? "";
  const ended = delivery.filter((r) => r.attempt === attempt && (r.status === "aborted" || r.status === "failed") && (r.endedAt ?? "") > since)
    .sort((a, b) => (b.endedAt ?? b.createdAt).localeCompare(a.endedAt ?? a.createdAt));
  const run = ended[0] ?? null;
  return run ? { run, ...howRunStopped(run, events, people) } : { run: null, by: null, why: null };
}

/** The sentence for how a Run stopped, without when: "Ana aborted the implement: “…”", "The fix failed: …". */
export function stoppedSentence(run: Run, by: string | null, why: string | null, ago: ReactNode = null): ReactNode {
  const what = runLabel(run).toLowerCase();
  if (run.status === "failed") return <><strong>The {what} failed</strong>{ago}{why ? `: ${why}` : "."}</>;
  return <><strong>{by ?? "Someone"} aborted the {what}</strong>{ago}{why ? <>: “{why}”</> : "."}</>;
}

/** The first line: who stopped what, when, and why. */
function stopSentence(stop: Stop): ReactNode {
  if (!stop.run) return <><strong>Delivery stopped</strong>.</>;
  const at = stop.run.endedAt;
  return stoppedSentence(stop.run, stop.by, stop.why,
    at ? <> <Duration ms={Math.max(0, Date.now() - Date.parse(at))} format="age" tone="muted" /> ago</> : null);
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

/**
 * The notice on a stopped task. `options` is null until read; a task that
 * cannot be picked back up any way (none offered) shows only how it stopped.
 */
export function StoppedNotice({ task, stop, options, owner, you, onOpenRun, onChoose }: {
  task: TaskDetail;
  stop: Stop;
  options: RecoveryOptions | null;
  /** The owner's name, for whom it waits on. */
  owner: string | null;
  /** Whether the reader may pick it up: they own it (or nobody does). */
  you: boolean;
  onOpenRun: (runId: string) => void;
  onChoose: (action: RecoverAction) => void;
}) {
  const actions = options?.actions ?? [];
  const head = Object.entries(stop.run?.heads ?? {})[0];
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
          <div className="escalationActions">
            {actions.map((a, i) => (
              <Button key={a} size="sm" variant={i === 0 ? "primary" : "secondary"} leadingIcon={ACTION_ICON[a]}
                onClick={() => onChoose(a)} data-testid={`recover-${a}`}>
                {ACTION_LABEL[a]}
              </Button>
            ))}
          </div>
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

function effects(action: RecoverAction, role: string, branch: string, head: string | null, attempt: number): Effect[] {
  const at = head ? <span className="ds-mono">{head}</span> : "what was pushed";
  switch (action) {
    case "resume":
      return [
        { what: "Conversation", how: "kept", text: `The same ${role} goes on: everything it read and decided is still in its context. Your note is its next message.` },
        { what: "Workspace", how: "kept", text: "Its checkout as it stopped, with anything it had not pushed." },
        { what: "Branch", how: "kept", text: <><span className="ds-mono">{branch}</span></> },
        { what: "Session", how: "kept", text: "The same session: its transcript carries on below where it stopped." },
      ];
    case "retry":
      return [
        { what: "Conversation", how: "new", text: `A fresh ${role}, told the task, the decisions so far, and your note.` },
        { what: "Workspace", how: "gone", text: <>It starts from {at}; what the last one had not pushed is not carried over.</> },
        { what: "Branch", how: "kept", text: <><span className="ds-mono">{branch}</span>, and any pull request on it</> },
        { what: "Session", how: "new", text: "A new session in this attempt; the stopped one stays to read." },
      ];
    case "restart":
      return [
        { what: "Conversation", how: "new", text: "Fresh agents, told the task as it is now — edit it first if what it asks for was the problem." },
        { what: "Workspace", how: "new", text: "A clean checkout of the default branch." },
        { what: "Branch", how: "new", text: <>A new branch for attempt {attempt + 1}. Attempt {attempt}'s is kept; a pull request on it is closed.</> },
        { what: "Session", how: "new", text: `Attempt ${attempt + 1}, the whole pipeline again. Attempt ${attempt}'s sessions stay, marked as such.` },
      ];
  }
}

const GO: Record<RecoverAction, (role: string, attempt: number) => string> = {
  resume: (role) => `Resume ${role}`,
  retry: () => "Try again",
  restart: (_, attempt) => `Start attempt ${attempt + 1}`,
};

/** The one dialog every way back opens, on the way it was opened with. */
export function PickUpDialog({ client, task, stop, options, initial, onEdit, onClose, onDone }: {
  client: ApiClient;
  task: TaskDetail;
  stop: Stop;
  options: RecoveryOptions;
  initial: RecoverAction;
  /** Edit the task before starting over. */
  onEdit: () => void;
  onClose: () => void;
  onDone: (action: RecoverAction) => void;
}) {
  const [action, setAction] = useState<RecoverAction>(initial);
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<string | null>(null);
  useEffect(() => setAction(initial), [initial]);
  // Who it was — the implementer, the reviewer — not what it did: "Resume the implementer".
  const role = stop.run?.role ? ROLE_LABEL[stop.run.role].toLowerCase() : "agent";
  const head = Object.values(stop.run?.heads ?? {}).map((sha) => shortId(sha, 7))[0] ?? null;
  const branch = stop.run?.branch ?? task.runs.find((r) => r.branch)?.branch ?? "its branch";
  const open = new Set(options.actions);
  const reason = (a: RecoverAction): string | undefined => {
    if (open.has(a)) return undefined;
    if (a === "resume") return "No longer kept: lux keeps a stopped session for a while, then lets it go.";
    return "Not for how this one stopped: start over instead.";
  };
  const choices: ChoiceOption<RecoverAction>[] = [
    { value: "resume", icon: "play", label: `Resume the ${role}`, description: "The same agent picks up where it stopped, with its conversation and unpushed work.", disabledReason: reason("resume") },
    { value: "retry", icon: "retry", label: `Try again with a new ${role}`, description: "Same branch, from what was pushed. Nothing it was thinking comes along.", disabledReason: reason("retry") },
    { value: "restart", icon: "git-branch", label: `Start over as attempt ${options.attempt + 1}`, description: "A new branch from the default branch, and the whole pipeline again.", disabledReason: reason("restart") },
  ];
  const go = async () => {
    setBusy(true);
    setProblem(null);
    try {
      await client.recover(task.id, action, note.trim());
      onDone(action);
    } catch (err) {
      setProblem(errorText(err));
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
              <ul className="pickUpEffects" aria-label="What it does">
                {effects(a, role, branch, head, options.attempt).map((e) => (
                  <li key={e.what} data-how={e.how}>
                    <span className="pickUpWhat">{e.what}</span>
                    <span className="pickUpHow">{e.how}</span>
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
