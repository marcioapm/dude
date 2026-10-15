import { useId, type HTMLAttributes, type ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { Checkbox } from "../primitives/Checkbox.tsx";
import styles from "./PageWithRail.module.css";

export interface PageWithRailProps extends HTMLAttributes<HTMLDivElement> {
  readonly rail?: ReactNode;
}

export function PageWithRail({ rail, children, className, ...rest }: PageWithRailProps) {
  return <div className={cx(styles["page"], className)} {...rest}>
    <div className={styles["layout"]}>
      {rail ? <div className={styles["railSlot"]}>{rail}</div> : null}
      <div className={styles["content"]}>{children}</div>
    </div>
  </div>;
}

export interface PageRailProps extends HTMLAttributes<HTMLElement> {}

export function PageRail({ className, children, ...rest }: PageRailProps) {
  return <aside className={cx(styles["rail"], className)} {...rest}>{children}</aside>;
}

export interface PageRailBlockProps extends Omit<HTMLAttributes<HTMLElement>, "title"> {
  readonly label: ReactNode;
  readonly live?: boolean | undefined;
}

export function PageRailBlock({ label, live, children, className, ...rest }: PageRailBlockProps) {
  const id = useId();
  return <section aria-labelledby={id} className={cx(styles["block"], className)} {...rest}>
    <h3 id={id} className={cx("ds-label", styles["label"])}>
      {label}{live ? <span className="ds-live-dot" aria-label="live" /> : null}
    </h3>
    {children}
  </section>;
}

export interface FacetOption {
  readonly value: string;
  readonly label: ReactNode;
  readonly count?: number | undefined;
  readonly disabled?: boolean | undefined;
  readonly description?: ReactNode;
}

export interface FacetListProps extends Omit<HTMLAttributes<HTMLDivElement>, "onChange"> {
  readonly label: string;
  readonly options: readonly FacetOption[];
  readonly value: readonly string[];
  readonly onValueChange: (value: string[]) => void;
  readonly empty?: ReactNode;
}

export function FacetList({ label, options, value, onValueChange, empty = "No options", className, ...rest }: FacetListProps) {
  return <div role="group" aria-label={label} className={cx(styles["facets"], className)} {...rest}>
    {options.length ? options.map((option) => <div key={option.value} className={styles["facet"]}>
      <Checkbox label={option.label} description={option.description} disabled={option.disabled}
        checked={value.includes(option.value)} onCheckedChange={(checked) => onValueChange(checked === true
          ? [...value.filter((v) => v !== option.value), option.value]
          : value.filter((v) => v !== option.value))} />
      {option.count !== undefined ? <span className={styles["count"]}>{option.count}</span> : null}
    </div>) : <span className={styles["empty"]}>{empty}</span>}
  </div>;
}
