/*
 * A later Run of the task (the fixer) as RunScreen heads it, and a
 * brainstorm session as SessionScreen lays it out, asking its owner.
 */

import { useRef } from "react";
import {
  ChatAside, ChatComposer, ChatMessage, ChatTranscript, MachineChip, MachineTip, ScreenHeader, SessionFacts, SessionHeader, SessionPeople, SessionRail,
  SessionRailBlock, SessionTitle, TierChip, ToolCallCard,
} from "@dude/design-system/components";
import { Button } from "@dude/design-system/primitives";
import { MachineRequestCard, RaisedMachineChip } from "./components.tsx";
import { P, SESSION, TASK, size, specOf } from "./data.ts";
import s from "./bigmachine.module.css";

const MIN = 60_000;
const standard = size("Standard");
const large = size("Large");
const small = size("Small");

export function RunPage() {
  const t0 = useRef(Date.now()).current;
  return (
    <div className="screen sessionScreen" data-testid="run-screen">
      <SessionHeader
        session={{
          id: "run_9b2m", role: "implementer", status: "running", owner: P["marcio"], title: "Fix · 2 findings", taskKey: TASK.key,
          startedAt: t0 - 6 * MIN, costUsd: 0.41, tokens: 182_000,
          subtitle: (
            <>
              <span>for Márcio</span>
              <TierChip tier="Claude (High)" model="claude-opus-5-5" effort="high" harness="Claude Code" />
              <RaisedMachineChip size={large} spec={specOf(large)} raised="for this task"
                tooltip={
                  <MachineTip name="Large">
                    Raised for this task: {TASK.run} (the implementer) asked for it 1h ago, “jest OOM-killed at 7.9 of 8 GiB”, and Márcio approved. The fixer
                    follows the implementer, so it runs on Large too; billing's settings name Standard. Reset on the task's Overview puts later Runs back on
                    Standard; this one keeps Large until it ends.
                  </MachineTip>
                } />
              <MachineChip icon="cube" name="acme-node" spec="v7" />
              <code>{TASK.key}</code>
            </>
          ),
        }}
        actions={<><Button size="sm" variant="quiet" leadingIcon="pause">Pause</Button><Button size="sm" variant="danger">Abort</Button></>}
      />
      <div className="runScreen" data-view="chat">
        <div className="runView">
          <div className="runChat">
            <ChatTranscript fill live revision={3} turns={3}
              footer={<ChatComposer mode="steer" onSubmit={() => true} sentAs="Márcio" landsHint="Lands at the agent's next step" />}>
              <ChatMessage role="system" intent="prompt" name="His Dudeness" startedAt={t0 - 6 * MIN}
                content={"Fix the review's 2 open findings on `dude/BILL-42/attempt-1`.\n\nYou run on Large (4 CPUs · 16 GiB · 40 GiB), raised for this task after the implementer's jest run ran out of memory. If you run out too, ask with `dude machine request`."} />
              <ChatMessage role="implementer" startedAt={t0 - 5 * MIN} costUsd={0.18}
                content="Both findings are in `stream.ts`: the cursor is not closed on a client abort, and the header row is written twice on retry. Fixing both, then the full suite." />
              <ChatAside>
                <ToolCallCard name="bash" status="running" summary={<code>npx jest --maxWorkers 4 packages/backend</code>} startedAt={t0 - 70_000} />
              </ChatAside>
            </ChatTranscript>
          </div>
        </div>
      </div>
    </div>
  );
}

export function SessionPage({ onDecide }: { onDecide: (approve: boolean) => void }) {
  const t0 = useRef(Date.now()).current;
  return (
    <div className="screen sessionScreen" data-testid="session-screen">
      <ScreenHeader fillTitle title={<SessionTitle title={SESSION.title} />}
        meta={<span className={s.meta}><span>Brainstorm · Claude (High) · Claude Code</span><MachineChip name="Small" spec={specOf(small)} /></span>}
        actions={<Button size="sm" variant="secondary">Share</Button>} />
      <div className="runScreen" data-view="chat">
        <div className="runView">
          <div className="runChat">
            <ChatTranscript fill live revision={3} turns={4}
              footer={<ChatComposer mode="chat" placeholder="Message the brainstorm…" onSubmit={() => true} to={<>To <b>Brainstorm</b></>}
                waitingFor="Brainstorm" waitingKey="mreq_s1" />}>
              <ChatMessage role="human" person={P["ana"]} name="Ana Ribeiro" intent="message" startedAt={t0 - 12 * MIN}
                content="How much would it cost to keep seven years of invoices in the archive? Use last year's as the sample." />
              <ChatMessage role="brainstorm" startedAt={t0 - 11 * MIN} costUsd={0.09}
                content="I'll load last year's invoices from the parquet export and measure what they take compressed." />
              <ChatAside>
                <ToolCallCard name="bash" status="failed" summary={<code>duckdb -c "CREATE TABLE inv AS SELECT * FROM 'invoices-2025.parquet'"</code>}
                  error="Out of Memory Error: could not allocate block of 262144 bytes (3.7 GiB/3.7 GiB used)" exitCode={1} durationMs={48_000} />
              </ChatAside>
              <MachineRequestCard role="brainstorm" who="Brainstorm" from={small} to={standard} peakGiB={3.9} showCost reason={SESSION.reason}
                askedAt={t0 - 2 * MIN} waitingOn="Ana" onlyThey onDecide={(a) => onDecide(a)} />
            </ChatTranscript>
            <SessionRail className="runRail" aria-label="The session">
              <SessionRailBlock label="People">
                <SessionPeople members={[{ person: P["ana"]!, role: "owner" }, { person: P["marcio"]!, role: "chat", open: true, you: true }]} />
              </SessionRailBlock>
              <SessionRailBlock label="Machine">
                <SessionFacts facts={[{ label: "Runs on", value: "Small" }, { label: "Asked for", value: "Standard" }, { label: "Decides", value: "Ana" }]} />
              </SessionRailBlock>
            </SessionRail>
          </div>
        </div>
      </div>
    </div>
  );
}
