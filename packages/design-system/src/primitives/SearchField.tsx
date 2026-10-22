import { forwardRef, type InputHTMLAttributes } from "react";
import { cx } from "../util/cx.ts";
import { Icon } from "../icons/index.tsx";
import styles from "./SearchField.module.css";

export interface SearchFieldProps extends Omit<InputHTMLAttributes<HTMLInputElement>, "value" | "onChange" | "type" | "size"> {
  readonly value: string;
  readonly onValueChange: (value: string) => void;
  /** The field's accessible name; the placeholder is never its name. */
  readonly label: string;
  /** Shown while empty: the key that reaches the field ("/"). */
  readonly shortcut?: string | undefined;
  /** On the box, not the input. */
  readonly className?: string | undefined;
}

/**
 * A search box in the row grammar of the list it searches: a magnifier,
 * the words, and a ✕ once there are some (or the shortcut before). Typing
 * is the caller's: it decides when to search and what Escape and the
 * arrows do (`onKeyDown`).
 */
export const SearchField = forwardRef<HTMLInputElement, SearchFieldProps>(function SearchField(
  { value, onValueChange, label, shortcut, className, ...rest }, ref,
) {
  return (
    <div className={cx(styles["root"], className)}>
      <Icon name="search" size={14} className={styles["icon"]} />
      <input ref={ref} type="search" className={styles["input"]} value={value} onChange={(e) => onValueChange(e.target.value)}
        aria-label={label} autoComplete="off" spellCheck={false} {...rest} />
      {value ? (
        <button type="button" className={styles["clear"]} aria-label="Clear search" onClick={() => onValueChange("")}>
          <Icon name="close" size={10} />
        </button>
      ) : shortcut ? (
        <kbd className={styles["kbd"]} aria-hidden>{shortcut}</kbd>
      ) : null}
    </div>
  );
});
