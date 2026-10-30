/**
 * The task's Changes tab: the whole-branch review of one repository of
 * one attempt, as a pinned snapshot (`useBranchDiff`), under one compact
 * bar. Comments on its lines and general feedback are drafts of the task's
 * feedback (`FeedbackOnDiff`), submitted with the rest from the drafts
 * tray. Pending session work is a separate scope that links to each live
 * session's own Changes; it is never merged into the branch view.
 */

import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type ReactNode } from "react";
import {
  AgentAvatar, CommentEditor, DiffStat, FeedbackCard, LiveDiff, Segmented, StatusMark, lineWords,
  type DiffRange, type DiffSide, type FileComments, type LiveDiffCommenting,
} from "@dude/design-system/components";
import { Button, Callout, EmptyState, IconButton, RefusedButton, SearchField, Select, Tooltip } from "@dude/design-system/primitives";
import { Icon, plural } from "@dude/design-system";
import { DEFAULT_RUN_ROLE, FEEDBACK_LIMITS, runLabel, type FeedbackItem, type TaskBranchDiff, type TaskBranchSnapshot } from "@dude/domain";
import type { ApiClient, Run, RunDiff } from "../api/client.ts";
import { errorText } from "../hooks/useSave.tsx";
import { FEEDBACK_FULL, LIVE_RUN, cardItem, threadOf, useTaskFeedback, type FeedbackOnDiff } from "../feedback.tsx";
import { lineKey } from "../diffComments.tsx";
import { runStatusLabel, runStatusShown } from "../runPresentation.tsx";
import {
  REFRESH_REFUSED, branchAnchorInput, commentsByFile, marksLabel, newerWords, nextComment, placeBranchItems, refreshedWords, short,
  useBranchDiff, type PlacedComment,
} from "../branchReview.ts";

/** The attempt's live sessions, newest first: their checkouts are the pending work. */
export function pendingSessions(runs: readonly Run[]): Run[] {
  return runs.filter((r) => LIVE_RUN.has(r.status) && r.kind !== "preview")
    .sort((a, b) => b.createdAt.localeCompare(a.createdAt));
}

/** At most once per this many ms, Pending work re-reads the sessions whose checkout moved. */
export const RUN_DIFF_EVERY_MS = 500;

/** `run.diff.updated`, coalesced: which sessions moved, counted so each batch is read once. */
export interface RunDiffsMoved {
  readonly n: number;
  readonly runs: readonly string[];
}

/** How many checkout reads Pending work has in flight at most. */
const PENDING_READS = 3;

/**
 * Read only while Pending is mounted, with at most three checkout reads in
 * flight: every session on mount (and Retry), then only the session an
 * event names. What was read stays shown while it is read again.
 */
