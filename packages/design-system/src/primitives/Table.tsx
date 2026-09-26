import type { HTMLAttributes, ReactNode, TdHTMLAttributes, ThHTMLAttributes } from "react";
import { cx } from "../util/cx.ts";
import { Icon } from "../icons/index.tsx";
import styles from "./Table.module.css";

/**
 * Dense data table. Composable: Table > THead/TBody > Tr > Th/Td.
 *
 * Sorting is *sortable-ready*: `Th` accepts `sort` state and an `onSort`
 * callback and renders the affordance + aria-sort; it does not sort data.
 * Virtualisation is the consumer's job (the markup is plain <table>, so
 * any row-virtualiser works).
 *
 * Numbers: right-align and set `mono` on Td. The table sets tabular-nums.
 *
 * Cells truncate with an ellipsis. A cell that must be read whole takes
 * `wrap` (more lines) or `fit` (one line, as wide as it needs).
 */

export type TableDensity = "compact" | "default" | "comfortable";
export type SortDirection = "asc" | "desc";

export interface TableProps extends HTMLAttributes<HTMLTableElement> {
  readonly density?: TableDensity | undefined;
  readonly striped?: boolean | undefined;
  readonly wrapClassName?: string | undefined;
  /** Max height for the scroll container; header stays sticky. */
  readonly maxHeight?: number | string | undefined;
}

export function Table({ density = "default", striped, wrapClassName, maxHeight, className, children, ...rest }: TableProps) {
  return (
    <div className={cx(styles["wrap"], wrapClassName)} style={maxHeight !== undefined ? { maxHeight } : undefined}>
      <table
        className={cx(styles["table"], density !== "default" && styles[density], className)}
        data-striped={striped ? "true" : undefined}
        {...rest}
      >
        {children}
      </table>
    </div>
  );
}

export function THead({ children, ...rest }: HTMLAttributes<HTMLTableSectionElement>) {
  return <thead {...rest}>{children}</thead>;
}

export function TBody({ children, ...rest }: HTMLAttributes<HTMLTableSectionElement>) {
  return <tbody {...rest}>{children}</tbody>;
}

export interface TrProps extends HTMLAttributes<HTMLTableRowElement> {
  readonly selected?: boolean | undefined;
  readonly interactive?: boolean | undefined;
  /** Flash briefly on mount — for rows that just streamed in. */
  readonly isNew?: boolean | undefined;
  readonly striped?: boolean | undefined;
}

export function Tr({ selected, interactive, isNew, striped, className, children, ...rest }: TrProps) {
  return (
    <tr
      className={cx(
        styles["tr"],
        interactive && styles["trInteractive"],
        selected && styles["trSelected"],
        isNew && styles["trNew"],
        striped && styles["trStriped"],
        className,
      )}
      aria-selected={selected}
      tabIndex={interactive ? 0 : undefined}
      {...rest}
    >
      {children}
    </tr>
  );
}

export type Align = "left" | "right" | "center";

export interface ThProps extends ThHTMLAttributes<HTMLTableCellElement> {
  readonly align?: Align | undefined;
  /** Current sort direction if this column is sorted. */
  readonly sort?: SortDirection | null | undefined;
  /** Present => the column is sortable. */
  readonly onSort?: ((next: SortDirection) => void) | undefined;
  readonly width?: number | string | undefined;
  readonly children?: ReactNode;
}

export function Th({ align = "left", sort, onSort, width, className, style, children, ...rest }: ThProps) {
  const sortable = onSort !== undefined;
  const ariaSort = sort === "asc" ? "ascending" : sort === "desc" ? "descending" : sortable ? "none" : undefined;
  const next: SortDirection = sort === "asc" ? "desc" : "asc";
  const inner = (
    <>
      {children}
      {sortable ? (
        <Icon
          className={styles["sortIcon"]}
          name={sort === "asc" ? "sort-asc" : sort === "desc" ? "sort-desc" : "sort"}
          size={11}
        />
      ) : null}
    </>
  );
  return (
    <th
      scope="col"
      className={cx(
        styles["th"],
        align === "right" && styles["alignRight"],
        align === "center" && styles["alignCenter"],
        sortable && styles["thSortable"],
        sort && styles["thSorted"],
        className,
      )}
      aria-sort={ariaSort}
      style={width !== undefined ? { width, ...style } : style}
      {...rest}
    >
      {sortable ? (
        <button type="button" className={styles["thInner"]} onClick={() => onSort(next)}>
          {inner}
        </button>
      ) : (
        <span className={styles["thInner"]}>{inner}</span>
      )}
    </th>
  );
}

export interface TdProps extends TdHTMLAttributes<HTMLTableCellElement> {
  readonly align?: Align | undefined;
  readonly mono?: boolean | undefined;
  readonly muted?: boolean | undefined;
  /** Allow wrapping instead of truncating. */
  readonly wrap?: boolean | undefined;
  /**
   * One line, never truncated: the column is at least as wide as this
   * cell's text and the other columns give way. For a short label column
   * ("Review · correctness") beside numbers. Cells truncate by default,
   * which shares the width evenly whatever the content.
   */
  readonly fit?: boolean | undefined;
  readonly children?: ReactNode;
}

export function Td({ align = "left", mono, muted, wrap, fit, className, children, ...rest }: TdProps) {
  return (
    <td
      className={cx(
        styles["td"],
        align === "right" && styles["alignRight"],
        align === "center" && styles["alignCenter"],
        mono && styles["mono"],
        muted && styles["muted"],
        wrap && styles["tdWrap"],
        fit && styles["tdFit"],
        className,
      )}
      {...rest}
    >
      {children}
    </td>
  );
}

export interface TableEmptyProps {
  readonly colSpan: number;
  readonly children?: ReactNode;
}

export function TableEmpty({ colSpan, children }: TableEmptyProps) {
  return (
    <tr>
      <td colSpan={colSpan} className={styles["empty"]}>
        {children ?? "Nothing here."}
      </td>
    </tr>
  );
}
