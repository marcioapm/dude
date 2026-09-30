import { useLayoutEffect, useRef, useState, type HTMLAttributes, type KeyboardEvent, type ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { Button, IconButton } from "../primitives/Button.tsx";
import { RowMenu } from "../primitives/RowMenu.tsx";
import { isPromptVariable } from "@dude/domain";
import { Markdown, type MarkdownVariant } from "./Markdown.tsx";
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
  /** Start in the editor (a new document, nothing to read yet). Needs onSave. */
  readonly defaultEditing?: boolean | undefined;
  /** Told when editing starts and ends, so the caller can hold other controls meanwhile. */
  readonly onEditingChange?: ((editing: boolean) => void) | undefined;
  /** How it reads: `prompt` for an agent's instructions (default), `document` for an artifact. */
  readonly variant?: Exclude<MarkdownVariant, "message"> | undefined;
  /** A single newline reads as a line break (`Markdown breaks`): on for text a person writes. */
  readonly breaks?: boolean | undefined;
  /** What `{{ }}` offers to insert while editing, with what each is. None: no button. */
  readonly variables?: ReadonlyArray<{ readonly name: string; readonly description: string }> | undefined;
}

/**
 * A toolbar is one Tab stop: only the button last moved to takes Tab (a
 * roving tabindex, `stop`), and ← → Home End move along it. Returns the
 * index moved to, or null for any other key.
 */
function moveAlongToolbar(e: KeyboardEvent<HTMLElement>): number | null {
  if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(e.key)) return null;
  const all = [...e.currentTarget.querySelectorAll<HTMLButtonElement>("button")];
  const buttons = all.filter((b) => !b.disabled);
  const at = buttons.indexOf(document.activeElement as HTMLButtonElement);
  if (at === -1) return null;
  e.preventDefault();
  const next = e.key === "Home" ? 0 : e.key === "End" ? buttons.length - 1 : (at + (e.key === "ArrowRight" ? 1 : -1) + buttons.length) % buttons.length;
  buttons[next]?.focus();
  return all.indexOf(buttons[next]!);
}

/**
 * Markdown around the selection, or at the caret: `**` for bold, a
 * backtick for code, and `## ` at the start of each selected line for a
 * heading (again removes it). Returns the new text and selection.
 */
