/**
 * Lifecycle (docs/design/tracking-work.md, Epic 1): a closed task's
 * resolution and an archived task's mark, the task menu's lifecycle items
 * with their reasons, and the dialogs that close and archive. Neutral
 * throughout: neither a closed nor an archived task needs anyone.
 */

import { useState, useId, type ReactNode } from "react";
import { RESOLUTION_LABEL, closedBy, keyList, lifecycleEnded, type CloseResolution, type TaskResolution, type TaskStatus } from "@dude/domain";
import { Icon } from "../icons/index.tsx";
import { Badge } from "../primitives/Badge.tsx";
import { Button } from "../primitives/Button.tsx";
import { Checkbox, type CheckedState } from "../primitives/Checkbox.tsx";
import { ChoiceList, type ChoiceOption } from "../primitives/ChoiceList.tsx";
import { Dialog } from "../primitives/Dialog.tsx";
import { Callout, Fieldset, FormStack } from "../primitives/Layout.tsx";
import type { RowMenuItem } from "../primitives/RowMenu.tsx";
import { Textarea } from "../primitives/Textarea.tsx";
import { LiveWork, liveWords, type LiveItem } from "./LiveWork.tsx";
import { RefLead } from "./RefLead.tsx";
import { SearchPicker } from "./SearchPicker.tsx";
import { StatusMark } from "./StatusMark.tsx";
import { cx } from "../util/cx.ts";
import styles from "./Lifecycle.module.css";

// ---------------------------------------------------------------------------
// Marks
// ---------------------------------------------------------------------------

export interface ResolutionMarkProps {
  readonly resolution: TaskResolution;
  /** A duplicate's canonical task: "Duplicate of CP-62". */
  readonly of?: string | undefined;
  readonly className?: string | undefined;
}

/** Why a person ended the task, beside its status: a neutral small badge. */
export function ResolutionMark({ resolution, of, className }: ResolutionMarkProps) {
  return (
    <Badge tone="neutral" size="sm" className={className} data-testid="resolution-mark" data-resolution={resolution}>
      {RESOLUTION_LABEL[resolution]}{of ? <> of <span className="ds-mono">{of}</span></> : null}
    </Badge>
  );
}

/** Put away: the `archive` glyph and the word, in muted ink. */
export function ArchivedMark({ className }: { readonly className?: string | undefined }) {
  return (
    <span className={cx(styles["archived"], className)} data-testid="archived-mark">
      <Icon name="archive" size={12} />
      Archived
    </span>
  );
}

// ---------------------------------------------------------------------------
// The task menu's lifecycle items
// ---------------------------------------------------------------------------

/** What the menu needs to know of a task and its reader. */
export interface LifecycleTask {
  readonly status: TaskStatus;
  readonly resolution?: TaskResolution | null | undefined;
  readonly archived?: boolean | undefined;
  /** A pull request of it is open on GitHub: Close and Archive ask whether to close it. */
  readonly pullRequestOpen?: boolean | undefined;
}

export interface LifecycleMenuOptions {
  /** Set when the reader may not decide: whose it is ("Only Ana, the owner, or an admin can do this"). */
  readonly notYours?: string | undefined;
  readonly onClose?: (() => void) | undefined;
  readonly onReopen?: (() => void) | undefined;
  /** Archive…: on a task that hasn't ended, the caller opens Close and archive. */
  readonly onArchive?: (() => void) | undefined;
  readonly onUnarchive?: (() => void) | undefined;
}

/**
 * The task `…` menu's lifecycle items, each disabled with its reason, in
 * the order the reasons are checked: who may not, then the task's own
 * lifecycle. Agents working and a pull request open disable nothing: the
 * dialog says what closing stops and asks about the pull request. Close…
 * becomes Reopen on a closed task, and Archive… becomes Unarchive on an
 * archived one.
 */
export function lifecycleMenuItems(t: LifecycleTask, o: LifecycleMenuOptions): RowMenuItem[] {
  const reason = (state?: string | undefined) => o.notYours ?? state;
  const items: RowMenuItem[] = [];
  if (closedBy(t.resolution)) {
    items.push({ id: "reopen", label: "Reopen", icon: "retry", onSelect: o.onReopen,
      disabled: Boolean(o.notYours || t.archived), disabledReason: reason(t.archived ? "Unarchive it first, then reopen it" : undefined) });
  } else {
    const ended = lifecycleEnded(t.status, t.resolution);
    const state = ended ? "Already ended" : undefined;
    items.push({ id: "close", label: ended ? "Closed" : "Close…", icon: "circle-x", onSelect: o.onClose,
      disabled: Boolean(o.notYours || state), disabledReason: reason(state) });
  }
  if (t.archived) {
    items.push({ id: "unarchive", label: "Unarchive", icon: "archive", onSelect: o.onUnarchive,
      disabled: Boolean(o.notYours), disabledReason: reason() });
  } else {
    items.push({ id: "archive", label: "Archive…", icon: "archive", onSelect: o.onArchive,
      disabled: Boolean(o.notYours), disabledReason: reason() });
  }
  return items;
}

