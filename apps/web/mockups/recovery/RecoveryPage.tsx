/*
 * Picking a stopped task back up, as proposed: the task page says how it
 * stopped and offers three ways on — Resume the session that stopped,
 * Try again with a fresh agent on the same branch, or Start over as a new
 * attempt — and one dialog says what each keeps and what it throws away.
 *
 * Composed from the app's own pieces (the task page's header and notices,
 * StepList, SessionList, the RunEnded strip, Dialog, Timeline). The one
 * new piece is the choice list in the dialog (`ChoiceList`): a radio group
 * whose options carry a sentence each, which the design system lacks.
 */

import { useState, type ReactNode } from "react";
import {
  AgentAvatar,
  Breadcrumb,
  Duration,
  Markdown,
  PersonAvatar,
  PersonLine,
  Segmented,
  SessionFacts,
  SessionItem,
  SessionList,
  SessionRail,
  SessionRailBlock,
  StatusMark,
  StepList,
  StepRow,
  ChatMessage,
  ChatNotice,
  ChatTranscript,
  Timeline,
  TimelineItem,
} from "@dude/design-system/components";
import { Button, Callout, Dialog, Tab, TabList, Tabs, Textarea, Tooltip } from "@dude/design-system/primitives";
import { Icon, cx } from "@dude/design-system";
import { DudeMark } from "../../src/DudeMark.tsx";
import { LAST_TURNS, LEFT, P, STOPPED, TASK, afterResume, afterRetry, afterStartOver, ago, firstAttempt, type MockRun, type Stop } from "./data.ts";
import styles from "./recovery.module.css";

export type Way = "resume" | "retry" | "restart";
export type Screen = "meaning" | "task" | "session" | "after" | "history";

const LABEL: Record<MockRun["phase"], string> = { implement: "Implement", review: "Review", fix: "Fix", simplify: "Simplify" };
const money = (n: number) => `$${n.toFixed(2)}`;

// --------------------------------------------------------------------------- the shell of a task page

function TaskFrame({ status, notice, actions, tab, children }: {
  readonly status: "aborted" | "failed" | "running";
  readonly notice?: ReactNode;
  readonly actions?: ReactNode;
  readonly tab: "overview" | "sessions" | "activity";
  readonly children: ReactNode;
}) {
  return (
    <div className={cx("screen taskScreen", tab === "sessions" && "fixed")}>
      <header className="taskTop">
        <div className="taskCrumbs">
          <Breadcrumb items={[
            { id: "p", label: TASK.project },
            { id: "e", label: TASK.epic, icon: "layers" },
            { id: "t", label: TASK.key, mono: true },
          ]} />
        </div>
        <span className="taskTopActions">
          {actions}
          <Button variant="quiet" leadingIcon="edit">Edit</Button>
        </span>
      </header>
      <div className="taskHead">
        <div className="taskHeadMain">
          <h1 className="taskTitle">{TASK.title}</h1>
          <div className="taskMeta">
            <StatusMark status={status} size="sm" />
            <span className="ds-mono">{TASK.key}</span>
            <span className="ds-mono">{TASK.branch}</span>
            <span>created <Duration ms={3 * 3_600_000} format="age" tone="muted" /> ago</span>
          </div>
        </div>
        <div className="taskPeople">
          <PersonLine person={P["marcio"]!} size={56} {...(status === "running" ? { agent: "implementer" as const, live: true } : {})}
            detail={status === "running" ? "Owner · Márcio's implementer is working" : "Owner"} />
        </div>
      </div>
      {notice ? <div className="taskNotices">{notice}</div> : null}
      <Tabs value={tab} fill>
        <TabList aria-label="Task" className="tabsInset">
          <Tab value="overview">Overview</Tab>
          <Tab value="findings">Findings</Tab>
          <Tab value="sessions" count={undefined}>Sessions</Tab>
          <Tab value="files">Files</Tab>
          <Tab value="activity">Activity</Tab>
        </TabList>
        {children}
      </Tabs>
    </div>
  );
}

// --------------------------------------------------------------------------- the notice on a stopped task

/**
 * In the escalation's place (and its grammar): how it stopped, what it left,
 * and the three ways on. The first is the one that fits how it stopped.
 */
