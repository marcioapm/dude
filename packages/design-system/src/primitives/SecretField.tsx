import {
  forwardRef,
  useEffect,
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
import { applyMaskedEdit, flatten, inferMaskedEdit, maskedOffset, wholeOffset, type MaskedEdit } from "./maskedEdit.ts";
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
// from its value. Masked, the input shows the value without them, and each
// edit is applied to the whole value where it was made (maskedEdit.ts).

/** The whole value a composition began on, the selection it replaces, and whether compositionend came. */
interface Snapshot {
  readonly value: string;
  readonly start: number;
  readonly end: number;
  readonly ended: boolean;
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
 *
 * Masked, line breaks are invisible, and an edit keeps them by these rules:
 * typing where a break is hidden starts the next line; deleting the
 * character just before or after a break deletes that character, not the
 * break; a break goes only when the characters on both sides of it are in
 * the deleted range, or when everything is deleted. An edit whose place
 * cannot be known (no beforeinput from the browser, and the field's change
 * fits more than one place) shows the value instead of guessing.
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
  // Masked editing. The whole value the field last showed (ahead of a
  // render that has not happened yet); the edit beforeinput announced; the
  // selection a composition started from; the selection last seen.
  const whole = useRef(value);
  whole.current = value;
  const announced = useRef<(MaskedEdit & { readonly shown: string }) | null>(null);
  const composing = useRef<Snapshot | null>(null);
  const lastSelection = useRef<{ start: number; end: number } | null>(null);

  useEffect(() => {
    const el = fieldRef.current;
    if (!el || revealed) return;
    const selection = () => ({ start: el.selectionStart ?? 0, end: el.selectionEnd ?? 0 });
    const onBeforeInput = (e: Event) => {
      const inputType = (e as InputEvent).inputType ?? "";
      if (inputType.includes("Composition") || (composing.current && !composing.current.ended)) return;
      composing.current = null;
      announced.current = { inputType, ...selection(), shown: flatten(whole.current) };
    };
    const onCompositionStart = () => {
      composing.current = { value: whole.current, ...selection(), ended: false };
    };
    // Browsers differ on whether the committing input comes before or after
    // compositionend: the snapshot stays until a later edit replaces it.
    const onCompositionEnd = () => {
      if (composing.current) composing.current = { ...composing.current, ended: true };
    };
    const onSelection = () => {
      if (document.activeElement === el) lastSelection.current = selection();
    };
    const events: [EventTarget, string, (e: Event) => void][] = [
      [el, "beforeinput", onBeforeInput],
      [el, "compositionstart", onCompositionStart],
      [el, "compositionend", onCompositionEnd],
      [el, "keydown", onSelection],
      [el, "select", onSelection],
      [el, "selectionchange", onSelection],
      [el, "focus", onSelection],
      [el, "pointerup", onSelection],
      [document, "selectionchange", onSelection],
    ];
    for (const [target, type, fn] of events) target.addEventListener(type, fn);
    return () => {
      for (const [target, type, fn] of events) target.removeEventListener(type, fn);
    };
  }, [revealed]);

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
    emit(value.slice(0, from) + text + value.slice(to));
  };

  const emit = (next: string) => {
    whole.current = next;
    onChange(next);
  };

  /** The masked field now shows next: apply the edit that made it to the whole value. */
  const onMaskedChange = (e: ChangeEvent<HTMLInputElement>) => {
    const el = e.target;
    const next = el.value;
    const before = whole.current;
    const shown = flatten(before);
    const edit = announced.current;
    announced.current = null;
    let after: string | null = null;
    const comp = composing.current;
    const native = e.nativeEvent as InputEvent;
    const inputType = native.inputType ?? "";
    const byComposition = comp !== null && (inputType.includes("Composition") || native.isComposing || (inputType === "" && !comp.ended));
    if (comp && byComposition) {
      // Every update of a composition replaces the same range of what the
      // field showed when it began.
      after = applyMaskedEdit(comp.value, next, { inputType: "insertCompositionText", start: comp.start, end: comp.end });
      if (comp.ended) composing.current = null;
    } else if (edit && edit.shown === shown) {
      composing.current = null;
      after = applyMaskedEdit(before, next, edit);
    } else {
      composing.current = null;
      after = inferMaskedEdit(before, next, lastSelection.current, el.selectionEnd);
    }
    lastSelection.current = { start: el.selectionStart ?? 0, end: el.selectionEnd ?? 0 };
    if (after === null) {
      // Where this edit was made cannot be known: show the value as it was,
      // so the person makes the edit where its line breaks can be seen.
      composing.current = null;
      restore.current = { focus: document.activeElement === el, caret: wholeOffset(before, Math.min(el.selectionEnd ?? 0, shown.length)) };
      setRevealed(true);
      return;
    }
    emit(after);
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
            value={flatten(value)}
            onChange={onMaskedChange}
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
