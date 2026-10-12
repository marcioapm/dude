/*
 * BILL-42's task page as TaskScreen lays it out (breadcrumb, head, tabs),
 * with its Chat (ChatSection's transcript: the conductor, the
 * implementer's run line, the machine request, notices and the move) and
 * its Overview (the pipeline, and the machines raised in the aside).
 */

import { useRef, useState, type ReactNode } from "react";
import {
  AgentAvatar, Breadcrumb, ChatComposer, ChatMessage, ChatNotice, ChatRunLine, ChatTranscript, DeciderLine, Markdown, PersonLine, StatusMark,
  StepList, StepRow, TaskHistory,
} from "@dude/design-system/components";
import { Button, Callout, Tab, TabList, TabPanel, Tabs } from "@dude/design-system/primitives";
import { MachineRaises, MachineRequestCard, MoveProgress, type MoveStep } from "./components.tsx";
import { DECLINE_NOTE, MOVE_STAGES, P, PROJECT, REASON_LARGE, REASON_XL, TASK, size, type Scenario } from "./data.ts";

const MIN = 60_000;
const DUDE = "El Duderino";
const standard = size("Standard");
const large = size("Large");
const xl = size("XL");

function TaskChrome({ tab, children }: { tab: "chat" | "overview"; children: ReactNode }) {
  return (
    <div className={tab === "chat" ? "screen taskScreen fixed" : "screen taskScreen"} data-testid="task-screen">
      <header className="taskTop">
        <div className="taskCrumbs">
          <Breadcrumb items={[{ id: "p", label: PROJECT, onSelect: () => undefined }, { id: "e", label: "Exports", onSelect: () => undefined }, { id: "t", label: TASK.key, mono: true }]} />
        </div>
        <span className="taskTopActions"><Button variant="quiet" leadingIcon="edit">Edit</Button></span>
      </header>
      <div className="taskHead" data-testid="task-header">
        <div className="taskHeadMain">
          <h1 className="taskTitle">{TASK.title}</h1>
          <div className="taskMeta">
            <StatusMark status="running" size="sm" />
            <span className="ds-mono">{TASK.key}</span>
            <span className="ds-mono">{TASK.branch}</span>
            <span>created 2h ago</span>
          </div>
        </div>
        <div className="taskPeople">
          <PersonLine person={P["marcio"]!} size={56} agent="implementer" live detail="Owner · Márcio's implementer is working" />
        </div>
      </div>
      <Tabs value={tab} onValueChange={() => undefined} fill>
        <TabList aria-label="Task" className="tabsInset">
          <Tab value="chat">Chat</Tab>
          <Tab value="overview">Overview</Tab>
          <Tab value="findings">Findings</Tab>
          <Tab value="sessions" count={3}>Sessions</Tab>
          <Tab value="files">Files</Tab>
          <Tab value="servers">Servers</Tab>
          <Tab value="activity">Activity</Tab>
        </TabList>
        <TabPanel value={tab} fill={tab === "chat"} className={tab === "overview" ? "taskPane" : undefined}>{children}</TabPanel>
      </Tabs>
    </div>
  );
}

const implLine = (status: "running" | "paused" | "starting", label: string, facts: string[]) => (
  <ChatRunLine data-testid="chat-run" role="implementer" status={status} statusLabel={label} what="Implement: stream the CSV export instead of building it in memory"
    facts={facts} onOpen={() => undefined} />
);

/** The stages of the move, at a moment: two done, the third live. */
function moveSteps(at: number): MoveStep[] {
  const took = [6_400, 11_200];
  return MOVE_STAGES.map((st, i) => ({
    id: st.id, label: st.now, doneLabel: st.done,
    state: i < 2 ? "done" : i === 2 ? "current" : "pending",
    tookMs: took[i], since: i === 2 ? at - 4_000 : undefined,
  }));
}