export function StoppedNotice({ stop, expired, onChoose, you = true }: {
  readonly stop: Stop;
  readonly expired: boolean;
  readonly onChoose: (w: Way) => void;
  readonly you?: boolean;
}) {
  const s = STOPPED[stop];
  return (
    <Callout tone={stop === "failed" ? "danger" : "attention"} data-testid="stopped">
      <div className="escalation">
        <p>
          {stop === "aborted"
            ? <><strong>Ana aborted the implementer</strong> <Duration ms={95 * 60_000} format="age" tone="muted" /> ago: “{s.why}”</>
            : <><strong>The implementer failed</strong> <Duration ms={95 * 60_000} format="age" tone="muted" /> ago: {s.why}.</>}{" "}
          <Button size="sm" variant="quiet" trailingIcon="arrow-right">Open Implement</Button>
        </p>
        <p className={styles["left"]}>
          It pushed <span className="ds-mono">{LEFT.pushed.sha}</span> ({LEFT.pushed.files} files, <span className={styles["add"]}>+{LEFT.pushed.additions}</span> <span className={styles["del"]}>−{LEFT.pushed.deletions}</span>) to <span className="ds-mono">{TASK.branch}</span>.{" "}
          {expired
            ? <>Its workspace and conversation are gone: lux keeps a stopped session {LEFT.keptFor}.</>
            : <>Its workspace (with {LEFT.unpushed.files} files it had not pushed) and its conversation are kept until {LEFT.keptUntil}.</>}
        </p>
        {you ? (
          <div className="escalationActions">
            {expired ? null : <Button size="sm" variant="primary" leadingIcon="play" onClick={() => onChoose("resume")}>Resume…</Button>}
            <Button size="sm" variant={expired ? "primary" : "secondary"} leadingIcon="retry" onClick={() => onChoose("retry")}>Try again…</Button>
            <Button size="sm" variant="secondary" leadingIcon="git-branch" onClick={() => onChoose("restart")}>Start over…</Button>
          </div>
        ) : (
          <p className="escalationWaiting">Only Márcio, its owner, can pick it back up. Take over the task to do it yourself.</p>
        )}
      </div>
    </Callout>
  );
}

// --------------------------------------------------------------------------- the dialog

/** The one new piece: a radio group whose options each carry a sentence. */
function ChoiceList<T extends string>({ value, onChange, label, options }: {
  readonly value: T;
  readonly onChange: (v: T) => void;
  readonly label: string;
  readonly options: ReadonlyArray<{ value: T; title: ReactNode; detail: ReactNode; icon: Parameters<typeof Icon>[0]["name"]; disabled?: string | undefined }>;
}) {
  return (
    <div role="radiogroup" aria-label={label} className={styles["choices"]}>
      {options.map((o) => {
        const row = (
          <button key={o.value} type="button" role="radio" aria-checked={o.value === value} disabled={Boolean(o.disabled)}
            className={cx(styles["choice"], o.value === value && styles["chosen"])} onClick={() => onChange(o.value)}>
            <span className={styles["dot"]} aria-hidden />
            <span className={styles["choiceIcon"]}><Icon name={o.icon} size={16} /></span>
            <span className={styles["choiceText"]}>
              <span className={styles["choiceTitle"]}>{o.title}</span>
              <span className={styles["choiceDetail"]}>{o.disabled ?? o.detail}</span>
            </span>
          </button>
        );
        return row;
      })}
    </div>
  );
}

