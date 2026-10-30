/**
 * Comments on a Run's Changes: the attempt's items and the reader's drafts
 * placed on this diff's lines (`relocateCodeAnchor`), what lost its lines
 * at its file's head, and the editor under a range picked in the gutter.
 * Reads nothing itself: the task page hands it the items and the actions
 * (`FeedbackProvider`), and it renders through LiveDiff's `commenting`.
 */

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { CommentEditor, FeedbackCard, lineWords, type DiffRange, type DiffSide, type LiveDiffCommenting } from "@dude/design-system/components";
import { Icon } from "@dude/design-system";
import { FEEDBACK_LIMITS, relocateCodeAnchor, type FeedbackItem, type RunDiff } from "@dude/domain";
import { errorText } from "./hooks/useSave.tsx";
import { FEEDBACK_FULL, cardItem, commentableRun, threadOf, type FeedbackOnDiff } from "./feedback.tsx";

type Head = "outdated" | "unchanged";
type Placed = { item: FeedbackItem; at: { path: string; side: DiffSide; endLine: number } | { path: string; head: Head } };

/** Each code item whose file is in `diff`: on its lines, or at its file's head and why. */
export function placeItems(items: ReadonlyArray<FeedbackItem>, runId: string, diff: Pick<RunDiff, "checksum" | "files">): Placed[] {
  const out: Placed[] = [];
  for (const item of items) {
    const anchor = item.anchor;
    if (anchor.kind !== "code") continue;
    const at = relocateCodeAnchor(anchor, { runId, checksum: diff.checksum, files: diff.files });
    switch (at.kind) {
      case "absent":
        break;
      case "lines":
        out.push({ item, at: item.outdated ? { path: at.path, head: "outdated" } : { path: at.path, side: at.side, endLine: at.endLine } });
        break;
      case "unchanged":
        out.push({ item, at: { path: at.path, head: "unchanged" } });
        break;
      case "outdated":
        out.push({ item, at: { path: at.path ?? anchor.path, head: "outdated" } });
        break;
    }
  }
  return out;
}

/** A diff line's key: its file, side and number. */
export const lineKey = (path: string, side: DiffSide, line: number) => `${path}\u0000${side}\u0000${line}`;

/**
 * The editor under a picked range, with its own text: a keystroke re-renders it alone, not the diff.
 * `onText` mirrors each keystroke into the hook so a remount (a press in the gutter) starts from `initial`.
 */
function InlineCommentEditor({ fb, runId, checksum, range, initial, onText, onDone }: {
  fb: FeedbackOnDiff; runId: string; checksum: string; range: DiffRange; initial: string; onText: (text: string) => void; onDone: () => void;
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
      await fb.createDraft({ kind: "code", runId, path: range.path, side: range.side, startLine: range.startLine, endLine: range.endLine,
        diffChecksum: checksum }, text.trim());
      onDone();
    } catch (err) {
      setProblem(errorText(err));
      setSaving(false);
    }
  };
  return (
    <CommentEditor where={<>Comment on <b>{lineWords(range.startLine, range.endLine)}</b>{range.side === "old" ? " (removed)" : ""}</>} value={text} onChange={setText}
      onSave={() => void save()} onCancel={onDone} saving={saving} problem={problem} maxLength={FEEDBACK_LIMITS.body}
      full={fb.full ? FEEDBACK_FULL : undefined} placeholder="What should change on these lines…" />
  );
}

