import { createPortal } from "react-dom";
import { useLayoutEffect, useMemo, useRef, useState, type HTMLAttributes, type ReactNode } from "react";
import type { RunDiffFile, RunDiffHunk, RunDiffLine } from "@dude/domain";
import { cx } from "../util/cx.ts";
import { IconButton } from "../primitives/Button.tsx";
import { DiffStat } from "./DiffStat.tsx";
import { Segmented } from "./ScreenHeader.tsx";
import { Switch } from "./Settings.tsx";
import styles from "./LiveDiff.module.css";

/** A live diff's file, line and hunk: the API's (GET /v1/runs/:id/diff). */
export type LiveDiffFile = Readonly<RunDiffFile>;
export type LiveDiffHunk = RunDiffHunk;
export type LiveDiffLine = RunDiffLine;

export interface LiveDiffProps extends Omit<HTMLAttributes<HTMLDivElement>, "children"> {
  readonly files: ReadonlyArray<LiveDiffFile>;
  /** The commit it is against; shown short. */
  readonly base: string;
  /** Still changing: the agent is at work. Offers Follow the agent. */
  readonly live?: boolean | undefined;
  /** What the agent did last ("Write LIVE.md · just now"): at the top of the file list, or above the diff without one. */
  readonly lastChange?: ReactNode;
  /**
   * First in the toolbar: what the diff sits among — a session's view
   * switch — so the diff's controls and the page's share one row rather
   * than stacking.
   */
  readonly leading?: ReactNode;
  /**
   * Draw the toolbar's controls into this element instead of above the
   * files: a bar the page keeps mounted (so its own controls, and the
   * focus on them, survive the diff coming and going). `leading` is then
   * the page's to place.
   */
  readonly toolbarIn?: HTMLElement | null | undefined;
  /** Shown when nothing changed yet. */
  readonly emptyMessage?: ReactNode;
  /** Opens a file in the viewer; each file's header offers it when given. */
  readonly onOpenFile?: ((path: string) => void) | undefined;
  /** Side by side or one column; unified until the person picks. */
  readonly defaultView?: LiveDiffView | undefined;
  /**
   * The file shown alone, null for all of them. Give it (with
   * `onSelectedChange`) to pick one from outside — a list of changed files
   * beside the diff; left out, the diff keeps its own. A file picked, here
   * or outside, turns Follow off: the person has taken over.
   */
  readonly selected?: string | null | undefined;
  readonly onSelectedChange?: ((path: string | null) => void) | undefined;
  /** The list of files down the left; off for one file on its own (in a viewer). */
  readonly fileList?: boolean | undefined;
}

export type LiveDiffView = "unified" | "split";

const STATUS_WORD: Record<LiveDiffFile["status"], string> = { M: "modified", A: "added", D: "deleted", R: "renamed" };

/** A line's identity across updates: which file, which side, which number, what text. */
const lineKey = (path: string, l: LiveDiffLine) => `${path}\u0000${l.kind}\u0000${l.old ?? ""}\u0000${l.new ?? ""}\u0000${l.text}`;

/** One row of a side-by-side hunk: the old line on the left, the new on the right, either may be missing. */
export interface SplitRow {
  readonly left: LiveDiffLine | null;
  readonly right: LiveDiffLine | null;
}

/**
 * A hunk's lines side by side: context on both sides, and each run of
 * removed lines paired with the added run that follows it, row by row, the
 * longer side's rest against nothing — as a split view reads a change.
 */
export function splitRows(lines: ReadonlyArray<LiveDiffLine>): SplitRow[] {
  const rows: SplitRow[] = [];
  let i = 0;
  while (i < lines.length) {
    const line = lines[i]!;
    if (line.kind === " ") {
      rows.push({ left: line, right: line });
      i++;
      continue;
    }
    const removed: LiveDiffLine[] = [];
    const added: LiveDiffLine[] = [];
    while (i < lines.length && lines[i]!.kind === "-") removed.push(lines[i++]!);
    while (i < lines.length && lines[i]!.kind === "+") added.push(lines[i++]!);
    for (let k = 0; k < Math.max(removed.length, added.length); k++) rows.push({ left: removed[k] ?? null, right: added[k] ?? null });
  }
  return rows;
}