export function applyFormat(
  text: string,
  start: number,
  end: number,
  kind: "heading" | "bold" | "code" | { readonly insert: string },
): { text: string; start: number; end: number } {
  if (typeof kind === "object") {
    const next = text.slice(0, start) + kind.insert + text.slice(end);
    const at = start + kind.insert.length;
    return { text: next, start: at, end: at };
  }
  if (kind === "heading") {
    const from = text.lastIndexOf("\n", start - 1) + 1;
    const lineEnd = text.indexOf("\n", Math.max(end - 1, from));
    const to = lineEnd === -1 ? text.length : lineEnd;
    const lines = text.slice(from, to).split("\n");
    const off = lines.every((l) => /^#{1,6} /.test(l));
    const changed = lines.map((l) => (off ? l.replace(/^#{1,6} /, "") : `## ${l.replace(/^#{1,6} /, "")}`)).join("\n");
    return { text: text.slice(0, from) + changed + text.slice(to), start: from, end: from + changed.length };
  }
  const mark = kind === "bold" ? "**" : "`";
  const inner = text.slice(start, end);
  const next = text.slice(0, start) + mark + inner + mark + text.slice(end);
  return { text: next, start: start + mark.length, end: end + mark.length };
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
      // A name dude does not fill in reads as text, as the agent will get it.
      out.push([t.startsWith("{{") ? (isPromptVariable(t) ? "var" : "text") : t.startsWith("`") ? "code" : "bold", t]);
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
export function MarkdownDocument({
  source,
  onSave,
  emptyText = "Nothing here yet.",
  meta,
  label = "Markdown source",
  defaultEditing = false,
  onEditingChange,
  variant = "prompt",
  breaks,
  variables,
  className,
  ...rest
}: MarkdownDocumentProps) {
  const [draft, setDraftState] = useState<string | null>(defaultEditing && onSave ? source : null);
  const setDraft = (next: string | null) => {
    if ((next === null) !== (draft === null)) onEditingChange?.(next !== null);
    setDraftState(next);
  };
  const [saving, setSaving] = useState(false);
  // Which formatting button takes Tab (see moveAlongToolbar).
  const [stop, setStop] = useState(0);
  const area = useRef<HTMLTextAreaElement>(null);
  const editing = draft !== null;

  // The field grows with its text, so the page scrolls, not the field.
  useLayoutEffect(() => {
    const el = area.current;
    if (!el) return;
    el.style.height = "auto";
    el.style.height = `${el.scrollHeight}px`;
  }, [draft]);

  // Edit puts the caret at the start of the field in the commit that opens
  // it. A frame later would move a selection made in between back to 0.
  const opening = useRef(false);
  const start = () => {
    opening.current = true;
    setDraft(source);
  };
  useLayoutEffect(() => {
    const el = area.current;
    if (!el || !opening.current) return;
    opening.current = false;
    el.focus();
    el.setSelectionRange(0, 0);
  }, [editing]);
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

  // Formatting from the bar acts on the field's selection, and leaves the
  // result selected, with the caret back in the field. A menu opened from
  // the bar takes focus; the field keeps its selection meanwhile.
  const pendingSelection = useRef<[number, number] | null>(null);
  const format = (kind: Parameters<typeof applyFormat>[3]) => {
    const el = area.current;
    if (!el || draft === null) return;
    const next = applyFormat(draft, el.selectionStart, el.selectionEnd, kind);
    pendingSelection.current = [next.start, next.end];
    setDraft(next.text);
  };
  // Once the new text is in the field, select what formatting left, in the
  // same commit: nothing typed or clicked in between can be overwritten.
  useLayoutEffect(() => {
    const el = area.current;
    const sel = pendingSelection.current;
    if (!el || !sel) return;
    pendingSelection.current = null;
    el.focus();
    el.setSelectionRange(sel[0], sel[1]);
  }, [draft]);

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
            <span
              className={styles["tools"]}
              role="toolbar"
              aria-label="Formatting"
              onKeyDownCapture={(e) => {
                const moved = moveAlongToolbar(e);
                if (moved !== null) setStop(moved);
              }}
              ref={(bar) => {
                // The stop, or the last button if the bar has since lost some.
                const buttons = bar?.querySelectorAll<HTMLButtonElement>("button") ?? [];
                const at = Math.min(stop, buttons.length - 1);
                buttons.forEach((b, i) => (b.tabIndex = i === at ? 0 : -1));
              }}
            >
              <IconButton size="sm" icon="heading" label="Heading" onClick={() => format("heading")} data-testid="markdown-heading" />
              <IconButton size="sm" icon="bold" label="Bold" onClick={() => format("bold")} data-testid="markdown-bold" />
              <IconButton size="sm" icon="code" label="Code" onClick={() => format("code")} data-testid="markdown-code" />
              {variables && variables.length > 0 ? (
                <RowMenu
                  label="Insert a variable"
                  trigger={<IconButton size="sm" icon="braces" label="Insert a variable" data-testid="markdown-variable" />}
                  items={variables.map((v) => ({ id: v.name, label: `{{${v.name}}}`, mono: true, description: v.description, onSelect: () => format({ insert: `{{${v.name}}}` }) }))}
                  onCloseAutoFocus={(e) => e.preventDefault()}
                />
              ) : null}
            </span>
            <span className={styles["divider"]} aria-hidden />
            <Button size="sm" variant="quiet" onClick={() => setDraft(null)} disabled={saving} data-testid="markdown-cancel">
              Cancel
            </Button>
            <Button size="sm" variant="primary" onClick={() => void save()} loading={saving} disabled={draft === source} data-testid="markdown-save">
              Save
            </Button>
          </>
        ) : (
          <Button size="sm" variant="secondary" leadingIcon="edit" onClick={start} data-testid="markdown-edit">
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
            data-testid="markdown-source"
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
        <div className={styles["view"]} data-testid="markdown-view">{source.trim() ? <Markdown source={source} variant={variant} breaks={breaks} /> : <p className={styles["empty"]}>{emptyText}</p>}</div>
      )}
    </div>
  );
}
