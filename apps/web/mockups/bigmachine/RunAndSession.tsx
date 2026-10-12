/*
 * A Run of the task as RunScreen heads it (the fixer later on, the
 * implementer refused, attempt 2's first implementer after Start over), and
 * a brainstorm session as SessionScreen lays it out, moving across pools by
 * a fresh agent that takes the conversation over.
 */

import { useRef, type ReactNode } from "react";
import {
  ChatAside, ChatComposer, ChatMessage, ChatNotice, ChatTranscript, MachineChip, MachineTip, ScreenHeader, SessionFacts, SessionHeader, SessionPeople,
  SessionRail, SessionRailBlock, SessionTitle, TierChip, ToolCallCard,
} from "@dude/design-system/components";
import { Button } from "@dude/design-system/primitives";
import { MachineRequestCall, RaisedMachineChip } from "./components.tsx";
import { P, REASON_XL, REFUSED_LARGEST, REFUSED_UNPUSHED, SESSION, TASK, size, specOf, type Size } from "./data.ts";
import s from "./bigmachine.module.css";

const MIN = 60_000;
const standard = size("Standard");
const large = size("Large");
const xl = size("XL");
const small = size("Small");

export type RunView = "fixer" | "refused" | "refused-largest" | "start-over";

function raisedTip(on: Size, extra: ReactNode) {
  return (
    <MachineTip name={on.name}>
      Asked for by {TASK.run} (the implementer), 1h ago: “The backend jest suite was OOM-killed (exit 137) with --maxWorkers 4.” {extra} billing's settings
      name Standard; Reset is on the task's Overview.
    </MachineTip>
  );
}