export function TaskChat({ scenario, onDecide }: { scenario: Scenario; onDecide: (approve: boolean) => void }) {
  const t0 = useRef(Date.now()).current;
  const asked = t0 - 3 * MIN;
  const decidedAt = t0 - 40_000;
  const fresh = scenario === "ask-fresh";
  const waiting = scenario === "ask" || scenario === "ask-fresh";
  const to = fresh ? xl : large;
  return (
    <TaskChrome tab="chat">
      <div className="taskChat" data-testid="task-chat">
        <div className="runScreen" data-view="chat">
          <ChatTranscript fill live revision={4} turns={6}
            pinned={<TaskHistory lead="The conductor decides" steps={["implementer"]} facts={["no findings yet", "$1.84"]} icon="conductor" />}
            footer={
              <>
                <DeciderLine decider="conductor" action={<Button variant="quiet" size="sm">Let Deliver finish it</Button>} />
                <ChatComposer mode="chat" onSubmit={() => true} sentAs="Márcio" to={<>To <b>Conductor</b></>}
                  {...(waiting ? { waitingFor: "Implementer", waitingKey: "mreq_1" } : {})} />
              </>
            }>
            <ChatMessage role="human" person={P["marcio"]} name="Márcio Martins" intent="message" startedAt={t0 - 41 * MIN}
              content="The export times out for Globex (2.1M invoices). Stream it instead of building the CSV in memory, and keep the PDF path as it is." />
            <ChatMessage role="conductor" startedAt={t0 - 40 * MIN} costUsd={0.12}
              content="Agreed. I'll start the implementer on streaming the CSV through `pg-cursor`; the PDF renderer stays. It runs the backend suite before it pushes." />
            {implLine(scenario === "resizing" ? "starting" : scenario === "auto" ? "running" : waiting ? "paused" : "running",
              scenario === "resizing" ? "Moving to Large" : waiting ? "Waiting for you" : "Running",
              [scenario === "resizing" || scenario === "auto" ? "Large" : "Standard", "38m", "$1.72"])}
            {scenario === "auto" ? (
              <>
                <ChatNotice kind="notice" by={DUDE} at={t0 - 2 * MIN} data-testid="auto-notice"
                  text="The implementer asked for Large (4 CPUs · 16 GiB): its jest run was OOM-killed at 7.9 of 8 GiB. billing lets agents go up to Large, so it moves now, and later implementer and fixer Runs in this task run on Large." />
                <ChatNotice kind="unparked" by={DUDE} at={t0 - 80_000} text="The implementer moved to Large in 41s, with its conversation and uncommitted work."
                  title={MOVE_STAGES.map((st, i) => `${st.done} ${[6.4, 11.2, 9.8, 3.1, 10.5][i]}s`).join("\n")} />
              </>
            ) : (
              <MachineRequestCard role="implementer" who="Implementer" from={standard} to={to} peakGiB={7.9} showCost
                reason={fresh ? REASON_XL : REASON_LARGE} askedAt={asked}
                fresh={fresh ? { pushed: TASK.commit, uncommitted: { files: ["src/export/stream.ts"], added: 42, removed: 7 } } : undefined}
                decision={scenario === "declined" ? { approved: false, by: "Márcio", at: decidedAt, note: DECLINE_NOTE }
                  : scenario === "resizing" ? { approved: true, by: "Márcio", at: decidedAt } : undefined}
                onDecide={waiting ? (approve) => onDecide(approve) : undefined} onAskToPush={() => undefined} />
            )}
            {scenario === "resizing" ? (
              <>
                <ChatNotice kind="notice" by={DUDE} at={decidedAt} data-testid="approved-notice"
                  text="Márcio approved Large for the implementer. It is moving now, with its conversation and uncommitted work; later implementer and fixer Runs in this task run on Large." />
                <MoveProgress verb="Moving to Large" steps={moveSteps(Date.now())} startedAt={decidedAt} defaultOpen
                  avatar={<AgentAvatar role="implementer" size="chat" />} />
              </>
            ) : null}
            {scenario === "declined" ? (
              <ChatNotice kind="notice" by={DUDE} at={decidedAt + 2_000} data-testid="declined-notice"
                text="Told the implementer: declined, with Márcio's note. It carries on on Standard." />
            ) : null}
          </ChatTranscript>
        </div>
      </div>
    </TaskChrome>
  );
}

export function TaskOverview() {
  const [reset, setReset] = useState(false);
  return (
    <TaskChrome tab="overview">
      <div className="taskOverview">
        <div className="taskMain">
          <section className="taskBlock" aria-label="Goal">
            <h2 className="ds-label">Goal</h2>
            <div className="taskGoal"><Markdown source="Exporting a large customer's invoices times out. Stream the CSV from the database instead of building it in memory, so Globex's 2.1M invoices export in under a minute." breaks /></div>
          </section>
          <section className="taskBlock" aria-label="Pipeline">
            <h2 className="ds-label">Pipeline</h2>
            <StepList data-testid="pipeline">
              <StepRow avatar={<AgentAvatar role="implementer" size="md" />} label="Implement" status={<StatusMark status="completed" size="sm" />}
                meta="Large ↑" metaTitle="Large, raised for this task" duration="52m" onOpen={() => undefined} />
              <StepRow avatar={<AgentAvatar role="reviewer" size="md" />} label="Review · correctness, performance" status={<StatusMark status="completed" size="sm" />}
                meta="Standard" duration="9m" onOpen={() => undefined} />
              <StepRow avatar={<AgentAvatar role="implementer" size="md" />} label="Fix · 2 findings" status={<StatusMark status="running" size="sm" />}
                meta="Large ↑" metaTitle="Large: the fixer follows the implementer" duration="6m" onOpen={() => undefined} />
            </StepList>
          </section>
        </div>
        <aside className="taskAside" aria-label="Machines">
          <MachineRaises where="raised in this task"
            raises={reset ? [] : [
              { role: "implementer", label: "Implementer", size: large, settings: standard, onReset: () => setReset(true),
                detail: <>Asked by {TASK.run} (jest OOM-killed at 7.9 of 8 GiB) · approved by Márcio, 1h ago</> },
              { role: "implementer", label: "Fixer", size: large, settings: standard,
                detail: <>Follows the implementer: it names no size of its own</> },
            ]} />
          {reset ? <Callout tone="neutral">Implementer and fixer back to Standard, billing's size for them. The fixer running now keeps Large until it ends.</Callout> : null}
        </aside>
      </div>
    </TaskChrome>
  );
}
