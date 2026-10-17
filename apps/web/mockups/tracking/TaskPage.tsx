/*
 * The task page in every state the proposal adds: closed with a
 * resolution (closed-wontdo, closed-duplicate), archived, a bug with its
 * report and its cause (bug-task, introduced-by-proposed, narrow), and its
 * Activity (activity-lifecycle). The frame is the app's task screen
 * (apps/web's taskTop / taskHead / taskNotices / taskPane classes from
 * app.css); the Overview rail is PageWithRail as the app opts Overview
 * into it.
 */

import { useState, type ReactNode } from "react";
import {
  AgentAvatar, Breadcrumb, Markdown, PageRail, PageRailBlock, PageWithRail, PersonAvatar, PersonLine, RelationList, RelationRow, StartGate, StatusMark, Timeline, TimelineItem,
} from "@dude/design-system/components";
import { Icon } from "@dude/design-system";
import { Button, Callout, RowMenu, Tab, TabList, TabPanel, Tabs, Tooltip, type RowMenuItem } from "@dude/design-system/primitives";
import relStyles from "../../../../packages/design-system/src/components/Relations.module.css";
import { EPICS, FINISHED, KIND, NOT_STARTED, P, PRIORITY, RESOLUTION, ROLE, TASKS, firstName, isOwner, task, type Task } from "./data.ts";
import { ArchivedMark, IntroducedByChip, KindMark, PriorityMark, ResolutionMark } from "./Marks.tsx";
import { ArchiveDialog, CloseDialog } from "./Dialogs.tsx";
import styles from "./pages.module.css";

export type TaskView = "overview" | "activity";
export type Overlay = null | "close" | "archive" | "archive-epic";

/** Who may close and archive: the owner, or an org admin. Ana is the owner of most; Márcio is a member. */
const mayManage = (t: Task) => ROLE === "owner" && isOwner(t);
const notYours = (t: Task) => `Only ${firstName(t.owner)}, the owner, or an admin can do this`;

/** The task's "…" menu: every lifecycle action, disabled with its reason when the reader may not. */
export function lifecycleMenu(t: Task, open: (o: Overlay) => void): RowMenuItem[] {
  const may = mayManage(t);
  const why = may ? undefined : notYours(t);
  const live = t.status === "running" || t.status === "awaiting_input";
  const finished = FINISHED.has(t.status);
  return [
    { id: "edit", label: "Edit", icon: "edit" },
    { kind: "separator" },
    { id: "close", label: finished ? "Closed" : "Close…", icon: "circle-x", disabled: !may || live || finished, disabledReason: why ?? (live ? "Stop its agents first, then close it" : finished ? "Already ended" : undefined), onSelect: () => open("close") },
    t.archived
      ? { id: "unarchive", label: "Unarchive", icon: "archive", disabled: !may, disabledReason: why }
      : { id: "archive", label: "Archive…", icon: "archive", disabled: !may || !finished, disabledReason: why ?? (!finished ? (live ? "Stop its agents first, then close it" : "Close it first: only ended tasks are archived") : undefined), onSelect: () => open("archive") },
  ];
}

export interface TaskPageProps {
  readonly k: string;
  readonly view: TaskView;
  readonly onView: (v: TaskView) => void;
  readonly overlay: Overlay;
  readonly onOverlay: (o: Overlay) => void;
  readonly onOpen: (key: string) => void;
  readonly onBoard: () => void;
  /** introduced-by-proposed: Ana's conductor's proposal, not yet confirmed. */
  readonly menuOpen?: boolean | undefined;
  /** The task as it was before it was closed: the close dialog opens on that. */
  readonly beforeClose?: boolean | undefined;
}