const SIGN = { "+": "+", "-": "−", " ": "" } as const;

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
 * Unified shows each file in one column; Split puts the old side beside
 * the new. Changes are told apart by sign and gutter as well as tint
 * (+, −), so the diff reads without colour.
 */
export function LiveDiff({ files, base, live, lastChange, leading, toolbarIn, emptyMessage, onOpenFile, defaultView = "unified", selected: given, onSelectedChange, fileList = true, className, ...rest }: LiveDiffProps) {
  const [view, setView] = useState<LiveDiffView>(defaultView);
  // Keep the latest change in view: on while live, until the person picks a file.
  const [follow, setFollow] = useState(live ?? false);
  const [own, setOwn] = useState<string | null>(null);
  // A file that is gone is no longer shown alone.
  const picked = given !== undefined ? given : own;
  const selected = picked && files.some((f) => f.path === picked) ? picked : null;
  const select = (path: string | null) => {
    if (given === undefined) setOwn(path);
    onSelectedChange?.(path);
  };
  // Picked, here or outside: the person has taken over.
  const [lastSelected, setLastSelected] = useState(selected);
  if (selected !== lastSelected) {
    setLastSelected(selected);
    if (selected) setFollow(false);
  }

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
  // Side by side, each hunk's rows once per diff, not on every render.
  const split = useMemo(
    () => (view === "split" ? new Map(files.map((f) => [f.path, f.hunks.map((h) => splitRows(h.lines))])) : null),
    [files, view],
  );

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

  const short = base.slice(0, 7);
  const tools = (
    <>
        {files.length > 0 ? (
          <span className={styles["since"]} title={base ? `The agent's checkout against ${base}, the commit it started from. Uncommitted work included.` : undefined}>
            {base ? (
              <>
                Since <span className={styles["mono"]}>{short}</span> ·{" "}
              </>
            ) : null}
            <b>{files.length} {files.length === 1 ? "file" : "files"}</b> <DiffStat additions={totals.a} deletions={totals.d} />
          </span>
        ) : null}
        <span className={styles["spacer"]} />
        {live && files.length > 0 ? (
          <Switch
            checked={follow}
            onCheckedChange={(on) => {
              if (on) select(null);
              setFollow(on);
            }}
            label="Follow the agent"
            testId="follow"
          />
        ) : null}
        {files.length > 0 ? (
          <Segmented label="Show the diff" size="sm" value={view} onChange={setView} data-testid="diff-view"
            options={[{ value: "unified", label: "Unified" }, { value: "split", label: "Split" }]} />
        ) : null}
    </>
  );
  return (
    <div className={cx(styles["root"], className)} {...rest}>
      {toolbarIn ? createPortal(<div className={cx(styles["head"], styles["headIn"])}>{tools}</div>, toolbarIn) : (
        <div className={styles["head"]}>
          {leading}
          {tools}
        </div>
      )}
      {files.length === 0 ? (
        <div className={styles["empty"]}>
          {lastChange ? <div className={styles["last"]} data-testid="last-change">{lastChange}</div> : null}
          {emptyMessage ?? "No changes yet."}
        </div>
      ) : (
        <div className={cx(styles["body"], !fileList && styles["bodyAlone"])}>
          {fileList ? (
            <nav className={styles["files"]} aria-label="Changed files">
              {lastChange ? <div className={styles["last"]} data-testid="last-change">{lastChange}</div> : null}
              <button type="button" className={cx(styles["file"], styles["all"], !selected && styles["current"])}
                aria-pressed={!selected} onClick={() => select(null)}>
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
                    onClick={() => select(f.path)}
                    data-testid="diff-file"
                    data-path={f.path}
                  >
                    <StatusGlyph status={f.status} />
                    <span className={styles["path"]}>
                      {slash >= 0 ? <span className={styles["dir"]}>{f.path.slice(0, slash + 1)}</span> : null}
                      <b>{f.path.slice(slash + 1)}</b>
                    </span>
                    <DiffStat className={styles["counts"]} additions={f.additions} deletions={f.deletions} />
                  </button>
                );
              })}
              <p className={styles["note"]}>Uncommitted work counts: this is the agent's checkout now, not what it has pushed.</p>
            </nav>
          ) : null}
          <div className={styles["diffs"]} ref={scroller} data-testid="diffs">
            {!fileList && lastChange ? <div className={styles["last"]} data-testid="last-change">{lastChange}</div> : null}
            {shown.map((f) => (
              <section key={f.path} className={styles["section"]} data-testid="diff-section" data-path={f.path}>
                <header className={styles["fileHead"]}>
                  <StatusGlyph status={f.status} />
                  <b className={styles["mono"]}>{f.path}</b>
                  {f.status === "A" ? <span className={styles["muted"]}>new file</span> : null}
                  {f.status === "D" ? <span className={styles["muted"]}>deleted</span> : null}
                  <span className={styles["spacer"]} />
                  <DiffStat className={styles["counts"]} additions={f.additions} deletions={f.deletions} />
                  {onOpenFile && f.status !== "D" ? (
                    <IconButton icon="external" label="Open in the viewer" size="sm" onClick={() => onOpenFile(f.path)} data-testid="diff-open" />
                  ) : null}
                </header>
                {f.binary ? <div className={styles["empty"]}>Binary file not shown.</div> : null}
                {f.hunks.map((h, hi) => (
                  <div key={hi}>
                    <div className={styles["hunk"]}>{h.header}</div>
                    {view === "unified"
                      ? h.lines.map((l, li) => {
                          const isFresh = fresh.lines.has(lineKey(f.path, l));
                          return (
                            <div
                              key={li}
                              className={cx(styles["line"], l.kind === "+" && styles["added"], l.kind === "-" && styles["removed"], isFresh && styles["fresh"])}
                              data-fresh={isFresh ? "" : undefined}
                            >
                              <span className={styles["n"]}>{l.old ?? ""}</span>
                              <span className={styles["n"]}>{l.new ?? ""}</span>
                              <span className={styles["sign"]} aria-hidden>{SIGN[l.kind]}</span>
                              <span className={styles["text"]}>{l.text || " "}</span>
                            </div>
                          );
                        })
                      : split!.get(f.path)![hi]!.map((row, ri) => {
                          // Context sits on both sides and is never fresh: one side is enough to ask.
                          const changed = row.left?.kind === " " ? null : (row.left ?? row.right);
                          const isFresh = changed !== null && (fresh.lines.has(lineKey(f.path, changed)) ||
                            (row.right !== null && row.right !== changed && fresh.lines.has(lineKey(f.path, row.right))));
                          return (
                            <div key={ri} className={cx(styles["split"], isFresh && styles["fresh"])} data-fresh={isFresh ? "" : undefined}
                              data-testid="split-row">
                              <SplitSide line={row.left} side="old" />
                              <SplitSide line={row.right} side="new" />
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

/** One side of a split row: its number, sign and text, or an empty stretch where that side has no line. */
function SplitSide({ line, side }: { line: LiveDiffLine | null; side: "old" | "new" }) {
  if (!line) return <span className={cx(styles["side"], styles["gap"])} />;
  return (
    <span className={cx(styles["side"], line.kind === "+" && styles["added"], line.kind === "-" && styles["removed"])}>
      <span className={styles["n"]}>{side === "old" ? line.old : line.new}</span>
      <span className={styles["sign"]} aria-hidden>{SIGN[line.kind]}</span>
      <span className={styles["text"]}>{line.text || " "}</span>
    </span>
  );
}

function StatusGlyph({ status }: { status: LiveDiffFile["status"] }) {
  return (
    <span className={cx(styles["status"], styles[`status${status}`])} title={STATUS_WORD[status]} aria-label={STATUS_WORD[status]}>
      {status}
    </span>
  );
}
