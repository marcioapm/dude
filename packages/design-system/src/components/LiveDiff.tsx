import { useEffect, useLayoutEffect, useMemo, useRef, useState, type HTMLAttributes, type ReactNode } from "react";
import type { RunDiffFile, RunDiffHunk, RunDiffLine } from "@dude/domain";
import { cx } from "../util/cx.ts";
import styles from "./LiveDiff.module.css";

/** A live diff's file, line and hunk: the API's (GET /v1/runs/:id/diff). */
export type LiveDiffFile = Readonly<RunDiffFile>;
export type LiveDiffHunk = RunDiffHunk;
export type LiveDiffLine = RunDiffLine;

export interface LiveDiffProps extends Omit<HTMLAttributes<HTMLDivElement>, "children"> {
  readonly files: ReadonlyArray<LiveDiffFile>;
  /** The commit it is against; shown short. */
  readonly base: string;
  /** Still changing: the agent is at work. Shows "Live". */
  readonly live?: boolean | undefined;
  /** What the agent did last ("Write LIVE.md · just now"), beside the toggle. */
  readonly lastChange?: ReactNode;
  /** Shown when nothing changed yet. */
  readonly emptyMessage?: ReactNode;
}

const STATUS_WORD: Record<LiveDiffFile["status"], string> = { M: "modified", A: "added", D: "deleted", R: "renamed" };

/** A line's identity across updates: which file, which side, which number, what text. */
const lineKey = (path: string, l: LiveDiffLine) => `${path}\u0000${l.kind}\u0000${l.old ?? ""}\u0000${l.new ?? ""}\u0000${l.text}`;

/**
 * A working agent's checkout against where it started, as it changes: the
 * files on the left with their status and counts, every file's diff on the
 * right under a header that sticks while its lines scroll by.
 *
 * Between one update and the next, the lines that are new flash and the
 * files they are in light up; with Follow on, the diff scrolls to the
 * newest change. Picking a file shows it alone and turns Follow off — the
 * person has taken over — and turning Follow on shows all again.
 *
 * Changes are told apart by sign and gutter as well as tint (+, −), so the
 * diff reads without colour.
 */