/** Kept, new, gone: what one way does to each thing the task has. */
const EFFECTS: Record<Way, ReadonlyArray<{ what: string; how: "kept" | "new" | "gone"; text: ReactNode }>> = {
  resume: [
    { what: "Conversation", how: "kept", text: "The same agent goes on: everything it read and decided is still in its context. Your note is its next message." },
    { what: "Workspace", how: "kept", text: <>Its checkout as it stopped, with the {LEFT.unpushed.files} files it had not pushed, on a new machine.</> },
    { what: "Branch", how: "kept", text: <><span className="ds-mono">{TASK.branch}</span>, from <span className="ds-mono">{LEFT.pushed.sha}</span></> },
    { what: "Session", how: "kept", text: "Implement, the same session: its transcript carries on below where it stopped." },
  ],
  retry: [
    { what: "Conversation", how: "new", text: "A fresh implementer. It is told the task, the decisions so far, why the last one stopped, and your note." },
    { what: "Workspace", how: "gone", text: <>It starts from what was pushed, <span className="ds-mono">{LEFT.pushed.sha}</span>; the {LEFT.unpushed.files} files the last one had not pushed are not carried over.</> },
    { what: "Branch", how: "kept", text: <><span className="ds-mono">{TASK.branch}</span>, and any pull request on it</> },
    { what: "Session", how: "new", text: "A new Implement session in this attempt; the stopped one stays to read." },
  ],
  restart: [
    { what: "Conversation", how: "new", text: "A fresh implementer, told only the task as it is now — edit it first if what it asks for was the problem." },
    { what: "Workspace", how: "new", text: "A clean checkout of main." },
    { what: "Branch", how: "new", text: <><span className="ds-mono">dude/wi_2408/attempt-2</span>, from main. Attempt 1's branch is kept; a pull request on it is closed.</> },
    { what: "Session", how: "new", text: "Attempt 2, from Implement: the whole pipeline again. Attempt 1's sessions stay, marked as such." },
  ],
};
const HOW_ICON = { kept: "check", new: "plus", gone: "minus" } as const;

export function PickUpDialog({ stop, expired, way, onWay, onClose, onDone }: {
  readonly stop: Stop;
  readonly expired: boolean;
  readonly way: Way;
  readonly onWay: (w: Way) => void;
  readonly onClose: () => void;
  readonly onDone: (w: Way) => void;
}) {
  const [note, setNote] = useState(stop === "aborted" ? "Don't touch the runner protocol. Read tool boundaries from the ACP events the shim already gets." : "");
  const go: Record<Way, string> = { resume: "Resume implementer", retry: "Try again", restart: "Start attempt 2" };
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} size="md" title="Pick WI-2408 back up"
      description={stop === "aborted" ? "Ana aborted its implementer. The task goes back to running." : "Its implementer failed. The task goes back to running."}
      footer={
        <>
          <Button variant="quiet" onClick={onClose}>Cancel</Button>
          <Button variant="primary" onClick={() => onDone(way)}>{go[way]}</Button>
        </>
      }>
      <div className={styles["dialogBody"]}>
        <ChoiceList label="How" value={way} onChange={onWay} options={[
          {
            value: "resume", icon: "play", title: "Resume the implementer",
            detail: "The same agent picks up where it stopped, with its conversation and unpushed work.",
            disabled: expired ? `Not any more: lux kept its workspace and conversation ${LEFT.keptFor}.` : undefined,
          },
          { value: "retry", icon: "retry", title: "Try again with a new implementer", detail: "Same branch, from what was pushed. Nothing it was thinking comes along." },
          { value: "restart", icon: "git-branch", title: "Start over as attempt 2", detail: "A new branch from main and the whole pipeline again." },
        ]} />

        <ul className={styles["effects"]} aria-label="What it does">
          {EFFECTS[way].map((e) => (
            <li key={e.what} className={styles["effect"]} data-how={e.how}>
              <span className={styles["effectWhat"]}>{e.what}</span>
              <span className={styles["effectHow"]}><Icon name={HOW_ICON[e.how]} size={12} />{e.how}</span>
              <span className={styles["effectText"]}>{e.text}</span>
            </li>
          ))}
        </ul>

        {way === "restart" ? (
          <Callout tone="info">
            What the task asks for can be edited before attempt 2 starts. <Button size="sm" variant="quiet" leadingIcon="edit">Edit the task first</Button>
          </Callout>
        ) : null}

        <Textarea label={way === "resume" ? "Tell the implementer (optional)" : "A note for the agents (optional)"} rows={3} value={note} onChange={(e) => setNote(e.target.value)}
          hint={way === "resume" ? "Its next message, from you. Leave it empty and it is told to go on where it left off." : "Kept with the task: every agent from here on is told it."} />
      </div>
    </Dialog>
  );
}

// --------------------------------------------------------------------------- pipeline and sessions

