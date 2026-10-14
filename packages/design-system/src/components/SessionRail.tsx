import type { ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { KeyValueList } from "../primitives/Layout.tsx";
import { DiffStat } from "./DiffStat.tsx";
import { ARTIFACT_KIND_SPECS, artifactKind } from "./ArtifactRow.tsx";
import { Icon } from "../icons/index.tsx";
import styles from "./SessionRail.module.css";

export { PageRail as SessionRail, PageRailBlock as SessionRailBlock } from "./PageWithRail.tsx";
export type { PageRailProps as SessionRailProps } from "./PageWithRail.tsx";

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

export interface PublishedFile {
  readonly name: string;
  readonly contentType: string;
  /** How many times it was published under this name; more than one shows as "v3". */
  readonly versions?: number | undefined;
  /** What the agent said it is for, one short line: a secondary line under the name. */
  readonly description?: string | undefined;
}

/**
 * What a brainstorm session's agent published, newest first: each file's
 * kind glyph and name, and under it what the agent said it is for when it
 * said; picking one opens it in the viewer. The name is the file's own;
 * its folder is in the tooltip.
 */
export function PublishedFiles({ files, onOpen, max = 8 }: {
  readonly files: ReadonlyArray<PublishedFile>;
  readonly onOpen: (name: string) => void;
  readonly max?: number;
}) {
  const shown = files.slice(0, max);
  return (
    <ul className={styles["files"]} data-testid="published-files">
      {shown.map((f) => (
        <li key={f.name}>
          <button type="button" className={cx(styles["file"], styles["publishedFile"])} title={f.name} onClick={() => onOpen(f.name)} data-name={f.name}>
            <span className={styles["fileLine"]}>
              <span className={styles["published"]}>
                <Icon name={ARTIFACT_KIND_SPECS[artifactKind(f.contentType, f.name)].glyph} size={12} className={styles["glyph"]} />
                <span className={styles["filePath"]}>{f.name.slice(f.name.lastIndexOf("/") + 1)}</span>
              </span>
              {(f.versions ?? 1) > 1 ? <span className={styles["version"]}>v{f.versions}</span> : null}
            </span>
            {f.description ? <span className={styles["fileDescription"]} title={f.description} data-testid="published-file-description">{f.description}</span> : null}
          </button>
        </li>
      ))}
      {files.length > shown.length ? <li className={styles["more"]}>{files.length - shown.length} more</li> : null}
    </ul>
  );
}
