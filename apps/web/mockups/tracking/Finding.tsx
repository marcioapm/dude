/*
 * find-work-tokens: the sidebar's one search, reading tokens and words.
 * Drawn in the Sidebar's own place (its `sessions` slot sits just under
 * the search), standing in for a proposed `Sidebar search` results mode:
 * the field shows each token it understood as a chip, then the results
 * in the tree's row grammar, archived and Later ones marked and last.
 *
 * meaning: every new word, in plain sentences.
 */

import { Fragment } from "react";
import { Icon } from "@dude/design-system";
import { RefLead } from "@dude/design-system/components";
import { KIND, PRIORITY, RESOLUTION, TASKS, laterOf, onShelf, type Kind, type Priority, type Resolution } from "./data.ts";
import { ArchivedMark, KindMark, LaterMark, PriorityMark, ResolutionMark } from "./Marks.tsx";
import styles from "./pages.module.css";

export interface ParsedQuery {
  readonly tokens: ReadonlyArray<{ readonly raw: string; readonly said: string }>;
  readonly words: string;
}

/** What Find work reads: `kind:`, `p0`…`p3`, `owner:`, `#KEY`, `is:later|archived|closed|open|blocked`, `epic:"…"`, then words. */
export function parse(q: string): ParsedQuery {
  const tokens: { raw: string; said: string }[] = [];
  const words: string[] = [];
  for (const part of q.match(/"[^"]*"|\S+/g) ?? []) {
    const p = part.toLowerCase();
    if (/^kind:(bug|feature|chore|spike)$/.test(p)) tokens.push({ raw: part, said: `${KIND[p.slice(5) as Kind].plural}` });
    else if (/^p[0-3]$/.test(p)) tokens.push({ raw: part, said: `priority ${p.toUpperCase()}` });
    else if (/^owner:\w+$/.test(p)) tokens.push({ raw: part, said: `owned by ${p.slice(6)}` });
    else if (/^#[a-z]+-\d+$/.test(p)) tokens.push({ raw: part, said: `the task ${part.slice(1).toUpperCase()}` });
    else if (/^is:(later|archived|closed|open|blocked|mine)$/.test(p)) tokens.push({ raw: part, said: p.slice(3) });
    else words.push(part);
  }
  return { tokens, words: words.join(" ") };
}

export const FIND_QUERY = "kind:bug owner:ana is:later csv";

export function FindWorkPane() {
  const parsed = parse(FIND_QUERY);
  const bugs = TASKS.filter((t) => t.kind === "bug" && t.key !== "CP-62");
  const open = bugs.filter((t) => !t.archived && !onShelf(t) && !t.resolution);
  const closed = bugs.filter((t) => t.resolution && !t.archived);
  const archived = bugs.filter((t) => t.archived);
  const Row = ({ k, sel }: { readonly k: string; readonly sel?: boolean }) => {
    const t = TASKS.find((x) => x.key === k)!;
    const l = laterOf(t);
    return (
      <li aria-selected={sel || undefined} data-dim={t.archived || onShelf(t) || t.resolution ? "" : undefined} role="option">
        <RefLead type="task" taskKey={t.key} status={t.status} />
        <KindMark kind={t.kind} quietDefault />
        <span className={styles["findTitle"]}>{t.title}</span>
        <PriorityMark priority={t.priority} />
        {t.resolution ? <ResolutionMark resolution={t.resolution} of={t.duplicateOf} /> : null}
        {l && !t.archived ? <LaterMark until={l.until} compact /> : null}
        {t.archived ? <ArchivedMark /> : null}
      </li>
    );
  };
  return (
    <div className={styles["findPane"]} data-testid="find-work">
      <div className={styles["findField"]} aria-label="Read as">
        {parsed.tokens.map((t) => <span key={t.raw} className={styles["token"]} title={t.said}>{t.raw}</span>)}
        <span className={styles["findWords"]}>“{parsed.words}”</span>
      </div>
      <span className={styles["findSaid"]} aria-live="polite">
        Bugs · owned by Ana · on Later · with “csv”: 1 match. Esc clears.
      </span>
      <ul className={styles["findResults"]} role="listbox" aria-label="Matches">
        <Row k="CP-62" sel />
      </ul>
      <span className={`ds-label ${styles["findGroup"]}`}>Ana's other bugs, without is:later · {open.length + closed.length + archived.length}</span>
      <ul className={styles["findResults"]} role="listbox" aria-label="Without is:later">
        {open.map((t) => <Row key={t.key} k={t.key} />)}
        {closed.map((t) => <Row key={t.key} k={t.key} />)}
        {archived.map((t) => <Row key={t.key} k={t.key} />)}
      </ul>
      <div className={styles["findHelp"]}>
        <span><code>kind:bug</code></span><span><code>p0</code>…<code>p3</code></span><span><code>owner:ana</code> <code>is:mine</code></span>
        <span><code>#CP-41</code></span><span><code>is:later</code> <code>is:archived</code> <code>is:closed</code></span><span>words: key, title, goal</span>
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// meaning
// ---------------------------------------------------------------------------

export function MeaningPage() {
  return (
    <div className={styles["doc"]} data-testid="meaning">
      <div className={styles["docInner"]}>
        <h1>Tracking work: what the words mean</h1>
        <p className={styles["lede"]}>
          dude already knows what delivery did to a task: it ran, it merged, it failed. These three pieces let a person say
          what they decided about it, what kind of work it is, and find it again later.
        </p>
        <div className={styles["epics3"]}>
          <section className={styles["epicBox"]}><h3>Lifecycle</h3><p>Every task gets a way out. Put it off (<b>Later</b>), end it with a reason (<b>Close</b>), or take what has ended out of the way (<b>Archive</b>).</p></section>
          <section className={styles["epicBox"]}><h3>Classification</h3><p>A task is a <b>bug</b>, a <b>feature</b>, a <b>chore</b> or a <b>spike</b>, and has a <b>priority</b>. The kind changes what the agents are told and what review checks.</p></section>
          <section className={styles["epicBox"]}><h3>Finding things</h3><p>A <b>list</b> beside the board with fixed views, filters on the fields above, and <b>Find work</b> that reads <code>kind:bug p0</code> as well as words.</p></section>
        </div>

        <h2>Lifecycle</h2>
        <dl className={styles["words"]}>
          <dt><Icon name="clock" size={14} /> Later</dt><dd>A shelf for work that should not start yet, until a date or with no date. A task on it keeps its status; nothing can start it, not a person, not an agent. It is not a status, and not a lane.</dd>
          <dt><Icon name="arrow-up" size={14} /> Bring it back</dt><dd>Take a task (or an epic) off the shelf now. It goes back to Backlog. It does not start it.</dd>
          <dt><Icon name="circle-x" size={14} /> Close</dt><dd>A person ends a task that never ran or that stopped, saying why. The why is its <b>resolution</b>. <b>Reopen</b> undoes it.</dd>
          <dt>Resolution</dt><dd>
            {(Object.keys(RESOLUTION) as Resolution[]).map((r) => <span key={r} style={{ display: "block" }}><ResolutionMark resolution={r} /> {RESOLUTION[r].description}</span>)}
          </dd>
          <dt><Icon name="archive" size={14} /> Archive</dt><dd>Take an ended task, or an epic whose tasks have all ended, out of the board, the sidebar, the counts and Find work. A link still opens it; the Archived view lists it; <b>Unarchive</b> puts it back. Nothing is deleted.</dd>
        </dl>

        <h2>Classification</h2>
        <dl className={styles["words"]}>
          {(Object.keys(KIND) as Kind[]).map((k) => <Fragment key={k}><dt><KindMark kind={k} variant="word" /></dt><dd>{KIND[k].line}</dd></Fragment>)}
          <dt>Priority</dt><dd>{(Object.keys(PRIORITY) as Priority[]).map((p) => <span key={p} style={{ display: "block" }}><PriorityMark priority={p} showDefault /> {PRIORITY[p].line}</span>)}</dd>
          <dt>Bug report</dt><dd>On a bug: steps to reproduce, what was expected, what happened, and where (a version, an environment).</dd>
          <dt>Introduced by</dt><dd>The task that caused a bug. An agent may propose one from what merged when (“Likely CP-30 · merged 2 Oct”); only the owner makes it a fact.</dd>
          <dt>No labels</dt><dd>Kind, priority and epic say what a label would. Free labels drift apart and nobody cleans them up.</dd>
        </dl>

        <h2>Finding things</h2>
        <dl className={styles["words"]}>
          <dt>List</dt><dd>A project's tasks as rows, beside the board. One click between them.</dd>
          <dt>Views</dt><dd>Open, Mine, Bugs, Needs you, Later, Closed, Archived. Fixed: everyone's Bugs view is the same.</dd>
          <dt>Find work</dt><dd>The sidebar's search. Words find keys, titles and goals; tokens narrow it. Archived and Later results come last, marked.</dd>
        </dl>
      </div>
    </div>
  );
}