function Pipeline({ runs, stop = "aborted", earlierOpen = false, onEarlierOpen }: {
  readonly runs: readonly MockRun[];
  readonly stop?: Stop;
  readonly earlierOpen?: boolean;
  readonly onEarlierOpen?: (open: boolean) => void;
}) {
  const attempt = Math.max(...runs.map((r) => r.attempt));
  const current = runs.filter((r) => r.attempt === attempt).sort((a, b) => a.startedAt - b.startedAt);
  const earlier = runs.filter((r) => r.attempt < attempt);
  return (
    <section className="taskBlock" aria-label="Pipeline">
      <h2 className="ds-label">Pipeline{attempt > 1 ? ` · attempt ${attempt}` : ""}</h2>
      <StepList>
        {current.map((r) => <Step key={r.id} run={r} />)}
      </StepList>
      {earlier.length === 0 ? null : earlierOpen ? (
        <EarlierAttempt runs={earlier} stop={stop} onFold={() => onEarlierOpen?.(false)} />
      ) : (
        <p className={styles["earlier"]}>
          <Icon name="layers" size={12} /> Attempt 1 stopped at Implement ({STOPPED[stop].short.toLowerCase()}).{" "}
          <Button size="sm" variant="quiet" trailingIcon="chevron-down" onClick={() => onEarlierOpen?.(true)}>Show attempt 1</Button>
        </p>
      )}
    </section>
  );
}

/**
 * An earlier attempt, folded open under the current one: its steps as they
 * ended, why it stopped, and what it left — read-only, every row still
 * opening its session. Nothing here is offered to resume: the attempt was
 * set aside on purpose.
 */
function EarlierAttempt({ runs, stop, onFold }: { readonly runs: readonly MockRun[]; readonly stop: Stop; readonly onFold: () => void }) {
  const s = STOPPED[stop];
  const spent = runs.reduce((n, r) => n + r.costUsd, 0);
  return (
    <div className={styles["attempt"]} aria-label="Attempt 1">
      <div className={styles["attemptHead"]}>
        <Icon name="layers" size={14} />
        <span className={styles["attemptTitle"]}>Attempt 1</span>
        <StatusMark status={stop} size="sm" />
        <span className={styles["muted"]}>
          <Duration ms={142 * 60_000} format="age" tone="muted" /> ago · ran <Duration since={runs.at(-1)!.startedAt} until={runs[0]!.endedAt} tone="muted" /> · {money(spent)}
        </span>
        <span className={styles["spacer"]} />
        <Button size="sm" variant="quiet" trailingIcon="chevron-up" onClick={onFold}>Hide</Button>
      </div>
      <StepList>
        {[...runs].sort((a, b) => a.startedAt - b.startedAt).map((r) => <Step key={r.id} run={r} />)}
      </StepList>
      <dl className={styles["attemptFacts"]}>
        <dt>Stopped</dt>
        <dd>
          {s.by ? <><b>{s.by.name}</b> aborted the implementer: “{s.why}”</> : <>The implementer failed: {s.why}.</>}
        </dd>
        <dt>Set aside</dt>
        <dd>
          <b>Márcio Martins</b> started over <Duration ms={60_000} format="age" tone="muted" /> ago:
          “Don't touch the runner protocol. Read tool boundaries from the ACP events the shim already gets.”
        </dd>
        <dt>Branch</dt>
        <dd>
          <span className="ds-mono">{TASK.branch}</span> at <span className="ds-mono">{LEFT.pushed.sha}</span> — kept on GitHub
          ({LEFT.pushed.files} files, <span className={styles["add"]}>+{LEFT.pushed.additions}</span> <span className={styles["del"]}>−{LEFT.pushed.deletions}</span>).{" "}
          <Button size="sm" variant="quiet" leadingIcon="file-diff">Compare with attempt 2</Button>
        </dd>
        <dt>Not pushed</dt>
        <dd>{LEFT.unpushed.files} files in its workspace, kept by lux until {LEFT.keptUntil}, then gone.</dd>
        <dt>Pull request</dt>
        <dd className={styles["muted"]}>None — it stopped before one was opened. (One would show here, closed.)</dd>
        <dt>Findings · files</dt>
        <dd className={styles["muted"]}>None — on the Findings and Files tabs, marked attempt 1, when there are.</dd>
      </dl>
    </div>
  );
}

