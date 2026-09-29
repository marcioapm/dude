import type { HTMLAttributes, ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { IconButton } from "../primitives/Button.tsx";
import styles from "./RemovableList.module.css";

/*
 * Things attached to something, each removable: what a memory is about, and
 * the next list like it. Compact rows told apart by space, a wash on hover,
 * a quiet remove button at the end. Without `onRemove` it is a plain list.
 */

export interface RemovableItem {
  readonly id: string;
  readonly content: ReactNode;
  /** Its name for the remove button: "Remove TEXT-12". */
  readonly label: string;
}

export interface RemovableListProps extends Omit<HTMLAttributes<HTMLUListElement>, "children"> {
  readonly items: ReadonlyArray<RemovableItem>;
  readonly onRemove?: ((id: string) => void) | undefined;
}

export function RemovableList({ items, onRemove, className, ...rest }: RemovableListProps) {
  return (
    <ul className={cx(styles["list"], className)} {...rest}>
      {items.map((item) => (
        <li key={item.id} className={styles["item"]}>
          <span className={styles["content"]}>{item.content}</span>
          {onRemove ? <IconButton size="sm" icon="close" label={`Remove ${item.label}`} onClick={() => onRemove(item.id)} /> : null}
        </li>
      ))}
    </ul>
  );
}
