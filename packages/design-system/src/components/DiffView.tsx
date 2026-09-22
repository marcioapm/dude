import { useMemo, useState, type HTMLAttributes, type ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { Icon } from "../icons/index.tsx";
import styles from "./DiffView.module.css";

export type DiffLineKind = "context" | "add" | "del" | "hunk";

export interface DiffLine {
  readonly kind: DiffLineKind;
  readonly text: string;
  readonly oldNo?: number | undefined;
  readonly newNo?: number | undefined;
}

export interface DiffHunk {
  readonly header: string;
  readonly lines: ReadonlyArray<DiffLine>;
}

export type FileChangeKind = "modified" | "added" | "deleted" | "renamed" | "binary";

export interface FileDiff {
  readonly path: string;
  readonly oldPath?: string | undefined;
  readonly kind?: FileChangeKind | undefined;
  readonly hunks: ReadonlyArray<DiffHunk>;
  readonly additions?: number | undefined;
  readonly deletions?: number | undefined;
}

/**
 * Parse a unified diff (git diff / patch format) into FileDiff[]. Good
 * enough for the diffs agents produce; not a full patch parser.
 */
export function parseUnifiedDiff(text: string): FileDiff[] {
  const files: FileDiff[] = [];
  let cur: { path: string; oldPath?: string; kind: FileChangeKind; hunks: DiffHunk[]; adds: number; dels: number } | null = null;
  let hunk: { header: string; lines: DiffLine[] } | null = null;
  let oldNo = 0;
  let newNo = 0;

  const flush = () => {
    if (cur) {
      if (hunk) cur.hunks.push(hunk);
      const fd: FileDiff = {
        path: cur.path,
        kind: cur.kind,
        hunks: cur.hunks,
        additions: cur.adds,
        deletions: cur.dels,
        ...(cur.oldPath !== undefined ? { oldPath: cur.oldPath } : {}),
      };
      files.push(fd);
    }
    cur = null;
    hunk = null;
  };

  for (const raw of text.split("\n")) {
    if (raw.startsWith("diff --git ")) {
      flush();
      const m = /^diff --git a\/(.+?) b\/(.+)$/.exec(raw);
      const oldP = m?.[1] ?? "";
      const newP = m?.[2] ?? oldP;
      cur = { path: newP, kind: "modified", hunks: [], adds: 0, dels: 0 };
      if (oldP !== newP) {
        cur.oldPath = oldP;
        cur.kind = "renamed";
      }
      continue;
    }
    if (!cur) {
      if (raw.startsWith("--- ") || raw.startsWith("+++ ")) {
        // Bare patch without a diff --git header.
        const p = raw.slice(4).replace(/^[ab]\//, "");
        if (raw.startsWith("--- ")) cur = { path: p, kind: "modified", hunks: [], adds: 0, dels: 0 };
      }
      continue;
    }
    if (raw.startsWith("new file mode")) {
      cur.kind = "added";
      continue;
    }
    if (raw.startsWith("deleted file mode")) {
      cur.kind = "deleted";
      continue;
    }
    if (raw.startsWith("Binary files")) {
      cur.kind = "binary";
      continue;
    }
    if (raw.startsWith("--- ") || raw.startsWith("+++ ") || raw.startsWith("index ") || raw.startsWith("similarity") || raw.startsWith("rename ")) {
      if (raw.startsWith("+++ ")) {
        const p = raw.slice(4).replace(/^[ab]\//, "");
        if (p !== "/dev/null") cur.path = p;
      }
      continue;
    }
    if (raw.startsWith("@@")) {
      if (hunk) cur.hunks.push(hunk);
      const m = /^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/.exec(raw);
      oldNo = Number(m?.[1] ?? 1);
      newNo = Number(m?.[2] ?? 1);
      hunk = { header: raw, lines: [] };
      continue;
    }
    if (!hunk) continue;
    if (raw.startsWith("+")) {
      hunk.lines.push({ kind: "add", text: raw.slice(1), newNo: newNo++ });
      cur.adds++;
    } else if (raw.startsWith("-")) {
      hunk.lines.push({ kind: "del", text: raw.slice(1), oldNo: oldNo++ });
      cur.dels++;
    } else if (raw.startsWith("\\")) {
      // "\ No newline at end of file" — skip
    } else if (raw === "" && hunk.lines.length === 0) {
      // trailing blank
    } else {
      hunk.lines.push({ kind: "context", text: raw.startsWith(" ") ? raw.slice(1) : raw, oldNo: oldNo++, newNo: newNo++ });
    }
  }
  flush();
  return files;
}

export interface DiffFileProps extends Omit<HTMLAttributes<HTMLDivElement>, "children"> {
  readonly file: FileDiff;
  readonly defaultCollapsed?: boolean | undefined;
  /** Collapse files above this many changed lines by default. */
  readonly collapseAbove?: number | undefined;
  /** Extra content in the header (e.g. "open in editor"). */
  readonly actions?: ReactNode;
}

const KIND_BADGE: Record<FileChangeKind, string | null> = {
  modified: null,
  added: "added",
  deleted: "deleted",
  renamed: "renamed",
  binary: "binary",
};

/**
 * Per-file unified diff. Added/removed are distinguished by background,
 * gutter color, AND the +/- sign column, so the diff reads in grayscale.
 * Line numbers are sticky on horizontal scroll.
 */
export function DiffFile({ file, defaultCollapsed, collapseAbove = 400, actions, className, ...rest }: DiffFileProps) {
  const adds = file.additions ?? file.hunks.reduce((n, h) => n + h.lines.filter((l) => l.kind === "add").length, 0);
  const dels = file.deletions ?? file.hunks.reduce((n, h) => n + h.lines.filter((l) => l.kind === "del").length, 0);
  const [collapsed, setCollapsed] = useState(defaultCollapsed ?? adds + dels > collapseAbove);
  const kind = file.kind ?? "modified";
  const badge = KIND_BADGE[kind];

  const bar = useMemo(() => {
    const total = adds + dels;
    if (total === 0) return [];
    const blocks = 5;
    const a = Math.round((adds / total) * blocks);
    return Array.from({ length: blocks }, (_, i) => (i < a ? "add" : i < a + Math.round((dels / total) * blocks) ? "del" : "none"));
  }, [adds, dels]);

  return (
    <div className={cx(styles["file"], collapsed && styles["collapsed"], className)} data-path={file.path} {...rest}>
      <div
        className={styles["fileHeader"]}
        role="button"
        tabIndex={0}
        aria-expanded={!collapsed}
        onClick={() => setCollapsed((v) => !v)}
        onKeyDown={(e) => {
          if (e.key === "Enter" || e.key === " ") {
            e.preventDefault();
            setCollapsed((v) => !v);
          }
        }}
      >
        <Icon name="chevron-down" size={12} className={styles["fileToggle"]} />
        <span className={styles["path"]} title={file.path}>
          <span>
            {file.oldPath ? (
              <>
                <span className={styles["pathOld"]}>{file.oldPath}</span>
                <span className={styles["pathArrow"]}>→</span>
              </>
            ) : null}
            {file.path}
          </span>
        </span>
        {badge ? <span className={styles["fileBadge"]}>{badge}</span> : null}
        <span className={styles["stats"]}>
          <span className={styles["statAdd"]}>+{adds}</span>
          <span className={styles["statDel"]}>−{dels}</span>
          <span className={styles["statBar"]} aria-hidden>
            {bar.map((k, i) => (
              <i key={i} data-k={k} />
            ))}
          </span>
        </span>
        {actions}
      </div>
      {collapsed ? null : kind === "binary" ? (
        <div className={styles["binary"]}>Binary file not shown.</div>
      ) : file.hunks.length === 0 ? (
        <div className={styles["empty"]}>No textual changes.</div>
      ) : (
        <div className={styles["body"]}>
          <table className={styles["table"]}>
            <tbody>
              {file.hunks.map((h, hi) => (
                <HunkRows key={hi} hunk={h} />
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

function HunkRows({ hunk }: { readonly hunk: DiffHunk }) {
  return (
    <>
      <tr className={cx(styles["line"], styles["hunk"])}>
        <td className={styles["gutter"]}>…</td>
        <td className={cx(styles["gutter"], styles["gutterNew"])} />
        <td className={styles["sign"]} />
        <td className={styles["code"]}>{hunk.header}</td>
      </tr>
      {hunk.lines.map((l, i) => (
        <tr key={i} className={cx(styles["line"], l.kind === "add" && styles["add"], l.kind === "del" && styles["del"])}>
          <td className={styles["gutter"]}>{l.oldNo ?? ""}</td>
          <td className={cx(styles["gutter"], styles["gutterNew"])}>{l.newNo ?? ""}</td>
          <td className={styles["sign"]} aria-hidden>
            {l.kind === "add" ? "+" : l.kind === "del" ? "−" : ""}
          </td>
          <td className={styles["code"]}>{l.text.length === 0 ? " " : l.text}</td>
        </tr>
      ))}
    </>
  );
}

export interface DiffViewProps extends Omit<HTMLAttributes<HTMLDivElement>, "children"> {
  readonly files: ReadonlyArray<FileDiff>;
  readonly collapseAbove?: number | undefined;
  /** Show the "N files, +a −d" summary line. */
  readonly summary?: boolean | undefined;
}

/** A list of per-file diffs with a blast-radius summary line. */
export function DiffView({ files, collapseAbove, summary = true, className, ...rest }: DiffViewProps) {
  const totals = useMemo(() => {
    let a = 0;
    let d = 0;
    for (const f of files) {
      a += f.additions ?? f.hunks.reduce((n, h) => n + h.lines.filter((l) => l.kind === "add").length, 0);
      d += f.deletions ?? f.hunks.reduce((n, h) => n + h.lines.filter((l) => l.kind === "del").length, 0);
    }
    return { a, d };
  }, [files]);
  return (
    <div className={className} style={{ display: "flex", flexDirection: "column", gap: 8 }} {...rest}>
      {summary ? (
        <div className={styles["summary"]}>
          <span>
            {files.length} {files.length === 1 ? "file" : "files"} changed
          </span>
          <span className={styles["summaryStats"]}>
            <span className={styles["statAdd"]}>+{totals.a}</span> <span className={styles["statDel"]}>−{totals.d}</span>
          </span>
        </div>
      ) : null}
      {files.map((f) => (
        <DiffFile key={f.path} file={f} collapseAbove={collapseAbove} />
      ))}
    </div>
  );
}
