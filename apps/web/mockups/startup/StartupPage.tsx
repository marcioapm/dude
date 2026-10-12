/*
 * The two places an agent starts: a brainstorm session (scenarios 1–5)
 * and a task's Run page (6). Both are the app's screens as they are today
 * (SessionScreen's header, transcript, composer and rail; RunScreen's
 * SessionHeader), with what the proposal adds: StartChip in the header,
 * StartProgress where the agent's next turn will appear, the queued
 * message saying it will be read when the agent starts, and the composer
 * saying the same.
 */

import { useRef } from "react";
import {
  AgentAvatar, Capabilities, ChatComposer, ChatMessage, ChatNotice, ChatTranscript, CostDisplay, LinkedProjects, MachineChip, ScreenHeader,
  SessionFacts, SessionHeader, SessionPeople, SessionRail, SessionRailBlock, SessionTitle, TierChip,
} from "@dude/design-system/components";
import { Button } from "@dude/design-system/primitives";
import { StartChip, StartProgress, type StartStep } from "./components.tsx";
import { P, RUN, SESSION, SLOW_AFTER_MS, SLOW_WORDS, START_STAGES, STAGE_WORDS, type StartState } from "./data.ts";
import s from "./startup.module.css";

/** The words the queued message and the composer share. */
export const READ_WHEN_STARTED = "Will be read when the agent starts.";
const READ_WHEN_RESUMED = "Will be read when the agent is back.";

/** lux's stages as the stepper's steps: what passed with its time, the current one live, the rest to come. */
export function stepsOf(start: StartState): StartStep[] {
  const at = START_STAGES.indexOf(start.stage);
  return START_STAGES.map((stage, i) => {
    const words = STAGE_WORDS[stage];
    const done = start.done.find((d) => d.stage === stage);
    const state = i < at ? "done" : i === at ? (start.failed ? "failed" : "current") : "pending";
    return { id: stage, label: words.now, doneLabel: words.done, state, tookMs: done?.tookMs, since: i === at ? start.stageSince : undefined };
  });
}

function slowNote(start: StartState, now: number) {
  if (start.failed || now - start.stageSince < SLOW_AFTER_MS[start.stage]) return undefined;
  return (
    <>
      {SLOW_WORDS[start.stage]} Your message is kept; nothing is spent meanwhile.
      {start.luxReason ? <span className={s.slowDetail}>lux: {start.luxReason}</span> : null}
    </>
  );
}

function Progress({ start, role, now, open }: { start: StartState; role: "brainstorm" | "implementer"; now: number; open?: boolean | undefined }) {
  const verb = start.kind === "resume" ? "Resuming" : "Starting";
  return (
    <StartProgress
      key={start.failed ? "failed" : "live"}
      verb={verb}
      steps={stepsOf(start)}
      startedAt={start.startedAt}
      avatar={<AgentAvatar role={role} size="chat" />}
      slowNote={slowNote(start, now)}
      failure={start.failed ? { reason: start.failed.reason, onRetry: () => undefined } : undefined}
      defaultOpen={open}
      data-testid="start-progress"
    />
  );
}

function chipOf(start: StartState, bare = false) {
  const step = stepsOf(start).find((st) => st.state === "current" || st.state === "failed")!;
  return <StartChip verb={start.kind === "resume" ? "Resuming" : "Starting"} step={step} bare={bare} data-testid="start-chip" />;
}

/** The parked line and the earlier exchange a resumed session has above the new message. */
function Earlier({ at }: { at: number }) {
  return (
    <>
      <ChatMessage role="human" person={P["marcio"]} name="Márcio Martins" intent="message" content={SESSION.earlier.ask} startedAt={at - 3_700_000} />
      <ChatMessage role="brainstorm" content={SESSION.earlier.answer} startedAt={at - 3_640_000} costUsd={0.21} />
      <ChatNotice kind="parked" by="El Duderino" text="Parked while nobody is writing — nothing is held; your next message resumes it." at={at - 2_980_000} />
    </>
  );
}

