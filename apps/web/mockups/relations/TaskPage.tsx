/*
 * Screens 2, 3, 4, 7 and 8: a blocked task's page (CP-44), its Start anyway
 * confirmation, its relation editor, and its history. The frame is the
 * app's task screen (apps/web's `taskTop`, `taskHead`, `taskPane` layout
 * classes from app.css); everything drawn in it is the design system's or
 * Relations.tsx's.
 */

import { useCallback, useState } from "react";
import {
  AgentAvatar, Breadcrumb, ChatNotice, Markdown, PersonAvatar, PersonLine, RefLead, SearchPicker, StatusMark, TaskHistory, Timeline, TimelineItem,
} from "@dude/design-system/components";
import { Button, Callout, Dialog, EmptyState, Select, Tab, TabList, TabPanel, Tabs, Textarea } from "@dude/design-system/primitives";
import { CP44, NOT_STARTED, P, allTasks, blockedOf, blockersOf, openBlockers, relatedOf, resolved, task, type By, type Task } from "./data.ts";
import { RelationGlyph, RelationList, StartGate, relationWords, type RelationGroupData, type RelationRowData } from "./Relations.tsx";
import styles from "./pages.module.css";

const row = (t: Task, here: Task): RelationRowData => ({
  key: t.key, title: t.title, status: t.status, owner: t.owner, agent: t.agent?.role,
  project: t.project !== here.project ? t.project : undefined, settled: resolved(t),
});

export type TaskView = "overview" | "chat" | "activity";
export type EditorState = "closed" | "open" | "cycle";

export interface TaskPageProps {
  readonly k: string;
  readonly view: TaskView;
  readonly onView: (v: TaskView) => void;
  readonly override: boolean;
  readonly onOverride: (open: boolean) => void;
  readonly editor: EditorState;
  readonly onEditor: (e: EditorState) => void;
  readonly onOpen: (key: string) => void;
  readonly onBoard: () => void;
}

