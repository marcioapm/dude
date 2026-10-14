/*
 * The screens, as the app lays them out: a Run's session (RunScreen's
 * header, transcript and rail), a task's Chat with its conductor
 * (ChatSection over RunScreen's chat variant) and a brainstorm session
 * (SessionScreen). Every turn is the app's own renderTurn, from the fold
 * of each state's ledger: today's, or the proposed one.
 */

import { Fragment, type ReactNode } from "react";
import {
  ChatAside, ChatComposer, ChatMessage, ChatTranscript, DeciderLine, MachineChip, MachineTip, ScreenHeader, SessionFacts, SessionHeader,
  SessionPeople, SessionRail, SessionRailBlock, SessionTitle, TaskHistory, TierChip,
} from "@dude/design-system/components";
import { Button } from "@dude/design-system/primitives";
import type { AgentRole, Machine, PersistedEvent, RunStatus } from "@dude/domain";
import type { People } from "../../src/people.tsx";
import { asides, renderTurn } from "../../src/screens/RunScreen.tsx";
import type { Turn } from "../../src/api/conversation.ts";
import dudeSvg from "../../public/dude.svg?url";
import { PEOPLE, SESSION_TITLE, TASK, TASK_ID, spec } from "./data.ts";
import { proposed, today, type ShownTurn } from "./proposed.ts";
import { NOW } from "./events.ts";

/** The task's dude name, as dudeName(task id) gives it. */
export const DUDE = "El Duderino";

const names = new Map<string, string>(Object.values(PEOPLE).map((p) => [p.id, p.name]));
const people: People = {
  you: PEOPLE.marcio.id, me: null, all: [], byId: new Map(), names,
  refresh: async () => people, seen: () => false,
};

/** dude's face, inlined: the app's DudeMark reads /dude.svg, which a file opened from disk has not. */
const dudeFace = <img src={dudeSvg} alt="" style={{ width: "100%", height: "100%" }} />;

export type Variant = "before" | "after";

/** A turn as the app renders it; dude's prompt and told turns wear the inlined face. */
function render(turn: ShownTurn, role: AgentRole) {
  if (turn.kind === "told") {
    return <ChatMessage key={turn.id} role="system" name={DUDE} avatar={dudeFace} intent="told" content={turn.text} startedAt={turn.at} data-testid="chat-told" />;
  }
  if (turn.kind === "prompt") {
    return <ChatMessage key={turn.id} role="system" name={DUDE} avatar={dudeFace} intent={turn.briefing ? "briefing" : "prompt"} content={turn.text} startedAt={turn.at} />;
  }
  return renderTurn(turn, role, 200_000, false, people, DUDE);
}

/** The transcript's turns, tools and thoughts grouped into asides as RunScreen groups them. */
function Turns({ turns, role }: { turns: readonly ShownTurn[]; role: AgentRole }) {
  const groups = asides(turns as Turn[]) as Array<ShownTurn | ShownTurn[]>;
  return <>{groups.map((g) => Array.isArray(g)
    ? <ChatAside key={g[0]!.id}>{g.map((t) => render(t, role))}</ChatAside>
    : <Fragment key={g.id}>{render(g, role)}</Fragment>)}</>;
}

function fold(events: readonly PersistedEvent[], variant: Variant, status: RunStatus = "running") {
  return variant === "before" ? today(events, status).turns as ShownTurn[] : proposed(events, status).turns;
}

/** A Run's session: whose agent, what it runs on, its transcript and rail. */
export function RunPage({ events, variant, role, title, machine, raised, runId }: {
  events: readonly PersistedEvent[]; variant: Variant; role: AgentRole; title: string; machine: Machine; raised: boolean; runId: string;
}) {
  const turns = fold(events, variant);
  return (
    <div className="screen sessionScreen" data-testid="run-screen">
      <SessionHeader
        session={{
          id: runId, role, status: "running", owner: PEOPLE.marcio, title, taskKey: TASK.key, taskId: TASK_ID,
          startedAt: NOW - 60 * 60_000, costUsd: 2.31, tokens: 182_000,
          subtitle: <>
            <span>for Márcio</span>
            <TierChip tier="Claude (High)" model="claude-opus-5-5" effort="high" harness="Claude Code" />
            <MachineChip name={machine.name} spec={spec(machine)} raised={raised ? "for this task" : undefined}
              tooltip={<MachineTip name={machine.name}>{raised ? "Asked for by run_7f3k (the implementer): its jest run ran out of memory." : "billing's size for the implementer."}</MachineTip>} />
            <code>{TASK.key}</code>
          </>,
        }}
        actions={<><Button size="sm" variant="quiet" leadingIcon="pause">Pause</Button><Button size="sm" variant="danger">Abort</Button></>}
      />
      <div className="runScreen" data-view="chat">
        <div className="runView">
          <div className="runChat">
            <ChatTranscript fill live revision={events.length} turns={turns.length}
              footer={<ChatComposer mode="steer" onSubmit={() => true} sentAs="Márcio" landsHint="Lands at the agent's next step" />}>
              <Turns turns={turns} role={role} />
            </ChatTranscript>
            <SessionRail className="runRail" aria-label="The session">
              <SessionRailBlock label="Machine">
                <SessionFacts facts={[{ label: "Runs on", value: `${machine.name}${raised ? " ↑" : ""}` }, { label: "Pool", value: machine.pool ?? "default" },
                  { label: "billing's size", value: "Standard" }]} />
              </SessionRailBlock>
            </SessionRail>
          </div>
        </div>
      </div>
    </div>
  );
}