// ---------------------------------------------------------------------------
// Close, and Close and archive
// ---------------------------------------------------------------------------

/** A task as the Close dialog's picker and lists name it. */
export interface LifecycleTaskRef {
  readonly key: string;
  readonly title: string;
  readonly status: TaskStatus;
  /** Closed as a duplicate of this key: the picker refuses it. */
  readonly duplicateOf?: string | undefined;
}

export interface CloseDecision {
  readonly resolution: CloseResolution;
  readonly duplicateOf?: string | undefined;
  readonly note?: string | undefined;
  readonly removeRelations?: boolean | undefined;
  readonly closePullRequest?: boolean | undefined;
}

export interface ClosablePullRequest {
  readonly number: number;
  /** "dude": opened for this task, ticked by default. "someone": the task was started from it, unticked by default. */
  readonly openedBy: "dude" | "someone";
}

export interface CloseDialogProps {
  readonly open: boolean;
  readonly onOpenChange: (open: boolean) => void;
  readonly task: LifecycleTaskRef;
  /** What waits on it: the not-started tasks it blocks. */
  readonly waiting?: ReadonlyArray<LifecycleTaskRef> | undefined;
  /** Close and archive: the same decision, archiving on the way; nothing chosen at first. */
  readonly andArchive?: boolean | undefined;
  /** Why Can't reproduce isn't for this task, when it isn't. */
  readonly cantReproduceReason?: string | undefined;
  /** Finds the task it duplicates, by key or title. */
  readonly find: (query: string) => Promise<ReadonlyArray<LifecycleTaskRef>>;
  readonly onConfirm: (decision: CloseDecision) => void | Promise<void>;
  /** Set when the reader may not decide: the primary is disabled with this, in the footer. */
  readonly refused?: string | undefined;
  /** The resolution chosen at first (a static picture); Close and archive starts with none. */
  readonly initial?: CloseResolution | undefined;
  /** The duplicate's task, chosen at first (a static picture). */
  readonly initialDuplicate?: LifecycleTaskRef | undefined;
  /** The server's refusal of the last confirm, in its words. */
  readonly problem?: string | null | undefined;
  readonly busy?: boolean | undefined;
  /** Where it is: the dialog's context line (a `Breadcrumb`). */
  readonly context?: ReactNode;
  /** What closing stops: every live Run. Empty or absent: nothing is live, and the dialog neither lists nor warns. */
  readonly live?: readonly LiveItem[] | undefined;
  /** The task's open pull requests, every one the close would touch: one Checkbox row asks whether to close them on GitHub too. */
  readonly pullRequests?: readonly ClosablePullRequest[] | undefined;
}

/**
 * Close a task, saying why — or, as Close and archive, close it and put it
 * away in one step. Four resolutions as a `ChoiceList`; Nothing to merge is
 * Mark done's and says so; a Duplicate picks the task it duplicates and
 * says what its blocking hands over; any other resolution with something
 * waiting on the task says it keeps waiting, and offers to remove the
 * relation in the same step. With anything live it is a danger dialog that
 * lists what stops first; with an open pull request it asks whether to
 * close that too.
 */
