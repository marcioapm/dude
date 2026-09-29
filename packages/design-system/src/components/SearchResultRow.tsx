import { useId, type HTMLAttributes, type ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { Icon } from "../icons/index.tsx";
import { RefLead, type RefLeadSpec } from "./RefLead.tsx";
import { useDisclosure } from "../util/useDisclosure.ts";
import styles from "./SearchResultRow.module.css";

/*
 * One search result: ArtifactRow's anatomy, for what a search found. A
 * 32px row — chevron, rank, what it is (a `RefLead`: a task's status mark
 * and key, an epic's layers, a project's face, the memory glyph), the
 * title — and quiet facts at the end: which search found it, where it
 * lives. Why it ranked where it did, and what it says, are behind the
 * click. Nothing is coloured but what the lead carries; a score is never a
 * chip or a bar.
 */

export interface SearchResultRowProps extends Omit<HTMLAttributes<HTMLLIElement>, "title"> {
  readonly rank: number;
  /** What it is, before the title, in the sidebar's grammar (`RefLead`). */
  readonly lead: RefLeadSpec;
  readonly title: ReactNode;
  /** Muted facts at the end: "words and meaning", the project. */
  readonly facts?: ReadonlyArray<ReactNode>;
  /** Beside the facts: a badge ("Text only"). */
  readonly badge?: ReactNode;
  /** Under the row when open: the snippet, the scores, the actions. */
  readonly children?: ReactNode;
  readonly expanded?: boolean | undefined;
  readonly defaultExpanded?: boolean | undefined;
  readonly onExpandedChange?: ((open: boolean) => void) | undefined;
}

export function SearchResultRow({
  rank,
  lead,
  title,
  facts,
  badge,
  children,
  expanded,
  defaultExpanded,
  onExpandedChange,
  className,
  ...rest
}: SearchResultRowProps) {
  const bodyId = useId();
  const disc = useDisclosure({ expanded, defaultExpanded, onExpandedChange });
  const expandable = children !== undefined && children !== null;
  const open = expandable && disc.open;
  const head = (
    <>
      <span className={styles["chevron"]} aria-hidden>
        {expandable ? <Icon name="chevron-right" size={12} className={styles["chevronIcon"]} /> : null}
      </span>
      <span className={styles["rank"]}>{rank}</span>
      <RefLead {...lead} />
      <span className={styles["title"]}>{title}</span>
    </>
  );
  return (
    <li className={cx(styles["root"], open && styles["open"], className)} {...rest}>
      <div className={styles["row"]}>
        {expandable ? (
          <button type="button" className={cx(styles["head"], styles["headButton"])} aria-expanded={open} aria-controls={open ? bodyId : undefined} onClick={disc.toggle}>
            {head}
          </button>
        ) : (
          <span className={styles["head"]}>{head}</span>
        )}
        <span className={styles["trailing"]}>
          {badge}
          {facts?.map((f, i) => (
            <span key={i} className={styles["fact"]}>
              {f}
            </span>
          ))}
        </span>
      </div>
      {open ? (
        <div id={bodyId} className={styles["body"]}>
          {children}
        </div>
      ) : null}
    </li>
  );
}

/** The ranked list: an ordered list, rows 2px apart as artifacts are. */
export function SearchResultList({ className, children, ...rest }: HTMLAttributes<HTMLOListElement>) {
  return (
    <ol className={cx(styles["list"], className)} {...rest}>
      {children}
    </ol>
  );
}