/** BILL-42's Chat with its conductor: the history pinned, the conductor's transcript, who decides, the chat composer. */
export function ConductorChat({ events, variant }: { events: readonly PersistedEvent[]; variant: Variant }) {
  const turns = fold(events, variant);
  return (
    <TaskChrome>
      <div className="taskChat" data-testid="task-chat">
        <div className="runScreen" data-view="chat">
          <div className="runView">
            <div className="runChat">
              <ChatTranscript fill live revision={events.length} turns={turns.length}
                pinned={<TaskHistory lead="The conductor decides" steps={["implementer"]} facts={["no findings yet", "$2.31"]} icon="conductor" />}
                footer={<>
                  <DeciderLine decider="conductor" action={<Button variant="quiet" size="sm">Let Deliver finish it</Button>} />
                  <ChatComposer mode="chat" onSubmit={() => true} sentAs="Márcio" to={<>To <b>Conductor</b></>} />
                </>}>
                <Turns turns={turns} role="conductor" />
              </ChatTranscript>
              <SessionRail className="runRail" aria-label="The conductor">
                <SessionRailBlock label="Conductor">
                  <SessionFacts facts={[{ label: "Model", value: "claude-opus-5-5", mono: true }, { label: "Machine", value: "Large" }]} />
                </SessionRailBlock>
              </SessionRail>
            </div>
          </div>
        </div>
      </div>
    </TaskChrome>
  );
}

function TaskChrome({ children }: { children: ReactNode }) {
  return (
    <div className="screen taskScreen fixed" data-testid="task-screen">
      <ScreenHeader fillTitle title={<span className="taskTitle">{TASK.title}</span>}
        meta={<><code>{TASK.key}</code><span className="ds-mono">{TASK.branch}</span><span>Chat</span></>}
        actions={<Button variant="quiet" leadingIcon="edit">Edit</Button>} />
      {children}
    </div>
  );
}

/** A brainstorm session, as SessionScreen lays it out. */
export function SessionPage({ events, variant, machine }: { events: readonly PersistedEvent[]; variant: Variant; machine: Machine }) {
  const turns = fold(events, variant);
  return (
    <div className="screen sessionScreen" data-testid="session-screen">
      <ScreenHeader fillTitle title={<SessionTitle title={SESSION_TITLE} />}
        meta={<><span>Brainstorm · Claude (High) · Claude Code</span>
          <MachineChip name={machine.name} spec={spec(machine)} raised="for this session"
            tooltip={<MachineTip name={machine.name}>The brainstorm asked for it: DuckDB ran out of memory on Small.</MachineTip>} /></>}
        actions={<><Button size="sm" variant="quiet">Archive</Button><Button size="sm" variant="secondary">Share</Button></>} />
      <div className="runScreen" data-view="chat">
        <div className="runView">
          <div className="runChat">
            <ChatTranscript fill live revision={events.length} turns={turns.length}
              footer={<ChatComposer mode="chat" placeholder="Message the brainstorm…" onSubmit={() => true} sentAs="Márcio · everyone in the session sees it" to={<>To <b>Brainstorm</b></>} />}>
              <Turns turns={turns} role="brainstorm" />
            </ChatTranscript>
            <SessionRail className="runRail" aria-label="The session">
              <SessionRailBlock label="People">
                <SessionPeople members={[{ person: PEOPLE.ana, role: "owner" }, { person: PEOPLE.marcio, role: "chat", open: true, you: true }]} />
              </SessionRailBlock>
              <SessionRailBlock label="Machine">
                <SessionFacts facts={[{ label: "Pool", value: machine.pool ?? "default" }, { label: "Settings' size", value: "Small" }]} />
                <Button size="sm" variant="quiet">Reset</Button>
              </SessionRailBlock>
            </SessionRail>
          </div>
        </div>
      </div>
    </div>
  );
}