const HEADS: Record<RunView, { id: string; title: string; status: "running" | "failed"; machine: ReactNode; started: number; cost: number }> = {
  fixer: {
    id: "run_9b2m", title: "Fix · 2 findings", status: "running", started: 6, cost: 0.41,
    machine: <RaisedMachineChip size={large} spec={specOf(large)} raised="for this task"
      tooltip={raisedTip(large, "The fixer follows the implementer, so it runs on Large too.")} />,
  },
  "start-over": {
    id: "run_c4n7", title: "Implement · attempt 2", status: "running", started: 3, cost: 0.22,
    machine: <RaisedMachineChip size={large} spec={specOf(large)} raised="for this task"
      tooltip={raisedTip(large, "Start over keeps the task, so attempt 2 runs on Large too.")} />,
  },
  refused: {
    id: TASK.run, title: "Implement", status: "running", started: 38, cost: 1.72,
    machine: <MachineChip name="Standard" spec={specOf(standard)} tooltip={<MachineTip name="Standard">billing's size for the implementer.</MachineTip>} data-testid="run-machine" />,
  },
  "refused-largest": {
    id: TASK.fresh, title: "Implement", status: "running", started: 20, cost: 0.96,
    machine: <RaisedMachineChip size={xl} spec={specOf(xl)} raised="for this task"
      tooltip={<MachineTip name="XL">Asked for by {TASK.run} (the implementer), 1h ago: “the full fixture's sort needs about 20 GiB.” It runs in the highmem pool. billing's settings name Standard; Reset is on the task's Overview.</MachineTip>} />,
  },
};

export function RunPage({ view }: { view: RunView }) {
  const t0 = useRef(Date.now()).current;
  const h = HEADS[view];
  return (
    <div className="screen sessionScreen" data-testid="run-screen">
      <SessionHeader
        session={{
          id: h.id, role: "implementer", status: h.status, owner: P["marcio"], title: h.title, taskKey: TASK.key,
          startedAt: t0 - h.started * MIN, costUsd: h.cost, tokens: 182_000,
          subtitle: (
            <>
              <span>for Márcio</span>
              <TierChip tier="Claude (High)" model="claude-opus-5-5" effort="high" harness="Claude Code" />
              {h.machine}
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
              {view === "fixer" || view === "start-over" ? <Later view={view} t0={t0} /> : <Refused view={view} t0={t0} />}
            </ChatTranscript>
          </div>
        </div>
      </div>
    </div>
  );
}

function Later({ view, t0 }: { view: "fixer" | "start-over"; t0: number }) {
  const fixer = view === "fixer";
  return (
    <>
      <ChatMessage role="system" intent="prompt" name="His Dudeness" startedAt={t0 - 6 * MIN}
        content={(fixer ? "Fix the review's 2 open findings on `dude/BILL-42/attempt-1`." : "Start over on `dude/BILL-42/attempt-2`: Márcio wants the export streamed from a server-side cursor, not paged.")
          + "\n\nYou run on Large (4 CPUs · 16 GiB · 40 GiB), raised for this task after the implementer's jest run ran out of memory. You may ask for XL (8 · 32, another pool: a fresh implementer would start from what you pushed) with `dude machine request XL --reason \"…\"`, saying what ran out."} />
      <ChatMessage role="implementer" startedAt={t0 - 5 * MIN} costUsd={0.18}
        content={fixer ? "Both findings are in `stream.ts`: the cursor is not closed on a client abort, and the header row is written twice on retry. Fixing both, then the full suite."
          : "Replacing the paged query with a `DECLARE … CURSOR` in a read-only transaction. The full suite runs with 4 workers: this machine has room for them."} />
      <ChatAside>
        <ToolCallCard name="bash" status="running" summary={<code>npx jest --maxWorkers 4 packages/backend</code>} startedAt={t0 - 70_000} />
      </ChatAside>
    </>
  );
}

function Refused({ view, t0 }: { view: "refused" | "refused-largest"; t0: number }) {
  const largest = view === "refused-largest";
  return (
    <>
      <ChatMessage role="implementer" startedAt={t0 - 4 * MIN} costUsd={0.21}
        content={largest ? "The chunked sort still holds the whole index: 2.1M rows × the sort key. Loading the full fixture again to measure it."
          : "The sort passes on the 100k fixture. The full 2.1M-row fixture needs about 20 GiB; Large would not fit it, so XL."} />
      <ChatAside>
        <ToolCallCard name="bash" status="failed" summary={<code>npx jest --maxWorkers 1 export/full-fixture</code>} exitCode={137}
          error="Killed" defaultExpanded={false} durationMs={largest ? 212_000 : 96_000} />
        <MachineRequestCall from={largest ? xl : standard} to={largest ? "a larger one" : xl} refused fresh={false}
          reason={largest ? "The full fixture's sort was OOM-killed at 32 GiB." : REASON_XL}
          answer={largest ? REFUSED_LARGEST : REFUSED_UNPUSHED} startedAt={t0 - 50_000} durationMs={1_400} defaultExpanded />
      </ChatAside>
      <ChatMessage role="implementer" startedAt={t0 - 40_000} costUsd={0.06}
        content={largest ? "No larger size. Sorting in 8 chunks of 270k rows and merging the runs on disk; that keeps the peak under 12 GiB."
          : "Committing the test and pushing both commits first, then asking again."} />
      {largest ? null : (
        <ChatAside>
          <ToolCallCard name="bash" status="completed" summary={<code>git add -A && git commit -m "Test the sort on the full fixture"</code>} durationMs={600} />
          <ToolCallCard name="push" icon="git-branch" status="running" summary={<>2 commits to <code>{TASK.runBranch}</code></>} startedAt={t0 - 6_000} />
        </ChatAside>
      )}
    </>
  );
}

const BEFORE = (t0: number) => (
  <>
    <ChatMessage role="human" person={P["ana"]} name="Ana Ribeiro" intent="message" startedAt={t0 - 14 * MIN}
      content="How much would it cost to keep seven years of invoices in the archive? Use last year's, with their lines, as the sample." />
    <ChatMessage role="brainstorm" startedAt={t0 - 13 * MIN} costUsd={0.09}
      content="I'll load last year's invoices and their lines from the parquet export into DuckDB and measure what they take compressed." />
    <ChatAside>
      <ToolCallCard name="bash" status="failed" summary={<code>duckdb archive.db -c "CREATE TABLE inv AS SELECT * FROM 'invoices-2025.parquet' JOIN 'lines-2025.parquet' USING (id)"</code>}
        error="Out of Memory Error: could not allocate block of 262144 bytes (3.7 GiB/3.7 GiB used)" exitCode={1} durationMs={48_000} />
      <MachineRequestCall from={small} to={xl} reason={SESSION.reason} startedAt={t0 - 3 * MIN} durationMs={800}
        answer="XL is in another pool: you will be replaced by a fresh brainstorm on XL with this conversation; files you wrote here (archive.db) are not kept. End your turn now." />
    </ChatAside>
  </>
);

export function SessionPage() {
  const t0 = useRef(Date.now()).current;
  return (
    <div className="screen sessionScreen" data-testid="session-screen">
      <ScreenHeader fillTitle title={<SessionTitle title={SESSION.title} />}
        meta={<span className={s.meta}><span>Brainstorm · Claude (High) · Claude Code</span>
          <RaisedMachineChip size={xl} spec={specOf(xl)} raised="for this session"
            tooltip={<MachineTip name="XL">The brainstorm asked for it 2m ago: “joining last year's invoices with their lines needs about 18 GiB.” XL is in the highmem pool, so a fresh brainstorm took over with the conversation. Acme's settings name Small.</MachineTip>} /></span>}
        actions={<Button size="sm" variant="secondary">Share</Button>} />
      <div className="runScreen" data-view="chat">
        <div className="runView">
          <div className="runChat">
            <ChatTranscript fill live revision={3} turns={5}
              footer={<ChatComposer mode="chat" placeholder="Message the brainstorm…" onSubmit={() => true} to={<>To <b>Brainstorm</b></>} />}>
              {BEFORE(t0)}
              <ChatNotice kind="restart" at={t0 - 2 * MIN} data-testid="takeover-notice"
                text="The brainstorm asked for XL, in another pool: started a fresh agent on XL; it has the conversation so far. Files the last one wrote (archive.db) were not kept." />
              <ChatMessage role="brainstorm" startedAt={t0 - 90_000} costUsd={0.11}
                content="Picking up on XL. The earlier agent's `archive.db` did not come with me, so I'm loading the two parquet files again, joined, and then measuring the compressed size per year." />
              <ChatAside>
                <ToolCallCard name="bash" status="running" startedAt={t0 - 50_000}
                  summary={<code>duckdb archive.db -c "CREATE TABLE inv AS SELECT * FROM 'invoices-2025.parquet' JOIN 'lines-2025.parquet' USING (id)"</code>} />
              </ChatAside>
            </ChatTranscript>
            <SessionRail className="runRail" aria-label="The session">
              <SessionRailBlock label="People">
                <SessionPeople members={[{ person: P["ana"]!, role: "owner" }, { person: P["marcio"]!, role: "chat", open: true, you: true }]} />
              </SessionRailBlock>
              <SessionRailBlock label="Machine">
                <SessionFacts facts={[{ label: "Runs on", value: "XL ↑" }, { label: "Pool", value: "highmem" }, { label: "Acme's size", value: "Small" }]} />
              </SessionRailBlock>
            </SessionRail>
          </div>
        </div>
      </div>
    </div>
  );
}
