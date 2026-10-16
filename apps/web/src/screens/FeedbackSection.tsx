/**
 * People's feedback on the task page: the Feedback tab (general feedback
 * written at its top, the reader's drafts, what needs handling, what is
 * resolved or answered), the drafts tray at the foot of every tab, the
 * submit confirmation, and the chip saying how many items hold the merge.
 *
 * One controller (`useTaskFeedbackPage`) reads the current attempt's
 * feedback with the page and after each of the reader's actions, and
 * hands the same items and actions to the tab, the tray and the open
 * session's Changes.
 */

import { useCallback, useMemo, useRef, useState } from "react";
import {
  CommentEditor, DraftsTray, FeedbackCard, FeedbackEmpty, PageRail, PageRailBlock, PageWithRail, Segmented, SubmitFeedbackDialog,
  feedbackHolds, type DraftsTrayRow,
} from "@dude/design-system/components";
import { Button, Callout, KeyValueList, Tooltip, useToast } from "@dude/design-system/primitives";
import { Icon, plural } from "@dude/design-system";
import { FEEDBACK_LIMITS, type FeedbackAnchorInput, type FeedbackItem } from "@dude/domain";
import type { ApiClient, PullRequest, TaskDetail } from "../api/client.ts";
import { errorText } from "../hooks/useSave.tsx";
import type { People } from "../people.tsx";
import {
  FEEDBACK_FULL, Submitter, cardItem, draftAnchorInput, feedbackDestination, runsOf, submitRefusal, threadOf, useTaskFeedback,
  type FeedbackOnDiff, type ViewedRunDiff,
} from "../feedback.tsx";

export interface TaskFeedbackController {
  /** The current attempt's items and the reader's drafts on it. */
  readonly items: ReadonlyArray<FeedbackItem>;
  readonly drafts: ReadonlyArray<FeedbackItem>;
  /** Submitted items of the current attempt that hold the merge, as the API counts them. */
  readonly blocking: number;
  /** Whether the list has been read at least once: before that, `blocking` is not known. */
  readonly loaded: boolean;
  readonly onDiff: FeedbackOnDiff;
  readonly reload: () => void;
  /** The draft open in its editor on the Feedback tab. */
  readonly editing: string | null;
  readonly setEditing: (id: string | null) => void;
  readonly highlight: string | null;
  readonly problem: string | null;
  /** Open an item's Run on Changes, brought into view. */
  readonly showInDiff: (runId: string, itemId: string) => void;
}

export function useTaskFeedbackPage({ client, taskId, task, current, version, people, onShowInDiff, onShowOnTab }: {
  client: ApiClient; taskId: string; task: TaskDetail | null; current: number; version: number; people: People;
  /** Open the Run's Changes on an item. */
  onShowInDiff: (runId: string) => void;
  onShowOnTab: () => void;
}): TaskFeedbackController {
  const { list, loaded, reload, problem } = useTaskFeedback(client, taskId, undefined, version);
  const [editing, setEditing] = useState<string | null>(null);
  const [highlight, setHighlight] = useState<string | null>(null);
  const [focus, setFocus] = useState<FeedbackOnDiff["focus"]>(null);
  // Kept across reloads that leave every Run's id and status as they were: a new map redraws the open session's diff.
  const runsKey = task?.runs.map((r) => `${r.id}:${r.status}`).join(",") ?? "";
  const runs = useMemo(() => runsOf(task), [runsKey]); // eslint-disable-line react-hooks/exhaustive-deps -- `runsKey` stands for the runs
  const drafts = useMemo(() => list.feedback.filter((i) => i.status === "draft"), [list]);
  const after = useCallback(async <T,>(p: Promise<T>): Promise<T> => {
    const out = await p;
    await reload();
    return out;
  }, [reload]);
  const latest = useRef({ onShowInDiff, onShowOnTab });
  latest.current = { onShowInDiff, onShowOnTab };

  const status = task?.status;
  const closed = status === "done" ? "This task is done: feedback on it goes in a follow-up task." : null;
  const onDiff = useMemo<FeedbackOnDiff>(() => ({
    taskId, items: list.feedback, current, closed, readOnly: status === "done", full: drafts.length >= FEEDBACK_LIMITS.drafts,
    runs, people, focus,
    createDraft: (anchor: FeedbackAnchorInput, body: string) => after(client.createFeedbackDraft(taskId, { body, anchor })),
    saveDraft: async (item: FeedbackItem, body: string, viewed: ViewedRunDiff | null) => {
      // A code draft keeps its lines where they are now in its own Run's diff: the one on screen
      // only when it is that Run's, else read; another Run's numbers and checksum are not its own.
      const anchor: FeedbackAnchorInput = item.anchor.kind === "code"
        ? draftAnchorInput(item.anchor, viewed && viewed.runId === item.anchor.runId
          ? viewed.diff : await client.runDiff(item.anchor.runId).catch(() => null))
        : item.anchor;
      return after(client.updateFeedbackDraft(item.id, { body, anchor }));
    },
    deleteDraft: (id: string) => after(client.deleteFeedbackDraft(id)),
    reply: (id: string, body: string) => after(client.replyFeedback(id, body)),
    resolve: (id: string) => after(client.resolveFeedback(id)),
    reopen: (id: string) => after(client.reopenFeedback(id)),
    openOnTab: (id: string) => {
      setHighlight(id);
      latest.current.onShowOnTab();
    },
  }), [taskId, status, list.feedback, current, closed, drafts.length, runs, people, focus, after, client]);

  const showInDiff = useCallback((runId: string, itemId: string) => {
    setFocus((f) => ({ itemId, runId, n: (f?.n ?? 0) + 1 }));
    latest.current.onShowInDiff(runId);
  }, []);

  return { items: list.feedback, drafts, blocking: list.blocking, loaded, problem, onDiff, reload: () => void reload(), editing, setEditing, highlight, showInDiff };
}