export function CloseDialog({ open, onOpenChange, task: t, waiting = [], andArchive, cantReproduceReason, find, onConfirm, refused, initial,
  initialDuplicate, problem, busy, context, live = [], pullRequests = [] }: CloseDialogProps) {
  const [why, setWhy] = useState<CloseResolution | null>(andArchive ? null : (initial ?? null));
  const [dup, setDup] = useState<LifecycleTaskRef | null>(initialDuplicate ?? null);
  const [unrelate, setUnrelate] = useState<CheckedState>(false);
  // Ticked only when dude opened every one: someone else's is never closed unless asked.
  const [closePR, setClosePR] = useState<CheckedState>(pullRequests.length > 0 && pullRequests.every((p) => p.openedBy === "dude"));
  const [note, setNote] = useState("");
  const footId = useId();
  const keys = keyList(waiting.map((w) => w.key));
  const one = waiting.length === 1;
  const stops = live.length > 0;
  const ready = why !== null && (why !== "duplicate" || dup !== null);
  const label = stops ? (andArchive ? "Stop, close and archive" : "Stop and close")
    : andArchive ? "Close and archive"
    : why === "duplicate" && dup ? `Close as duplicate of ${dup.key}`
    : why ? `Close as ${RESOLUTION_LABEL[why]}` : "Close";
  const options: ChoiceOption<CloseResolution | "nothing_to_merge">[] = [
    { value: "wont_do", label: RESOLUTION_LABEL.wont_do, icon: "circle-x", description: "Nobody will do it: not needed, or not any more." },
    { value: "duplicate", label: RESOLUTION_LABEL.duplicate, icon: "copy", description: "Another task covers it. What waits on this waits on that one." },
    { value: "cant_reproduce", label: RESOLUTION_LABEL.cant_reproduce, icon: "question", description: "The problem doesn't happen: nothing to fix.",
      disabledReason: cantReproduceReason },
    { value: "nothing_to_merge", label: RESOLUTION_LABEL.nothing_to_merge, icon: "check",
      disabledReason: "Only for work that ran: Mark done gives this, once there is a write-up or an answer" },
  ];
  const confirm = () => {
    if (!why || !ready) return;
    void onConfirm({ resolution: why, ...(why === "duplicate" && dup ? { duplicateOf: dup.key } : {}),
      ...(note.trim() ? { note: note.trim() } : {}), ...(why !== "duplicate" && unrelate === true ? { removeRelations: true } : {}),
      ...(pullRequests.length > 0 ? { closePullRequest: closePR === true } : {}) });
  };
  const hint = refused ?? (!why ? "Pick why it is closed." : why === "duplicate" && !dup ? "Pick the task it duplicates." : undefined);
  const description = andArchive
    ? `${t.key} hasn't ended, so archiving it closes it first: say why. ${stops ? "What is live stops now. " : ""}Both happen at once. To bring it back, Unarchive it, then Reopen it.`
    : stops
      ? "Closing says nobody will work on it, and why. What is live stops now and it ends here. Reopen brings it back to Backlog."
      : "Closing says nobody will work on it, and why. It never ran, or it stopped: either way it ends here. Reopen brings it back to Backlog.";
  return (
    <Dialog open={open} onOpenChange={onOpenChange} size="md" context={context} title={andArchive ? `Close and archive ${t.key}` : `Close ${t.key}`}
      tone={stops ? "danger" : undefined} description={description}
      footerProblem={problem}
      footerStart={hint ? <span id={footId} className={styles["muted"]}>{hint}</span> : undefined}
      footer={<>
        <Button variant="quiet" onClick={() => onOpenChange(false)}>Cancel</Button>
        <Button variant={stops ? "danger" : "primary"} solid={stops} leadingIcon={stops ? "stop" : andArchive ? "archive" : undefined}
          disabled={!ready || Boolean(refused) || busy}
          aria-describedby={hint ? footId : undefined} onClick={confirm} data-testid="close-confirm">{label}</Button>
      </>}>
      <FormStack>
        {stops ? (
          <LiveWork className={styles["live"]} items={live} summary={`Closing stops ${liveWords(live)}. Nothing is kept to resume.`} />
        ) : null}
        <ChoiceList<CloseResolution | "nothing_to_merge"> label="Why it is closed" value={why ?? ("" as CloseResolution)} options={options}
          onChange={(r) => r !== "nothing_to_merge" && setWhy(r)} data-testid="close-why" />
        {why === "duplicate" ? (
          <div className={styles["stack"]} data-testid="duplicate-picker">
            {dup ? (
              <div className={styles["picked"]}>
                <RefLead type="task" taskKey={dup.key} status={dup.status} />
                <span className={styles["title"]}>{dup.title}</span>
                <StatusMark status={dup.status} size="sm" />
                <Button size="sm" variant="quiet" onClick={() => setDup(null)}>Change</Button>
              </div>
            ) : (
              <SearchPicker<LifecycleTaskRef> label="Duplicate of" placeholder="Find the task that covers it, by key or title" autoFocus={false}
                findOnEmpty find={find} optionKey={(x) => x.key} onPick={setDup}
                optionDisabled={(x) => (x.key === t.key ? "This task" : x.duplicateOf ? `Closed as a duplicate of ${x.duplicateOf}: pick that one` : null)}
                renderOption={(x) => (
                  <span className={styles["option"]}>
                    <RefLead type="task" taskKey={x.key} status={x.status} />
                    <span className={styles["title"]}>{x.title}</span>
                    <StatusMark status={x.status} size="sm" />
                  </span>
                )}
                empty={(q) => `No task matches “${q}”.`} />
            )}
            {dup ? (
              <Callout tone="neutral" data-testid="duplicate-handover">
                {waiting.length
                  ? <>{keys} {one ? "waits" : "wait"} on {t.key}. Once closed, {one ? "it waits" : "they wait"} on <b>{dup.key}</b> instead, and {one ? "starts" : "start"} when {dup.key} is merged.</>
                  : <>Nothing waits on {t.key}.</>}
                {" "}{dup.key} gets “Duplicated by {t.key}” in its Relations.
              </Callout>
            ) : null}
          </div>
        ) : null}
        {why && why !== "duplicate" && waiting.length ? (
          <Callout tone="attention" data-testid="waits-on">
            <span className={styles["callTitle"]}>{keys} {one ? "waits" : "wait"} on this.</span>{" "}
            {unrelate === true
              ? <>Closing also removes the relation, so {one ? "it" : "they"} can start without {t.key}.</>
              : <>{one ? "It" : "They"} will keep waiting: a task closed without merging releases nothing.</>}
            <div className={styles["callActions"]} data-testid="remove-relations">
              <Checkbox checked={unrelate} onCheckedChange={setUnrelate}
                label="Remove the relation" description={`${keys} no longer ${one ? "waits" : "wait"} on ${t.key}; said in ${one ? "its" : "their"} Activity.`} />
            </div>
          </Callout>
        ) : null}
        {pullRequests.length > 0 ? <PullRequestCloseRow prs={pullRequests} checked={closePR} onCheckedChange={setClosePR} /> : null}
        <Textarea label="Note (optional)" rows={2} value={note} onChange={(e) => setNote(e.target.value)} maxLength={2000}
          placeholder={andArchive ? "Said in Activity beside the close and the archive." : "Said in Activity beside the close."} />
      </FormStack>
    </Dialog>
  );
}

