import { forwardRef, useEffect, useId, useState, type KeyboardEvent } from "react";
import { cx } from "../util/cx.ts";
import { Icon } from "../icons/index.tsx";
import styles from "./NumberInput.module.css";

export interface NumberInputProps {
  /** The value; null while the field holds something that is not a number. */
  readonly value: number | null;
  readonly onValueChange: (value: number | null) => void;
  /** What − and +, and ↑ ↓, move it by. */
  readonly step: number;
  readonly min?: number | undefined;
  readonly max?: number | undefined;
  /** After the number, muted: "CPUs", "GiB". */
  readonly unit?: string | undefined;
  readonly label?: string | undefined;
  /** Under the field: "In steps of 0.5". */
  readonly hint?: string | undefined;
  /** In place of the hint, in the danger tone. */
  readonly error?: string | undefined;
  readonly disabled?: boolean | undefined;
  readonly size?: "sm" | "md" | undefined;
  readonly id?: string | undefined;
  readonly className?: string | undefined;
  readonly "data-testid"?: string | undefined;
}

/** `n` with at most as many decimals as `step` has, so 0.1 + 0.2 reads 0.3. */
function tidy(n: number, step: number): number {
  const decimals = (String(step).split(".")[1] ?? "").length;
  return Number(n.toFixed(decimals));
}

/** A value's text in the field: as typed while editing, the number otherwise. */
const shown = (v: number | null) => (v === null ? "" : String(v));

/**
 * A number moved in steps: − value + with a unit after it. ↑ ↓ are a step
 * (Page Up / Page Down ten), Home / End the bounds; − and + stop at them.
 * What is typed is kept as typed — an off-step value is the caller's to
 * refuse, by `error`, naming the step — so a person sees what they wrote.
 * The buttons are out of the tab order: the keys do what they do.
 */
export const NumberInput = forwardRef<HTMLInputElement, NumberInputProps>(function NumberInput(
  { value, onValueChange, step, min, max, unit, label, hint, error, disabled, size = "md", id, className, "data-testid": testId },
  ref,
) {
  const autoId = useId();
  const inputId = id ?? autoId;
  const hintId = `${inputId}-hint`;
  const [text, setText] = useState(shown(value));
  // Follow a value set from outside (a reset, another size picked), not one this field just set.
  useEffect(() => {
    setText((t) => (Number(t) === value && t.trim() !== "" ? t : shown(value)));
  }, [value]);

  const clamp = (n: number) => Math.min(max ?? Infinity, Math.max(min ?? -Infinity, n));
  const set = (n: number) => {
    const next = tidy(clamp(n), step);
    setText(shown(next));
    onValueChange(next);
  };
  // A step from an off-step value lands on the step's grid, as a browser's number field does.
  const move = (by: number) => {
    const from = value ?? min ?? 0;
    const onGrid = Math.abs(from / step - Math.round(from / step)) < 1e-9;
    set(onGrid ? from + by * step : by > 0 ? Math.ceil(from / step) * step : Math.floor(from / step) * step);
  };
  const onKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    const moves: Record<string, () => void> = {
      ArrowUp: () => move(1),
      ArrowDown: () => move(-1),
      PageUp: () => move(10),
      PageDown: () => move(-10),
      ...(min !== undefined ? { Home: () => set(min) } : {}),
      ...(max !== undefined ? { End: () => set(max) } : {}),
    };
    const act = moves[e.key];
    if (act && !disabled) {
      e.preventDefault();
      act();
    }
  };
  const message = error ?? hint;
  const atMin = value !== null && min !== undefined && value <= min;
  const atMax = value !== null && max !== undefined && value >= max;
  return (
    <div className={cx(styles["field"], className)}>
      {label ? (
        <label className={styles["label"]} htmlFor={inputId}>
          {label}
        </label>
      ) : null}
      <div
        className={cx(styles["control"], size === "sm" && styles["sm"])}
        data-invalid={error ? "true" : undefined}
        data-disabled={disabled ? "true" : undefined}
      >
        <button type="button" tabIndex={-1} className={styles["step"]} aria-label={`${step} less`} disabled={disabled || atMin}
          onClick={() => move(-1)} data-step="down">
          <Icon name="minus" size={12} />
        </button>
        <input
          ref={ref}
          id={inputId}
          className={styles["input"]}
          type="text"
          inputMode="decimal"
          role="spinbutton"
          aria-valuenow={value ?? undefined}
          aria-valuemin={min}
          aria-valuemax={max}
          aria-invalid={error ? true : undefined}
          aria-describedby={message ? hintId : undefined}
          disabled={disabled}
          value={text}
          data-testid={testId}
          onKeyDown={onKeyDown}
          onChange={(e) => {
            const raw = e.target.value;
            setText(raw);
            const n = raw.trim() === "" ? NaN : Number(raw.replace(",", "."));
            onValueChange(Number.isFinite(n) ? n : null);
          }}
        />
        {unit ? <span className={styles["unit"]}>{unit}</span> : null}
        <button type="button" tabIndex={-1} className={styles["step"]} aria-label={`${step} more`} disabled={disabled || atMax}
          onClick={() => move(1)} data-step="up">
          <Icon name="plus" size={12} />
        </button>
      </div>
      {message ? (
        <div id={hintId} className={cx(styles["hint"], error && styles["hintError"])}>
          {message}
        </div>
      ) : null}
    </div>
  );
});
