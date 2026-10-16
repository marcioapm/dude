import { createPortal } from "react-dom";
import { Fragment, memo, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type HTMLAttributes, type ReactNode } from "react";
import type { RunDiffFile, RunDiffHunk, RunDiffLine } from "@dude/domain";
import { cx } from "../util/cx.ts";
import { plural } from "../util/format.ts";
import { Icon } from "../icons/index.tsx";
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
  readonly lastChange?: LastChange | undefined;
  /**
   * First in the diff's own toolbar, when it draws one (no `toolbarIn`):
   * what the diff sits among, so its controls and the page's share one row
   * rather than stacking.
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
  /** Comments on lines: a gutter "+", a range picked by press and drag, and rows under a line. */
  readonly commenting?: LiveDiffCommenting | undefined;
}

export type LiveDiffView = "unified" | "split";
export type DiffSide = "old" | "new";

/** Lines of one side of one file, as numbered on that side. */
export interface DiffRange {
  readonly path: string;
  readonly side: DiffSide;
  readonly startLine: number;
  readonly endLine: number;
}

export interface LiveDiffCommenting {
  /** Lines can be picked to comment on: the "+" and the drag. Off, only what `renderAfterLine` draws shows. */
  readonly canStart?: boolean | undefined;
  /** At most this many lines in one range. */
  readonly maxLines: number;
  /** The range picked, the app's to keep. */
  readonly selection: DiffRange | null;
  /** A range picked: `done` false while the pointer still drags it, true once let go (or picked by key). */
  readonly onSelectionChange: (range: DiffRange | null, done: boolean) => void;
  /** Under the line numbered `line` on `side`: what was written there, the editor. Called for every line: keep it cheap. */
  readonly renderAfterLine?: ((path: string, side: DiffSide, line: number) => ReactNode) | undefined;
  /** Under a file's header, before its first hunk: what no longer has lines in it. */
  readonly renderFileHead?: ((path: string) => ReactNode) | undefined;
  /** After a file's name in the list: how many comments it has. */
  readonly fileMark?: ((path: string) => ReactNode) | undefined;
}

/** A line's "+" and numbers carry where they are, so one handler on the diff serves all of them. */
interface Point {
  readonly path: string;
  readonly side: DiffSide;
  readonly line: number;
  readonly hunk: number;
}

const pointOf = (el: Element | null, side?: DiffSide): Point | null => {
  const at = el?.closest<HTMLElement>("[data-cline]");
  if (!at) return null;
  // A unified context line is on both sides: dragged over from the old side, it is its old number.
  const old = at.dataset["cold"];
  if (side === "old" && old !== undefined) return { path: at.dataset["cpath"]!, side, line: Number(old), hunk: Number(at.dataset["chunk"]) };
  return { path: at.dataset["cpath"]!, side: at.dataset["cside"] as DiffSide, line: Number(at.dataset["cline"]), hunk: Number(at.dataset["chunk"]) };
};

const pointRange = (at: Point): DiffRange => ({ path: at.path, side: at.side, startLine: at.line, endLine: at.line });

/** `from` stretched to `to` on its side and hunk, capped at `max` lines counted from `from`. */
export function stretchRange(from: Point, to: Point, max: number): DiffRange | null {
  if (from.path !== to.path || from.side !== to.side || from.hunk !== to.hunk) return null;
  let start = Math.min(from.line, to.line);
  let end = Math.max(from.line, to.line);
  if (end - start + 1 > max) {
    if (to.line > from.line) end = start + max - 1;
    else start = end - max + 1;
  }
  return { path: from.path, side: from.side, startLine: start, endLine: end };
}

const sideOfLine = (l: LiveDiffLine): DiffSide => (l.kind === "-" ? "old" : "new");
const numberOn = (l: LiveDiffLine, side: DiffSide) => (side === "old" ? l.old : l.new);
const inRange = (r: DiffRange | null, path: string, side: DiffSide, n: number | null) =>
  r !== null && n !== null && r.path === path && r.side === side && n >= r.startLine && n <= r.endLine;

/** Unified: which rows of a hunk a range covers, the other side's lines between its ends included. */
function coveredRows(lines: ReadonlyArray<LiveDiffLine>, path: string, r: DiffRange | null): [number, number] | null {
  if (!r || r.path !== path) return null;
  const on = (l: LiveDiffLine) => (r.side === "old" ? l.kind !== "+" : l.kind !== "-");
  const first = lines.findIndex((l) => on(l) && numberOn(l, r.side) === r.startLine);
  const last = lines.findIndex((l) => on(l) && numberOn(l, r.side) === r.endLine);
  return first >= 0 && last >= first ? [first, last] : null;
}

