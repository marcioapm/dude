/*
 * A Run of the task as RunScreen heads it (the fixer later on, the tester
 * refused by lux's platform check, the implementer refused at the largest
 * size or past 3 moves, attempt 2's first implementer after Start over),
 * and a brainstorm session as SessionScreen lays it out, moving to another
 * pool like any Run: parked, then resumed there with its files.
 */

import { useRef, type ReactNode } from "react";
import {
  ChatAside, ChatComposer, ChatMessage, ChatNotice, ChatTranscript, MachineChip, MachineTip, ScreenHeader, SessionFacts, SessionHeader, SessionPeople,
  SessionRail, SessionRailBlock, SessionTitle, TierChip, ToolCallCard,
} from "@dude/design-system/components";
import { Button } from "@dude/design-system/primitives";
import { MachineRequestCall, RaisedMachineChip } from "./components.tsx";
import {
  ANSWER_SESSION, ANSWER_TESTER_LARGE, P, REASON_LARGE, REASON_TESTER, REASON_TESTER_LARGE, REFUSED_LARGEST, REFUSED_MOVES, REFUSED_PLATFORM, SESSION, TASK, size, specOf, type Size,
} from "./data.ts";
import s from "./bigmachine.module.css";

const MIN = 60_000;
const standard = size("Standard");
const large = size("Large");
const xl = size("XL");
const small = size("Small");

export type RunView = "fixer" | "refused-platform" | "refused-largest" | "refused-moves" | "start-over";
type RefusedView = "refused-platform" | "refused-largest" | "refused-moves";

function raisedTip(on: Size, extra: ReactNode) {
  return (
    <MachineTip name={on.name}>
      Asked for by {TASK.run} (the implementer), 1h ago: “The backend jest suite was OOM-killed (exit 137) with --maxWorkers 4.” {extra} billing's settings
      name Standard; Reset is on the task's Overview.
    </MachineTip>
  );
}

