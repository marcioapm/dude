/*
 * BILL-42's task page as TaskScreen lays it out (breadcrumb, head, tabs),
 * with its Chat (ChatSection's transcript: the conductor, the
 * implementer's run line, its request_machine call, dude's notice and the
 * move) and its Overview (the pipeline, and the machines raised in the
 * aside, with Reset and its confirmation).
 */

import { useRef, useState, type ReactNode } from "react";
import {
  AgentAvatar, Breadcrumb, ChatAside, ChatComposer, ChatMessage, ChatNotice, ChatRunLine, ChatTranscript, DeciderLine, Markdown, PersonLine, StatusMark,
  StepList, StepRow, TaskHistory,
} from "@dude/design-system/components";
import { Button, Callout, Tab, TabList, TabPanel, Tabs } from "@dude/design-system/primitives";
import { MachineRaises, MachineRequestCall, ResetMachineConfirm, StartProgress, startSteps } from "./components.tsx";
import {
  ANSWER_CONDUCTOR, ANSWER_LARGE, ANSWER_XL, MOVE_STAGES, P, PROJECT, REASON_CONDUCTOR, REASON_LARGE, REASON_XL, RESET_NOTE, START_STAGES, TASK, size,
} from "./data.ts";

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

const WHAT = "Implement: stream the CSV export instead of building it in memory";

/** Task Chat: the implementer moves, in its pool (resized in place) or to another (a fresh Run). */
export function TaskChat({ otherPool }: { otherPool: boolean }) {
  const t0 = useRef(Date.now()).current;
  const asked = t0 - 50_000;
  return (
    <TaskChrome tab="chat">
      <div className="taskChat" data-testid="task-chat">
        <div className="runScreen" data-view="chat">
          <ChatTranscript fill live revision={4} turns={6}
            pinned={<TaskHistory lead="The conductor decides" steps={["implementer"]} facts={["no findings yet", "$1.84"]} icon="conductor" />}
            footer={
              <>
                <DeciderLine decider="conductor" action={<Button variant="quiet" size="sm">Let Deliver finish it</Button>} />
                <ChatComposer mode="chat" onSubmit={() => true} sentAs="Márcio" to={<>To <b>Conductor</b></>} />
              </>
            }>
            <ChatMessage role="human" person={P["marcio"]} name="Márcio Martins" intent="message" startedAt={t0 - 41 * MIN}
              content="The export times out for Globex (2.1M invoices). Stream it instead of building the CSV in memory, and keep the PDF path as it is." />
            <ChatMessage role="conductor" startedAt={t0 - 40 * MIN} costUsd={0.12}
              content="Agreed. I'll start the implementer on streaming the CSV through `pg-cursor`; the PDF renderer stays. It runs the backend suite before it pushes." />
            {otherPool ? (
              <>
                <ChatRunLine data-testid="chat-run" role="implementer" status="aborted" statusLabel="Replaced" what={WHAT}
                  facts={["Standard", "38m", "$1.72"]} onOpen={() => undefined} />
                <ChatAside>
                  <MachineRequestCall from={standard} to={xl} reason={REASON_XL} answer={ANSWER_XL} startedAt={asked} durationMs={9_800} defaultExpanded />
                </ChatAside>
                <ChatNotice kind="restart" by={DUDE} at={asked + 10_000} data-testid="move-notice"
                  text={`XL is in another pool, so a fresh implementer starts on XL from ${TASK.commit}, which the move pushed, with the reason ${TASK.run} gave as its note. Later implementer Runs in this task run on XL too.`} />
                <ChatRunLine role="implementer" status="starting" statusLabel="Starting" what={WHAT} facts={["XL", TASK.fresh]} onOpen={() => undefined} />
                <StartProgress verb="Starting" steps={startSteps(START_STAGES, 1, Date.now(), [21_400])} startedAt={asked + 10_000} defaultOpen
                  avatar={<AgentAvatar role="implementer" size="chat" />} />
              </>
            ) : (
              <>
                <ChatRunLine data-testid="chat-run" role="implementer" status="starting" statusLabel="Moving to Large" what={WHAT}
                  facts={["Large", "38m", "$1.72"]} onOpen={() => undefined} />
                <ChatAside>
                  <MachineRequestCall from={standard} to={large} reason={REASON_LARGE} answer={ANSWER_LARGE} startedAt={asked} durationMs={700} defaultExpanded />
                </ChatAside>
                <ChatNotice kind="notice" by={DUDE} at={asked + 1_000} data-testid="move-notice"
                  text="Moving the implementer to Large: it keeps its conversation and work. Later implementer Runs in this task run on Large too." />
                <StartProgress verb="Moving to Large" steps={startSteps(MOVE_STAGES, 2, Date.now(), [6_400, 11_200])} startedAt={asked + 1_000} defaultOpen
                  avatar={<AgentAvatar role="implementer" size="chat" />} />
              </>
            )}
          </ChatTranscript>
        </div>
      </div>
    </TaskChrome>
  );
}