/** What the agent did last: its face, the tool ("Write"), the file's name, and when. */
export interface LastChange {
  readonly face?: ReactNode;
  readonly tool: string;
  readonly path?: string | undefined;
  readonly when?: ReactNode;
}

/** The last change on one line: the face stays whole, the words give way — the tool first, the file name last. */
function LastChangeLine({ change }: { readonly change: LastChange }) {
  return (
    <div className={styles["last"]} data-testid="last-change">
      {change.face ? <span className={styles["lastFace"]}>{change.face}</span> : null}
      <span className={styles["lastTool"]}>{change.tool}</span>
      {change.path ? <code className={styles["lastPath"]}>{change.path}</code> : null}
      {change.when ? <span className={styles["lastWhen"]}>· {change.when}</span> : null}
    </div>
  );
}

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
export function LiveDiff({ files, base, live, lastChange, leading, toolbarIn, emptyMessage, onOpenFile, defaultView = "unified", selected: given, onSelectedChange, fileList = true, commenting, className, ...rest }: LiveDiffProps) {
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
    const diffs = scroller.current;
    const marks = diffs?.querySelectorAll<HTMLElement>("[data-fresh]");
    const last = marks?.[marks.length - 1];
    // On a narrow screen the file list and the diff scroll as one, in the body around them.
    const box = diffs && getComputedStyle(diffs).overflowY === "visible" ? diffs.parentElement : diffs;
    if (box && last) {
      const top = last.getBoundingClientRect().top - box.getBoundingClientRect().top + box.scrollTop;
      box.scrollTo({ top: Math.max(0, top - box.clientHeight / 3), behavior: "smooth" });
    }
  }, [follow, fresh]);

  const short = base.slice(0, 7);
  const last = lastChange ? <LastChangeLine change={lastChange} /> : null;

  // Picking lines: a press on a "+" or a line number starts a range, moving
  // over lines of the same side and hunk stretches it, letting go anywhere ends it.
  const canStart = Boolean(commenting?.canStart);
  const drag = useRef<{ from: Point; range: DiffRange } | null>(null);
  const [dragging, setDragging] = useState(false);
  const latestCommenting = useRef(commenting);
  latestCommenting.current = commenting;
  useEffect(() => {
    if (!dragging) return;
    const up = () => {
      const d = drag.current;
      drag.current = null;
      setDragging(false);
      if (d) latestCommenting.current?.onSelectionChange(d.range, true);
    };
    window.addEventListener("mouseup", up);
    return () => window.removeEventListener("mouseup", up);
  }, [dragging]);
  const hovered = useRef<HTMLElement | null>(null);
  const setHover = (el: HTMLElement | null) => {
    if (hovered.current === el) return;
    hovered.current?.removeAttribute("data-hover");
    hovered.current = el;
    el?.setAttribute("data-hover", "");
  };
  const pointer = commenting ? {
    onMouseDown: (e: React.MouseEvent) => {
      if (!canStart || e.button !== 0) return;
      const start = (e.target as Element).closest("[data-cstart]");
      const at = start ? pointOf(start) : null;
      if (!at) return;
      e.preventDefault();
      const range = pointRange(at);
      drag.current = { from: at, range };
      setDragging(true);
      commenting.onSelectionChange(range, false);
    },
    onMouseOver: (e: React.MouseEvent) => {
      const row = (e.target as Element).closest<HTMLElement>("[data-cline]");
      setHover(canStart && !drag.current ? row : null);
      const d = drag.current;
      const at = pointOf(row, d?.from.side);
      if (!d || !at) return;
      const range = stretchRange(d.from, at, commenting.maxLines);
      if (range && (range.startLine !== d.range.startLine || range.endLine !== d.range.endLine)) {
        d.range = range;
        commenting.onSelectionChange(range, false);
      }
    },
    onMouseLeave: () => setHover(null),
  } : {};
  // The "+" by key: Enter or Space picks its line; with Shift, stretches the range picked to it.
  // One function for the diff's life (it reads the latest `commenting`), so it never re-renders a hunk.
  const pickByKey = useCallback((e: React.MouseEvent<HTMLButtonElement>) => {
    const commenting = latestCommenting.current;
    if (!commenting || e.detail !== 0) return;
    const at = pointOf(e.currentTarget);
    if (!at) return;
    const sel = commenting.selection;
    // Stretched only within the hunk the range is in: the start's line, found as the drag would find it.
    const from = sel && e.shiftKey
      ? [...(scroller.current?.querySelectorAll("[data-cline]") ?? [])].map((el) => pointOf(el, sel.side))
        .find((p) => p?.path === sel.path && p.side === sel.side && p.line === sel.startLine) ?? null
      : null;
    const range = (from && stretchRange(from, pointOf(e.currentTarget, from.side) ?? at, commenting.maxLines)) ?? pointRange(at);
    commenting.onSelectionChange(range, true);
  }, []);
  const selection = commenting?.selection ?? null;
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
          {last}
          {emptyMessage ?? "No changes yet."}
        </div>
      ) : (
        <div className={cx(styles["body"], !fileList && styles["bodyAlone"])}>
          {fileList ? (
            <nav className={styles["files"]} aria-label="Changed files">
              {last}
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
                    <span className={styles["fileTail"]}>
                      {commenting?.fileMark?.(f.path)}
                      <DiffStat className={styles["counts"]} additions={f.additions} deletions={f.deletions} />
                    </span>
                  </button>
                );
              })}
              <p className={styles["note"]}>Uncommitted work counts: this is the agent's checkout now, not what it has pushed.</p>
            </nav>
          ) : null}
          <div className={cx(styles["diffs"], commenting && styles["commentable"], dragging && styles["dragging"])} ref={scroller} data-testid="diffs" {...pointer}>
            {fileList ? null : last}
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
                {commenting?.renderFileHead ? <FileHead node={commenting.renderFileHead(f.path)} /> : null}
                {f.binary ? <div className={styles["empty"]}>Binary file not shown.</div> : null}
                {f.hunks.map((h, hi) => (
                  <Hunk key={hi} path={f.path} hunk={h} index={hi} view={view} rows={split?.get(f.path)?.[hi] ?? null} fresh={fresh.lines}
                    commentable={Boolean(commenting)} canStart={canStart} maxLines={commenting?.maxLines ?? 0}
                    selection={touches(h, f.path, selection) ? selection : null} dragging={dragging && touches(h, f.path, selection)}
                    renderAfterLine={commenting?.renderAfterLine} onPlus={pickByKey} />
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

/** Whether range `r` is on lines of hunk `h` of `path`: the one hunk a picked range can be in. */
const touches = (h: LiveDiffHunk, path: string, r: DiffRange | null) =>
  r !== null && r.path === path && h.lines.some((l) => (r.side === "old" ? l.kind !== "+" : l.kind !== "-") && inRange(r, path, r.side, numberOn(l, r.side)));

interface HunkProps {
  readonly path: string;
  readonly hunk: LiveDiffHunk;
  readonly index: number;
  readonly view: LiveDiffView;
  /** Split: the hunk's rows side by side. */
  readonly rows: ReadonlyArray<SplitRow> | null;
  readonly fresh: ReadonlySet<string>;
  readonly commentable: boolean;
  readonly canStart: boolean;
  readonly maxLines: number;
  /** The range picked, only when it is in this hunk: a drag elsewhere leaves this hunk as it is. */
  readonly selection: DiffRange | null;
  readonly dragging: boolean;
  readonly renderAfterLine: LiveDiffCommenting["renderAfterLine"];
  readonly onPlus: (e: React.MouseEvent<HTMLButtonElement>) => void;
}

/** One hunk's lines, re-rendered only when what it shows changes. */
const Hunk = memo(function Hunk({ path, hunk: h, index: hi, view, rows, fresh, commentable, canStart, maxLines, selection, dragging, renderAfterLine, onPlus }: HunkProps) {
  // A line's place, read by the diff's handlers; `data-cstart` marks where a press starts a range.
  const lineAttrs = (side: DiffSide, n: number | null, old?: number | null) =>
    commentable && n !== null ? { "data-cpath": path, "data-cside": side, "data-cline": n, "data-chunk": hi, ...(old != null ? { "data-cold": old } : {}) } : {};
  const numberAttrs = (n: number | null) => (canStart && n !== null ? { "data-cstart": "" } : {});
  const plus = (side: DiffSide, n: number | null) => canStart && n !== null ? (
    <button type="button" className={styles["plus"]} data-cstart="" data-testid="gutter-add" onClick={onPlus}
      aria-label={`Comment on line ${n}${side === "old" ? " (removed)" : ""}`}>
      <Icon name="plus" size={12} />
    </button>
  ) : null;
  const hint = (side: DiffSide, n: number | null) => dragging && selection && selection.side === side && selection.endLine === n ? (
    <div className={styles["dragHint"]} data-testid="drag-hint">
      {plural(selection.endLine - selection.startLine + 1, "line")} · let go to comment
      <span className={styles["muted"]}> · at most {maxLines}, one side</span>
    </div>
  ) : null;
  const after = (side: DiffSide, n: number | null) => {
    const node = n !== null ? renderAfterLine?.(path, side, n) : null;
    return node ? <div className={styles["after"]} data-testid="after-line" data-side={side} data-line={n ?? undefined}>{node}</div> : null;
  };
  const covered = coveredRows(h.lines, path, selection);
  return (
    <div>
      <div className={styles["hunk"]}>{h.header}</div>
      {view === "unified" || !rows
        ? h.lines.map((l, li) => {
            const isFresh = fresh.has(lineKey(path, l));
            const side = sideOfLine(l);
            const n = numberOn(l, side);
            const picked = covered !== null && li >= covered[0] && li <= covered[1];
            // A context line ends a range on either side: what goes under it is asked for both.
            const below = commentable ? (
              <>
                {hint(side, n)}
                {after(side, n)}
                {l.kind === " " ? hint("old", l.old) : null}
                {l.kind === " " ? after("old", l.old) : null}
              </>
            ) : null;
            return (
              <Fragment key={li}>
                <div
                  className={cx(styles["line"], l.kind === "+" && styles["added"], l.kind === "-" && styles["removed"], isFresh && styles["fresh"], picked && styles["picked"])}
                  data-fresh={isFresh ? "" : undefined}
                  data-picked={picked || undefined}
                  {...lineAttrs(side, n, l.kind === " " ? l.old : null)}
                >
                  <span className={styles["n"]} {...numberAttrs(n)}>{l.old ?? ""}</span>
                  <span className={styles["n"]} {...numberAttrs(n)}>{l.new ?? ""}</span>
                  <span className={styles["sign"]} aria-hidden>{SIGN[l.kind]}</span>
                  <span className={styles["text"]}>{l.text || " "}</span>
                  {plus(side, n)}
                </div>
                {below}
              </Fragment>
            );
          })
        : rows.map((row, ri) => {
            // Context sits on both sides and is never fresh: one side is enough to ask.
            const changed = row.left?.kind === " " ? null : (row.left ?? row.right);
            const isFresh = changed !== null && (fresh.has(lineKey(path, changed)) ||
              (row.right !== null && row.right !== changed && fresh.has(lineKey(path, row.right))));
            const below = (line: LiveDiffLine | null, s: DiffSide) => {
              if (!commentable || !line) return null;
              const n = numberOn(line, s);
              const hinted = hint(s, n);
              const written = after(s, n);
              return hinted || written ? <>{hinted}{written}</> : null;
            };
            const left = below(row.left, "old");
            const right = below(row.right, "new");
            const side = (line: LiveDiffLine | null, s: DiffSide) => {
              if (!line) return <SplitSide line={null} side={s} />;
              const n = numberOn(line, s);
              return <SplitSide line={line} side={s} picked={inRange(selection, path, s, n)} attrs={lineAttrs(s, n)} numberAttrs={numberAttrs(n)} plus={plus(s, n)} />;
            };
            return (
              <Fragment key={ri}>
                <div className={cx(styles["split"], isFresh && styles["fresh"])} data-fresh={isFresh ? "" : undefined}
                  data-testid="split-row">
                  {side(row.left, "old")}
                  {side(row.right, "new")}
                </div>
                {left || right ? (
                  <div className={styles["splitAfter"]}><div>{left}</div><div>{right}</div></div>
                ) : null}
              </Fragment>
            );
          })}
    </div>
  );
});

/** One side of a split row: its number, sign and text, or an empty stretch where that side has no line. */
function SplitSide({ line, side, picked, attrs, numberAttrs, plus }: {
  line: LiveDiffLine | null; side: "old" | "new"; picked?: boolean; attrs?: object; numberAttrs?: object; plus?: ReactNode;
}) {
  if (!line) return <span className={cx(styles["side"], styles["gap"])} />;
  return (
    <span className={cx(styles["side"], line.kind === "+" && styles["added"], line.kind === "-" && styles["removed"], picked && styles["picked"])}
      data-picked={picked || undefined} {...attrs}>
      <span className={styles["n"]} {...numberAttrs}>{side === "old" ? line.old : line.new}</span>
      <span className={styles["sign"]} aria-hidden>{SIGN[line.kind]}</span>
      <span className={styles["text"]}>{line.text || " "}</span>
      {plus}
    </span>
  );
}

/** What the app puts under a file's header, when it has something to put there. */
function FileHead({ node }: { node: ReactNode }) {
  return node ? <div className={styles["fileNotes"]} data-testid="file-head">{node}</div> : null;
}

function StatusGlyph({ status }: { status: LiveDiffFile["status"] }) {
  return (
    <span className={cx(styles["status"], styles[`status${status}`])} title={STATUS_WORD[status]} aria-label={STATUS_WORD[status]}>
      {status}
    </span>
  );
}