/** LiveDiff's `commenting` for one Run's diff: the same object, and the same callbacks, until what they draw changes. */
export function useDiffComments(fb: FeedbackOnDiff | null, runId: string, diff: RunDiff | null): LiveDiffCommenting | undefined {
  const [selection, setSelection] = useState<DiffRange | null>(null);
  const [editing, setEditing] = useState(false);
  const placed = useMemo(() => (fb && diff ? placeItems(fb.items, runId, diff) : []), [fb, runId, diff]);
  // Under a line, at a file's head, and how many per file: what the three render callbacks read.
  const { byLine, heads, counts } = useMemo(() => {
    const byLine = new Map<string, FeedbackItem[]>();
    const heads = new Map<string, Array<{ item: FeedbackItem; head: Head }>>();
    const counts = new Map<string, number>();
    for (const p of placed) {
      if ("side" in p.at) {
        const key = lineKey(p.at.path, p.at.side, p.at.endLine);
        byLine.set(key, [...(byLine.get(key) ?? []), p.item]);
      } else {
        heads.set(p.at.path, [...(heads.get(p.at.path) ?? []), { item: p.item, head: p.at.head }]);
      }
      counts.set(p.at.path, (counts.get(p.at.path) ?? 0) + 1);
    }
    return { byLine, heads, counts };
  }, [placed]);

  // Where the editor is: unset while a drag is under way, so a drag step leaves renderAfterLine as it is.
  const editorAt = editing ? selection : null;
  const onSelectionChange = useCallback((range: DiffRange | null, done: boolean) => {
    setSelection(range);
    setEditing(done && range !== null);
  }, []);
  // The unsaved comment: kept across the editor's remounts, cleared on save or cancel. A ref, so a keystroke re-renders only the editor.
  const unsaved = useRef("");
  const keepUnsaved = useCallback((text: string) => { unsaved.current = text; }, []);
  const closeEditor = useCallback(() => {
    unsaved.current = "";
    setEditing(false);
    setSelection(null);
  }, []);
  const card = useCallback((item: FeedbackItem, placement?: Head) => fb && diff ? (
    <div key={item.id} data-fb-item={item.id}>
      <FeedbackCard inline placement={placement} item={cardItem(item, fb.people, fb.runs)} thread={threadOf(item, fb.people, fb.runs)}
        readOnly={fb.readOnly && item.status !== "draft"} maxLength={FEEDBACK_LIMITS.body}
        onReply={(b) => fb.reply(item.id, b)} onResolve={() => fb.resolve(item.id)} onReopen={() => fb.reopen(item.id)}
        onSaveDraft={(b) => fb.saveDraft(item, b, { runId, diff })} onDelete={() => fb.deleteDraft(item.id)} />
    </div>
  ) : null, [fb, diff, runId]);
  const renderAfterLine = useCallback((path: string, side: DiffSide, line: number) => {
    const here = byLine.get(lineKey(path, side, line));
    const editor = fb && diff && editorAt && editorAt.path === path && editorAt.side === side && editorAt.endLine === line;
    if (!here && !editor) return null;
    return (
      <>
        {here?.map((i) => card(i))}
        {editor ? <InlineCommentEditor fb={fb} runId={runId} checksum={diff.checksum} range={editorAt}
          initial={unsaved.current} onText={keepUnsaved} onDone={closeEditor} /> : null}
      </>
    );
  }, [byLine, card, fb, diff, runId, editorAt, closeEditor, keepUnsaved]);
  const renderFileHead = useCallback((path: string) => {
    const off = heads.get(path);
    if (!off) return null;
    return (
      <>
        <p className="diffHeadNote"><Icon name="file-diff" size={12} /> Not on lines this diff shows</p>
        {off.map((o) => card(o.item, o.head))}
      </>
    );
  }, [heads, card]);
  const fileMark = useCallback((path: string) => {
    const n = counts.get(path);
    return n ? <span className="diffComments" aria-label={`${n} ${n === 1 ? "comment" : "comments"}`}><Icon name="comments" size={12} />{n}</span> : null;
  }, [counts]);
  const run = fb?.runs.get(runId);
  const canStart = fb !== null && fb.closed === null && commentableRun(run, fb.current);

  return useMemo(() => (fb && diff ? { canStart, maxLines: FEEDBACK_LIMITS.lines, selection, onSelectionChange, renderAfterLine, renderFileHead, fileMark } : undefined),
    [fb, diff, canStart, selection, onSelectionChange, renderAfterLine, renderFileHead, fileMark]);
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