/** Task Chat: the conductor itself moves, in its pool, resized in place with the Chat's conversation. */
export function ConductorChat() {
  const t0 = useRef(Date.now()).current;
  const asked = t0 - 40_000;
  return (
    <TaskChrome tab="chat">
      <div className="taskChat" data-testid="task-chat">
        <div className="runScreen" data-view="chat">
          <ChatTranscript fill live revision={4} turns={6}
            pinned={<TaskHistory lead="The conductor decides" steps={["implementer"]} facts={["no findings yet", "$2.31"]} icon="conductor" />}
            footer={
              <>
                <DeciderLine decider="conductor" action={<Button variant="quiet" size="sm">Let Deliver finish it</Button>} />
                <ChatComposer mode="chat" onSubmit={() => true} sentAs="Márcio" to={<>To <b>Conductor</b></>} />
              </>
            }>
            <ChatRunLine role="implementer" status="completed" what={WHAT} facts={["Standard", "44m", "$1.96"]} onOpen={() => undefined} />
            <ChatMessage role="human" person={P["marcio"]} name="Márcio Martins" intent="message" startedAt={t0 - 3 * MIN}
              content="Before it goes to review, run the whole backend suite yourself with the workers CI uses (4). I want to see it green on this branch." />
            <ChatMessage role="conductor" startedAt={t0 - 2 * MIN} costUsd={0.08}
              content="With 4 workers the suite renders PDFs in each of them; on Standard (8 GiB) that is what OOM-killed the implementer. Asking for Large first, then I run it." />
            <ChatAside>
              <MachineRequestCall from={standard} to={large} reason={REASON_CONDUCTOR} answer={ANSWER_CONDUCTOR} startedAt={asked} durationMs={600} defaultExpanded />
            </ChatAside>
            <ChatNotice kind="notice" by={DUDE} at={asked + 1_000} data-testid="move-notice"
              text="Moving the conductor to Large: it keeps this conversation and its checkout. Later conductors in this task run on Large too." />
            <StartProgress verb="Moving to Large" steps={startSteps(MOVE_STAGES, 3, Date.now(), [5_900, 10_300, 7_800])} startedAt={asked + 1_000} defaultOpen
              avatar={<AgentAvatar role="conductor" size="chat" />} />
          </ChatTranscript>
        </div>
      </div>
    </TaskChrome>
  );
}

/** The Overview's machines; `confirm` opens Reset's confirmation, `onXL` puts the implementer on XL. */
export function TaskOverview({ confirm, onXL }: { confirm?: boolean; onXL?: boolean }) {
  const [open, setOpen] = useState(confirm ?? false);
  const [reset, setReset] = useState(false);
  const raised = onXL ? xl : large;
  const reason = onXL ? "the 2.1M-row fixture's sort needs about 20 GiB" : "jest OOM-killed with --maxWorkers 4 on 8 GiB";
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
              <StepRow avatar={<AgentAvatar role="implementer" size="md" />} label="Implement" status={<StatusMark status={reset ? "starting" : "running"} size="sm" />}
                meta={reset ? "Standard" : `${raised.name} ↑`} metaTitle={reset ? "Standard, billing's size" : `${raised.name}, raised for this task`} duration="52m" onOpen={() => undefined} />
              <StepRow avatar={<AgentAvatar role="reviewer" size="md" />} label="Review" status={<StatusMark status="pending" size="sm" />}
                meta="Standard" onOpen={() => undefined} />
            </StepList>
          </section>
        </div>
        <aside className="taskAside" aria-label="Machines">
          <MachineRaises where="raised in this task"
            raises={reset ? [] : [
              { role: "implementer", label: "Implementer", size: raised, settings: standard, onReset: () => setOpen(true),
                detail: <>Asked for by {onXL ? TASK.fresh : TASK.run}, 1h ago: {reason}</> },
              { role: "implementer", label: "Fixer", size: raised, settings: standard,
                detail: <>Follows the implementer: it names no size of its own</> },
            ]} />
          {reset ? (
            <Callout tone="neutral" data-testid="reset-done">
              {onXL ? "A fresh implementer is starting on Standard from what the last one pushed, with your note."
                : "The implementer is moving back to Standard, keeping its work, with your note."} Later implementer and fixer Runs in this task run on billing's Standard.
            </Callout>
          ) : null}
        </aside>
      </div>
      <ResetMachineConfirm open={open} onOpenChange={setOpen} label="implementer" from={raised} to={standard} defaultNote={RESET_NOTE} onConfirm={() => setReset(true)} />
    </TaskChrome>
  );
}