export function LiveDiff({ files, base, live, lastChange, emptyMessage, className, ...rest }: LiveDiffProps) {
  // Keep the latest change in view: on while live, until the person picks a file.
  const [follow, setFollow] = useState(live ?? false);
  const [selected, setSelected] = useState<string | null>(null);

  // What is new since the last files: lines, and the files they are in.
  // The first render has nothing to compare against, so nothing flashes.
  const seen = useRef<Set<string> | null>(null);
  const fresh = useMemo(() => {
    const before = seen.current;
    const lines = new Set<string>();
    const paths = new Set<string>();
    const now = new Set<string>();
    for (const f of files) {
      for (const h of f.hunks) {
        for (const l of h.lines) {
          const key = lineKey(f.path, l);
          now.add(key);
          if (before && l.kind !== " " && !before.has(key)) {
            lines.add(key);
            paths.add(f.path);
          }
        }
      }
    }
    seen.current = now;
    return { lines, paths };
  }, [files]);

  const totals = useMemo(
    () => files.reduce((t, f) => ({ a: t.a + f.additions, d: t.d + f.deletions }), { a: 0, d: 0 }),
    [files],
  );
  const shown = selected ? files.filter((f) => f.path === selected) : files;

  // Following: bring the newest change into view.
  const scroller = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => {
    if (!follow || fresh.lines.size === 0) return;
    const box = scroller.current;
    const marks = box?.querySelectorAll<HTMLElement>("[data-fresh]");
    const last = marks?.[marks.length - 1];
    if (box && last) {
      box.scrollTo({ top: Math.max(0, last.offsetTop - box.clientHeight / 3), behavior: "smooth" });
    }
  }, [follow, fresh]);

  // A file that is gone is no longer selectable.
  useEffect(() => {
    if (selected && !files.some((f) => f.path === selected)) setSelected(null);
  }, [files, selected]);

  const short = base.slice(0, 7);
  return (
    <div className={cx(styles["root"], className)} {...rest}>
      <div className={styles["head"]}>
        {live ? (
          <span className={styles["live"]} data-testid="live-pill">
            <i aria-hidden /> Live
          </span>
        ) : null}
        <span className={styles["since"]} title={base ? `The agent's checkout against ${base}, the commit it started from. Uncommitted work included.` : undefined}>
          {base ? (
            <>
              Since <span className={styles["mono"]}>{short}</span> ·{" "}
            </>
          ) : null}
          <b>{files.length} {files.length === 1 ? "file" : "files"}</b> <span className={styles["add"]}>+{totals.a}</span>{" "}
          <span className={styles["del"]}>−{totals.d}</span>
        </span>
        <span className={styles["spacer"]} />
        {lastChange ? <span className={styles["last"]}>{lastChange}</span> : null}
        {live ? (
          <button
            type="button"
            role="switch"
            aria-checked={follow}
            className={styles["follow"]}
            onClick={() => {
              if (!follow) setSelected(null);
              setFollow(!follow);
            }}
            data-testid="follow"
          >
            <span className={cx(styles["toggle"], follow && styles["on"])} aria-hidden />
            Follow the agent
          </button>
        ) : null}
      </div>
      {files.length === 0 ? (
        <div className={styles["empty"]}>{emptyMessage ?? "No changes yet."}</div>
      ) : (
        <div className={styles["body"]}>
          <nav className={styles["files"]} aria-label="Changed files">
            <button type="button" className={cx(styles["file"], styles["all"], !selected && styles["current"])}
              aria-pressed={!selected} onClick={() => setSelected(null)}>
              <span className={styles["path"]}>
                <b>All files</b>
              </span>
              <span className={styles["counts"]}>{files.length}</span>
            </button>
            {files.map((f) => {
              const slash = f.path.lastIndexOf("/");
              return (
                <button
                  type="button"
                  key={f.path}
                  className={cx(styles["file"], selected === f.path && styles["current"], fresh.paths.has(f.path) && styles["touched"])}
                  aria-pressed={selected === f.path}
                  title={`${f.path} · ${STATUS_WORD[f.status]}`}
                  onClick={() => {
                    setSelected(f.path);
                    setFollow(false);
                  }}
                  data-testid="diff-file"
                  data-path={f.path}
                >
                  <StatusGlyph status={f.status} />
                  <span className={styles["path"]}>
                    {slash >= 0 ? <span className={styles["dir"]}>{f.path.slice(0, slash + 1)}</span> : null}
                    <b>{f.path.slice(slash + 1)}</b>
                  </span>
                  <span className={styles["counts"]}>
                    <span className={styles["add"]}>+{f.additions}</span> <span className={styles["del"]}>−{f.deletions}</span>
                  </span>
                </button>
              );
            })}
            <p className={styles["note"]}>Uncommitted work counts: this is the agent's checkout now, not what it has pushed.</p>
          </nav>
          <div className={styles["diffs"]} ref={scroller} data-testid="diffs">
            {shown.map((f) => (
              <section key={f.path} className={styles["section"]} data-testid="diff-section" data-path={f.path}>
                <header className={styles["fileHead"]}>
                  <StatusGlyph status={f.status} />
                  <b className={styles["mono"]}>{f.path}</b>
                  {f.status === "A" ? <span className={styles["muted"]}>new file</span> : null}
                  {f.status === "D" ? <span className={styles["muted"]}>deleted</span> : null}
                  <span className={styles["spacer"]} />
                  <span className={styles["counts"]}>
                    <span className={styles["add"]}>+{f.additions}</span> <span className={styles["del"]}>−{f.deletions}</span>
                  </span>
                </header>
                {f.binary ? <div className={styles["empty"]}>Binary file not shown.</div> : null}
                {f.hunks.map((h, hi) => (
                  <div key={hi}>
                    <div className={styles["hunk"]}>{h.header}</div>
                    {h.lines.map((l, li) => {
                      const isFresh = fresh.lines.has(lineKey(f.path, l));
                      return (
                        <div
                          key={li}
                          className={cx(styles["line"], l.kind === "+" && styles["added"], l.kind === "-" && styles["removed"], isFresh && styles["fresh"])}
                          data-fresh={isFresh ? "" : undefined}
                        >
                          <span className={styles["n"]}>{l.old ?? ""}</span>
                          <span className={styles["n"]}>{l.new ?? ""}</span>
                          <span className={styles["sign"]} aria-hidden>
                            {l.kind === "+" ? "+" : l.kind === "-" ? "−" : ""}
                          </span>
                          <span className={styles["text"]}>{l.text || " "}</span>
                        </div>
                      );
                    })}
                  </div>
                ))}
                {f.truncated ? <div className={styles["empty"]}>Longer than shown: the rest of this file is left out.</div> : null}
              </section>
            ))}
          </div>
        </div>
      )}
    </div>
  );
}

function StatusGlyph({ status }: { status: LiveDiffFile["status"] }) {
  return (
    <span className={cx(styles["status"], styles[`status${status}`])} title={STATUS_WORD[status]} aria-label={STATUS_WORD[status]}>
      {status}
    </span>
  );
}