function Step({ run }: { readonly run: MockRun }) {
  const running = run.status === "running";
  return (
    <StepRow onOpen={() => undefined}
      avatar={<AgentAvatar role={run.role} size="lg" live={running} />}
      label={LABEL[run.phase]}
      note={run.note ?? (run.status === "aborted" ? "aborted by Ana" : run.status === "failed" ? "host lost" : undefined)}
      status={<StatusMark status={run.status} size="sm" iconOnly={run.status === "completed"} />}
      meta={run.head}
      duration={<Duration since={run.startedAt} until={run.endedAt} live={running} tone="muted" />} />
  );
}

function Sessions({ runs, open, onOpen }: { readonly runs: readonly MockRun[]; readonly open: string; readonly onOpen: (id: string) => void }) {
  // With more than one attempt, each is a group under its own heading, newest first.
  const attempts = [...new Set(runs.map((r) => r.attempt))].sort((a, b) => b - a);
  const item = (r: MockRun) => (
    <SessionItem key={r.id} current={r.id === open} onOpen={() => onOpen(r.id)}
      avatar={<AgentAvatar role={r.role} size="lg" live={r.status === "running"} />}
      title={LABEL[r.phase] + (r.note?.startsWith("again") ? " · again" : "")}
      detail={<>{r.model} · <Duration since={r.startedAt} until={r.endedAt} live={r.status === "running"} tone="muted" /></>}
      trailing={<StatusMark status={r.status} size="sm" iconOnly={r.status === "completed"} />} />
  );
  if (attempts.length === 1) return <SessionList className="taskSessionList">{runs.map(item)}</SessionList>;
  return (
    <div className="taskSessionList">
      {attempts.map((a, i) => (
        <section key={a} aria-label={`Attempt ${a}`}>
          <h3 className={styles["groupHead"]}>
            Attempt {a}
            <span className={styles["groupNote"]}>{i === 0 ? "current" : "set aside"}</span>
          </h3>
          <SessionList>{runs.filter((r) => r.attempt === a).map(item)}</SessionList>
        </section>
      ))}
    </div>
  );
}

// --------------------------------------------------------------------------- a stopped session, and one resumed

