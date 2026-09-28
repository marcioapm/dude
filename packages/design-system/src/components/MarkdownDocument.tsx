import { useLayoutEffect, useRef, useState, type HTMLAttributes, type ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { Button } from "../primitives/Button.tsx";
import { Markdown } from "./Markdown.tsx";
import styles from "./MarkdownDocument.module.css";

export interface MarkdownDocumentProps extends Omit<HTMLAttributes<HTMLDivElement>, "children" | "onChange"> {
  readonly source: string;
  /** Saving it; resolve when saved, reject to stay in the editor (the caller says why). Omit for read-only. */
  readonly onSave?: ((source: string) => void | Promise<void>) | undefined;
  /** Shown when there is nothing to read. */
  readonly emptyText?: ReactNode;
  /** Beside Edit in the bar: "Last changed by Eli · yesterday · History". */
  readonly meta?: ReactNode;
  /** Label for the source field, for screen readers. */
  readonly label?: string | undefined;
}

/** A line of source as spans: what is Markdown syntax, what is a variable, what is code. */
export type HighlightKind = "heading-mark" | "heading" | "list-mark" | "quote" | "fence" | "code" | "bold" | "var" | "text";
export type HighlightSpan = readonly [HighlightKind, string];

/**
 * Light highlighting for Markdown source, one line at a time: the marks
 * of headings, lists and quotes, code fences and what is inside them,
 * inline code, bold and `{{variables}}`. Enough to read the shape of a
 * prompt while editing it — not a parser; `Markdown` renders it.
 */
export function highlightMarkdown(source: string): HighlightSpan[][] {
  let inFence = false;
  return source.split("\n").map((line): HighlightSpan[] => {
    if (/^\s*(```|~~~)/.test(line)) {
      inFence = !inFence;
      return [["fence", line]];
    }
    if (inFence) return [["code", line]];
    const heading = /^(#{1,6} )(.*)$/.exec(line);
    if (heading) return [["heading-mark", heading[1]!], ["heading", heading[2]!]];
    if (/^\s*> /.test(line)) return [["quote", line]];
    const out: HighlightSpan[] = [];
    let rest = line;
    const list = /^(\s*(?:[-*+]|\d+\.) )/.exec(rest);
    if (list) {
      out.push(["list-mark", list[1]!]);
      rest = rest.slice(list[1]!.length);
    }
    const inline = /(\{\{[^}]+\}\}|`[^`]+`|\*\*[^*]+\*\*)/g;
    let at = 0;
    for (const m of rest.matchAll(inline)) {
      if (m.index! > at) out.push(["text", rest.slice(at, m.index)]);
      const t = m[0];
      out.push([t.startsWith("{{") ? "var" : t.startsWith("`") ? "code" : "bold", t]);
      at = m.index! + t.length;
    }
    if (at < rest.length) out.push(["text", rest.slice(at)]);
    return out;
  });
}

/**
 * A Markdown document you read, and can edit in place. Reading is the
 * default: the rendered document. Edit swaps it for its source, at the
 * same place — a plain textarea over a highlighted copy, so the caret,
 * selection and undo are the browser's own — and Save or Cancel swaps it
 * back. No split view: one thing at a time, the thing you are doing.
 */
export function MarkdownDocument({ source, onSave, emptyText = "Nothing here yet.", meta, label = "Markdown source", className, ...rest }: MarkdownDocumentProps) {
  const [draft, setDraft] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const area = useRef<HTMLTextAreaElement>(null);
  const editing = draft !== null;

  // The field grows with its text, so the page scrolls, not the field.
  useLayoutEffect(() => {
    const el = area.current;
    if (!el) return;
    el.style.height = "auto";
    el.style.height = `${el.scrollHeight}px`;
  }, [draft]);

  const start = () => {
    setDraft(source);
    requestAnimationFrame(() => {
      area.current?.focus();
      area.current?.setSelectionRange(0, 0);
    });
  };
  const save = async () => {
    if (draft === null || !onSave) return;
    setSaving(true);
    try {
      await onSave(draft);
      setDraft(null);
    } catch {
      // The caller said why; the draft stays for another go.
    } finally {
      setSaving(false);
    }
  };

  const text = draft ?? source;
  const lines = text.split("\n").length;
  const words = text.trim() ? text.trim().split(/\s+/).length : 0;

  return (
    <div className={cx(styles["root"], editing && styles["editing"], className)} data-editing={editing ? "true" : undefined} {...rest}>
      <div className={styles["bar"]}>
        {words > 0 ? (
          <span className={styles["count"]}>
            {lines} {lines === 1 ? "line" : "lines"} · {words} {words === 1 ? "word" : "words"}
          </span>
        ) : null}
        {meta ? <span className={styles["meta"]}>{meta}</span> : null}
        <span className={styles["spacer"]} />
        {!onSave ? null : editing ? (
          <>
            <Button size="sm" variant="quiet" onClick={() => setDraft(null)} disabled={saving}>
              Cancel
            </Button>
            <Button size="sm" variant="primary" onClick={() => void save()} loading={saving} disabled={draft === source}>
              Save
            </Button>
          </>
        ) : (
          <Button size="sm" variant="secondary" leadingIcon="edit" onClick={start}>
            Edit
          </Button>
        )}
      </div>
      {editing ? (
        <div className={styles["source"]}>
          <pre className={styles["highlight"]} aria-hidden>
            {highlightMarkdown(draft).map((spans, i) => (
              <span key={i} className={styles["line"]}>
                {spans.map(([kind, t], j) => (
                  <span key={j} className={styles[`hl-${kind}`]}>
                    {t}
                  </span>
                ))}
                {"\n"}
              </span>
            ))}
          </pre>
          <textarea
            ref={area}
            className={styles["field"]}
            value={draft}
            spellCheck={false}
            aria-label={label}
            onChange={(e) => setDraft(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Escape") {
                e.preventDefault();
                setDraft(null);
              } else if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
                e.preventDefault();
                void save();
              }
            }}
          />
        </div>
      ) : (
        <div className={styles["view"]}>{source.trim() ? <Markdown source={source} variant="document" /> : <p className={styles["empty"]}>{emptyText}</p>}</div>
      )}
    </div>
  );
}