const HEADS: Record<RunView, { id: string; role: "implementer" | "qa_browser"; title: string; status: "running" | "aborted"; machine: ReactNode; started: number; cost: number }> = {
  fixer: {
    id: "run_9b2m", role: "implementer", title: "Fix · 2 findings", status: "running", started: 6, cost: 0.41,
    machine: <RaisedMachineChip size={large} spec={specOf(large)} raised="for this task"
      tooltip={raisedTip(large, "The fixer follows the implementer, so it runs on Large too.")} />,
  },
  "start-over": {
    id: "run_c4n7", role: "implementer", title: "Implement · attempt 2", status: "running", started: 3, cost: 0.22,
    machine: <RaisedMachineChip size={large} spec={specOf(large)} raised="for this task"
      tooltip={raisedTip(large, "Start over keeps the task, so attempt 2 runs on Large too.")} />,
  },
  "refused-platform": {
    id: "run_t5q1", role: "qa_browser", title: "Test · the export page", status: "running", started: 12, cost: 0.38,
    machine: <MachineChip name="Standard" spec={specOf(standard)} tooltip={<MachineTip name="Standard">billing's size for the tester.</MachineTip>} data-testid="run-machine" />,
  },
  "refused-moves": {
    id: "run_m3d8", role: "implementer", title: "Implement", status: "running", started: 71, cost: 2.64,
    machine: <MachineChip name="Standard" spec={specOf(standard)} tooltip={<MachineTip name="Standard">billing's size for the implementer; Márcio reset it from Large 12m ago.</MachineTip>} data-testid="run-machine" />,
  },
  "refused-largest": {
    id: TASK.run, role: "implementer", title: "Implement", status: "running", started: 52, cost: 2.31,
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
          id: h.id, role: h.role, status: h.status, owner: P["marcio"], title: h.title, taskKey: TASK.key,
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
        actions={h.status === "running" ? <><Button size="sm" variant="quiet" leadingIcon="pause">Pause</Button><Button size="sm" variant="danger">Abort</Button></> : undefined}
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
          + "\n\nYou run on Large (4 CPUs · 16 GiB · 40 GiB), raised for this task after the implementer's jest run ran out of memory. You may move to XL (8 · 32, highmem pool) with `dude machine request XL --reason \"…\"`, saying what ran out. A move keeps your conversation and checkout; running processes and /tmp are not kept."} />
      <ChatMessage role="implementer" startedAt={t0 - 5 * MIN} costUsd={0.18}
        content={fixer ? "Both findings are in `stream.ts`: the cursor is not closed on a client abort, and the header row is written twice on retry. Fixing both, then the full suite."
          : "Replacing the paged query with a `DECLARE … CURSOR` in a read-only transaction. The full suite runs with 4 workers: this machine has room for them."} />
      <ChatAside>
        <ToolCallCard name="bash" status="running" summary={<code>npx jest --maxWorkers 4 packages/backend</code>} startedAt={t0 - 70_000} />
      </ChatAside>
    </>
  );
}

function Refused({ view, t0 }: { view: RefusedView; t0: number }) {
  if (view === "refused-largest" || view === "refused-moves") {
    const largest = view === "refused-largest";
    return (
      <>
        <ChatMessage role="implementer" startedAt={t0 - 4 * MIN} costUsd={0.21}
          content={largest ? "The chunked sort still holds the whole index: 2.1M rows × the sort key. Loading the full fixture again to measure it."
            : "Back on Standard after the Reset. The PDF suite with 4 workers is killed again; asking for Large once more."} />
        <ChatAside>
          <ToolCallCard name="bash" status="failed" summary={<code>{largest ? "npx jest --maxWorkers 1 export/full-fixture" : "npx jest --maxWorkers 4 packages/backend"}</code>}
            exitCode={137} error="Killed" defaultExpanded={false} durationMs={largest ? 212_000 : 131_000} />
          <MachineRequestCall from={largest ? xl : standard} to={largest ? "a larger one" : large} refused
            reason={largest ? "The full fixture's sort was OOM-killed at 32 GiB." : REASON_LARGE}
            answer={largest ? REFUSED_LARGEST : REFUSED_MOVES} startedAt={t0 - 50_000} durationMs={300} defaultExpanded />
        </ChatAside>
        <ChatMessage role="implementer" startedAt={t0 - 40_000} costUsd={0.06}
          content={largest ? "No larger size. Sorting in 8 chunks of 270k rows and merging the runs on disk; that keeps the peak under 12 GiB."
            : "No more moves for this Run. Running the suite with --maxWorkers 2 (9 minutes) and noting in my summary that 4 workers do not fit Standard."} />
      </>
    );
  }
  return (
    <>
      <ChatMessage role="qa_browser" startedAt={t0 - 4 * MIN} costUsd={0.14}
        content="Two of the four browser workers crashed while the full preview rendered. Each peaks at about 5 GiB; four need XL." />
      <ChatAside>
        <ToolCallCard name="bash" status="failed" summary={<code>npx playwright test --workers 4 e2e/export.spec.ts</code>} exitCode={1}
          error="Target crashed" defaultExpanded={false} durationMs={96_000} />
        <MachineRequestCall from={standard} to={xl} refused reason={REASON_TESTER}
          answer={REFUSED_PLATFORM} startedAt={t0 - 80_000} durationMs={1_100} defaultExpanded />
      </ChatAside>
      <ChatMessage role="qa_browser" startedAt={t0 - 70_000} costUsd={0.05}
        content="XL is out for this image. Large holds three workers, so asking for Large and running with --workers 3." />
      <ChatAside>
        <MachineRequestCall from={standard} to={large} reason={REASON_TESTER_LARGE} answer={ANSWER_TESTER_LARGE} startedAt={t0 - 60_000} durationMs={700} />
      </ChatAside>
      <ChatNotice kind="notice" at={t0 - 59_000}
        text="Moving the tester to Large: it keeps its conversation and checkout; running processes stop. Later tester Runs in this task run on Large too." />
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
      <MachineRequestCall from={small} to={xl} reason={SESSION.reason} startedAt={t0 - 3 * MIN} durationMs={800} answer={ANSWER_SESSION} defaultExpanded />
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
            tooltip={<MachineTip name="XL">The brainstorm asked for it 2m ago: “joining last year's invoices with their lines needs about 18 GiB.” It moved to the highmem pool with this conversation and its files. Acme's settings name Small.</MachineTip>} /></span>}
        actions={<Button size="sm" variant="secondary">Share</Button>} />
      <div className="runScreen" data-view="chat">
        <div className="runView">
          <div className="runChat">
            <ChatTranscript fill live revision={3} turns={5}
              footer={<ChatComposer mode="chat" placeholder="Message the brainstorm…" onSubmit={() => true} to={<>To <b>Brainstorm</b></>} />}>
              {BEFORE(t0)}
              <ChatNotice kind="notice" at={t0 - 2 * MIN} data-testid="move-notice"
                text="The brainstorm moved to XL, in the highmem pool, in 52s: it keeps this conversation and its files (archive.db, the parquet exports); running processes stopped." />
              <ChatMessage role="brainstorm" startedAt={t0 - 90_000} costUsd={0.11}
                content="On XL now, in the same workspace: `archive.db` and both parquet files are where I left them. Running the join again, then measuring the compressed size per year." />
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
