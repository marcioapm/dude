import {
  forwardRef,
  useId,
  useImperativeHandle,
  useLayoutEffect,
  useRef,
  useState,
  type ChangeEvent,
  type ClipboardEvent,
  type HTMLAttributes,
  type Ref,
} from "react";
import { cx } from "../util/cx.ts";
import { IconButton } from "./Button.tsx";
import inputStyles from "./Input.module.css";
import areaStyles from "./Textarea.module.css";
import { TEXTAREA_LINE_PX, TEXTAREA_PADDING_PX, scrollPositions, textareaHeight } from "./Textarea.tsx";
import styles from "./SecretField.module.css";

type Field = HTMLInputElement | HTMLTextAreaElement;

export interface SecretFieldProps extends Omit<HTMLAttributes<Field>, "onChange" | "defaultValue"> {
  readonly label?: string | undefined;
  readonly hint?: string | undefined;
  readonly error?: string | undefined;
  readonly value: string;
  readonly onChange: (value: string) => void;
  readonly placeholder?: string | undefined;
  readonly disabled?: boolean | undefined;
  readonly name?: string | undefined;
  /** Shown as typed at first; the eye toggles it either way. Default false. */
  readonly defaultRevealed?: boolean | undefined;
  /** Rows the revealed value grows to before it scrolls. Default 8. */
  readonly maxRows?: number | undefined;
}

/** "4 lines · 157 characters", "12 characters", or "" for nothing: what a masked value holds. */
export function secretLength(value: string): string {
  if (value === "") return "";
  const chars = Array.from(value).length;
  const lines = value.replace(/\n+$/, "").split("\n").length;
  const n = `${chars} ${chars === 1 ? "character" : "characters"}`;
  return lines > 1 ? `${lines} lines · ${n}` : n;
}

// A password input cannot hold a line break: the browser drops CR and LF
// from its value. Masked, the input shows the value without them, and its
// offsets are mapped back onto the whole value, which keeps them.
const BREAK = /[\r\n]/g;
const isBreak = (c: string) => c === "\r" || c === "\n";

/** Where the masked input's offset i falls in the whole value. */
function wholeOffset(value: string, i: number): number {
  for (let at = 0, seen = 0; at < value.length; at++) {
    if (isBreak(value[at]!)) continue;
    if (seen === i) return at;
    seen++;
  }
  return value.length;
}

/** The masked input's offset for the whole value's offset at. */
function maskedOffset(value: string, at: number): number {
  return value.slice(0, at).replace(BREAK, "").length;
}

/** The whole value after the masked input changed from what it showed to next. */
function edited(value: string, next: string): string {
  const shown = value.replace(BREAK, "");
  let start = 0;
  while (start < shown.length && start < next.length && shown[start] === next[start]) start++;
  let tail = 0;
  while (tail < shown.length - start && tail < next.length - start && shown[shown.length - 1 - tail] === next[next.length - 1 - tail]) tail++;
  if (start === 0 && tail === 0) return next;
  const removedEnd = shown.length - tail;
  const from = wholeOffset(value, start);
  const to = removedEnd > start ? wholeOffset(value, removedEnd - 1) + 1 : from;
  return value.slice(0, from) + next.slice(start, next.length - tail) + value.slice(to);
}

/**
 * A value to be written once and not read back: an API key, a PEM block.
 * Masked, it is a native password field (so assistive technology and the
 * page's text are not given it); a paste there keeps every line of what
 * was copied, and a typed line break needs the eye. Shown, it is a
 * multi-line textarea of the same value. The length line beside the hint
 * says how much is there either way, lines included, so a pasted key can
 * be trusted whole. Password managers and spellcheck are told to leave it
 * alone.
 */
