import { forwardRef, useCallback, useEffect, useId, useImperativeHandle, useLayoutEffect, useRef, type CSSProperties, type TextareaHTMLAttributes } from "react";
import { cx } from "../util/cx.ts";
import inputStyles from "./Input.module.css";
import styles from "./Textarea.module.css";

export interface TextareaProps extends Omit<TextareaHTMLAttributes<HTMLTextAreaElement>, "rows"> {
  readonly label?: string | undefined;
  readonly hint?: string | undefined;
  readonly error?: string | undefined;
  /** Monospace text — for commands, snippets, config. */
  readonly mono?: boolean | undefined;
  /** Visible rows when empty. Default 3. */
  readonly rows?: number | undefined;
  /** The field grows with its content up to this many rows, then scrolls. Default 12. */
  readonly maxRows?: number | undefined;
}

/** Line height and vertical padding the autosize maths assumes; must match the CSS. */
export const TEXTAREA_LINE_PX = 20;
export const TEXTAREA_PADDING_PX = 12;

/**
 * The height an auto-growing textarea should take for its content: at least
 * `rows` lines, at most `maxRows`, otherwise exactly what the content needs.
 */
export function textareaHeight(scrollHeight: number, rows: number, maxRows: number, linePx = TEXTAREA_LINE_PX, paddingPx = TEXTAREA_PADDING_PX): number {
  const min = Math.max(1, rows) * linePx + paddingPx;
  const max = Math.max(rows, maxRows) * linePx + paddingPx;
  return Math.min(max, Math.max(min, scrollHeight));
}

/** Every scrolled ancestor with its current offset, so a measurement can put them back. */
export function scrollPositions(el: HTMLElement): Array<[Element, number]> {
  const out: Array<[Element, number]> = [];
  for (let n: HTMLElement | null = el.parentElement; n; n = n.parentElement) if (n.scrollTop > 0) out.push([n, n.scrollTop]);
  const doc = el.ownerDocument.scrollingElement;
  if (doc && doc.scrollTop > 0 && !out.some(([n]) => n === doc)) out.push([doc, doc.scrollTop]);
  return out;
}

/**
 * Multi-line text field with the same anatomy as `Input` — label, hint,
 * error, `aria-describedby` — so a form never has two field grammars. Grows
 * with its content from `rows` to `maxRows`, then scrolls; never a resize
 * handle, because the layout around it is dense and fixed.
 */
export const Textarea = forwardRef<HTMLTextAreaElement, TextareaProps>(function Textarea(
  { label, hint, error, mono, rows = 3, maxRows = 12, className, id, disabled, value, defaultValue, onChange, style, ...rest },
  ref,
) {
  const autoId = useId();
  const inputId = id ?? autoId;
  const hintId = `${inputId}-hint`;
  const message = error ?? hint;

  const areaRef = useRef<HTMLTextAreaElement>(null);
  useImperativeHandle(ref, () => areaRef.current as HTMLTextAreaElement);

  const fit = useCallback(() => {
    const el = areaRef.current;
    if (!el) return;
    // Collapsing to measure shortens the page for a frame, and a scrolled
    // ancestor would jump up to fill it; pin every scroll position first.
    const pinned = scrollPositions(el);
    el.style.height = "0px";
    el.style.height = `${textareaHeight(el.scrollHeight, rows, maxRows)}px`;
    for (const [node, top] of pinned) if (node.scrollTop !== top) node.scrollTop = top;
  }, [rows, maxRows]);

  // Controlled: refit when the value changes. Uncontrolled: on every input
  // (below) and once on mount for the default value.
  useLayoutEffect(fit, [fit, value]);
  // A narrower field wraps more lines: refit when the width changes.
  useEffect(() => {
    const el = areaRef.current;
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

  const min = Math.max(1, rows) * TEXTAREA_LINE_PX + TEXTAREA_PADDING_PX;
  const areaStyle: CSSProperties = { minHeight: min, ...style };

  return (
    <div className={cx(inputStyles["field"], className)}>
      {label ? (
        <label className={inputStyles["label"]} htmlFor={inputId}>
          {label}
        </label>
      ) : null}
      <div className={styles["control"]} data-invalid={error ? "true" : undefined} data-disabled={disabled ? "true" : undefined}>
        <textarea
          ref={areaRef}
          id={inputId}
          className={cx(styles["textarea"], mono && inputStyles["mono"])}
          rows={rows}
          aria-invalid={error ? true : undefined}
          aria-describedby={message ? hintId : undefined}
          disabled={disabled}
          style={areaStyle}
          {...(value !== undefined ? { value } : {})}
          {...(defaultValue !== undefined ? { defaultValue } : {})}
          onChange={(e) => {
            onChange?.(e);
            if (value === undefined) fit();
          }}
          {...rest}
        />
      </div>
      {message ? (
        <div id={hintId} className={cx(inputStyles["hint"], error && inputStyles["hintError"])}>
          {message}
        </div>
      ) : null}
    </div>
  );
});
