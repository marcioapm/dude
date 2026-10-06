import { forwardRef, useId, useImperativeHandle, useLayoutEffect, useRef, useState, type ChangeEvent, type TextareaHTMLAttributes } from "react";
import { cx } from "../util/cx.ts";
import { IconButton } from "./Button.tsx";
import inputStyles from "./Input.module.css";
import areaStyles from "./Textarea.module.css";
import { TEXTAREA_LINE_PX, TEXTAREA_PADDING_PX, scrollPositions, textareaHeight } from "./Textarea.tsx";
import styles from "./SecretField.module.css";

export interface SecretFieldProps extends Omit<TextareaHTMLAttributes<HTMLTextAreaElement>, "value" | "defaultValue" | "onChange" | "rows"> {
  readonly label?: string | undefined;
  readonly hint?: string | undefined;
  readonly error?: string | undefined;
  readonly value: string;
  readonly onChange: (value: string, event: ChangeEvent<HTMLTextAreaElement>) => void;
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

/**
 * A value to be written once and not read back: an API key, a PEM block.
 * Multi-line and kept exactly as typed, masked until the eye shows it.
 * Masked, it is one row (its line breaks are drawn as dots too, so its
 * shape says nothing), and the length line beside the hint says how much
 * is there, so a pasted key can be trusted whole. Password managers and
 * spellcheck are told to leave it alone.
 */
export const SecretField = forwardRef<HTMLTextAreaElement, SecretFieldProps>(function SecretField(
  { label, hint, error, value, onChange, defaultRevealed = false, maxRows = 8, id, className, disabled, ...rest },
  ref,
) {
  const autoId = useId();
  const fieldId = id ?? autoId;
  const hintId = `${fieldId}-hint`;
  const [revealed, setRevealed] = useState(defaultRevealed);
  const areaRef = useRef<HTMLTextAreaElement>(null);
  useImperativeHandle(ref, () => areaRef.current as HTMLTextAreaElement);

  useLayoutEffect(() => {
    const el = areaRef.current;
    if (!el) return;
    if (!revealed) {
      el.style.height = "";
      return;
    }
    const pinned = scrollPositions(el);
    el.style.height = "0px";
    el.style.height = `${textareaHeight(el.scrollHeight, 1, maxRows)}px`;
    for (const [node, top] of pinned) if (node.scrollTop !== top) node.scrollTop = top;
  }, [revealed, value, maxRows]);

  const message = error ?? hint;
  const length = secretLength(value);
  return (
    <div className={cx(inputStyles["field"], className)}>
      {label ? (
        <label className={inputStyles["label"]} htmlFor={fieldId}>
          {label}
        </label>
      ) : null}
      <div className={cx(areaStyles["control"], styles["control"])} data-invalid={error ? "true" : undefined} data-disabled={disabled ? "true" : undefined}>
        <textarea
          ref={areaRef}
          id={fieldId}
          className={cx(areaStyles["textarea"], inputStyles["mono"], styles["area"])}
          data-masked={revealed ? undefined : "true"}
          rows={1}
          style={{ minHeight: TEXTAREA_LINE_PX + TEXTAREA_PADDING_PX }}
          value={value}
          onChange={(e) => onChange(e.target.value, e)}
          disabled={disabled}
          autoComplete="off"
          autoCapitalize="off"
          autoCorrect="off"
          spellCheck={false}
          data-1p-ignore=""
          data-lpignore="true"
          data-bwignore=""
          data-form-type="other"
          aria-invalid={error ? true : undefined}
          aria-describedby={message || length ? hintId : undefined}
          {...rest}
        />
        <span className={styles["trail"]}>
          <IconButton
            icon="eye"
            size="sm"
            label={revealed ? "Hide value" : "Show value"}
            aria-pressed={revealed}
            aria-controls={fieldId}
            disabled={disabled}
            onClick={() => setRevealed((r) => !r)}
          />
        </span>
      </div>
      {message || length ? (
        <div id={hintId} className={styles["below"]}>
          <span className={cx(inputStyles["hint"], styles["message"], error && inputStyles["hintError"])}>{message}</span>
          {length ? (
            <span className={cx(inputStyles["hint"], styles["length"])} data-testid="secret-length">
              {length}
            </span>
          ) : null}
        </div>
      ) : null}
    </div>
  );
});