export function SessionScreen({ start, now, sentAt, resumed }: {
  /** Where its agent is; null once it runs (scenario 5). */
  start: StartState | null;
  now: number;
  sentAt: number;
  /** The session had an earlier exchange and was parked. */
  resumed: boolean;
}) {
  const at = useRef(Date.now()).current;
  const running = start === null;
  const title = resumed ? SESSION.title : null;
  const failed = Boolean(start?.failed);
  const message = resumed ? SESSION.wake : SESSION.first;
  return (
    <div className="screen sessionScreen" data-testid="session-screen">
      <ScreenHeader fillTitle title={<SessionTitle title={title} onRename={async () => undefined} />}
        meta={<span className={s.meta}><span>Brainstorm · Claude (High) · Claude Code</span>{start ? chipOf(start) : null}</span>}
        actions={<><Button size="sm" variant="quiet">Archive</Button><Button size="sm" variant="secondary">Share</Button></>} />
      <div className="runScreen" data-view="chat">
        <div className="runView">
          <div className="runChat">
            <ChatTranscript fill live revision={running ? 4 : 2} turns={running ? 4 : 2}
              footer={
                <ChatComposer mode="chat" placeholder="Message the brainstorm…" onSubmit={() => true}
                  to={<>To <b>Brainstorm</b></>}
                  toAside={start && !failed ? <span className="muted" data-testid="composer-start-hint">· {resumed ? READ_WHEN_RESUMED : READ_WHEN_STARTED}</span> : null} />
              }>
              {resumed ? <Earlier at={at} /> : null}
              <ChatMessage role="human" person={P["marcio"]} name="Márcio Martins" intent="message" content={message} startedAt={sentAt}
                {...(running ? { deliveredAt: sentAt + 14_000, read: true }
                  : failed ? { deliveredAt: null, failed: "the agent did not start. Try again starts it with this message." }
                  : { deliveredAt: null, pendingReason: resumed ? READ_WHEN_RESUMED : READ_WHEN_STARTED })} />
              {running ? (
                <>
                  <ChatNotice kind="unparked" by="El Duderino" text="Taken back up in 14s." at={sentAt + 14_000} data-testid="resumed-line"
                    title={[`dude asked lux 0.2s`, `${STAGE_WORDS.waiting.done} 9.4s`, `${STAGE_WORDS.image.done} 1.2s`, `${STAGE_WORDS.volumes.done} 8.6s`,
                      `${STAGE_WORDS.repositories.done} 1.9s`, `${STAGE_WORDS.container.done} 1.4s`, "agent reloaded 0.9s", "first words 2.1s"].join("\n")} />
                  <ChatMessage role="brainstorm" content={SESSION.reply} startedAt={sentAt + 16_000} streaming activity="streaming" />
                </>
              ) : <Progress start={start} role="brainstorm" now={now} open={resumed && !failed ? true : undefined} />}
            </ChatTranscript>
            <SessionRail className="runRail" aria-label="The session">
              <SessionRailBlock label={<span className="sessionRailHead">People<Button size="sm" variant="quiet">Share</Button></span>}>
                <SessionPeople members={[{ person: P["marcio"]!, role: "owner", open: true, you: true }, ...(resumed ? [{ person: P["ana"]!, role: "chat" as const }] : [])]} />
              </SessionRailBlock>
              <SessionRailBlock label={<span className="sessionRailHead">Linked<Button size="sm" variant="quiet">Link</Button></span>}>
                <LinkedProjects projects={SESSION.linked} />
              </SessionRailBlock>
              <SessionRailBlock label="It can">
                <Capabilities can={["Read the linked projects' code, tasks, pull requests and findings", "Propose epics, tasks, edits and comments · members file them"]}
                  cannot={["Change code, push, start or steer work", "Read projects nobody linked"]} />
              </SessionRailBlock>
              <SessionRailBlock label="Cost"><SessionFacts facts={[{ label: "Session", value: <CostDisplay usd={resumed ? 0.21 : 0} /> }]} /></SessionRailBlock>
            </SessionRail>
          </div>
        </div>
      </div>
    </div>
  );
}

export function RunScreen({ start, now }: { start: StartState; now: number }) {
  const at = useRef(Date.now()).current;
  return (
    <div className="screen sessionScreen" data-testid="run-screen">
      <SessionHeader
        session={{
          id: "run_bill212_impl",
          role: "implementer",
          status: "starting",
          owner: P["ana"],
          title: "Implement",
          taskKey: RUN.key,
          startedAt: start.startedAt,
          costUsd: 0,
          tokens: 0,
          statusNote: chipOf(start, true),
          subtitle: (
            <>
              <span>for Ana</span>
              <TierChip tier="Claude (High)" model="claude-opus-5-5" effort="high" harness="Claude Code" />
              <MachineChip name="Large" spec="8 CPUs · 16 GiB · 80 GiB" />
              <MachineChip icon="cube" name="acme-node" spec="v7" />
              <code>{RUN.key}</code>
            </>
          ),
        }}
        actions={<><Button size="sm" variant="quiet" leadingIcon="pause" disabled>Pause</Button><Button size="sm" variant="danger">Abort</Button></>}
      />
      <div className="runScreen" data-view="chat">
        <div className="runView">
          <div className="runChat">
            <ChatTranscript fill live revision={3} turns={3}
              footer={<ChatComposer mode="steer" onSubmit={() => true} sentAs="Ana" landsHint={READ_WHEN_STARTED.replace(/\.$/, "")} />}>
              <ChatMessage role="system" intent="prompt" name="His Dudeness" content={RUN.prompt} startedAt={start.startedAt} />
              <ChatMessage role="human" person={P["ana"]} name="Ana Ribeiro" intent="steer" content={RUN.steer} startedAt={at - 9_000}
                deliveredAt={null} pendingReason={READ_WHEN_STARTED} />
              <Progress start={start} role="implementer" now={now} />
            </ChatTranscript>
          </div>
        </div>
      </div>
    </div>
  );
}