export function TaskPage({ k, view, onView, override, onOverride, editor, onEditor, onOpen, onBoard }: TaskPageProps) {
  const t = task(k);
  const blockers = NOT_STARTED.has(t.status) ? openBlockers(k) : [];
  const groups: RelationGroupData[] = [
    { kind: "blocked-by", rows: blockersOf(k).map((b) => row(b.task, t)) },
    { kind: "blocks", rows: blockedOf(k).map((b) => row(b.task, t)) },
    { kind: "related", rows: relatedOf(k).map((b) => row(b.task, t)) },
  ];
  const failedBlocker = blockers.find((b) => b.status === "failed" || b.status === "aborted");

  return (
    <div className="screen taskScreen" data-testid="task-screen">
      <header className={`taskTop ${styles["top"]}`}>
        <div className="taskCrumbs">
          <Breadcrumb items={[
            { id: "p", label: t.project, onSelect: onBoard },
            ...(t.epic ? [{ id: "e", label: t.epic === "billing" ? "Billing v2" : "Webhook reliability", icon: "layers" as const, onSelect: onBoard }] : []),
            { id: "k", label: t.key, mono: true },
          ]} />
        </div>
        <span className="taskTopActions">
          {blockers.length > 0 ? (
            <StartGate blockers={blockers} onStartAnyway={() => onOverride(true)}
              starts={[{ id: "talk", label: "Talk it through", icon: "message" }, { id: "deliver", label: "Deliver", icon: "zap" }]} />
          ) : NOT_STARTED.has(t.status) ? (
            <>
              <Button leadingIcon="message">Talk it through</Button>
              <Button leadingIcon="zap">Deliver</Button>
            </>
          ) : null}
          <Button variant="quiet" leadingIcon="edit">Edit</Button>
        </span>
      </header>

      <div className="taskHead" data-testid="task-header">
        <div className="taskHeadMain">
          <h1 className="taskTitle">{t.title}</h1>
          <div className="taskMeta">
            <StatusMark status={t.status} size="sm" />
            <span className="ds-mono">{t.key}</span>
            <span>created 3d ago</span>
          </div>
        </div>
        <div className="taskPeople">
          <PersonLine person={t.owner} size={56} detail="Owner" />
        </div>
      </div>

      {blockers.length > 0 ? (
        <div className="taskNotices">
          <Callout tone="neutral" data-testid="blocked-callout">
            <span className={styles["blockedNote"]}>
              <b>{relationWords("blocked-by", blockers)}.</b> It can start once {blockers.length === 1 ? "that is" : "they are"} done; nothing starts it on its own then.
            </span>
            {failedBlocker ? (
              <p className={styles["callLine"]}>
                {failedBlocker.key} failed. It keeps blocking until someone picks it back up, removes the relation, or starts this anyway.
              </p>
            ) : null}
          </Callout>
        </div>
      ) : null}

      <Tabs value={view} onValueChange={(v) => onView(v as TaskView)} fill>
        <TabList aria-label="Task" className="tabsInset">
          <Tab value="chat">Chat</Tab>
          <Tab value="overview">Overview</Tab>
          <Tab value="findings">Findings</Tab>
          <Tab value="sessions">Sessions</Tab>
          <Tab value="files">Files</Tab>
          <Tab value="activity">Activity</Tab>
        </TabList>

        <TabPanel value="overview" className="taskPane">
          <div className="taskOverview">
            <div className="taskMain">
              {k === "CP-44" ? (
                <>
                  <section className="taskBlock" aria-label="Goal">
                    <h2 className="ds-label">Goal</h2>
                    <div className="taskGoal"><Markdown source={CP44.goal} breaks /></div>
                  </section>
                  <section className="taskBlock" aria-label="Acceptance criteria">
                    <h2 className="ds-label">Acceptance criteria</h2>
                    <ul className="taskGoal taskCriteria">
                      {CP44.criteria.map((c) => <li key={c}><Markdown source={c} breaks /></li>)}
                    </ul>
                  </section>
                </>
              ) : null}
              <div className="taskBlock">
                <RelationList groups={groups} onOpen={onOpen} onAdd={() => onEditor("open")} onRemove={() => undefined}
                  adding={editor !== "closed" ? <RelationEditor here={t} state={editor} onState={onEditor} /> : null} />
              </div>
              <section className="taskBlock" aria-label="Pipeline">
                <h2 className="ds-label">Pipeline</h2>
                {blockers.length > 0 ? (
                  <EmptyState compact icon="clock" title="Waiting on its blockers"
                    description={`Deliver and Talk it through open once ${blockers.map((b) => b.key).join(" and ")} ${blockers.length === 1 ? "is" : "are"} done. A person can start it anyway; an agent can't.`} />
                ) : (
                  <EmptyState compact icon="git-pr" title={NOT_STARTED.has(t.status) ? "Not started" : "Under way"}
                    description={NOT_STARTED.has(t.status) ? "Nothing waits in front of it: Deliver or Talk it through when you're ready." : "Its sessions are on the Sessions tab."} />
                )}
              </section>
            </div>
          </div>
        </TabPanel>

        <TabPanel value="chat" className="taskPane">
          <TaskChatHistory k={k} />
        </TabPanel>

        <TabPanel value="activity" className="taskPane">
          <ActivityLines k={k} />
        </TabPanel>
      </Tabs>

      <OverrideDialog open={override} onOpenChange={onOverride} here={t} blockers={blockers} />
    </div>
  );
}

// ---------------------------------------------------------------------------
// Screen 3: Start anyway
// ---------------------------------------------------------------------------

