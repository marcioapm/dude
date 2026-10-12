/*
 * Screens 2, 3, 4, 7 and 8: a blocked task's page (CP-44), its Start anyway
 * and Start when unblocked dialogs, its relation editor, the page as
 * someone who doesn't own the task reads it (CP-42), a blocker marked done
 * with nothing merged (CP-51), and the history. The frame is the app's
 * task screen (apps/web's `taskTop`, `taskHead`, `taskPane` layout classes
 * from app.css); everything drawn in it is the design system's or
 * Relations.tsx's.
 */

import { useCallback, useState } from "react";
import {
  AgentAvatar, Breadcrumb, ChatNotice, Markdown, PersonAvatar, PersonLine, RefLead, SearchPicker, StatusMark, TaskHistory, Timeline, TimelineItem,
} from "@dude/design-system/components";
import { Button, Callout, EmptyState, Select, Tab, TabList, TabPanel, Tabs } from "@dude/design-system/primitives";
import {
  CP44, EDGES, HOW_WORDS, NOT_STARTED, P, YOU, allTasks, autoStartOf, blockedOf, blockersOf, doneUnmerged, isOwner, neverFires, openBlockers, relatedOf, removeHint, resolved, task,
  type By, type Edge, type Task,
} from "./data.ts";
import { AutoStartMark, RelationGlyph, RelationList, StartGate, keyList, relationWords, type RelationGroupData, type RelationRowData } from "./Relations.tsx";
import { AutoStartDialog, OverrideDialog } from "./StartDialogs.tsx";
import styles from "./pages.module.css";

const firstName = (n: string) => n.split(" ")[0]!;

const row = (t: Task, here: Task, e: Edge): RelationRowData => ({
  key: t.key, title: t.title, status: t.status, owner: t.owner, agent: t.agent?.role,
  project: t.project !== here.project ? t.project : undefined, settled: resolved(t), unmerged: doneUnmerged(t),
  removeHint: removeHint(e, here.key) ?? undefined,
});

export type TaskView = "overview" | "chat" | "activity";
export type EditorState = "closed" | "open" | "cycle";
export type OverrideState = "closed" | "open" | "comms";

export interface TaskPageProps {
  readonly k: string;
  readonly view: TaskView;
  readonly onView: (v: TaskView) => void;
  readonly override: OverrideState;
  readonly onOverride: (o: OverrideState) => void;
  readonly auto: boolean;
  readonly onAuto: (open: boolean) => void;
  readonly editor: EditorState;
  readonly onEditor: (e: EditorState) => void;
  readonly onOpen: (key: string) => void;
  readonly onBoard: () => void;
}