export function TaskPage({ k, view, onView, overlay, onOverlay, onOpen, onBoard, menuOpen, beforeClose }: TaskPageProps) {
  const t0 = task(k);
  const t: Task = beforeClose ? { ...t0, status: "received", resolution: undefined, duplicateOf: undefined, blockedBy: undefined } : t0;
  const may = mayManage(t);
  const [introduced, setIntroduced] = useState(t.bug?.introducedBy?.state ?? null);
  const items = lifecycleMenu(t, onOverlay);
  const epic = t.epic ? EPICS[t.epic].title : null;
  const starts = [{ id: "talk", label: "Talk it through", icon: "message" as const }, { id: "deliver", label: "Deliver", icon: "zap" as const }];

  let actions: ReactNode = null;
  if (t.archived) actions = null;
  else if (NOT_STARTED.has(t.status) && (t.blockedBy ?? []).length) {
    actions = <StartGate blockers={(t.blockedBy ?? []).map((b) => ({ key: b, title: task(b).title, status: task(b).status }))} starts={starts} onStartAnyway={() => undefined} />;
  } else if (NOT_STARTED.has(t.status)) {
    actions = <><Button leadingIcon="message">Talk it through</Button><Button leadingIcon="zap">Deliver</Button></>;
  }

  return (
    <div className="screen taskScreen" data-testid="task-screen">
      <header className={`taskTop ${styles["top"]}`}>
        <div className="taskCrumbs">
          <Breadcrumb items={[
            { id: "p", label: "control-plane", onSelect: onBoard },
            ...(epic ? [{ id: "e", label: epic, icon: "layers" as const, onSelect: onBoard }] : []),
            { id: "k", label: t.key, mono: true },
          ]} />
        </div>
        <span className="taskTopActions">
          {actions}
          <Button variant="quiet" leadingIcon="edit">Edit</Button>
          <RowMenu label={`Actions for ${t.key}`} items={items} align="end" defaultOpen={menuOpen || (!may && overlay !== null)} />
        </span>
      </header>

      <div className="taskHead" data-testid="task-header">
        <div className="taskHeadMain">
          <h1 className="taskTitle">{t.title}</h1>
          <div className="taskMeta">
            <StatusMark status={t.status} size="sm" />
            {t.resolution ? <ResolutionMark resolution={t.resolution} of={t.duplicateOf} /> : null}
            <span className="ds-mono">{t.key}</span>
            <KindMark kind={t.kind} variant="word" />
            <PriorityMark priority={t.priority} showDefault />
            {t.archived ? <ArchivedMark /> : null}
            <span>created {Math.round(t.hoursInStatus / 24) || 1}d ago</span>
          </div>
        </div>
        <div className="taskPeople">
          <PersonLine person={t.owner} size={56} detail={isOwner(t) ? "Owner · you" : "Owner"} />
        </div>
      </div>

      <Notices t={t} may={may} onOpen={onOpen} />

      <Tabs value={view} onValueChange={(v) => onView(v as TaskView)} fill>
        <TabList aria-label="Task" className="tabsInset">
          <Tab value="chat">Chat</Tab>
          <Tab value="overview">Overview</Tab>
          <Tab value="findings">Findings</Tab>
          <Tab value="sessions">Sessions</Tab>
          <Tab value="files">Files</Tab>
          <Tab value="activity">Activity</Tab>
        </TabList>
        <TabPanel value="overview" className="railPanel">
          <PageWithRail rail={<Rail t={t} introduced={introduced} setIntroduced={setIntroduced} onOpen={onOpen} />}>
            <div className="taskPane">
              <div className="taskMain">
                <section className="taskBlock" aria-label="Goal">
                  <h2 className="ds-label">Goal</h2>
                  <div className="taskGoal"><Markdown source={GOALS[t.key] ?? GOALS["default"]!} breaks /></div>
                </section>
                {t.bug ? <BugReport t={t} introduced={introduced} setIntroduced={setIntroduced} onOpen={onOpen} /> : null}
                <div className="taskBlock"><Relations t={t} onOpen={onOpen} beforeClose={beforeClose} /></div>
              </div>
            </div>
          </PageWithRail>
        </TabPanel>
        <TabPanel value="activity" className="taskPane">
          <ActivityLines k={k} />
        </TabPanel>
      </Tabs>

      <CloseDialog open={may && overlay === "close"} onOpenChange={(o) => onOverlay(o ? "close" : null)} t={t} />
      <ArchiveDialog open={may && overlay === "archive"} onOpenChange={(o) => onOverlay(o ? "archive" : null)} t={t} />
    </div>
  );
}

const GOALS: Record<string, string> = {
  default: "What it should do and why, as its owner wrote it.",
  "CP-62": "Customers with a comma in their organisation's name get a CSV with **rows missing**. The export must quote every field (RFC 4180), and the rows for those organisations must all be there.",
  "CP-61": "On the night clocks go back, the hourly meter writes the **01:00 hour twice**, and the invoice charges it twice. Bucket by UTC and label in the organisation's zone.",
  "CP-60": "A push that arrives while control-plane deploys is acknowledged to GitHub and never processed. Nothing retries it: the push is lost.",
  "CP-46": "Show this month's usage under the organisation's name in the sidebar footer.",
  "CP-63": "Exporting usage breaks when a name contains a comma.",
  "CP-64": "Could Stripe's meter events replace our hourly job? Write up cost, latency, and what we'd lose.",
  "CP-34": "Under load the webhook endpoint returns 500s.",
};

