import { forwardRef, useId, type InputHTMLAttributes, type ReactNode } from "react";
import { cx } from "../util/cx.ts";
import styles from "./Input.module.css";

export interface InputProps extends Omit<InputHTMLAttributes<HTMLInputElement>, "size"> {
  readonly label?: string | undefined;
  readonly hint?: string | undefined;
  readonly error?: string | undefined;
  readonly size?: "sm" | "md" | undefined;
  /** Monospace text — for IDs, SHAs, paths, commands. */
  readonly mono?: boolean | undefined;
  readonly leading?: ReactNode;
  readonly trailing?: ReactNode;
}

/**
 * Text input. Renders its own label/hint/error so every field in the product
 * has the same anatomy. Error text is tied to the input via aria-describedby.
 */
export const Input = forwardRef<HTMLInputElement, InputProps>(function Input(
  { label, hint, error, size = "md", mono, leading, trailing, className, id, disabled, ...rest },
  ref,
) {
  const autoId = useId();
  const inputId = id ?? autoId;
  const hintId = `${inputId}-hint`;
  const message = error ?? hint;
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
        {leading ? <span className={styles["adornment"]}>{leading}</span> : null}
        <input
          ref={ref}
          id={inputId}
          className={cx(styles["input"], mono && styles["mono"])}
          aria-invalid={error ? true : undefined}
          aria-describedby={message ? hintId : undefined}
          disabled={disabled}
          {...rest}
        />
        {trailing ? <span className={styles["adornment"]}>{trailing}</span> : null}
      </div>
      {message ? (
        <div id={hintId} className={cx(styles["hint"], error && styles["hintError"])}>
          {message}
        </div>
      ) : null}
    </div>
  );
});