function PendingWork({ client, runs, runDiffs, attempt, onOpenRunChanges }: Pick<ChangesTabProps, "client" | "runs" | "runDiffs" | "attempt" | "onOpenRunChanges">) {
  const pending = pendingSessions(runs);
  const ids = pending.map((r) => r.id).join("\u0000");
  const [reads, setReads] = useState<Record<string, { diff?: RunDiff; problem?: string }>>({});
  const [retry, setRetry] = useState(0);
  const queue = useRef<string[]>([]);
  const active = useRef(0);
  const alive = useRef(true);
  useEffect(() => {
    alive.current = true;
    return () => { alive.current = false; };
  }, []);
  const ask = useCallback((wanted: readonly string[]) => {
    for (const id of wanted) if (!queue.current.includes(id)) queue.current.push(id);
    const worker = async () => {
      active.current++;
      try {
        while (alive.current && queue.current.length) {
          const id = queue.current.shift()!;
          try {
            const diff = await client.runDiff(id);
            if (alive.current) setReads((was) => ({ ...was, [id]: { diff } }));
          } catch (err) {
            if (alive.current) setReads((was) => ({ ...was, [id]: { problem: errorText(err) } }));
          }
        }
      } finally {
        active.current--;
      }
    };
    // A worker takes its first id before it first waits: each started here holds one read.
    while (active.current < PENDING_READS && queue.current.length > 0) void worker();
  }, [client]);
  useEffect(() => {
    ask(ids ? ids.split("\u0000") : []);
  }, [ask, ids, retry]);
  const seen = useRef(runDiffs.n);
  useEffect(() => {
    if (seen.current === runDiffs.n) return;
    seen.current = runDiffs.n;
    const live = new Set(ids ? ids.split("\u0000") : []);
    ask(runDiffs.runs.filter((id) => live.has(id)));
  }, [ask, ids, runDiffs]);
  return <section className="changesRegion" data-testid="changes-pending" aria-label="Pending work">
    <p className="muted">Session checkout changes (uncommitted work included). Committed changes since each session's base are included too; these are separate from the branch snapshot.</p>
    {pending.length ? <ul className="changesSessions" aria-label="Sessions at work">
      {pending.map((r) => {
        const read = reads[r.id];
        const files = read?.diff?.files;
        return <li key={r.id} data-testid="changes-session" data-run={r.id}>
          <AgentAvatar role={r.role ?? DEFAULT_RUN_ROLE} size="sm" live={r.status === "running"} />
          <span className="changesSessionLabel">{runLabel(r)}</span>
          <StatusMark status={runStatusShown(r)} label={runStatusLabel(r)} size="sm" />
          <span data-testid="checkout-summary" role="status">
            {read?.problem ? <>Checkout changes unavailable: {read.problem} <Button size="sm" variant="quiet" onClick={() => setRetry((n) => n + 1)}>Retry</Button></>
              : files ? files.length ? <>{plural(files.length, "file")} <DiffStat additions={files.reduce((n, f) => n + f.additions, 0)} deletions={files.reduce((n, f) => n + f.deletions, 0)} /></>
                : "No checkout changes recorded" : "Reading checkout changes…"}
          </span>
          <Button size="sm" variant="quiet" trailingIcon="arrow-right" onClick={() => onOpenRunChanges(r.id)}>Open its Changes</Button>
        </li>;
      })}
    </ul> : <p data-testid="changes-pending-none">No live sessions on attempt {attempt}.</p>}
  </section>;
}

/** Whether the viewport is at least `px` wide, following it as it changes. */
function useWide(px: number): boolean {
  const query = `(min-width: ${px}px)`;
  const [wide, setWide] = useState(() => typeof window !== "undefined" && typeof window.matchMedia === "function" && window.matchMedia(query).matches);
  useLayoutEffect(() => {
    if (typeof window.matchMedia !== "function") return;
    const m = window.matchMedia(query);
    const on = () => setWide(m.matches);
    on();
    m.addEventListener?.("change", on);
    return () => m.removeEventListener?.("change", on);
  }, [query]);
  return wide;
}

/** The editor under a picked range: its own text, mirrored up so Refresh knows it is unsaved. */
function BranchCommentEditor({ fb, snapshot, range, initial, onText, onDone }: {
  fb: FeedbackOnDiff; snapshot: TaskBranchSnapshot; range: DiffRange; initial: string; onText: (text: string) => void; onDone: () => void;
}) {
  const [text, setTextState] = useState(initial);
  const setText = (t: string) => {
    setTextState(t);
    onText(t);
  };
  const [saving, setSaving] = useState(false);
  const [problem, setProblem] = useState<string | null>(null);
  const save = async () => {
    if (!text.trim()) return;
    setSaving(true);
    setProblem(null);
    try {
      await fb.createDraft(branchAnchorInput(snapshot, range), text.trim());
      onDone();
    } catch (err) {
      setProblem(errorText(err));
      setSaving(false);
    }
  };
  return (
    <CommentEditor where={<>Comment on <code>{range.path}</code> <b>{lineWords(range.startLine, range.endLine)}</b>{range.side === "old" ? " (removed)" : ""}</>}
      value={text} onChange={setText} onSave={() => void save()} onCancel={onDone} saving={saving} problem={problem} maxLength={FEEDBACK_LIMITS.body}
      full={fb.full ? FEEDBACK_FULL : undefined} placeholder="What should change on these lines…" />
  );
}

interface BranchComments {
  readonly commenting: LiveDiffCommenting | undefined;
  readonly byFile: Map<string, PlacedComment[]>;
  /** An inline editor holds text not yet saved. */
  readonly unsaved: boolean;
}