/** What the page says before its tabs: why it ended, that it is archived. */
function Notices({ t, may, onOpen }: { readonly t: Task; readonly may: boolean; readonly onOpen: (k: string) => void }) {
  const out: ReactNode[] = [];
  if (t.archived) {
    out.push(
      <Callout key="arch" tone="neutral" data-testid="archived-callout">
        <span className={styles["callTitle"]}>Archived by {firstName(t.archived.by)}, {t.archived.ago} ago.</span>{" "}
        It is out of the board, the sidebar, the counts and Find work; its history is all here.
        <div className={styles["callActions"]}>
          {may ? <Button size="sm" variant="secondary" leadingIcon="archive" data-lifecycle-action="">Unarchive</Button>
            : <DisabledWithHint label="Unarchive" hint={`Only ${firstName(t.owner)}, the owner, or an admin can unarchive this`} />}
        </div>
      </Callout>,
    );
  }
  if (t.resolution && t.resolution !== "nothing_to_merge") {
    const r = RESOLUTION[t.resolution];
    const dup = t.duplicateOf ? task(t.duplicateOf) : null;
    out.push(
      <Callout key="closed" tone="neutral" data-testid="closed-callout">
        <span className={styles["callTitle"]}>Closed as {r.label} by {firstName(t.owner)}.</span>{" "}
        {t.resolution === "wont_do" ? "Nobody will work on it. What waited on it still waits: a task closed without merging releases nothing." : null}
        {t.resolution === "cant_reproduce" ? "Nobody could make it happen again. If it comes back, reopen it with the steps that showed it." : null}
        {dup ? (
          <>It is the same work as{" "}
            <button type="button" className={styles["linkish"]} onClick={() => onOpen(dup.key)}><span className="ds-mono">{dup.key}</span></button>
            , which stays open. What waited on {t.key} now waits on {dup.key}.</>
        ) : null}
        <div className={styles["callActions"]}>
          {may ? <Button size="sm" variant="secondary" leadingIcon="retry" data-lifecycle-action="">Reopen</Button>
            : <DisabledWithHint label="Reopen" hint={`Only ${firstName(t.owner)}, the owner, or an admin can reopen this`} />}
          {may && !t.archived ? <Button size="sm" variant="quiet" leadingIcon="archive" data-lifecycle-action="">Archive</Button> : null}
        </div>
      </Callout>,
    );
  }
  return out.length ? <div className="taskNotices">{out}</div> : null;
}

/** The README's rule: a disabled control the reader can still reach, its reason its description. */
export function DisabledWithHint({ label, hint, icon }: { readonly label: string; readonly hint: string; readonly icon?: "archive" | "clock" | "circle-x" | "retry" | undefined }) {
  const id = `h-${label.replace(/\W/g, "")}`;
  return (
    <Tooltip content={hint} side="bottom">
      <span className={styles["gateTriggerish"]} tabIndex={0} role="button" aria-disabled="true" aria-label={label} aria-describedby={id} data-lifecycle-action="">
        <span id={id} className="ds-sr-only" data-lifecycle-hint="">{hint}</span>
        <Button size="sm" variant="secondary" leadingIcon={icon} disabled style={{ pointerEvents: "none" }}>{label}</Button>
      </span>
    </Tooltip>
  );
}