/** "#12", "#12 and #14", "#12, #14 and #15". */
export const pullRequestNumbers = (prs: ReadonlyArray<Pick<ClosablePullRequest, "number">>) => keyList(prs.map((p) => `#${p.number}`));

/**
 * Whether closing the task closes its open pull requests on GitHub too: one
 * `Checkbox` in a bordered row naming each. One someone else opened (the
 * task was started from it) is named with its own hint.
 */
function PullRequestCloseRow({ prs, checked, onCheckedChange }: {
  readonly prs: readonly ClosablePullRequest[]; readonly checked: CheckedState; readonly onCheckedChange: (c: CheckedState) => void;
}) {
  const on = checked === true;
  const ours = prs.filter((p) => p.openedBy === "dude");
  const theirs = prs.filter((p) => p.openedBy === "someone");
  const one = prs.length === 1;
  // One pull request is "it"; several are named, each group with its own hint.
  const name = (group: readonly ClosablePullRequest[], it: string) => (one ? it : pullRequestNumbers(group));
  const are = (group: readonly ClosablePullRequest[], singular: string, plural: string) => (group.length === 1 ? singular : plural);
  const description = [
    ours.length === 0 ? null : on
      ? `dude opened ${name(ours, "it")} for this task; closed with a comment saying why. ${are(ours, "Its branch is", "Their branches are")} kept.`
      : `The task closes; ${pullRequestNumbers(ours)} ${are(ours, "stays", "stay")} open on GitHub.`,
    theirs.length === 0 ? null : on
      ? `${name(theirs, "It")} ${are(theirs, "was", "were")} opened by someone else; dude closes ${are(theirs, "it", "them")}, with a comment saying why.`
      : `${name(theirs, "It")} ${are(theirs, "was", "were")} opened by someone else; ${are(theirs, "it stays", "they stay")} open unless you tick this.`,
  ].filter((s) => s !== null).join(" ");
  return (
    <div className={styles["prRow"]} data-testid="close-pull-request" data-opened-by={theirs.length === 0 ? "dude" : ours.length === 0 ? "someone" : "both"}>
      <Checkbox checked={checked} onCheckedChange={onCheckedChange} label={`Close pull request${one ? "" : "s"} ${pullRequestNumbers(prs)} on GitHub`}
        description={description} />
    </div>
  );
}

// ---------------------------------------------------------------------------
// Archive: an ended task, and an epic
// ---------------------------------------------------------------------------

export interface ArchiveDialogProps {
  readonly open: boolean;
  readonly onOpenChange: (open: boolean) => void;
  readonly task: LifecycleTaskRef;
  readonly onConfirm: () => void | Promise<void>;
  readonly refused?: string | undefined;
  readonly problem?: string | null | undefined;
  readonly busy?: boolean | undefined;
  readonly context?: ReactNode;
}

