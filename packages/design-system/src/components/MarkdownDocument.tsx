import { useEffect, useLayoutEffect, useRef, useState, type HTMLAttributes, type ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { markdownSourceTokens, sourceCounts } from "../util/markdownSource.ts";
import { Button } from "../primitives/Button.tsx";
import { Markdown } from "./Markdown.tsx";
import styles from "./MarkdownDocument.module.css";

export interface MarkdownDocumentProps extends Omit<HTMLAttributes<HTMLDivElement>, "onChange" | "children"> {
  /** The saved text. */
  readonly source: string;
  /**
   * Save an edit. Resolve to go back to reading; reject to stay editing
   * (the caller says why). Without it the document is read-only.
   */
  readonly onSave?: ((source: string) => Promise<void> | void) | undefined;
  /** Shown when there is no text, in place of it. */
  readonly empty?: ReactNode;
  /** Beside the counts in the bar: when it last changed, a History link. */
  readonly meta?: ReactNode;
  /** Start editing (a new document). */
  readonly defaultEditing?: boolean | undefined;
  /** Tells the caller when editing starts and ends, so it can hold other controls. */
  readonly onEditingChange?: ((editing: boolean) => void) | undefined;
  readonly saveLabel?: string | undefined;
}

/**
 * A Markdown document that reads rendered and edits as source, in place.
 *
 * Reading is the default: the text rendered, with an Edit button. Edit
 * swaps the rendering for the Markdown source at the same place, lightly
 * highlighted (headings, lists, code, bold, `{{variables}}`) so its
 * structure reads while it is written; Save keeps it, Cancel puts it back.
 * One mode at a time — a split preview halves the width of both.
 *
 * The highlighting is a layer of spans under a transparent textarea with
 * the same metrics, so the caret, selection and undo stay the browser's.
 */
export function MarkdownDocument({
  source,
  onSave,
  empty = "Nothing here yet.",
  meta,
  defaultEditing = false,
  onEditingChange,
  saveLabel = "Save",
  className,
  ...rest
}: MarkdownDocumentProps) {
  const [editing, setEditingState] = useState(defaultEditing && Boolean(onSave));
  const [draft, setDraft] = useState(source);
  const [saving, setSaving] = useState(false);
  const area = useRef<HTMLTextAreaElement>(null);
  const setEditing = (on: boolean) => {
    setEditingState(on);
    onEditingChange?.(on);
  };

  // A new saved text (another save, a restore) replaces what is read.
  useEffect(() => {
    if (!editing) setDraft(source);
  }, [source, editing]);

  // The textarea grows with its text, so the page scrolls, not the field.
  useLayoutEffect(() => {
    const el = area.current;
    if (!el) return;
    el.style.height = "auto";
    el.style.height = `${el.scrollHeight}px`;
  }, [draft, editing]);

  const counts = sourceCounts(editing ? draft : source);
  const dirty = draft !== source;

  async function save() {
    if (!onSave) return;
    setSaving(true);
    try {
      await onSave(draft);
      setEditing(false);
    } catch {
      // The caller said why; the draft stays for another try.
    } finally {
      setSaving(false);
    }
  }

  return (
    <div className={cx(styles["root"], editing && styles["editing"], className)} data-editing={editing || undefined} {...rest}>
      <div className={styles["bar"]}>
        <span className={styles["counts"]}>
          {counts.lines} {counts.lines === 1 ? "line" : "lines"} · {counts.words} {counts.words === 1 ? "word" : "words"}
        </span>
        {meta ? <span className={styles["meta"]}>{meta}</span> : null}
        <span className={styles["actions"]}>
          {editing ? (
            <>
              <Button size="sm" variant="quiet" disabled={saving} data-testid="markdown-cancel"
                onClick={() => {
                  setDraft(source);
                  setEditing(false);
                }}>
                Cancel
              </Button>
              <Button size="sm" variant="primary" disabled={saving || !dirty} data-testid="markdown-save" onClick={() => void save()}>
                {saving ? "Saving…" : saveLabel}
              </Button>
            </>
          ) : onSave ? (
            <Button size="sm" variant="secondary" leadingIcon="edit" data-testid="markdown-edit"
              onClick={() => {
                setDraft(source);
                setEditing(true);
                requestAnimationFrame(() => {
                  area.current?.focus();
                  area.current?.setSelectionRange(0, 0);
                });
              }}>
              Edit
            </Button>
          ) : null}
        </span>
      </div>
      {editing ? (
        <div className={styles["source"]}>
          <pre className={styles["highlight"]} aria-hidden="true">
            {markdownSourceTokens(draft).map((line, n) => (
              <span key={n}>
                {line.map((t, k) => (t.kind === "text" ? t.text : <span key={k} className={styles[t.kind]}>{t.text}</span>))}
                {"\n"}
              </span>
            ))}
          </pre>
          <textarea
            ref={area}
            className={styles["textarea"]}
            value={draft}
            spellCheck={false}
            aria-label="Markdown source"
            data-testid="markdown-source"
            onChange={(e) => setDraft(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Escape") {
                setDraft(source);
                setEditing(false);
              } else if (e.key === "Enter" && (e.metaKey || e.ctrlKey) && dirty) {
                e.preventDefault();
                void save();
              }
            }}
          />
        </div>
      ) : (
        <div className={styles["view"]} data-testid="markdown-view">
          {source.trim() ? <Markdown source={source} variant="document" /> : <p className={styles["empty"]}>{empty}</p>}
        </div>
      )}
    </div>
  );
}
