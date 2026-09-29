import type { HTMLAttributes, ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { KeyValueList } from "../primitives/Layout.tsx";
import { DiffStat } from "./DiffStat.tsx";
import styles from "./SessionRail.module.css";

export interface SessionRailProps extends HTMLAttributes<HTMLElement> {
  readonly children?: ReactNode;
}

/**
 * Beside a session's conversation, on the chrome shade: what it is running
 * on, what it has used, and the files it has changed so far. Blocks under
 * small caps labels; no lines between them, the shade and space do that.
 */
export function SessionRail({ className, children, ...rest }: SessionRailProps) {
  return (
    <aside className={cx(styles["rail"], className)} {...rest}>
      {children}
    </aside>
  );
}

export function SessionRailBlock({ label, live, children, ...rest }: Omit<HTMLAttributes<HTMLElement>, "title"> & {
  readonly label: ReactNode;
  /** What it lists is changing now: a breathing dot beside the label. */
  readonly live?: boolean | undefined;
  readonly children?: ReactNode;
}) {
  return (
    <section className={styles["block"]} {...rest}>
      <h3 className={cx("ds-label", styles["label"])}>
        {label}
        {live ? <span className="ds-live-dot" aria-label="live" /> : null}
      </h3>
      {children}
    </section>
  );
}

/** Label and value pairs (a `KeyValueList`), the values on the right as a rail reads them. */
export function SessionFacts({ facts }: { readonly facts: ReadonlyArray<{ readonly label: ReactNode; readonly value: ReactNode; readonly mono?: boolean }> }) {
  return <KeyValueList className={styles["facts"]} items={facts} />;
}

export interface ToolCount {
  readonly name: string;
  readonly count: number;
}

/** Which tools the agent called, most used first, each with a bar against the most. */
export function ToolUsage({ tools, max = 6 }: { readonly tools: ReadonlyArray<ToolCount>; readonly max?: number }) {
  const shown = [...tools].sort((a, b) => b.count - a.count || a.name.localeCompare(b.name)).slice(0, max);
  const top = shown[0]?.count ?? 0;
  return (
    <div className={styles["bars"]} data-testid="tool-usage">
      {shown.map((t) => (
        <div key={t.name} className={styles["bar"]}>
          <span className={styles["barName"]}>{t.name}</span>
          <i style={{ width: `${top > 0 ? Math.max(4, (t.count / top) * 100) : 0}%` }} aria-hidden />
          <span className={styles["barCount"]}>{t.count}</span>
        </div>
      ))}
    </div>
  );
}

export interface ChangedFile {
  readonly path: string;
  readonly additions: number;
  readonly deletions: number;
}

/**
 * The files changed so far, with their counts; picking one opens the
 * Changes. The name is the file's own; its folder is in the tooltip.
 */
export function ChangedFiles({ files, onOpen, max = 8 }: {
  readonly files: ReadonlyArray<ChangedFile>;
  readonly onOpen: (path: string) => void;
  readonly max?: number;
}) {
  const shown = files.slice(0, max);
  return (
    <ul className={styles["files"]} data-testid="changed-files">
      {shown.map((f) => (
        <li key={f.path}>
          <button type="button" className={styles["file"]} title={f.path} onClick={() => onOpen(f.path)}>
            <span className={styles["filePath"]}>{f.path.slice(f.path.lastIndexOf("/") + 1)}</span>
            <DiffStat additions={f.additions} deletions={f.deletions} />
          </button>
        </li>
      ))}
      {files.length > shown.length ? <li className={styles["more"]}>{files.length - shown.length} more</li> : null}
    </ul>
  );
}