/**
 * LiveDiff's `commenting` on the snapshot shown: the attempt's branch items
 * placed on it, the editor under a picked range, and each file's mark.
 * `canStart` false (an earlier attempt, a done task): read only.
 */
function useBranchComments({ fb, diff, canStart, you, cursor, onFileComments }: {
  fb: FeedbackOnDiff; diff: TaskBranchDiff | null; canStart: boolean; you: string | null;
  cursor: Readonly<Record<string, number>>; onFileComments: (path: string) => void;
}): BranchComments {
  const snapshot = diff?.snapshot ?? null;
  const placed = useMemo(() => (diff && snapshot ? placeBranchItems(fb.items, { placements: diff.placements, snapshot }) : []),
    [fb.items, diff, snapshot]);
  const byFile = useMemo(() => commentsByFile(placed), [placed]);
  const { byLine, heads } = useMemo(() => {
    const byLine = new Map<string, FeedbackItem[]>();
    const heads = new Map<string, Array<{ item: FeedbackItem; head: "outdated" | "unchanged" }>>();
    for (const p of placed) {
      if ("head" in p.at) heads.set(p.path, [...(heads.get(p.path) ?? []), { item: p.item, head: p.at.head }]);
      else {
        const key = lineKey(p.path, p.at.side, p.at.endLine);
        byLine.set(key, [...(byLine.get(key) ?? []), p.item]);
      }
    }
    return { byLine, heads };
  }, [placed]);

  const [selection, setSelection] = useState<DiffRange | null>(null);
  const [editing, setEditing] = useState(false);
  const [unsaved, setUnsaved] = useState(false);
  // Cards holding unsaved words (a draft's editor, a reply), by item: told by FeedbackCard's onDirtyChange.
  const [cardUnsaved, setCardUnsaved] = useState<Readonly<Record<string, boolean>>>({});
  const text = useRef("");
  const onText = useCallback((t: string) => {
    text.current = t;
    setUnsaved(t.trim() !== "");
  }, []);
  const close = useCallback(() => {
    text.current = "";
    setUnsaved(false);
    setEditing(false);
    setSelection(null);
  }, []);
  useEffect(() => { close(); setCardUnsaved({}); }, [snapshot?.id, close]);
  // A range picked while the open editor holds words keeps the editor where it is.
  const unsavedRef = useRef(unsaved);
  unsavedRef.current = unsaved;
  const onSelectionChange = useCallback((range: DiffRange | null, done: boolean) => {
    if (unsavedRef.current) return;
    setSelection(range);
    setEditing(done && range !== null);
  }, []);
  const editorAt = editing ? selection : null;

  const card = useCallback((item: FeedbackItem, placement?: "outdated" | "unchanged") => (
    <div key={item.id} data-fb-item={item.id} tabIndex={-1} className="branchComment">
      <FeedbackCard inline placement={placement} item={cardItem(item, fb.people, fb.runs)} thread={threadOf(item, fb.people, fb.runs)}
        onDirtyChange={(dirty) => setCardUnsaved((was) => ({ ...was, [item.id]: dirty }))}
        readOnly={!!diff?.readOnly || (fb.readOnly && item.status !== "draft")} maxLength={FEEDBACK_LIMITS.body}
        onReply={(b) => fb.reply(item.id, b)} onResolve={() => fb.resolve(item.id)} onReopen={() => fb.reopen(item.id)}
        onSaveDraft={(b) => fb.saveDraft(item, b, null)} onDelete={() => fb.deleteDraft(item.id)} />
    </div>
  ), [fb, diff?.readOnly]);
  const renderAfterLine = useCallback((path: string, side: DiffSide, line: number) => {
    const here = byLine.get(lineKey(path, side, line));
    const editor = snapshot && editorAt && editorAt.path === path && editorAt.side === side && editorAt.endLine === line;
    if (!here && !editor) return null;
    return (
      <>
        {here?.map((i) => card(i))}
        {editor ? <BranchCommentEditor fb={fb} snapshot={snapshot} range={editorAt} initial={text.current} onText={onText} onDone={close} /> : null}
      </>
    );
  }, [byLine, card, fb, snapshot, editorAt, onText, close]);
  const renderFileHead = useCallback((path: string) => {
    const off = heads.get(path);
    if (!off) return null;
    return (
      <>
        <p className="diffHeadNote"><Icon name="file-diff" size={12} /> Not on lines this snapshot shows</p>
        {off.map((o) => card(o.item, o.head))}
      </>
    );
  }, [heads, card]);
  const fileComments = useCallback((path: string): FileComments | null => {
    const list = byFile.get(path);
    if (!list?.length) return null;
    const at = cursor[path];
    return { count: list.length, label: marksLabel(list, you), position: at === undefined ? undefined : (at % list.length) + 1 };
  }, [byFile, cursor, you]);

  const commenting = useMemo<LiveDiffCommenting | undefined>(() => (snapshot ? {
    canStart, maxLines: FEEDBACK_LIMITS.lines, selection, onSelectionChange, renderAfterLine, renderFileHead, fileComments, onFileComments,
  } : undefined), [snapshot, canStart, selection, onSelectionChange, renderAfterLine, renderFileHead, fileComments, onFileComments]);
  return { commenting, byFile, unsaved: unsaved || Object.values(cardUnsaved).some(Boolean) };
}

