import type { HTMLAttributes, ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { Icon, type IconName } from "../icons/index.tsx";
import styles from "./StartChoice.module.css";

export interface StartOption {
  readonly id: string;
  readonly icon: IconName;
  readonly title: string;
  /** What choosing it does, in a sentence or two. */
  readonly description: ReactNode;
  /** What to expect, a few short points. */
  readonly points?: readonly ReactNode[] | undefined;
  /** Small print at the foot: who does the work. */
  readonly foot?: ReactNode;
  /** The button that starts it. */
  readonly action: ReactNode;
}

export interface StartChoiceProps extends Omit<HTMLAttributes<HTMLDivElement>, "children"> {
  /** The ways to start, side by side and equal: no default among them. */
  readonly options: readonly StartOption[];
}

/**
 * How to start a task, chosen every time: the ways side by side, each a
 * card of equal weight — a face, a name, what it does, what to expect, and
 * its own button. None is primary and none is preselected: the person
 * picks. Narrow, the cards stack.
 */
export function StartChoice({ options, className, ...rest }: StartChoiceProps) {
  return (
    <div className={cx(styles["root"], className)} role="group" aria-label="How to start" {...rest}>
      {options.map((o) => (
        <section key={o.id} className={styles["option"]} aria-label={o.title} data-option={o.id}>
          <h4 className={styles["title"]}>
            <Icon name={o.icon} size={16} className={styles["icon"]} />
            {o.title}
          </h4>
          <p className={styles["description"]}>{o.description}</p>
          {o.points && o.points.length > 0 ? (
            <ul className={styles["points"]}>
              {o.points.map((p, i) => <li key={i}>{p}</li>)}
            </ul>
          ) : null}
          <div className={styles["foot"]}>
            <span className={styles["small"]}>{o.foot}</span>
            {o.action}
          </div>
        </section>
      ))}
    </div>
  );
}