export const SecretField = forwardRef<Field, SecretFieldProps>(function SecretField(
  { label, hint, error, value, onChange, defaultRevealed = false, maxRows = 8, id, className, disabled, ...rest },
  ref,
) {
  const autoId = useId();
  const fieldId = id ?? autoId;
  const hintId = `${fieldId}-hint`;
  const [revealed, setRevealed] = useState(defaultRevealed);
  const fieldRef = useRef<Field>(null);
  // Focus and a caret (in the whole value's offsets) to put back after a
  // toggle or a paste replaced what the field shows.
  const restore = useRef<{ focus: boolean; caret: number } | null>(null);
  useImperativeHandle(ref, () => fieldRef.current as Field);

  useLayoutEffect(() => {
    const el = fieldRef.current;
    if (!el || !revealed) return;
    const pinned = scrollPositions(el);
    el.style.height = "0px";
    el.style.height = `${textareaHeight(el.scrollHeight, 1, maxRows)}px`;
    for (const [node, top] of pinned) if (node.scrollTop !== top) node.scrollTop = top;
  }, [revealed, value, maxRows]);

  useLayoutEffect(() => {
    const el = fieldRef.current;
    const want = restore.current;
    if (!el || !want) return;
    restore.current = null;
    if (want.focus) el.focus();
    const caret = revealed ? want.caret : maskedOffset(value, want.caret);
    el.setSelectionRange(caret, caret);
  });

  const toggle = () => {
    const el = fieldRef.current;
    if (el) {
      const at = el.selectionEnd ?? 0;
      restore.current = { focus: document.activeElement === el, caret: revealed ? at : wholeOffset(value, at) };
    }
    setRevealed((r) => !r);
  };

  const onPaste = (e: ClipboardEvent<HTMLInputElement>) => {
    const text = e.clipboardData.getData("text/plain");
    if (!/[\r\n]/.test(text)) return; // the input takes it as it is
    e.preventDefault();
    const el = e.currentTarget;
    const from = wholeOffset(value, el.selectionStart ?? 0);
    const end = el.selectionEnd ?? 0;
    const to = end > (el.selectionStart ?? 0) ? wholeOffset(value, end - 1) + 1 : from;
    restore.current = { focus: true, caret: from + text.length };
    onChange(value.slice(0, from) + text + value.slice(to));
  };

  const message = error ?? hint;
  const length = secretLength(value);
  const shared = {
    id: fieldId,
    className: cx(areaStyles["textarea"], inputStyles["mono"], styles["area"]),
    disabled,
    autoComplete: "off",
    autoCapitalize: "off",
    autoCorrect: "off",
    spellCheck: false,
    "data-1p-ignore": "",
    "data-lpignore": "true",
    "data-bwignore": "",
    "data-form-type": "other",
    "aria-invalid": error ? true : undefined,
    "aria-describedby": message || length ? hintId : undefined,
  } as const;
  return (
    <div className={cx(inputStyles["field"], className)}>
      {label ? (
        <label className={inputStyles["label"]} htmlFor={fieldId}>
          {label}
        </label>
      ) : null}
      <div className={cx(areaStyles["control"], styles["control"])} data-invalid={error ? "true" : undefined} data-disabled={disabled ? "true" : undefined}>
        {revealed ? (
          <textarea
            ref={fieldRef as Ref<HTMLTextAreaElement>}
            {...shared}
            {...rest}
            rows={1}
            style={{ minHeight: TEXTAREA_LINE_PX + TEXTAREA_PADDING_PX }}
            value={value}
            onChange={(e: ChangeEvent<HTMLTextAreaElement>) => onChange(e.target.value)}
          />
        ) : (
          <input
            ref={fieldRef as Ref<HTMLInputElement>}
            {...shared}
            {...rest}
            type="password"
            // Without its line breaks: what the browser would make of it, so
            // React never writes it back (which would move the caret).
            value={value.replace(BREAK, "")}
            onChange={(e) => onChange(edited(value, e.target.value))}
            onPaste={onPaste}
            onKeyDown={(e) => {
              rest.onKeyDown?.(e);
              // Enter is a line break in this value, never a form's submit:
              // one cannot be typed masked, so plain Enter does nothing here.
              if (e.key === "Enter" && !e.ctrlKey && !e.metaKey && !e.nativeEvent.isComposing) e.preventDefault();
            }}
          />
        )}
        <span className={styles["trail"]}>
          <IconButton
            icon="eye"
            size="sm"
            label={revealed ? "Hide value" : "Show value"}
            aria-pressed={revealed}
            aria-controls={fieldId}
            disabled={disabled}
            // A click leaves the field focused, its caret where it was.
            onMouseDown={(e) => e.preventDefault()}
            onClick={toggle}
          />
        </span>
      </div>
      {message || length ? (
        <div id={hintId} className={styles["below"]}>
          <span className={cx(inputStyles["hint"], styles["message"], error && inputStyles["hintError"])}>{message}</span>
          {length ? <span className={cx(inputStyles["hint"], styles["length"])}>{length}</span> : null}
        </div>
      ) : null}
    </div>
  );
});
