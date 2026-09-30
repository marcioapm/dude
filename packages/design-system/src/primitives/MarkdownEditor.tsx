import { useCallback, useEffect, useId, useLayoutEffect, useRef, useState, type CSSProperties, type KeyboardEvent, type ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { continueList, countState, editorKey, FORMAT_KEYS, formatEdit, type MarkdownFormat, type TextEdit } from "../util/markdownEdit.ts";
import { Icon, type IconName } from "../icons/index.tsx";
import { Markdown } from "../components/Markdown.tsx";
import { Segmented } from "../components/ScreenHeader.tsx";
import { IconButton } from "./Button.tsx";
import { Tooltip } from "./Tooltip.tsx";
import { scrollPositions } from "./Textarea.tsx";
import inputStyles from "./Input.module.css";
import styles from "./MarkdownEditor.module.css";

export type MarkdownEditorMode = "write" | "preview";

export interface MarkdownEditorProps {
  readonly value: string;
  readonly onChange: (value: string) => void;
  readonly label?: string | undefined;
  /** Accessible name when there is no visible label. */
  readonly "aria-label"?: string | undefined;
  /** Beside the label: what to write here. When locked, why it cannot be changed. */
  readonly hint?: string | undefined;
  readonly error?: string | undefined;
  readonly placeholder?: string | undefined;
  /** Source lines shown when empty; the field grows from here with its content. Default 6. */
  readonly minRows?: number | undefined;
  /** Hard limit on the source, and the count's denominator. */
  readonly maxLength?: number | undefined;
  /** Nothing can be changed: opens in Preview with Write disabled. */
  readonly disabled?: boolean | undefined;
  /** Written and fixed (a delivery started on it): as `disabled`, said by the caller's `hint`. */
  readonly locked?: boolean | undefined;
  /** In the footer after "Markdown": what the text amounts to ("4 criteria"). */
  readonly summary?: ReactNode;
  /** In the footer, attention ink: something about the text worth knowing before saving. */
  readonly notice?: string | undefined;
  readonly defaultMode?: MarkdownEditorMode | undefined;
  /**
   * The `Markdown` variant Preview renders in: the one the text will be read
   * in. `message` (default) for a field shown on a screen, `document` for
   * text published as an artifact.
   */
  readonly variant?: "message" | "document" | undefined;
  readonly id?: string | undefined;
  readonly name?: string | undefined;
  readonly autoFocus?: boolean | undefined;
  readonly className?: string | undefined;
  /** On the textarea, as `Textarea` passes it. */
  readonly "data-testid"?: string | undefined;
}

const FORMATS: ReadonlyArray<{ format: MarkdownFormat; icon: IconName; label: string; optional?: true } | "gap"> = [
  { format: "heading", icon: "heading", label: "Heading" },
  { format: "bold", icon: "bold", label: "Bold" },
  { format: "italic", icon: "italic", label: "Italic" },
  "gap",
  { format: "code", icon: "code", label: "Code" },
  { format: "link", icon: "link", label: "Link" },
  { format: "quote", icon: "quote", label: "Quote", optional: true },
  "gap",
  { format: "bullet", icon: "list-bullet", label: "Bulleted list" },
  { format: "checklist", icon: "list-check", label: "Checklist" },
];

/**
 * Put an edit into the field through the browser, so Undo takes it back:
 * `insertText` (or `delete`) where `execCommand` still works, `setRangeText`
 * and a dispatched `input` event where it does not. Either way React sees an
 * ordinary change.
 */
function applyEdit(el: HTMLTextAreaElement, edit: TextEdit) {
  el.focus();
  el.setSelectionRange(edit.from, edit.to);
  let done = false;
  try {
    done = edit.insert === "" ? edit.from === edit.to || document.execCommand("delete") : document.execCommand("insertText", false, edit.insert);
  } catch {
    done = false;
  }
  if (!done || el.value.slice(edit.from, edit.from + edit.insert.length) !== edit.insert) {
    el.setRangeText(edit.insert, edit.from, edit.to, "end");
    el.dispatchEvent(new Event("input", { bubbles: true }));
  }
  el.setSelectionRange(edit.selectionStart, edit.selectionEnd);
}

/**
 * A field for writing one Markdown document: `Textarea`'s anatomy (label,
 * hint, error, `aria-describedby`) around a frame with a Write / Preview
 * switch and quiet formatting on the chrome shade, the source in mono, and
 * a footer that counts. The source grows with its content — the page or
 * dialog scrolls, never the field — and Preview is the one safe renderer,
 * `Markdown` in the caller's `variant`, at least as tall as the source was.
 */
export function MarkdownEditor({
  value,
  onChange,
  label,
  "aria-label": ariaLabel,
  hint,
  error,
  placeholder,
  minRows = 6,
  maxLength,
  disabled,
  locked,
  summary,
  notice,
  defaultMode = "write",
  variant = "message",
  id,
  name,
  autoFocus,
  className,
  "data-testid": testId,
}: MarkdownEditorProps) {
  const autoId = useId();
  const fieldId = id ?? autoId;
  const tabs = `${fieldId.replace(/[^a-zA-Z0-9_-]/g, "")}-view`;
  const hintId = `${fieldId}-hint`;
  const errorId = `${fieldId}-error`;
  const footId = `${fieldId}-foot`;
  const fixed = Boolean(locked || disabled);
  const [chosen, setChosen] = useState<MarkdownEditorMode>(defaultMode);
  const mode: MarkdownEditorMode = fixed ? "preview" : chosen;
  const [previewMin, setPreviewMin] = useState<number | undefined>(undefined);
  const area = useRef<HTMLTextAreaElement>(null);
  const previewTab = useRef<HTMLDivElement>(null);
  const focusAfter = useRef<MarkdownEditorMode | null>(null);

  const switchTo = (next: MarkdownEditorMode) => {
    if (next === mode) return;
    // Preview keeps the source's height, so the dialog does not jump.
    if (next === "preview" && area.current?.offsetHeight) setPreviewMin(area.current.offsetHeight);
    setChosen(next);
  };

  const fit = useCallback(() => {
    const el = area.current;
    if (!el || el.offsetParent === null) return;
    const pinned = scrollPositions(el);
    el.style.height = "0px";
    el.style.height = `${el.scrollHeight}px`;
    for (const [node, top] of pinned) if (node.scrollTop !== top) node.scrollTop = top;
  }, []);
  useLayoutEffect(fit, [fit, value, mode]);
  useEffect(() => {
    const el = area.current;
    if (!el || typeof ResizeObserver === "undefined") return;
    let width = el.clientWidth;
    const ro = new ResizeObserver(() => {
      if (el.clientWidth === width) return;
      width = el.clientWidth;
      fit();
    });
    ro.observe(el);
    return () => ro.disconnect();
  }, [fit]);
  useEffect(() => {
    const target = focusAfter.current;
    focusAfter.current = null;
    if (target === "write") area.current?.focus();
    else if (target === "preview") previewTab.current?.parentElement?.querySelector<HTMLButtonElement>('[role="tab"][aria-selected="true"]')?.focus();
  }, [mode]);

  const format = (f: MarkdownFormat) => {
    const el = area.current;
    if (!el || fixed) return;
    applyEdit(el, formatEdit(el.value, el.selectionStart, el.selectionEnd, f));
  };

  const onFrameKey = (e: KeyboardEvent<HTMLDivElement>) => {
    const k = editorKey(e);
    if (k?.kind !== "toggle") return;
    e.preventDefault();
    if (fixed) return;
    const next = mode === "write" ? "preview" : "write";
    focusAfter.current = next;
    switchTo(next);
  };

  const onAreaKey = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.nativeEvent.isComposing) return;
    const k = editorKey(e);
    const el = e.currentTarget;
    if (k?.kind === "format") {
      e.preventDefault();
      format(k.format);
    } else if (k?.kind === "newline" && el.selectionStart === el.selectionEnd) {
      const edit = continueList(el.value, el.selectionStart);
      if (edit) {
        e.preventDefault();
        applyEdit(el, edit);
      }
    }
  };

  // The toolbar is one Tab stop; ← → Home End move along the buttons shown.
  const [stop, setStop] = useState<MarkdownFormat>("heading");
  const toolbar = useRef<HTMLDivElement>(null);
  const focusedFormat = useRef<MarkdownFormat | null>(null);
  // Quote hides under a container query. When it holds the Tab stop, the
  // stop (and focus, if Quote had it) moves to the button before it, or the
  // toolbar would have no Tab stop left.
  useEffect(() => {
    const bar = toolbar.current;
    if (!bar || typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(() => {
      const quote = bar.querySelector<HTMLButtonElement>('[data-format="quote"]');
      if (!quote || quote.offsetParent !== null) return;
      setStop((s) => (s === "quote" ? "link" : s));
      const active = document.activeElement;
      if (focusedFormat.current === "quote" && (active === quote || active === null || active === document.body)) {
        bar.querySelector<HTMLButtonElement>('[data-format="link"]')?.focus();
      }
    });
    ro.observe(bar);
    return () => ro.disconnect();
  }, []);
  const moveAlongToolbar = (e: KeyboardEvent<HTMLDivElement>) => {
    if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(e.key)) return;
    const buttons = [...e.currentTarget.querySelectorAll<HTMLButtonElement>("button")].filter((b) => b.offsetParent !== null);
    const at = buttons.indexOf(document.activeElement as HTMLButtonElement);
    if (at === -1) return;
    e.preventDefault();
    const next = e.key === "Home" ? 0 : e.key === "End" ? buttons.length - 1 : (at + (e.key === "ArrowRight" ? 1 : -1) + buttons.length) % buttons.length;
    buttons[next]?.focus();
  };

  const count = countState(value.length, maxLength);
  const described = [hint ? hintId : null, error ? errorId : null, summary || notice ? footId : null].filter(Boolean).join(" ") || undefined;
  const frameStyle = { "--mde-rows": String(Math.max(1, minRows)) } as CSSProperties;

  return (
    <div className={cx(inputStyles["field"], className)}>
      {label || hint ? (
        <div className={styles["head"]}>
          {label ? (
            <label className={inputStyles["label"]} htmlFor={fieldId}>
              {label}
            </label>
          ) : null}
          {hint ? (
            <span id={hintId} className={inputStyles["hint"]}>
              {hint}
            </span>
          ) : null}
        </div>
      ) : null}
      <div
        className={styles["frame"]}
        style={frameStyle}
        data-mode={mode}
        data-invalid={error ? "true" : undefined}
        data-locked={fixed ? "true" : undefined}
        onKeyDown={onFrameKey}
      >
        <div className={styles["bar"]}>
          <Segmented
            label={`${label ?? ariaLabel ?? "Markdown"} view`}
            size="toolbar"
            tabs={tabs}
            value={mode}
            onChange={switchTo}
            options={[
              { value: "write", label: <><Icon name="edit" size={14} />Write</>, disabled: fixed },
              { value: "preview", label: <><Icon name="eye" size={14} />Preview</> },
            ]}
          />
          <div
            className={styles["tools"]}
            role="toolbar"
            aria-label="Formatting"
            aria-controls={fieldId}
            ref={toolbar}
            aria-hidden={mode === "preview" ? true : undefined}
            {...(mode === "preview" ? { inert: true } : {})}
            onKeyDown={moveAlongToolbar}
          >
            {FORMATS.map((f, i) =>
              f === "gap" ? (
                <span key={i} className={styles["gap"]} aria-hidden />
              ) : (
                <Tooltip key={f.format} content={f.label} shortcut={FORMAT_KEYS[f.format] ? ["mod", FORMAT_KEYS[f.format]!.toUpperCase()] : undefined}>
                  <IconButton
                    size="sm"
                    icon={f.icon}
                    label={f.label}
                    title={undefined}
                    className={f.optional ? styles["optional"] : undefined}
                    tabIndex={f.format === stop ? 0 : -1}
                    onFocus={() => {
                      focusedFormat.current = f.format;
                      setStop(f.format);
                    }}
                    onBlur={(e) => {
                      // A button blurred because it stopped rendering still counts as holding focus.
                      if (e.currentTarget.offsetParent !== null) focusedFormat.current = null;
                    }}
                    // The field keeps focus and its selection while a button is pressed.
                    onMouseDown={(e) => e.preventDefault()}
                    onClick={() => format(f.format)}
                    data-format={f.format}
                  />
                </Tooltip>
              ),
            )}
          </div>
        </div>
        <div role="tabpanel" id={`${tabs}-write-panel`} aria-labelledby={`${tabs}-write-tab`} hidden={mode !== "write"}>
          <textarea
            ref={area}
            id={fieldId}
            name={name}
            className={styles["source"]}
            value={value}
            placeholder={placeholder}
            maxLength={maxLength}
            disabled={fixed}
            spellCheck
            autoFocus={autoFocus}
            aria-label={label ? undefined : ariaLabel}
            aria-invalid={error ? true : undefined}
            aria-describedby={described}
            onChange={(e) => onChange(e.target.value)}
            onKeyDown={onAreaKey}
            data-testid={testId}
          />
        </div>
        <div
          ref={previewTab}
          role="tabpanel"
          id={`${tabs}-preview-panel`}
          aria-labelledby={`${tabs}-preview-tab`}
          tabIndex={0}
          className={styles["preview"]}
          hidden={mode !== "preview"}
          style={previewMin !== undefined ? { minHeight: previewMin } : undefined}
          data-testid={testId ? `${testId}-preview` : undefined}
        >
          {mode !== "preview" ? null : value.trim() ? (
            <Markdown source={value} variant={variant} unmeasured />
          ) : (
            <p className={styles["empty"]}>Nothing to preview yet.</p>
          )}
        </div>
        <div className={styles["foot"]}>
          <span className={styles["md"]}>
            <Icon name="markdown" size={12} />
            Markdown
          </span>
          {summary || notice ? (
            <span id={footId} className={styles["summary"]}>
              {summary}
              {notice ? <span className={styles["notice"]}>{notice}</span> : null}
            </span>
          ) : null}
          <span className={styles["grow"]} />
          {maxLength !== undefined ? (
            <span className={cx(styles["count"], "ds-tnum")} data-state={count}>
              {value.length.toLocaleString("en-US")} / {maxLength.toLocaleString("en-US")}
            </span>
          ) : null}
        </div>
      </div>
      {error ? (
        <div id={errorId} className={cx(inputStyles["hint"], inputStyles["hintError"])}>
          {error}
        </div>
      ) : null}
    </div>
  );
}