function BugReport({ t, introduced, setIntroduced, onOpen }: { readonly t: Task; readonly introduced: string | null; readonly setIntroduced: (s: "proposed" | "confirmed" | null) => void; readonly onOpen: (k: string) => void }) {
  const b = t.bug!;
  return (
    <section className="taskBlock" aria-label="Bug report" data-testid="bug-report">
      <h2 className="ds-label">Bug report</h2>
      <dl className={styles["bugGrid"]}>
        <dt>Steps to reproduce</dt>
        <dd><ol>{b.steps.map((s) => <li key={s}><Markdown source={s} /></li>)}</ol></dd>
        <dt>Expected</dt><dd>{b.expected}</dd>
        <dt>Actual</dt><dd>{b.actual}</dd>
        <dt>Environment</dt><dd className="ds-mono">{b.environment}</dd>
        <dt>Introduced by</dt>
        <dd>
          {b.introducedBy && introduced ? (
            <IntroducedByChip taskKey={b.introducedBy.key} status="done" title={task(b.introducedBy.key).title} merged={b.introducedBy.merged}
              state={introduced as "proposed" | "confirmed"} by={introduced === "confirmed" && b.introducedBy.state === "proposed" ? "Ana" : b.introducedBy.by}
              onConfirm={mayManage(t) ? () => setIntroduced("confirmed") : undefined} onClear={mayManage(t) ? () => setIntroduced(null) : undefined}
              hint={mayManage(t) ? undefined : `${firstName(t.owner)}, the owner, confirms or clears it`} />
          ) : <span className={styles["muted"]}>Not known. {mayManage(t) ? <button type="button" className={styles["linkish"]}>Set it…</button> : null}</span>}
          {b.introducedBy?.state === "proposed" && introduced === "proposed" ? (
            <p className={styles["muted"]} data-testid="introduced-why">
              Why: the reproduction passes at <span className="ds-mono">a41c9e2</span> (before {b.introducedBy.key} merged) and fails at <span className="ds-mono">7d03b11</span>, its merge. The two merges either side of it (CP-29, CP-36) were checked out and run: only {b.introducedBy.key} changes the result.
              {" "}<button type="button" className={styles["linkish"]} onClick={() => onOpen(b.introducedBy!.key)}>Open {b.introducedBy.key}</button>
            </p>
          ) : null}
        </dd>
      </dl>
    </section>
  );
}

function Rail({ t, introduced }: { readonly t: Task; readonly introduced: string | null; readonly setIntroduced: (s: "proposed" | "confirmed" | null) => void; readonly onOpen: (k: string) => void }) {
  return (
    <PageRail aria-label="About this task">
      <PageRailBlock label="This task">
        <div className={styles["railFacts"]}>
          <span className={styles["railFact"]}><span>Kind</span><KindMark kind={t.kind} variant="word" /></span>
          <span className={styles["railFact"]}><span>Priority</span><span><PriorityMark priority={t.priority} showDefault /> <span className={styles["muted"]}>{PRIORITY[t.priority].line}</span></span></span>
          <span className={styles["railFact"]}><span>Status</span><StatusMark status={t.status} size="sm" /></span>
          {t.resolution ? <span className={styles["railFact"]}><span>Resolution</span><ResolutionMark resolution={t.resolution} of={t.duplicateOf} /></span> : null}
          {t.bug?.introducedBy && introduced ? <span className={styles["railFact"]}><span>Introduced by</span><span className="ds-mono">{introduced === "proposed" ? "likely " : ""}{t.bug.introducedBy.key}</span></span> : null}
          <span className={styles["railFact"]}><span>Epic</span><span>{t.epic ? EPICS[t.epic].title : "No epic"}</span></span>
        </div>
      </PageRailBlock>
      <PageRailBlock label="What the kind changes">
        <p className={styles["muted"]}>{KIND[t.kind].line}</p>
      </PageRailBlock>
      <PageRailBlock label="People">
        <PersonLine person={t.owner} size={28} detail="Owner" />
      </PageRailBlock>
    </PageRail>
  );
}