function OverrideDialog({ open, onOpenChange, here, blockers }: { readonly open: boolean; readonly onOpenChange: (o: boolean) => void; readonly here: Task; readonly blockers: ReadonlyArray<Task> }) {
  const [how, setHow] = useState<"deliver" | "talk">("talk");
  return (
    <Dialog open={open} onOpenChange={onOpenChange} size="md" tone="attention"
      title={`Start ${here.key} anyway?`}
      footerStart={<span>Recorded in {here.key}'s history, with your note.</span>}
      footer={
        <>
          <Button variant="quiet" onClick={() => onOpenChange(false)} autoFocus>Keep waiting</Button>
          <Button variant="primary" leadingIcon={how === "deliver" ? "zap" : "message"} data-testid="confirm-start-anyway">
            {how === "deliver" ? "Deliver anyway" : "Talk it through anyway"}
          </Button>
        </>
      }>
      <div className={styles["dialogBody"]}>
        <p>It waits on {blockers.length === 1 ? "this, which isn't" : `these ${blockers.length}, which aren't`} done. Its agents start from <span className={styles["keyMono"]}>main</span> as it is now: what {blockers.length === 1 ? "it changes" : "they change"} won't be there.</p>
        <ul className={styles["blockerList"]} aria-label="Blocked by">
          {blockers.map((b) => (
            <li key={b.key}>
              <RefLead type="task" taskKey={b.key} status={b.status} />
              <span className={styles["blockerTitle"]}>{b.title}</span>
              <StatusMark status={b.status} size="sm" />
              <PersonAvatar person={b.owner} size={20} agent={b.agent?.role} live={b.agent ? true : undefined} />
            </li>
          ))}
        </ul>
        <Select label="Start with" value={how} onValueChange={(v) => setHow(v)}
          options={[
            { value: "talk", label: "Talk it through", meta: "the conductor plans with you first" },
            { value: "deliver", label: "Deliver", meta: "the automatic pipeline" },
          ]} />
        <Textarea label="Why (optional)" rows={2} placeholder="CP-43's sync is enough for the page's first version" hint="The relations stay: it still shows as after them on the board." />
      </div>
    </Dialog>
  );
}

// ---------------------------------------------------------------------------
// Screen 4: the relation editor, and a cycle refused
// ---------------------------------------------------------------------------

type Kind = "blocked-by" | "blocks" | "related";
const KIND_WORDS: Record<Kind, string> = { "blocked-by": "Blocked by", blocks: "Blocks", related: "Related to" };

function RelationEditor({ here, state, onState }: { readonly here: Task; readonly state: EditorState; readonly onState: (s: EditorState) => void }) {
  const [kind, setKind] = useState<Kind>(state === "cycle" ? "blocks" : "blocked-by");
  const already = new Set([...blockersOf(here.key), ...blockedOf(here.key), ...relatedOf(here.key)].map((r) => r.task.key));
  const find = useCallback(async (q: string) => {
    const words = q.trim().toLowerCase();
    return allTasks().filter((t) => !words || t.key.toLowerCase().includes(words) || t.title.toLowerCase().includes(words)).slice(0, 8);
  }, []);
  return (
    <div className={styles["editor"]} data-testid="relation-editor">
      <Select label="Relation" value={kind} onValueChange={(v) => setKind(v)}
        options={(Object.keys(KIND_WORDS) as Kind[]).map((k) => ({ value: k, label: KIND_WORDS[k], description: k === "related" ? "For reading: never stops a start." : k === "blocks" ? "It can't start until this is done." : "This can't start until it is done." }))} />
      <div>
        <SearchPicker<Task>
          label="Task"
          placeholder="Find a task in Acme by key or title"
          autoFocus={state === "open"}
          findOnEmpty
          find={find}
          optionKey={(t) => t.key}
          group={(t) => (t.project === here.project ? (t.epic === here.epic ? "Billing v2" : "control-plane") : t.project)}
          optionDisabled={(t) => (t.key === here.key ? "This task" : already.has(t.key) ? "Already related" : null)}
          renderOption={(t) => (
            <span className={styles["option"]}>
              <RefLead type="task" taskKey={t.key} status={t.status} />
              <span className={styles["optionTitle"]}>{t.title}</span>
              <StatusMark status={t.status} size="sm" />
            </span>
          )}
          onPick={(t) => onState(kind === "blocks" && t.key === "CP-40" ? "cycle" : "closed")}
          onCancel={() => onState("closed")}
          empty={(q) => `No task in Acme matches “${q}”.`}
        />
      </div>
      {state === "cycle" ? (
        <p className={[styles["problem"], styles["editorFull"]].join(" ")} role="alert" data-testid="cycle-error">
          <span>Not added: {here.key} can't block CP-40, which is already ahead of it —</span>
          <span className={styles["cycle"]} aria-label="CP-40 blocks CP-41, which blocks CP-44">
            CP-40 <RelationGlyph name="blocks" size={12} /> CP-41 <RelationGlyph name="blocks" size={12} /> {here.key} <RelationGlyph name="blocks" size={12} /> CP-40
          </span>
          <span>would be a loop nothing could start.</span>
          <Button variant="quiet" size="sm" onClick={() => onState("open")}>Make it Related instead</Button>
        </p>
      ) : null}
      <div className={styles["editorFull"]}>
        <Button variant="quiet" size="sm" onClick={() => onState("closed")}>Done</Button>
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Screen 7: history. CP-42 the morning CP-41 merged; CP-49 an hour after Tom
// started it past its failed blocker. Each task's lines are its own.
// ---------------------------------------------------------------------------

function Who({ by, size = 24 }: { readonly by: By; readonly size?: 24 | 28 }) {
  return by.kind === "person" ? <PersonAvatar person={by.person} size={size} /> : <PersonAvatar person={by.for} size={size} agent={by.role} />;
}

const name = (by: By) => (by.kind === "person" ? by.person.name.split(" ")[0] : `${by.for.name.split(" ")[0]}'s ${by.role === "brainstorm" ? "Brainstorm" : "conductor"}`);

/** The task's Chat: its one-line history, then dude's notices in time. */
function TaskChatHistory({ k }: { readonly k: string }) {
  const T0 = Date.UTC(2026, 9, 12, 9, 0, 0);
  if (k !== "CP-42") return <EmptyState compact icon="message" title="Nobody has written here yet" description="Your message starts planning this task with its conductor." />;
  return (
    <div className={styles["history"]} data-testid="chat-history">
      <TaskHistory icon="clock" lead="Not started · ready to start" steps={[]} facts={["was blocked by CP-41 for 3d"]} />
      <ChatNotice kind="notice" by="El Duderino" text="Ana filed this from her Brainstorm session, after CP-41: it can't start until CP-41 is done." at={T0 - 3 * 86_400_000} />
      <ChatNotice kind="notice" by="El Duderino" data-testid="unblocked-notice" text="CP-41 merged — CP-42 can start. Nothing started it: Deliver or Talk it through when you're ready." at={T0} />
    </div>
  );
}

/** The task's Activity: who changed its relations, and what they unblocked. */
function ActivityLines({ k }: { readonly k: string }) {
  if (k === "CP-49") {
    return (
      <div className={styles["history"]} data-testid="activity">
        <Timeline aria-label="Activity">
          <TimelineItem who={<Who by={{ kind: "person", person: P.tom }} size={28} />} when="1h" data-testid="override-line"
            quote="The spike failed; tax waits for next quarter. Building the rates table without Stripe Tax.">
            <b>Tom</b> started it anyway, past <RelationGlyph name="lock" size={12} /> <b>CP-38</b> (failed) — Talk it through
          </TimelineItem>
          <TimelineItem who={<AgentAvatar role="system" size="lg" />} when="2d">
            <b>CP-38</b> failed. It still blocks <b>CP-49</b> until someone picks it back up, removes the relation, or starts CP-49 anyway.
          </TimelineItem>
          <TimelineItem who={<Who by={{ kind: "person", person: P.tom }} size={28} />} when="5d">
            <b>Tom</b> added <RelationGlyph name="lock" size={12} /> blocked by <b>CP-38</b>
          </TimelineItem>
        </Timeline>
      </div>
    );
  }
  const brainstorm: By = { kind: "agent", role: "brainstorm", for: P.ana, on: "a session" };
  const conductor: By = { kind: "agent", role: "conductor", for: P.ana, on: "CP-42" };
  return (
    <div className={styles["history"]} data-testid="activity">
      <Timeline aria-label="Activity">
        <TimelineItem who={<AgentAvatar role="system" size="lg" />} when="09:00" data-testid="unblocked-line">
          <b>CP-41</b> merged (#431) — <b>CP-42</b> can start. It waits for a person: nothing starts on its own.
        </TimelineItem>
        <TimelineItem who={<Who by={{ kind: "person", person: P.marcio }} size={28} />} when="yesterday">
          <b>Márcio</b> removed <RelationGlyph name="link" size={12} /> related <b>WEB-9</b>
        </TimelineItem>
        <TimelineItem who={<Who by={conductor} size={28} />} when="2d" data-testid="agent-added-line"
          quote="The line items read usage_events directly; CP-40 has to be in first.">
          <b>{name(conductor)}</b> added <RelationGlyph name="lock" size={12} /> blocked by <b>CP-40</b>
        </TimelineItem>
        <TimelineItem who={<Who by={brainstorm} size={28} />} when="3d">
          <b>Ana</b> filed it with <RelationGlyph name="lock" size={12} /> blocked by <b>CP-41</b>, as <b>{name(brainstorm)}</b> proposed
        </TimelineItem>
      </Timeline>
    </div>
  );
}