export function TaskPage({ k, view, onView, override, onOverride, auto: autoOpen, onAuto, editor, onEditor, onOpen, onBoard }: TaskPageProps) {
  const t = task(k);
  const blockers = NOT_STARTED.has(t.status) ? openBlockers(k) : [];
  const groups: RelationGroupData[] = [
    { kind: "blocked-by", rows: blockersOf(k).map((b) => row(b.task, t, b.edge)) },
    { kind: "blocks", rows: blockedOf(k).map((b) => row(b.task, t, b.edge)) },
    { kind: "related", rows: relatedOf(k).map((b) => row(b.task, t, b.edge)) },
  ];
  const failedBlocker = blockers.find((b) => b.status === "failed" || b.status === "aborted");
  const unmerged = blockers.filter(doneUnmerged);
  const mine = isOwner(t);
  const autoSet = blockers.length ? autoStartOf(k) : undefined;
  const never = neverFires(k);
  const autoView = autoSet ? { how: autoSet.how, by: autoSet.by.id === YOU.id ? "You" : firstName(autoSet.by.name), ago: autoSet.ago, onCancel: mine ? () => undefined : undefined } : undefined;

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
            <StartGate blockers={blockers.map((b) => ({ ...b, unmerged: doneUnmerged(b) }))} onStartAnyway={() => onOverride("open")}
              onStartWhenUnblocked={mine && !never && !autoSet ? () => onAuto(true) : undefined}
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
          <PersonLine person={t.owner} size={56} detail={mine ? "Owner · you" : "Owner"} />
        </div>
      </div>

      {blockers.length > 0 ? (
        <div className="taskNotices">
          <Callout tone="neutral" data-testid="blocked-callout">
            <span className={styles["blockedNote"]}>
              <b>{relationWords("blocked-by", blockers.map((b) => ({ ...b, unmerged: doneUnmerged(b) })))}.</b>{" "}
              {autoSet
                ? null
                : unmerged.length === blockers.length
                  ? "Only merged work releases what waits on it."
                  : `It can start once ${blockers.length === 1 ? "that is" : "they are"} merged; nothing starts it on its own then.`}
            </span>
            {autoView ? (
              <p className={styles["callAuto"]} data-testid="auto-line">
                <AutoStartMark auto={autoView} blockers={blockers} />
              </p>
            ) : null}
            {unmerged.length > 0 ? (
              <p className={styles["callLine"]} data-testid="unmerged-line">
                {keyList(unmerged.map((b) => b.key))} {unmerged.length === 1 ? "was" : "were"} marked done by {firstName(unmerged[0]!.owner.name)} with nothing merged, so {unmerged.length === 1 ? "it" : "they"} will never release {t.key}.
                {" "}{mine ? "Remove the relation if its answer is all this needed, or start this anyway." : `${firstName(t.owner.name)}, the owner, can remove the relation; anyone can start this anyway.`}
              </p>
            ) : null}
            {failedBlocker ? (
              <p className={styles["callLine"]}>
                {failedBlocker.key} failed. It keeps blocking until someone picks it back up and it merges, the owner removes the relation, or someone starts this anyway.
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
                    description={autoSet
                      ? `${HOW_WORDS[autoSet.how]} starts here once ${keyList(blockers.map((b) => b.key))} ${blockers.length === 1 ? "merges" : "merge"}. A person can start it sooner; an agent can't.`
                      : `Deliver and Talk it through open once ${keyList(blockers.map((b) => b.key))} ${blockers.length === 1 ? "is" : "are"} merged. A person can start it anyway; an agent can't.`} />
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

      <OverrideDialog open={override !== "closed"} comms={override === "comms"} onOpenChange={(o) => onOverride(o ? "open" : "closed")} here={t}
        blockers={blockers.map((b) => ({ ...b, unmerged: doneUnmerged(b) }))} />
      <AutoStartDialog open={autoOpen} onOpenChange={onAuto} here={t} blockers={blockers} chosen={autoOpen ? "deliver" : undefined} />
    </div>
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
        options={(Object.keys(KIND_WORDS) as Kind[]).map((k) => ({ value: k, label: KIND_WORDS[k], description: k === "related" ? "For reading: never stops a start." : k === "blocks" ? "It can't start until this is merged." : "This can't start until it is merged." }))} />
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
      <TaskHistory icon="play" lead="Started when unblocked · Deliver" steps={["implementer"]} facts={["was blocked by CP-41 for 3d"]} />
      <ChatNotice kind="notice" by="El Duderino" text="Ana filed this from her Brainstorm session, after CP-41: it can't start until CP-41 is merged." at={T0 - 3 * 86_400_000} />
      <ChatNotice kind="notice" by="El Duderino" text="Ana set this to start when it's unblocked, with Deliver. Nothing starts before CP-41 merges." at={T0 - 2 * 86_400_000} />
      <ChatNotice kind="notice" by="El Duderino" data-testid="unblocked-notice" text="CP-41 merged — CP-42 started (Deliver), as Ana asked. Nothing else was waiting in front of it." at={T0} />
    </div>
  );
}

/** The task's Activity: who changed its relations, what they unblocked, and what started it. */
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
            <b>CP-38</b> failed. It still blocks <b>CP-49</b> until it is picked back up and merges, Tom removes the relation, or someone starts CP-49 anyway.
          </TimelineItem>
          <TimelineItem who={<Who by={{ kind: "person", person: P.tom }} size={28} />} when="5d">
            <b>Tom</b> added <RelationGlyph name="lock" size={12} /> blocked by <b>CP-38</b>
          </TimelineItem>
        </Timeline>
      </div>
    );
  }
  if (k === "CP-51") {
    return (
      <div className={styles["history"]} data-testid="activity">
        <Timeline aria-label="Activity">
          <TimelineItem who={<AgentAvatar role="system" size="lg" />} when="1d" data-testid="unmerged-activity">
            <b>CP-37</b> was marked done by <b>Tom</b> with nothing merged. It still blocks <b>CP-51</b>: only merged work releases what waits on it.
          </TimelineItem>
          <TimelineItem who={<Who by={{ kind: "person", person: P.marcio }} size={28} />} when="3d">
            <b>Márcio</b> added <RelationGlyph name="lock" size={12} /> blocked by <b>CP-37</b>
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
        <TimelineItem who={<Who by={{ kind: "person", person: P.ana }} size={28} />} when="09:00" data-testid="auto-fired-line">
          <b>CP-41</b> merged (#431) — <b>CP-42</b> started (Deliver), as <b>Ana</b> asked. Every blocker was checked again: none open.
        </TimelineItem>
        <TimelineItem who={<Who by={{ kind: "person", person: P.marcio }} size={28} />} when="yesterday">
          <b>Márcio</b> removed <RelationGlyph name="link" size={12} /> related <b>WEB-9</b>
        </TimelineItem>
        <TimelineItem who={<Who by={{ kind: "person", person: P.ana }} size={28} />} when="2d" data-testid="auto-set-line">
          <b>Ana</b> set it to start when unblocked — Deliver
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