function Session({ run, stop, resumed, expired, onChoose, setAside }: {
  readonly run: MockRun;
  readonly stop: Stop;
  readonly resumed: boolean;
  readonly expired: boolean;
  readonly onChoose: (w: Way) => void;
  /** An earlier attempt's session, read after Start over: nothing is offered on it. */
  readonly setAside?: "retry" | "restart" | undefined;
}) {
  const s = STOPPED[stop];
  const live = run.status === "running";
  const ended = !live;
  return (
    <div className="runScreen" data-view="chat">
      <div className="runView">
        <div className="runChat">
          <ChatTranscript fill live={live}
            session={{ id: run.id, role: run.role, status: run.status, model: run.model, taskKey: TASK.key, title: LABEL[run.phase], owner: P["marcio"], startedAt: run.startedAt, endedAt: run.endedAt, costUsd: run.costUsd }}
            headerActions={live ? <><Button size="sm" variant="secondary">Pause</Button><Button size="sm" variant="danger">Abort…</Button></> : undefined}
            footer={setAside ? (
              <Callout tone="neutral">
                <span className="runEnded">
                  <span>
                    {setAside === "restart" ? "Attempt 1 · set aside when Márcio started over." : "Set aside when Márcio tried again with a new implementer."}{" "}
                    <span className={styles["muted"]}>Kept to read; the work goes on in {setAside === "restart" ? "attempt 2's sessions" : "Implement · again"}.</span>
                  </span>
                  <Button size="sm" variant="quiet" trailingIcon="arrow-right">{setAside === "restart" ? "Go to attempt 2" : "Go to the new session"}</Button>
                </span>
              </Callout>
            ) : ended ? (
              <Callout tone={stop === "failed" ? "danger" : "attention"}>
                <span className="runEnded">
                  <span>{stop === "aborted" ? "This run was aborted." : "This run failed."} {expired ? <span className={styles["muted"]}>Its workspace is gone; try again from the task.</span> : <span className={styles["muted"]}>Kept until {LEFT.keptUntil}.</span>}</span>
                  <span className={styles["endActions"]}>
                    {expired ? null : <Button size="sm" variant="primary" leadingIcon="play" onClick={() => onChoose("resume")}>Resume…</Button>}
                    <Button size="sm" variant="quiet" onClick={() => onChoose("retry")}>Other ways…</Button>
                  </span>
                </span>
              </Callout>
            ) : (
              <div className={styles["fakeComposer"]}>Steer the implementer…</div>
            )}>
            <ChatMessage role="implementer" model={run.model} content={LAST_TURNS[0]!.content} startedAt={LAST_TURNS[0]!.at} activity="completed" />
            <ChatMessage role="implementer" model={run.model} content={LAST_TURNS[1]!.content} startedAt={LAST_TURNS[1]!.at} activity="completed" />
            <Callout tone={stop === "failed" ? "danger" : "neutral"}>
              {stop === "aborted" ? `Aborted by Ana: ${s.why}` : `Failed: ${s.why}`}
            </Callout>
            {resumed ? (
              <>
                <ChatNotice kind="unparked" text="Márcio resumed it on lux-pool-a-07 · the workspace as it stopped" at={ago(1)} />
                <ChatMessage role="human" name="Márcio" intent="steer" content="Don't touch the runner protocol. Read tool boundaries from the ACP events the shim already gets." startedAt={ago(1)} deliveredAt={ago(1)} read />
                <ChatMessage role="implementer" model={run.model} content="Understood — dropping the `tool_boundary` frame. The shim already sees `tool_call` / `tool_call_update`; I'll track the open call there and stop on its completion." startedAt={ago(0.5)} streaming activity="streaming" />
              </>
            ) : null}
          </ChatTranscript>
          <SessionRail className="runRail" aria-label="This session">
            <SessionRailBlock label="Session">
              <SessionFacts facts={[
                { label: "Model", value: run.model, mono: true },
                { label: "Agent", value: "opencode" },
                { label: "Attempt", value: run.attempt },
                ...(setAside ? [{ label: "Set aside", value: setAside === "restart" ? "for attempt 2" : "for a new session" }] : ended ? [{ label: "Kept until", value: expired ? "gone" : LEFT.keptUntil }] : [{ label: "Resumed", value: "once" }]),
              ]} />
            </SessionRailBlock>
            <SessionRailBlock label="Left behind">
              <SessionFacts facts={[
                { label: "Pushed", value: LEFT.pushed.sha, mono: true },
                { label: "Not pushed", value: `${LEFT.unpushed.files} files` },
              ]} />
            </SessionRailBlock>
          </SessionRail>
        </div>
      </div>
    </div>
  );
}

// --------------------------------------------------------------------------- the screens

export function StoppedTask({ stop, expired, you, onChoose }: { readonly stop: Stop; readonly expired: boolean; readonly you: boolean; readonly onChoose: (w: Way) => void }) {
  return (
    <TaskFrame status={stop} tab="overview" notice={<StoppedNotice stop={stop} expired={expired} onChoose={onChoose} you={you} />}>
      <div className="taskPane">
        <div className="taskOverview">
          <div className="taskMain">
            <section className="taskBlock" aria-label="Goal">
              <h2 className="ds-label">Goal</h2>
              <div className="taskGoal"><Markdown source={TASK.goal} /></div>
            </section>
            <section className="taskBlock" aria-label="Acceptance criteria">
              <h2 className="ds-label">Acceptance criteria</h2>
              <ul className="taskGoal taskCriteria">{TASK.criteria.map((c) => <li key={c}><Markdown source={c} /></li>)}</ul>
            </section>
            <Pipeline runs={firstAttempt(stop)} />
          </div>
        </div>
      </div>
    </TaskFrame>
  );
}

export function StoppedSession({ stop, expired, onChoose }: { readonly stop: Stop; readonly expired: boolean; readonly onChoose: (w: Way) => void }) {
  const runs = firstAttempt(stop);
  return (
    <TaskFrame status={stop} tab="sessions" notice={<StoppedNotice stop={stop} expired={expired} onChoose={onChoose} />}>
      <div className="taskSessions">
        <Sessions runs={runs} open={runs[0]!.id} onOpen={() => undefined} />
        <Session run={runs[0]!} stop={stop} resumed={false} expired={expired} onChoose={onChoose} />
      </div>
    </TaskFrame>
  );
}