/** Relations with the proposed Duplicate of / Duplicated by group, in RelationList's own anatomy. */
function Relations({ t, onOpen, beforeClose }: { readonly t: Task; readonly onOpen: (k: string) => void; readonly beforeClose?: boolean | undefined }) {
  const rel = (k: string) => { const x = task(k); return { key: x.key, title: x.title, status: x.status, owner: x.owner }; };
  const blockedBy = (t.blockedBy ?? []).map(rel);
  // Before CP-63 was closed, CP-47 waited on it; closing it handed that to CP-62.
  const blocks = (k: string) => (beforeClose && k === "CP-63" ? ["CP-47"] : beforeClose ? [] : TASKS_BLOCKING(k)).map(rel);
  const dupOf = t.duplicateOf ? [rel(t.duplicateOf)] : [];
  const dupBy = TASKS_DUPLICATING(t.key).map(rel);
  const inherited = t.key === "CP-62" ? ["CP-47"] : [];
  return (
    <>
      {dupOf.length || dupBy.length ? (
        <section className={relStyles["relations"]} aria-label="Duplicates" data-testid="duplicate-group">
          <div className={relStyles["group"]} role="group" aria-label={dupOf.length ? "Duplicate of" : "Duplicated by"}>
            <div className={relStyles["groupHead"]}>
              <Icon name="copy" size={12} className={relStyles["glyph"]} />
              <span>{dupOf.length ? "Duplicate of" : "Duplicated by"}</span>
              <span className={relStyles["groupCount"]}>{dupOf.length + dupBy.length}</span>
            </div>
            <ul className={relStyles["rows"]}>
              {[...dupOf, ...dupBy].map((r) => <RelationRow key={r.key} group="related" row={r} hintId={`dup-${r.key}`} onOpen={onOpen} />)}
            </ul>
            {dupOf.length ? <p className={styles["muted"]}>Closed as a duplicate: what waited on {t.key} waits on {dupOf[0]!.key} instead.</p> : null}
            {inherited.length ? <p className={styles["muted"]} data-testid="inherited">CP-63's blocking came here with it: {inherited.join(", ")} now waits on {t.key}.</p> : null}
          </div>
        </section>
      ) : null}
      <RelationList groups={[{ kind: "blocked-by", rows: blockedBy }, { kind: "blocks", rows: blocks(t.key) }]} onOpen={onOpen} onAdd={() => undefined} />
    </>
  );
}

const TASKS_BLOCKING = (k: string) => TASKS.filter((x) => (x.blockedBy ?? []).includes(k)).map((x) => x.key);
const TASKS_DUPLICATING = (k: string) => TASKS.filter((x) => x.duplicateOf === k).map((x) => x.key);

// ---------------------------------------------------------------------------
// activity-lifecycle: every new line, both ways, as the Activity tab says them.
// ---------------------------------------------------------------------------

export function ActivityLines({ k }: { readonly k: string }) {
  const ana = <PersonAvatar person={P.ana} size={28} />;
  const marcio = <PersonAvatar person={P.marcio} size={28} />;
  const conductor = <PersonAvatar person={P.ana} size={28} agent="conductor" />;
  const brainstorm = <PersonAvatar person={P.ana} size={28} agent="brainstorm" />;
  const dude = <AgentAvatar role="system" size="lg" />;
  const t = task(k);
  return (
    <div data-testid="activity">
      <Timeline aria-label="Activity" wide>
        <TimelineItem who={marcio} when="2d" data-testid="line-archived">
          <b>Márcio</b> archived <b>CP-34</b> — it leaves the board, the counts and Find work
        </TimelineItem>
        <TimelineItem who={ana} when="2d" data-testid="line-unarchived">
          <b>Ana</b> unarchived <b>CP-29</b>
        </TimelineItem>
        <TimelineItem who={ana} when="5d" data-testid="line-closed-duplicate">
          <b>Ana</b> closed <b>CP-63</b> as a duplicate of <b>CP-62</b> — <b>CP-47</b>, which waited on CP-63, now waits on CP-62
        </TimelineItem>
        <TimelineItem who={ana} when="5d" data-testid="line-closed-wontdo" quote="The footer is going away in the new shell.">
          <b>Ana</b> closed <b>CP-46</b> as Won't do
        </TimelineItem>
        <TimelineItem who={ana} when="6d" data-testid="line-reopened">
          <b>Ana</b> reopened <b>CP-59</b> (was Can't reproduce)
        </TimelineItem>
        <TimelineItem who={ana} when="8d" data-testid="line-nothing-to-merge">
          <b>Ana</b> marked <b>CP-37</b> done — Nothing to merge: its write-up is the answer
        </TimelineItem>
        <TimelineItem who={ana} when="8d" data-testid="line-kind">
          <b>Ana</b> changed <b>CP-61</b> from Feature to Bug, and its priority from P2 to P1
        </TimelineItem>
        <TimelineItem who={conductor} when="9d" data-testid="line-introduced-proposed">
          <b>Ana's conductor</b> proposed that <b>CP-30</b> introduced <b>CP-60</b>: the reproduction passes before its merge and fails after
        </TimelineItem>
        <TimelineItem who={ana} when="9d" data-testid="line-introduced-confirmed">
          <b>Ana</b> confirmed <b>CP-40</b> introduced <b>CP-61</b>
        </TimelineItem>
        <TimelineItem who={brainstorm} when="10d" data-testid="line-filed">
          <b>Ana</b> filed <b>CP-60</b> as a P0 bug, as <b>Ana's Brainstorm</b> proposed
        </TimelineItem>
      </Timeline>
      <p className={styles["muted"]} style={{ marginTop: 16 }}>Every new line at once, for reading; on a real task each line sits on the task it is about ({t.key}'s own are a subset).</p>
    </div>
  );
}