/** Archive a task that has ended: a plain confirmation. Keep it holds the focus. */
export function ArchiveDialog({ open, onOpenChange, task: t, onConfirm, refused, problem, busy, context }: ArchiveDialogProps) {
  const footId = useId();
  return (
    <Dialog open={open} onOpenChange={onOpenChange} size="sm" context={context} title={`Archive ${t.key}?`} footerProblem={problem}
      description="It leaves the board, the sidebar, every count and Find work. A link to it still works, and Archived lists it. Unarchive puts it back as it was."
      footerStart={refused ? <span id={footId} className={styles["muted"]}>{refused}</span> : undefined}
      footer={<>
        <Button variant="quiet" onClick={() => onOpenChange(false)} autoFocus>Keep it</Button>
        <Button variant="primary" leadingIcon="archive" disabled={Boolean(refused) || busy} aria-describedby={refused ? footId : undefined}
          onClick={() => void onConfirm()} data-testid="archive-confirm">Archive</Button>
      </>}>
      <TaskList tasks={[t]} />
    </Dialog>
  );
}

function TaskList({ tasks, testId }: { readonly tasks: ReadonlyArray<LifecycleTaskRef & { readonly owner?: string | undefined }>; readonly testId?: string }) {
  return (
    <ul className={styles["list"]} data-testid={testId}>
      {tasks.map((t) => (
        <li key={t.key}><StatusMark status={t.status} size="sm" /><span className="ds-mono">{t.key}</span><span className={styles["title"]}>{t.title}</span>
          {t.owner ? <span className={styles["muted"]}>{t.owner}</span> : null}</li>
      ))}
    </ul>
  );
}

export interface ArchiveEpicDialogProps {
  readonly open: boolean;
  readonly onOpenChange: (open: boolean) => void;
  readonly epic: string;
  /** Its tasks that haven't ended: archiving the epic is refused while there are any. */
  readonly unfinished: ReadonlyArray<LifecycleTaskRef & { readonly owner?: string | undefined }>;
  readonly ended: ReadonlyArray<LifecycleTaskRef>;
  /** Archive the epic (none unfinished), or its ended tasks alone (some are). */
  readonly onConfirm: () => void | Promise<void>;
  readonly refused?: string | undefined;
  readonly problem?: string | null | undefined;
  readonly busy?: boolean | undefined;
  readonly context?: ReactNode;
}

/**
 * Archive an epic. With every task ended, a confirmation that archives it
 * and them together. With some unfinished it is refused, listing them,
 * and its one action archives the ended ones, leaving the epic.
 */
export function ArchiveEpicDialog({ open, onOpenChange, epic, unfinished, ended, onConfirm, refused, problem, busy, context }: ArchiveEpicDialogProps) {
  const blocked = unfinished.length > 0;
  const footId = useId();
  const label = blocked ? `Archive its ${ended.length} ended task${ended.length === 1 ? "" : "s"}` : "Archive";
  return (
    <Dialog open={open} onOpenChange={onOpenChange} size="md" context={context} footerProblem={problem}
      title={blocked ? `${epic} can't be archived yet` : `Archive ${epic}?`}
      description={blocked
        ? `${unfinished.length} of its tasks haven't ended. Archiving the epic would hide work that is still going on, so dude won't: finish them, close them, or move them to another epic first.`
        : `It and its ${ended.length} task${ended.length === 1 ? "" : "s"} leave the board, the sidebar and every count. Unarchive brings them back.`}
      footerStart={refused ? <span id={footId} className={styles["muted"]}>{refused}</span> : undefined}
      footer={<>
        <Button variant="quiet" onClick={() => onOpenChange(false)} autoFocus>Cancel</Button>
        {!blocked || ended.length > 0 ? (
          <Button variant={blocked ? "secondary" : "primary"} leadingIcon="archive" disabled={Boolean(refused) || busy}
            aria-describedby={refused ? footId : undefined} onClick={() => void onConfirm()} data-testid={blocked ? "archive-ended" : "archive-confirm"}>{label}</Button>
        ) : null}
      </>}>
      <div className={styles["stack"]}>
        {blocked ? <Fieldset legend={`Not ended · ${unfinished.length}`}><TaskList tasks={unfinished} testId="unfinished" /></Fieldset> : null}
        {ended.length > 0 ? (
          <Fieldset legend={`Ended · ${ended.length}`} hint={blocked ? "These can go now: the epic stays, with only its unfinished tasks on the board." : undefined}>
            <TaskList tasks={ended} />
          </Fieldset>
        ) : null}
      </div>
    </Dialog>
  );
}