export function After({ stop, way, tab, earlierOpen, onEarlierOpen, openOld, onOpenOld }: {
  readonly stop: Stop;
  readonly way: Way;
  readonly tab: "overview" | "sessions" | "activity";
  readonly earlierOpen: boolean;
  readonly onEarlierOpen: (open: boolean) => void;
  /** Sessions: the old attempt's session open, rather than the running one. */
  readonly openOld: boolean;
  readonly onOpenOld: (old: boolean) => void;
}) {
  const runs = way === "resume" ? afterResume(stop) : way === "retry" ? afterRetry(stop) : afterStartOver(stop);
  const old = runs.length > 1 ? runs[runs.length - 1]! : null;
  const showOld = openOld && old !== null;
  const open = showOld ? old : runs[0]!;
  const what = way === "resume" ? "resumed the implementer" : way === "retry" ? "tried again with a new implementer" : "started over as attempt 2";
  return (
    <TaskFrame status="running" tab={tab}>
      {tab === "overview" ? (
        <div className="taskPane">
          <div className="taskOverview"><div className="taskMain"><Pipeline runs={runs} stop={stop} earlierOpen={earlierOpen} onEarlierOpen={onEarlierOpen} /></div></div>
        </div>
      ) : tab === "sessions" ? (
        <div className="taskSessions">
          <Sessions runs={runs} open={open.id} onOpen={(id) => onOpenOld(id === old?.id)} />
          {showOld ? (
            <Session run={old} stop={stop} resumed={false} expired={false} onChoose={() => undefined} setAside={way === "resume" ? undefined : way} />
          ) : way === "resume" ? (
            <Session run={open} stop={stop} resumed expired={false} onChoose={() => undefined} />
          ) : (
            <div className="runScreen">
              <ChatTranscript fill live
                session={{ id: open.id, role: open.role, status: "running", model: open.model, taskKey: TASK.key, title: LABEL[open.phase], owner: P["marcio"], startedAt: open.startedAt, costUsd: open.costUsd }}
                footer={<div className={styles["fakeComposer"]}>Steer the implementer…</div>}>
                <ChatMessage role="system" intent="prompt" name="dude" startedAt={ago(1)}
                  content={way === "retry"
                    ? `**Implement ${TASK.key}** — again.\n\nThe last implementer was aborted by Ana: “${STOPPED.aborted.why}”\n\nStart from \`${LEFT.pushed.sha}\` on \`${TASK.branch}\`; what it had not pushed is gone.\n\n**Note from Márcio:** Don't touch the runner protocol. Read tool boundaries from the ACP events the shim already gets.`
                    : `**Implement ${TASK.key}** — attempt 2, from \`main\` on \`dude/wi_2408/attempt-2\`.\n\n**Note from Márcio:** Don't touch the runner protocol. Read tool boundaries from the ACP events the shim already gets.`} />
                <ChatMessage role="implementer" model={open.model} content="Reading `internal/shim/acp.go` for where tool calls start and end." startedAt={ago(0.5)} streaming activity="streaming" />
              </ChatTranscript>
            </div>
          )}
        </div>
      ) : (
        <div className="taskPane">
          <Timeline>
            <TimelineItem who={<PersonAvatar person={P["marcio"]!} size={32} />} when={<Duration ms={60_000} format="age" tone="muted" />}
              quote="Don't touch the runner protocol. Read tool boundaries from the ACP events the shim already gets.">
              <b>Márcio Martins</b> picked the task back up: {what}
              {way === "restart" ? <span className={styles["groupNote"]}> · attempt 2 begins; attempt 1 is kept</span> : null}
            </TimelineItem>
            {stop === "aborted" ? (
              <TimelineItem who={<PersonAvatar person={P["ana"]!} size={32} />} when={<Duration ms={95 * 60_000} format="age" tone="muted" />} quote={STOPPED.aborted.why}>
                <b>Ana Ribeiro</b> aborted the implement
              </TimelineItem>
            ) : (
              <TimelineItem who={<DudeMark size={32} />} when={<Duration ms={95 * 60_000} format="age" tone="muted" />} quote={STOPPED.failed.why}>
                The <b>implement</b> failed
              </TimelineItem>
            )}
            <TimelineItem who={<AgentAvatar role="implementer" size="lg" />} when={<Duration ms={142 * 60_000} format="age" tone="muted" />}>
              The <b>implement</b> started
            </TimelineItem>
          </Timeline>
        </div>
      )}
    </TaskFrame>
  );
}