export interface ChangesTabProps {
  readonly client: ApiClient;
  readonly taskId: string;
  /** The attempt shown, and the task's current one: an earlier attempt's branch is only to read. */
  readonly attempt: number;
  readonly current: number;
  /** The attempt shown's sessions. */
  readonly runs: readonly Run[];
  /** The page's re-read: moves `newer`, never the snapshot shown. */
  readonly version: number;
  /** Sessions whose checkout moved (`run.diff.updated`, coalesced): Pending work re-reads those alone. */
  readonly runDiffs: RunDiffsMoved;
  readonly fb: FeedbackOnDiff;
  readonly you: string | null;
  /** Open a session on its own Changes. */
  readonly onOpenRunChanges: (runId: string) => void;
}

type Scope = "branch" | "pending";

const RELATION_WORDS: Record<TaskBranchSnapshot["relation"], string> = {
  initial: "the first snapshot of this branch",
  "fast-forward": "fast-forward (new commits)",
  "merged-target": "target merged in, base moved",
  rewritten: "rewritten (rebase or force-push)",
};

const when = (at: string | null) => (at ? new Date(at).toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" }) : "never");

/** The snapshot's details: what was compared, from where, as of when. */
function SnapshotDetails({ diff, snapshot }: { diff: TaskBranchDiff; snapshot: TaskBranchSnapshot }) {
  const pr = diff.pullRequest;
  return (
    <dl className="snapshotFacts" data-testid="snapshot-facts">
      <dt>Snapshot</dt><dd><code>{snapshot.id}</code>{diff.readOnly ? " · read-only" : " · pinned"}</dd>
      <dt>Target</dt><dd><code>{snapshot.targetRef}</code> at <code>{short(snapshot.targetSha)}</code></dd>
      <dt>Merge base</dt><dd><code>{short(snapshot.mergeBaseSha)}</code> · merge-base(target, head), recomputed at every capture</dd>
      <dt>Branch head</dt><dd><code>{short(snapshot.headSha)}</code> · <code>{snapshot.branchRef}</code></dd>
      <dt>Checksum</dt><dd><code>{snapshot.checksum}</code></dd>
      <dt>Source</dt><dd>{snapshot.source === "github-compare" ? "GitHub compare" : snapshot.source} · captured {when(snapshot.capturedAt)}</dd>
      {pr ? (
        <>
          <dt>Pull request</dt>
          <dd>#{pr.number} head <code>{short(pr.headSha)}</code> · synced {when(pr.syncedAt)}
            {pr.headSha !== snapshot.headSha ? <> · not the branch head reviewed here: its checks apply to <code>{short(pr.headSha)}</code></> : " · the branch head reviewed here"}</dd>
        </>
      ) : null}
      <dt>Lineage</dt><dd>{snapshot.previousSnapshotId ? <>after <code>{snapshot.previousSnapshotId}</code> · </> : null}{RELATION_WORDS[snapshot.relation]}</dd>
      {snapshot.status === "partial" ? <><dt>Partial</dt><dd>{snapshot.reason ?? "The file list may be incomplete."}</dd></> : null}
      <dt>Scope</dt><dd>Committed changes only · uncommitted work excluded</dd>
    </dl>
  );
}

/** Under the file list: what this is, and what it does not show line by line. */
function listNote(snapshot: TaskBranchSnapshot): ReactNode {
  const unseen = snapshot.files.filter((f) => f.binary || f.truncated);
  return (
    <>
      Committed changes only · pinned branch snapshot. Pending session work is separate.
      {unseen.length > 0 ? (
        <span className="changesUnseen" data-testid="changes-unseen">
          {" "}Not shown line by line: {unseen.map((f, i) => <span key={f.path}>{i > 0 ? ", " : ""}<code>{f.path.split("/").pop()}</code> ({f.binary ? "binary" : "too large"})</span>)}.
        </span>
      ) : null}
      {snapshot.stats.fileCapReached || snapshot.status === "partial" ? (
        <span className="changesUnseen" data-testid="changes-partial">
          {" "}{snapshot.reason ?? "GitHub lists at most 300 files in a compare: more may have changed than are listed."}
        </span>
      ) : null}
    </>
  );
}

/** The tab: the bar, at most one slim line, then the diff (or what stands in its place, truthfully). */
export function ChangesTab(props: ChangesTabProps) {
  // A separately mounted reader avoids a current-attempt fetch/cache flashing in history.
  return props.attempt !== props.current ? <HistoricalChanges key={`${props.taskId}:${props.attempt}`} {...props} /> : <ChangesReview {...props} />;
}

function HistoricalChanges(props: ChangesTabProps) {
  const read = useTaskFeedback(props.client, props.taskId, props.attempt, props.version);
  const refuse = async () => { throw new Error(`Attempt ${props.attempt} is read-only.`); };
  const fb: FeedbackOnDiff = { ...props.fb, items: read.list.feedback, readOnly: true,
    closed: `Attempt ${props.attempt} is read-only.`, createDraft: refuse, saveDraft: refuse,
    deleteDraft: refuse, reply: refuse, resolve: refuse, reopen: refuse, focus: null, openOnTab: () => {} };
  return <>
    {read.problem ? <Callout tone="danger" data-testid="historical-feedback-problem">Could not read attempt {props.attempt}'s feedback: {read.problem}
      <Button size="sm" onClick={() => void read.reload()}>Retry</Button></Callout> : !read.loaded ? <p role="status">Reading earlier feedback…</p> : null}
    <ChangesReview {...props} fb={fb} />
  </>;
}

function ChangesReview({ client, taskId, attempt, current, runs, version, runDiffs, fb, you, onOpenRunChanges }: ChangesTabProps) {
  // The words "General feedback" from 1600px; its icon below, so the bar stays one row at 1440.
  const wide = useWide(1600);
  const desktopDiff = useWide(641);
  const [repo, setRepo] = useState<string | null>(null);
  const [scope, setScope] = useState<Scope>("branch");
  const [filter, setFilter] = useState("");
  const [filterOpen, setFilterOpen] = useState(false);
  const filterField = useRef<HTMLInputElement>(null);
  const [selected, setSelected] = useState<string | null>(null);
  const [details, setDetails] = useState(false);
  const detailsButton = useRef<HTMLButtonElement>(null);
  const detailsClose = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    if (!details) return;
    detailsClose.current?.focus();
    const closeOutside = (e: MouseEvent) => {
      if (!(e.target as Element).closest(".snapshotPopover, .snapshotLine, .prMark")) setDetails(false);
    };
    const escape = (e: KeyboardEvent) => {
      if (e.key === "Escape") { setDetails(false); detailsButton.current?.focus(); }
    };
    document.addEventListener("mousedown", closeOutside);
    document.addEventListener("keydown", escape);
    return () => { document.removeEventListener("mousedown", closeOutside); document.removeEventListener("keydown", escape); };
  }, [details]);
  const [toolbar, setToolbar] = useState<HTMLElement | null>(null);
  const [cursor, setCursor] = useState<Record<string, number>>({});
  const [target, setTarget] = useState<{ id: string; n: number } | null>(null);
  const [refreshedFrom, setRefreshedFrom] = useState<TaskBranchSnapshot | null>(null);
  const [general, setGeneral] = useState(false);
  const [generalText, setGeneralText] = useState("");
  const [generalBusy, setGeneralBusy] = useState(false);
  const [generalProblem, setGeneralProblem] = useState<string | null>(null);

  const read = useBranchDiff(client, taskId, attempt, repo, version);
  const diff = read.diff;
  const snapshot = diff?.availability.state === "ok" ? diff.snapshot : null;
  const readOnly = diff?.readOnly ?? attempt !== current;
  const closed = readOnly ? `Attempt ${attempt} is read-only: its branch takes no new comments.` : fb.closed;

  const all = useMemo(() => snapshot?.files ?? [], [snapshot]);
  const needle = filter.trim().toLowerCase();
  const visible = useMemo(() => (needle ? all.filter((f) => f.path.toLowerCase().includes(needle)) : all), [all, needle]);

  // Each file's comments, in the order its mark visits them; the cycle restarts with the snapshot and the repository.
  const showNextRef = useRef<(path: string) => void>(() => {});
  const onFileComments = useCallback((path: string) => showNextRef.current(path), []);
  const comments = useBranchComments({ fb, diff, canStart: closed === null, you, cursor, onFileComments });
  showNextRef.current = (path: string) => {
    const list = comments.byFile.get(path);
    if (!list?.length) return;
    const next = nextComment(cursor[path], list.length);
    setCursor((c) => ({ ...c, [path]: next }));
    if (!visible.some((f) => f.path === path)) {
      // Hidden by the filter: cleared, and the file shown alone if one was.
      setFilter("");
      setFilterOpen(false);
      if (selected !== null) setSelected(path);
    } else if (selected !== null && selected !== path) {
      setSelected(path);
    }
    setTarget((t) => ({ id: list[next]!.item.id, n: (t?.n ?? 0) + 1 }));
  };
  useLayoutEffect(() => {
    if (!target) return;
    const el = document.querySelector<HTMLElement>(`[data-fb-item="${CSS.escape(target.id)}"]`);
    if (!el) return;
    for (const was of document.querySelectorAll("[data-fb-item][data-flash]")) was.removeAttribute("data-flash");
    void el.offsetWidth; // restarts the flash on the same comment
    el.setAttribute("data-flash", "");
    el.focus({ preventScroll: true });
    el.scrollIntoView?.({ block: "center" });
  }, [target]);
  const filteredOut = useMemo(() => (needle ? [...comments.byFile.keys()].filter((p) => !visible.some((f) => f.path === p)) : []),
    [needle, comments.byFile, visible]);

  const unsaved = comments.unsaved || (general && generalText.trim() !== "");
  const pickRepo = (r: string) => {
    setRepo(r);
    setSelected(null);
    setFilter("");
    setCursor({});
    setRefreshedFrom(null);
  };
  const refresh = () => {
    if (!snapshot || !diff?.newer || unsaved) return;
    setRefreshedFrom(snapshot);
    setCursor({});
    read.refresh();
  };
  const saveGeneral = async () => {
    if (!generalText.trim()) return;
    setGeneralBusy(true);
    setGeneralProblem(null);
    try {
      await fb.createDraft({ kind: "general" }, generalText.trim());
      setGeneral(false);
      setGeneralText("");
    } catch (err) {
      setGeneralProblem(errorText(err));
    } finally {
      setGeneralBusy(false);
    }
  };

  const repositories = diff?.repositories ?? [];
  const pr = diff?.pullRequest ?? null;
  const prDiffers = snapshot !== null && pr !== null && pr.headSha !== snapshot.headSha;
  const totals = snapshot ? snapshot.stats : null;
  const canWrite = closed === null && diff !== null && diff.availability.state !== "unavailable";

  const bar = (
    <div className="changesBar" data-testid="changes-bar" data-filtering={filterOpen || filter !== "" || undefined}>
      {repositories.length > 1 ? (
        <Select size="sm" className="changesRepo" aria-label="Repository" value={diff?.repository ?? repositories[0]!.name}
            onValueChange={(r) => { if (!unsaved) pickRepo(r); }} options={repositories.map((r) => ({ value: r.name, label: r.name }))} disabled={unsaved} data-testid="changes-repo" />
      ) : null}
      <Segmented label="Review scope" size="toolbar" value={scope} onChange={(s: Scope) => setScope(s)} data-testid="changes-scope"
        options={[{ value: "branch", label: "Branch" }, { value: "pending", label: "Pending work" }]} />
      {snapshot && scope === "branch" ? (
        <button ref={detailsButton} type="button" className="snapshotLine" data-testid="snapshot-line" aria-expanded={details} aria-haspopup="dialog"
          aria-label={`Snapshot ${snapshot.targetRef} ${short(snapshot.mergeBaseSha)} to ${short(snapshot.headSha)}, ${plural(snapshot.stats.files, "file")}, +${snapshot.stats.additions} −${snapshot.stats.deletions}. Show details`}
          onClick={() => setDetails((d) => !d)}>
          <span className="snapshotRef">{snapshot.targetRef} </span>
          <span data-testid="snapshot-base">{short(snapshot.mergeBaseSha)}</span>…<span data-testid="snapshot-head">{short(snapshot.headSha)}</span>
          {totals ? <span className="snapshotTotals"> · {plural(totals.files, "file")} <DiffStat additions={totals.additions} deletions={totals.deletions} /></span> : null}
          <Icon name="info" size={14} />
        </button>
      ) : null}
      {prDiffers && scope === "branch" ? (
        <Tooltip content={<>Published PR head <code>{short(pr.headSha)}</code> differs from branch head <code>{short(snapshot.headSha)}</code>. PR checks apply to <code>{short(pr.headSha)}</code>.</>} side="bottom">
          <button type="button" className="prMark" data-testid="pr-mark" onClick={() => setDetails(true)}
            aria-label={`Published pull request at ${short(pr.headSha)}, not the reviewed branch head ${short(snapshot.headSha)}. Show details`}>
            PR at <code>{short(pr.headSha)}</code>
          </button>
        </Tooltip>
      ) : null}
      <span className="changesSpacer" />
      {scope === "branch" && snapshot && all.length > 0 ? (
        <>
          <SearchField className="changesFilter" label="Filter files" placeholder="Filter files" value={filter} data-testid="changes-filter"
            onValueChange={(v) => { setFilter(v); setSelected(null); }} disabled={unsaved} onBlur={() => { if (!filter) setFilterOpen(false); }} ref={filterField} />
          <IconButton className="changesFilterIcon" icon="search" size="sm" label="Filter files" aria-expanded={filterOpen}
            onClick={() => { setFilterOpen(true); requestAnimationFrame(() => filterField.current?.focus()); }} />
        </>
      ) : null}
      {canWrite && scope === "branch" ? (
        wide ? <Button size="sm" variant="quiet" leadingIcon="edit" onClick={() => setGeneral(true)} data-testid="changes-general">General feedback</Button>
          : <IconButton icon="edit" size="sm" label="Write general feedback" onClick={() => setGeneral(true)} data-testid="changes-general" />
      ) : null}
      <span className="changesTools" ref={setToolbar} />
      {details && snapshot && diff ? (
        <div className="snapshotPopover" role="dialog" aria-label="Snapshot details" data-testid="snapshot-details"
          onKeyDown={(e) => { if (e.key === "Escape") { setDetails(false); detailsButton.current?.focus(); } }}
          // Focus leaving it (Tab past Close, Shift+Tab out) closes it: a popover nobody is in does not stay open over the diff.
          onBlur={(e) => {
            const to = e.relatedTarget as Element | null;
            if (to && !e.currentTarget.contains(to) && !to.closest(".snapshotLine, .prMark")) setDetails(false);
          }}>
          <SnapshotDetails diff={diff} snapshot={snapshot} />
          <Button ref={detailsClose} size="sm" variant="quiet" onClick={() => { setDetails(false); detailsButton.current?.focus(); }}>Close</Button>
        </div>
      ) : null}
    </div>
  );

  const newer = !readOnly && snapshot ? diff?.newer ?? null : null;
  const notice = scope !== "branch" ? null : newer && snapshot ? (
    <div className="slimLine" data-testid="changes-newer" role="status">
      <Icon name="info" size={14} />
      <span className="slimText"><b>New changes available</b> · <span data-testid="changes-newer-words">{newerWords(newer, snapshot.targetRef)}</span></span>
      {unsaved
        ? <RefusedButton size="sm" variant="secondary" label="Refresh" reason={REFRESH_REFUSED} data-testid="changes-refresh" />
        : <Button size="sm" variant="secondary" onClick={refresh} data-testid="changes-refresh">Refresh</Button>}
    </div>
  ) : refreshedFrom && snapshot && refreshedFrom.id !== snapshot.id ? (
    <div className="slimLine" data-testid="changes-refreshed" role="status">
      <span className="slimText">{refreshedWords(refreshedFrom, snapshot)}</span>
      <IconButton icon="close" size="sm" label="Dismiss" onClick={() => setRefreshedFrom(null)} />
    </div>
  ) : readOnly ? (
    <div className="slimLine" data-testid="changes-read-only">
      <span className="slimText">Attempt {attempt} · ended, read-only. Drafts from the current attempt are kept, not submitted here.</span>
    </div>
  ) : null;

  const generalEditor = general && canWrite ? (
    <div className="changesGeneral" data-testid="changes-general-editor">
      <CommentEditor where="General · the task as a whole" value={generalText} onChange={setGeneralText} onSave={() => void saveGeneral()}
        onCancel={() => { setGeneral(false); setGeneralText(""); setGeneralProblem(null); }} saving={generalBusy} problem={generalProblem}
        maxLength={FEEDBACK_LIMITS.body} full={fb.full ? FEEDBACK_FULL : undefined} placeholder="What should change, for the task as a whole…" />
    </div>
  ) : null;

  const body = scope === "pending" ? (
    <PendingWork client={client} runs={runs} runDiffs={runDiffs} attempt={attempt} onOpenRunChanges={onOpenRunChanges} />
  ) : read.problem && !diff ? (
    <Callout tone="danger" data-testid="changes-problem">Could not read the branch: {read.problem}</Callout>
  ) : !diff ? (
    <p className="muted changesRegion" data-testid="changes-loading">Reading the branch…</p>
  ) : diff.availability.state === "preparing" ? (
    <section className="changesRegion" data-testid="changes-preparing">
      <h2>Preparing the branch snapshot</h2>
      <p>dude is reading the branch from GitHub{diff.repository ? <> for <code>{diff.repository}</code></> : null}. This is not an empty diff: it shows here once captured.</p>
    </section>
  ) : diff.availability.state === "unavailable" ? (
    <section className="changesRegion" data-testid="changes-unavailable">
      <h2>Snapshot unavailable</h2>
      <p>{diff.availability.reason}</p>
      <p className="muted">This is not an empty diff. Line comments wait until it can be read; no draft was discarded.</p>
      <Button size="sm" variant="secondary" onClick={read.reload} data-testid="changes-retry">Try again</Button>
    </section>
  ) : diff.availability.state === "none" ? (
    <section className="changesRegion" data-testid="changes-none">
      <EmptyState compact icon="git-pr" title="No branch snapshot" description={diff.availability.reason} />
    </section>
  ) : snapshot ? (
    <LiveDiff className="changesDiff" data-testid="branch-diff" files={visible} base={snapshot.mergeBaseSha} toolbarIn={toolbar} scopeLabel={null}
      view={desktopDiff ? undefined : "unified"} filteredOut={filteredOut} selected={selected} onSelectedChange={setSelected} commenting={comments.commenting} listNote={listNote(snapshot)}
      emptyMessage={needle && all.length > 0
        ? <span data-testid="changes-no-match">No files match “{filter}”. <Button size="sm" variant="quiet" onClick={() => setFilter("")}>Clear the filter</Button></span>
        : <span data-testid="changes-empty">No committed changes between the merge base <code>{short(snapshot.mergeBaseSha)}</code> and the branch head <code>{short(snapshot.headSha)}</code>.</span>} />
  ) : null;

  return (
    <div className="changesTab" data-testid="changes-tab" data-snapshot={snapshot?.id}>
      {bar}
      {notice}
      {generalEditor}
      {body}
    </div>
  );
}
