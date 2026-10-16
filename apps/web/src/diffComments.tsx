/**
 * Comments on a Run's Changes: the attempt's items and the reader's drafts
 * placed on this diff's lines (`relocateCodeAnchor`), what lost its lines
 * at its file's head, and the editor under a range picked in the gutter.
 * Reads nothing itself: the task page hands it the items and the actions
 * (`FeedbackProvider`), and it renders through LiveDiff's `commenting`.
 */

import { useEffect, useMemo, useRef, useState } from "react";
import { CommentEditor, FeedbackCard, type DiffRange, type DiffSide, type LiveDiffCommenting } from "@dude/design-system/components";
import { Icon } from "@dude/design-system";
import { FEEDBACK_LIMITS, relocateCodeAnchor, type FeedbackCodeAnchor, type FeedbackItem, type RunDiff } from "@dude/domain";
import { errorText } from "./hooks/useSave.tsx";
import { FEEDBACK_FULL, cardItem, commentableRun, threadOf, type FeedbackOnDiff } from "./feedback.tsx";

type Placed = { item: FeedbackItem; at: { path: string; side: DiffSide; endLine: number } | { path: string; head: "outdated" | "unchanged" } };

/** Each code item whose file is in `diff`: on its lines, or at its file's head and why. */
export function placeItems(items: ReadonlyArray<FeedbackItem>, runId: string, diff: Pick<RunDiff, "checksum" | "files">): Placed[] {
  const out: Placed[] = [];
  for (const item of items) {
    if (item.anchor.kind !== "code") continue;
    const at = relocateCodeAnchor(item.anchor, { runId, checksum: diff.checksum, files: diff.files });
    if (at.kind === "absent") continue;
    if (at.kind === "lines" && !item.outdated) out.push({ item, at: { path: at.path, side: at.side, endLine: at.endLine } });
    else if (at.kind === "lines") out.push({ item, at: { path: at.path, head: "outdated" } });
    else out.push({ item, at: { path: at.path ?? (item.anchor as FeedbackCodeAnchor).path, head: at.kind === "unchanged" ? "unchanged" : "outdated" } });
  }
  return out;
}

const lineWords = (r: DiffRange) => (r.startLine === r.endLine ? `line ${r.startLine}` : `lines ${r.startLine}–${r.endLine}`);

/** LiveDiff's `commenting` for one Run's diff, with the editor's state. */
export function useDiffComments(fb: FeedbackOnDiff | null, runId: string, diff: RunDiff | null): LiveDiffCommenting | undefined {
  const [selection, setSelection] = useState<DiffRange | null>(null);
  const [editing, setEditing] = useState(false);
  const [text, setText] = useState("");
  const [saving, setSaving] = useState(false);
  const [problem, setProblem] = useState<string | null>(null);
  const placed = useMemo(() => (fb && diff ? placeItems(fb.items, runId, diff) : []), [fb, runId, diff]);
  const byLine = useMemo(() => {
    const m = new Map<string, FeedbackItem[]>();
    for (const p of placed) {
      if (!("side" in p.at)) continue;
      const key = `${p.at.path}\u0000${p.at.side}\u0000${p.at.endLine}`;
      m.set(key, [...(m.get(key) ?? []), p.item]);
    }
    return m;
  }, [placed]);
  const heads = useMemo(() => {
    const m = new Map<string, Array<{ item: FeedbackItem; head: "outdated" | "unchanged" }>>();
    for (const p of placed) if ("head" in p.at) m.set(p.at.path, [...(m.get(p.at.path) ?? []), { item: p.item, head: p.at.head }]);
    return m;
  }, [placed]);
  const counts = useMemo(() => {
    const m = new Map<string, number>();
    for (const p of placed) m.set(p.at.path, (m.get(p.at.path) ?? 0) + 1);
    return m;
  }, [placed]);

  if (!fb || !diff) return undefined;
  const run = fb.runs.get(runId);
  const canStart = fb.closed === null && commentableRun(run, fb.current);

  const save = async () => {
    if (!selection || !text.trim()) return;
    setSaving(true);
    setProblem(null);
    try {
      await fb.createDraft({ kind: "code", runId, path: selection.path, side: selection.side, startLine: selection.startLine, endLine: selection.endLine,
        diffChecksum: diff.checksum }, text.trim());
      setText("");
      setEditing(false);
      setSelection(null);
    } catch (err) {
      setProblem(errorText(err));
    } finally {
      setSaving(false);
    }
  };
  const cancel = () => {
    setEditing(false);
    setSelection(null);
    setProblem(null);
  };
  const card = (item: FeedbackItem, placement?: "outdated" | "unchanged") => (
    <div key={item.id} data-fb-item={item.id}>
      <FeedbackCard inline placement={placement} item={cardItem(item, fb.people, fb.runs)} thread={threadOf(item, fb.people, fb.runs)}
        readOnly={fb.readOnly && item.status !== "draft"} maxLength={FEEDBACK_LIMITS.body}
        onReply={(b) => fb.reply(item.id, b)} onResolve={() => fb.resolve(item.id)} onReopen={() => fb.reopen(item.id)}
        onSaveDraft={(b) => fb.saveDraft(item, b, diff)} onDelete={() => fb.deleteDraft(item.id)} />
    </div>
  );

  return {
    canStart, maxLines: FEEDBACK_LIMITS.lines, selection,
    onSelectionChange: (range, done) => {
      setSelection(range);
      setEditing(done && range !== null);
      setProblem(null);
    },
    renderAfterLine: (path, side, line) => {
      const here = byLine.get(`${path}\u0000${side}\u0000${line}`);
      const editor = editing && selection && selection.path === path && selection.side === side && selection.endLine === line;
      if (!here && !editor) return null;
      return (
        <>
          {here?.map((i) => card(i))}
          {editor ? (
            <CommentEditor where={<>Comment on <b>{lineWords(selection)}</b>{side === "old" ? " (removed)" : ""}</>} value={text} onChange={setText}
              onSave={() => void save()} onCancel={cancel} saving={saving} problem={problem} maxLength={FEEDBACK_LIMITS.body}
              full={fb.full ? FEEDBACK_FULL : undefined} placeholder="What should change on these lines…" />
          ) : null}
        </>
      );
    },
    renderFileHead: (path) => {
      const off = heads.get(path);
      if (!off) return null;
      return (
        <>
          <p className="diffHeadNote"><Icon name="file-diff" size={12} /> Not on lines this diff shows</p>
          {off.map((o) => card(o.item, o.head))}
        </>
      );
    },
    fileMark: (path) => {
      const n = counts.get(path);
      return n ? <span className="diffComments" aria-label={`${n} ${n === 1 ? "comment" : "comments"}`}><Icon name="comments" size={12} />{n}</span> : null;
    },
  };
}

/** Bring an item into view once its card is drawn: a jump from the tray or the Feedback tab, once per jump. */
export function useScrollToItem(focus: FeedbackOnDiff["focus"], runId: string, drawn: unknown): void {
  const done = useRef(0);
  useEffect(() => {
    if (!focus || focus.runId !== runId || done.current === focus.n) return;
    const el = document.querySelector<HTMLElement>(`[data-fb-item="${CSS.escape(focus.itemId)}"]`);
    // Not drawn yet (the diff or the items still to read): tried again when they are.
    if (!el) return;
    done.current = focus.n;
    el.scrollIntoView({ block: "center" });
  }, [focus, runId, drawn]);
}
