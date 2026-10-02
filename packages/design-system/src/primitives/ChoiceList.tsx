import { useRef, type HTMLAttributes, type KeyboardEvent, type ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { Icon, type IconName } from "../icons/index.tsx";
import styles from "./ChoiceList.module.css";

export interface ChoiceOption<T extends string> {
  readonly value: T;
  /** What it does, in a few words: "Resume the implementer". */
  readonly label: ReactNode;
  /** What it means, in a sentence, under the label. */
  readonly description?: ReactNode;
  readonly icon?: IconName | undefined;
  /**
   * Why it cannot be chosen now, said in place of its description: an
   * option is never greyed out without its reason.
   */
  readonly disabledReason?: ReactNode;
}

export interface ChoiceListProps<T extends string> extends Omit<HTMLAttributes<HTMLDivElement>, "onChange" | "role"> {
  readonly options: ReadonlyArray<ChoiceOption<T>>;
  readonly value: T;
  readonly onChange: (value: T) => void;
  /** Names the group for a screen reader: "How to pick it back up". */
  readonly label: string;
}

/**
 * One of a few ways to do something, each with a sentence on what it
 * means — a decision, usually in a dialog, where the options differ by more
 * than a word. A radio group: one tab stop, ↑↓ (and ←→) move and choose,
 * Home End to the ends; options that cannot be chosen are skipped and say
 * why. For two or three views of one thing use `Segmented`; for a value
 * from a list, `Select`.
 */
export function ChoiceList<T extends string>({ options, value, onChange, label, className, ...rest }: ChoiceListProps<T>) {
  const refs = useRef(new Map<T, HTMLButtonElement>());
  const enabled = options.filter((o) => !o.disabledReason);
  function move(e: KeyboardEvent<HTMLButtonElement>, from: T): void {
    const step = e.key === "ArrowDown" || e.key === "ArrowRight" ? 1 : e.key === "ArrowUp" || e.key === "ArrowLeft" ? -1 : 0;
    if (step === 0 && e.key !== "Home" && e.key !== "End") return;
    e.preventDefault();
    if (enabled.length === 0) return;
    const at = enabled.findIndex((o) => o.value === from);
    const next = e.key === "Home" ? enabled[0]! : e.key === "End" ? enabled.at(-1)! : enabled[(at + step + enabled.length) % enabled.length]!;
    onChange(next.value);
    refs.current.get(next.value)?.focus();
  }
  // The chosen option is the tab stop; with none chosen that can be, the first that can.
  const stop = enabled.some((o) => o.value === value) ? value : enabled[0]?.value;
  return (
    <div role="radiogroup" aria-label={label} className={cx(styles["list"], className)} {...rest}>
      {options.map((o) => {
        const chosen = o.value === value;
        const disabled = Boolean(o.disabledReason);
        return (
          <button
            key={o.value}
            ref={(el) => {
              if (el) refs.current.set(o.value, el);
              else refs.current.delete(o.value);
            }}
            type="button"
            role="radio"
            aria-checked={chosen}
            aria-disabled={disabled || undefined}
            tabIndex={o.value === stop ? 0 : -1}
            className={cx(styles["option"], chosen && styles["chosen"])}
            data-value={o.value}
            onClick={() => !disabled && onChange(o.value)}
            onKeyDown={(e) => move(e, o.value)}
          >
            <span className={styles["dot"]} aria-hidden />
            {o.icon ? <Icon name={o.icon} size={16} className={styles["icon"]} /> : null}
            <span className={styles["text"]}>
              <span className={styles["label"]}>{o.label}</span>
              {o.disabledReason ?? o.description ? (
                <span className={styles["description"]}>{o.disabledReason ?? o.description}</span>
              ) : null}
            </span>
          </button>
        );
      })}
    </div>
  );
}