// --------------------------------------------------------------------------- what the words mean (for the discussion; not an app page)

const ROWS: ReadonlyArray<{ q: string; resume: ReactNode; retry: ReactNode; restart: ReactNode }> = [
  { q: "Agent", resume: "The same one, same conversation", retry: "New, told why the last stopped", restart: "New, told only the task" },
  { q: "Starts from", resume: "Its workspace as it stopped (unpushed work too)", retry: "The last pushed commit on the branch", restart: "main" },
  { q: "Branch · PR", resume: "Same", retry: "Same", restart: "New branch; old PR closed" },
  { q: "Pipeline", resume: "Carries on from the step that stopped", retry: "Re-runs the step that stopped, then on", restart: "From Implement, all of it" },
  { q: "Findings, decisions", resume: "Kept", retry: "Kept, and told to the agent", restart: "Decisions kept; findings stay with attempt 1" },
  { q: "Sessions tab", resume: "Same session, transcript continues", retry: "New session, same attempt", restart: "New sessions, attempt 2" },
  { q: "Possible while", resume: <>lux keeps the stopped Run's volumes ({LEFT.keptFor})</>, retry: "Always", restart: "Always" },
  { q: "Good for", resume: "A crash, a lost host, or a nudge in a new direction", retry: "The agent went down a hole; a clean head helps", restart: "The approach, or the task itself, was wrong" },
  { q: "Today", resume: "Only a paused Run", retry: <>Only at an escalation (“Try again”)</>, restart: "Not possible; create a new task" },
];

export function Meaning() {
  return (
    <div className={styles["meaning"]}>
      <h1 className={styles["h1"]}>Three ways to pick a stopped task back up</h1>
      <p className={styles["lede"]}>
        Today an <b>aborted</b> or <b>failed</b> task is a dead end: the task can't leave that state, dude cancels the lux Run (so its
        workspace and conversation are thrown away), and Deliver won't run twice. The proposal: keep the stopped Run in lux (stop, don't
        cancel), let the task leave <code>aborted</code>/<code>failed</code>, and offer three ways on.
      </p>
      <table className={styles["table"]}>
        <thead>
          <tr>
            <th />
            <th><span className={styles["th"]}><Icon name="play" size={14} />Resume</span><span className={styles["thSub"]}>same session</span></th>
            <th><span className={styles["th"]}><Icon name="retry" size={14} />Try again</span><span className={styles["thSub"]}>same attempt, new session</span></th>
            <th><span className={styles["th"]}><Icon name="git-branch" size={14} />Start over</span><span className={styles["thSub"]}>new attempt</span></th>
          </tr>
        </thead>
        <tbody>
          {ROWS.map((r) => (
            <tr key={r.q}>
              <th scope="row">{r.q}</th>
              <td>{r.resume}</td>
              <td>{r.retry}</td>
              <td>{r.restart}</td>
            </tr>
          ))}
        </tbody>
      </table>
      <h2 className={styles["h2"]}>Open questions</h2>
      <ol className={styles["questions"]}>
        <li><b>Who may?</b> Proposed: the task's owner, as with an escalation; anyone else takes the task over first.</li>
        <li><b>Resume after a failure that will just fail again</b> (a model misconfigured, a refused spec)? Proposed: offered anyway; the failure's words say why.</li>
        <li><b>Parallel reviewers:</b> aborting one today leaves its siblings running. Proposed: abort stops the whole step, and Resume / Try again re-runs only the stopped ones.</li>
        <li><b>A task closed because its PR was closed</b> (also <code>aborted</code>): offer only Start over?</li>
        <li><b>Dead-lettered workflows</b> (a dude bug, not the agent's) look stuck today. Same notice, with only Try again?</li>
      </ol>
      <p className={styles["lede"]}><Tooltip content="Switch screens in the bar above."><span><Icon name="info" size={12} /> Use the bar above to walk through the screens.</span></Tooltip></p>
    </div>
  );
}
