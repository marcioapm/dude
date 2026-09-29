import type { HTMLAttributes, ReactNode } from "react";
import { cx } from "../util/cx.ts";
import styles from "./EntityLine.module.css";

/*
 * Something named, in a row: its face or glyph, then its name in strong ink
 * over one muted line of detail, and anything that belongs at the end. The
 * one shape for a member in a table, a memory in a list, whoever wrote it —
 * so they share a size and an ink wherever they sit. `PersonLine` and
 * `AuthorLine` are it with a face; nothing app-side draws this again.
 */

export interface EntityLineProps extends Omit<HTMLAttributes<HTMLSpanElement>, "children"> {
  /** Before the words: a face, an avatar, a glyph. */
  readonly lead?: ReactNode;
  readonly name: ReactNode;
  /** Under the name, muted: their part, a kind and an age. */
  readonly detail?: ReactNode;
  /** After the words, pushed to the end: badges. */
  readonly trailing?: ReactNode;
  /** A small name for a table cell or a byline (the transcript's size is the default). */
  readonly size?: "sm" | "md" | undefined;
}

export function EntityLine({ lead, name, detail, trailing, size = "md", className, ...rest }: EntityLineProps) {
  return (
    <span className={cx(styles["line"], size === "sm" && styles["sm"], trailing !== undefined && styles["fill"], className)} {...rest}>
      {lead !== undefined ? <span className={styles["lead"]}>{lead}</span> : null}
      <span className={styles["text"]}>
        <span className={styles["name"]}>{name}</span>
        {detail ? <span className={styles["detail"]}>{detail}</span> : null}
      </span>
      {trailing !== undefined ? <span className={styles["trailing"]}>{trailing}</span> : null}
    </span>
  );
}