// ---------------------------------------------------------------------------
// The tray and the submit
// ---------------------------------------------------------------------------

/** The drafts tray at the foot of the page, and the confirmation its Submit opens. */
export function FeedbackTray({ client, task, fb, current, prs, openBlockers }: {
  client: ApiClient; task: TaskDetail; fb: TaskFeedbackController; current: number; prs: ReadonlyArray<PullRequest>; openBlockers: number;
}) {
  const toast = useToast();
  const [open, setOpen] = useState(false);
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<string | null>(null);
  // One per page: a retry of the same drafts keeps its key.
  const submitter = useMemo(() => new Submitter(client), [client]);
  const destination = feedbackDestination(task, current, openBlockers);
  const refused = submitRefusal(task.status) ?? undefined;
  const rows: DraftsTrayRow[] = fb.drafts.map((d) => ({ id: d.id, anchor: d.anchor, body: d.body }));
  const open_ = prs.filter((pr) => pr.state === "open" || pr.state === "draft");
  const holds = open_.length > 0
    ? <>Pull request{open_.length > 1 ? "s" : ""} {open_.map((pr, i) => <span key={pr.id}>{i > 0 ? ", " : ""}<b>#{pr.number}</b></span>)} can't merge from dude and the task can't be marked done.
      An answer instead of a change, or anyone's Resolve, lets each go.</>
    : <>No pull request opens and the task can't be marked done while any is open. An answer instead of a change, or anyone's Resolve, lets each go.</>;

  const submit = async () => {
    setBusy(true);
    setProblem(null);
    try {
      const out = await submitter.submit(task.id, fb.drafts.map((d) => d.id));
      setConfirming(false);
      setOpen(false);
      toast.toast({ tone: "success", title: `Submitted ${plural(out.itemIds.length, "feedback item")}`,
        description: out.waiting ? `Nobody works on them yet: ${out.waiting}.` : undefined });
      fb.reload();
    } catch (err) {
      // Kept open with the same drafts: Submit again sends the same key.
      setProblem(errorText(err));
    } finally {
      setBusy(false);
    }
  };

  const jump = (id: string) => {
    const d = fb.drafts.find((x) => x.id === id);
    if (d?.anchor.kind === "code") fb.showInDiff(d.anchor.runId, id);
    else fb.onDiff.openOnTab(id);
  };
  return (
    <>
      <DraftsTray drafts={rows} max={FEEDBACK_LIMITS.drafts} destination={destination.who} open={open} onOpenChange={setOpen}
        onSubmit={() => { setProblem(null); setConfirming(true); }} submitRefused={refused}
        onJump={jump} onEdit={(id) => { fb.setEditing(id); fb.onDiff.openOnTab(id); }}
        onDelete={(id) => void fb.onDiff.deleteDraft(id).catch((err: unknown) => toast.toast({ tone: "danger", title: "Could not delete it", description: errorText(err) }))} />
      {confirming ? (
        <SubmitFeedbackDialog drafts={rows} destination={destination} holds={holds} busy={busy} problem={problem}
          onCancel={() => setConfirming(false)} onConfirm={() => void submit()} />
      ) : null}
    </>
  );
}

/** "3 feedback items hold the merge", beside the status: neutral, never amber, a way to the Feedback tab. */
export function MergeHoldChip({ count, onOpen }: { count: number; onOpen: () => void }) {
  if (count === 0) return null;
  return (
    <Tooltip content="Submitted feedback not yet handled: dude won't merge the pull request, and the task can't be marked done, until each is resolved or answered.">
      <button type="button" className="mergeHold" onClick={onOpen} data-testid="merge-hold">
        <Icon name="comments" size={12} /> <b>{plural(count, "feedback item")}</b> {count === 1 ? "holds" : "hold"} the merge
      </button>
    </Tooltip>
  );
}

// ---------------------------------------------------------------------------
// The tab
// ---------------------------------------------------------------------------

/** An earlier attempt's feedback, read for it alone: to read, never to act on. */
export function EarlierFeedbackTab({ client, task, fb, shown, current, version, onCurrent }: {
  client: ApiClient; task: TaskDetail; fb: TaskFeedbackController; shown: number; current: number; version: number; onCurrent: () => void;
}) {
  const { list, loaded, problem } = useTaskFeedback(client, task.id, shown, version);
  return <FeedbackTab task={task} fb={fb} items={list.feedback} loaded={loaded} problem={problem} earlier shown={shown} current={current} onCurrent={onCurrent} />;
}

/** General feedback written at the tab's top, with its own text: a keystroke re-renders it alone, not the tab's cards. */
function GeneralComposer({ createDraft, full }: { createDraft: FeedbackOnDiff["createDraft"]; full: boolean }) {
  const [general, setGeneral] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [problem, setProblem] = useState<string | null>(null);
  const save = async () => {
    if (general === null || !general.trim()) return;
    setSaving(true);
    setProblem(null);
    try {
      await createDraft({ kind: "general" }, general.trim());
      setGeneral(null);
    } catch (err) {
      setProblem(errorText(err));
    } finally {
      setSaving(false);
    }
  };
  return general === null ? (
    <button type="button" className="feedbackGeneral" onClick={() => setGeneral("")} disabled={full} data-testid="general-open">
      <Icon name="message" size={16} />
      <span>{full ? FEEDBACK_FULL : "Write general feedback on the task…"}</span>
      {full ? null : <span className="muted">a private draft, submitted with your line comments</span>}
    </button>
  ) : (
    <CommentEditor where={<><Icon name="message" size={14} /> <b>General</b> <span className="muted">· the task as a whole</span></>}
      value={general} onChange={setGeneral} onSave={() => void save()} onCancel={() => { setGeneral(null); setProblem(null); }}
      saving={saving} problem={problem} maxLength={FEEDBACK_LIMITS.body} placeholder="What should change, for the task as a whole…" />
  );
}

export function FeedbackTab({ task, fb, items, loaded, problem: readProblem, earlier, shown, current, onCurrent, onStopped }: {
  task: TaskDetail;
  fb: TaskFeedbackController;
  /** The attempt shown's items: the current one's (`fb.items`), or an earlier one's. */
  items: ReadonlyArray<FeedbackItem>;
  /** Whether `items` has been read; with `problem`, why the last read failed. */
  loaded: boolean;
  problem: string | null;
  earlier: boolean;
  shown: number;
  current: number;
  onCurrent: () => void;
  /** A failed or aborted task: its way back. */
  onStopped?: (() => void) | undefined;
}) {
  const [filter, setFilter] = useState<"needs" | "all">("needs");
  const d = fb.onDiff;
  const drafts = earlier ? [] : items.filter((i) => i.status === "draft");
  const submitted = items.filter((i) => i.status !== "draft");
  const needs = submitted.filter((i) => feedbackHolds(i.status, i.resolutionPending));
  const settled = submitted.filter((i) => !feedbackHolds(i.status, i.resolutionPending));
  const done = task.status === "done";
  const stopped = task.status === "failed" || task.status === "aborted";
  const readOnly = earlier || done;
  const authors = [...new Set(submitted.map((i) => i.author?.name.split(" ")[0] ?? "someone"))];

  const card = (i: FeedbackItem) => (
    <FeedbackCard key={i.id} item={cardItem(i, d.people, d.runs)} thread={threadOf(i, d.people, d.runs)} readOnly={readOnly && i.status !== "draft"}
      className={fb.highlight === i.id ? "feedbackHighlight" : undefined}
      onJump={i.anchor.kind === "code" ? () => fb.showInDiff((i.anchor as { runId: string }).runId, i.id) : undefined}
      onReply={(b) => d.reply(i.id, b)} onResolve={() => d.resolve(i.id)} onReopen={() => d.reopen(i.id)}
      onSaveDraft={(b) => d.saveDraft(i, b, null)} onDelete={() => d.deleteDraft(i.id)} maxLength={FEEDBACK_LIMITS.body}
      editing={i.status === "draft" ? fb.editing === i.id : undefined}
      onEditingChange={(on) => fb.setEditing(on ? i.id : null)} />
  );

  return (
    <PageWithRail rail={
      <PageRail aria-label="Feedback">
        <PageRailBlock label="Show">
          <Segmented label="Show" size="sm" value={filter} onChange={setFilter} data-testid="feedback-filter"
            options={[{ value: "needs", label: `Needs handling · ${needs.length}` }, { value: "all", label: `All · ${submitted.length}` }]} />
        </PageRailBlock>
        <PageRailBlock label={earlier ? `Attempt ${shown}` : "This attempt"}>
          <KeyValueList items={[
            { label: earlier ? "Open when set aside" : "Holding the merge", value: String(needs.length) },
            { label: "Resolved", value: String(settled.filter((i) => i.status === "resolved").length) },
            { label: "Answered", value: String(settled.filter((i) => i.status === "replied").length) },
            { label: "From", value: authors.join(", ") || "nobody yet" },
          ]} />
          <p className="muted">Feedback belongs to an attempt. Starting over begins with none; an earlier attempt's stays here to read.</p>
        </PageRailBlock>
        <PageRailBlock label="What holds the merge">
          <p className="muted">An item submitted and not yet handled: open, with an agent, or resolved in a commit not yet published. An answer, or anyone's Resolve, lets it go; Reopen holds it again.</p>
        </PageRailBlock>
      </PageRail>
    }>
      <div className="taskPane feedbackPane" data-testid="feedback-tab">
        {readProblem ? (
          <Callout tone="danger" data-testid="feedback-problem">
            <b>Could not read this attempt's feedback.</b> {readProblem}
          </Callout>
        ) : null}
        {earlier ? (
          <Callout tone="neutral" data-testid="feedback-earlier">
            <span className="feedbackLine">
              <span>Attempt {shown} was set aside. Its feedback is here to read: nothing on it can be replied to, resolved or reopened.</span>
              <Button size="sm" variant="secondary" trailingIcon="arrow-right" onClick={onCurrent}>Go to attempt {current}</Button>
            </span>
          </Callout>
        ) : done ? (
          <Callout tone="neutral" data-testid="feedback-done">
            <b>This task is done.</b> It takes no more feedback: what is left to change goes in a follow-up task.
          </Callout>
        ) : (
          <>
            {stopped ? (
              <Callout tone={task.status === "failed" ? "danger" : "attention"} data-testid="feedback-recover">
                <span className="feedbackLine">
                  <span><b>This task {task.status === "failed" ? "failed" : "was aborted"}.</b> Recover it first, then submit: your drafts are kept, and you can still write them.</span>
                  {onStopped ? <Button size="sm" variant="secondary" onClick={onStopped}>Pick it back up…</Button> : null}
                </span>
              </Callout>
            ) : null}
            <GeneralComposer createDraft={d.createDraft} full={d.full} />
          </>
        )}

        {drafts.length > 0 ? (
          <section className="feedbackGroup" aria-label="Your drafts" data-testid="feedback-drafts">
            <h2 className="feedbackGroupHead"><Icon name="lock" size={12} /> Your drafts <span className="ds-tnum">{drafts.length}</span>
              <span className="muted">only you see them · submitted together from the tray</span></h2>
            {drafts.map(card)}
          </section>
        ) : null}

        {submitted.length === 0 ? (
          // Unread or unreadable is not "no feedback".
          drafts.length > 0 || !loaded || readProblem ? null : (
            <FeedbackEmpty description={readOnly ? "Nobody gave this attempt feedback."
              : <>Comment on lines in a Run's <b>Changes</b> (Sessions › a Run › Changes), or write general feedback above. What you write is a private draft until you submit it; then it goes to whoever drives the task, and holds the merge until it is handled.</>} />
          )
        ) : (
          <>
            <section className="feedbackGroup" aria-label="Needs handling" data-testid="feedback-needs">
              <h2 className="feedbackGroupHead">Needs handling <span className="ds-tnum">{needs.length}</span>
                <span className="muted">holds the pull request's merge and Mark done</span></h2>
              {needs.length === 0 ? <p className="muted">Nothing: every item is resolved or answered.</p> : needs.map(card)}
            </section>
            {filter === "all" ? (
              <section className="feedbackGroup" aria-label="Resolved or answered" data-testid="feedback-settled">
                <h2 className="feedbackGroupHead">Resolved or answered <span className="ds-tnum">{settled.length}</span>
                  <span className="muted">holds nothing · can be reopened</span></h2>
                {settled.map(card)}
              </section>
            ) : settled.length > 0 ? (
              <span><Button variant="quiet" size="sm" trailingIcon="chevron-down" onClick={() => setFilter("all")} data-testid="feedback-show-settled">
                Show {settled.length} resolved or answered</Button></span>
            ) : null}
          </>
        )}
      </div>
    </PageWithRail>
  );
}
