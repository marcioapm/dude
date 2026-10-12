/*
 * Screens 2–5: transcripts with peers in them, built from the Run screen's
 * own parts (SessionHeader, the Conversation / Changes / Events bar,
 * ChatTranscript, ChatAside, ToolCallCard, ThinkingBlock, SessionRail) and
 * a task's Chat's (TaskHistory, ChatRunLine, DeciderLine, ChatNotice).
 * What is new is PeerMessage, PeerWaitLine, EpicNoteRow, EpicNoteNotice,
 * SinceDigest and PeerList, from Peer.tsx.
 *
 * The sessions' clocks are frozen at the moment shown, so the header's
 * elapsed reads the same in every screenshot.
 */

import type { ReactNode } from "react";
import type { AgentRole, RunStatus } from "@dude/domain";
import {
  ChatAside, ChatComposer, ChatMessage, ChatNotice, ChatRunLine, ChatTranscript, DeciderLine, Segmented, SessionFacts, SessionHeader,
  SessionRail, SessionRailBlock, TaskHistory, ThinkingBlock, ToolCallCard, type ChatTranscriptSession,
} from "@dude/design-system/components";
import { Badge, Tab, TabList, Tabs } from "@dude/design-system/primitives";
import dudeSvg from "../../public/dude.svg?url";
import dudeOutlinedSvg from "../../public/dude-outlined.svg?url";
import { EpicNoteNotice, EpicNoteRow, PeerList, PeerMessage, PeerWaitLine, PinGlyph, SinceDigest, TaskChip } from "./Peer.tsx";
import {
  EPIC, KEYS, MSG, NOTES, NOTE_CONTRACT, NOTE_CONTRACT_1, NOTE_LIMIT, NOTE_PATHS, P, PEERS, at, type Note, type Peer, type PeerMsg, type TaskKey,
} from "./data.ts";
import s from "./screens.module.css";

function DudeFace() {
  return (
    <span className="dudeMark" style={{ width: "100%", height: "100%" }} aria-hidden="true">
      <img className="dudeMarkLight" src={dudeSvg} alt="" style={{ width: "100%", height: "100%" }} />
      <img className="dudeMarkDark" src={dudeOutlinedSvg} alt="" style={{ width: "100%", height: "100%" }} />
    </span>
  );
}

function RunBar() {
  return (
    <div className="runBar">
      <Segmented size="sm" label="View" tabs="run" value="chat" onChange={() => undefined}
        options={[{ value: "chat", label: "Conversation" }, { value: "changes", label: "Changes" }, { value: "events", label: "Events" }]} />
      <div className="runBarTools" />
    </div>
  );
}

type Overrides = Partial<Record<TaskKey, Partial<Peer>>>;

/**
 * The session rail with the proposed epic blocks: the task's peers (what
 * `list_tasks --peers` returns, compact: the epic's other tasks, then any
 * linked by "depends on"), its epic notes, and which peer tools the role
 * has. A reviewer reads; it gets the notes in its briefing to check the
 * code against them.
 */
function Rail({ me, role, model, notes = NOTES.length, peersOverride, linked = [] }: {
  readonly me: TaskKey;
  readonly role: AgentRole;
  readonly model: string;
  readonly notes?: number;
  readonly peersOverride?: Overrides | undefined;
  /** Peers by a "depends on" link, after the epic's tasks. */
  readonly linked?: readonly TaskKey[];
}) {
  const reads = role === "reviewer" || role === "simplifier" || role === "qa_browser";
  return (
    <SessionRail className="runRail" aria-label="This session">
      <SessionRailBlock label="Session">
        <SessionFacts facts={[{ label: "Model", value: model, mono: true }, { label: "Agent", value: "Claude Code" }, { label: "Attempt", value: 1 }]} />
      </SessionRailBlock>
      <SessionRailBlock label="Peers">
        <PeerList variant="rail" label={`Peers of ${me}`} peers={[...KEYS.filter((k) => k !== me), ...linked].map((k) => PEERS[k])} overrides={peersOverride} />
      </SessionRailBlock>
      <SessionRailBlock label="Epic notes">
        <span style={{ display: "inline-flex", alignItems: "center", gap: 6, color: "var(--ds-color-text-secondary)", fontSize: "var(--ds-text-sm)" }}>
          <PinGlyph />{reads ? `Epic notes: ${notes}, in briefing` : `${notes} of ${NOTE_LIMIT} pinned · all read`}
        </span>
      </SessionRailBlock>
      <SessionRailBlock label="Role">
        <SessionFacts facts={[{ label: "With peers", value: reads ? "list, diff · read only" : "list, diff, message, note" }]} />
      </SessionRailBlock>
    </SessionRail>
  );
}

function session(key: TaskKey, role: AgentRole, status: RunStatus, from: string, to: string, cost: number, note?: ReactNode): ChatTranscriptSession {
  const p = PEERS[key];
  return {
    id: `run_${key}_${role}`,
    role,
    status,
    taskKey: key,
    title: p.title,
    model: "claude-opus-5-5",
    repo: "abs",
    branch: p.branch,
    owner: p.owner,
    startedAt: at(from),
    endedAt: at(to),
    costUsd: cost,
    statusNote: note,
  };
}

const steerHint = (k: TaskKey, waiting: boolean) => waiting
  ? <>Waiting on <b>{k}</b>: a steer is read now, and the wait stays open</>
  : "Lands at the agent's next step";

/** The "On this epic" section every Run on an epic gets: its peers, how to read them, and when to ask and when to wait. */
function onThisEpic(others: string, notes: string, linked?: string): string {
  return `## On this epic

*${EPIC}* has 3 other tasks: ${others}.${linked ? `\n\nLinked to this task by "depends on": ${linked}. It is a peer too, but not on this epic: it doesn't get the epic notes, so a contract it needs goes in a message.` : ""}

Before you assume anything about your peers, look: \`dude task list --peers\` shows each one's phase, branch, files and last words; \`dude diff --task KEY [PATH…]\` reads its changes; the epic notes below are what is settled. If you need a decision or a fact that only a peer's agent has, ask early with \`dude message KEY\`: it returns at once and the reply lands at your next step. Keep working on whatever doesn't depend on the answer, and run \`dude wait MSG\` only at the point where you can't go on without it. A turn that ends with your question unanswered waits on it too; to go ahead on an assumption instead, reply to your own question saying what you assumed. Never message to report progress. A fact every task on the epic needs is a note (\`dude note\`), not a message.

${notes}`;
}

const NOTES_1 = `**Epic notes (2)**

- **Note 2 · Preview API (ABS-2).** ABS-2 serves \`POST /v1/metrics/preview/history\` and \`/histogram\`; ABS-4 calls them. The engine's estimate histogram is ABS-1's: ABS-2 calls it and never builds it. — from a brainstorm session, filed by Márcio, 13:40
- **Note 1.** Read \`.ai/personal/marcio/docs/llm-agent/INDEX.md\` for how to run tests here. — from a brainstorm session, filed by Márcio, 13:40`;

// --------------------------------------------------------------------------- Screen 2: ABS-4

const PROMPT_4 = `**ABS-4 · Frontend: preview panel in the metric form**

Add a preview panel to the metric form: the metric's history and its histogram, computed from a sample, for the form's current values.

${onThisEpic("ABS-1 (engine), ABS-2 (backend preview endpoints), ABS-3 (custom SQL)", NOTES_1)}`;

const PEERS_THEN: Overrides = { "ABS-2": { files: [{ path: "backend/src/engine/client.ts", add: 6, del: 1 }], head: "c02b9e8", lastSaid: "Reading the metric routes. Asking ABS-1 for its engine path before writing the preview handler." } };
const PEERS_WAITING: Overrides = { ...PEERS_THEN, "ABS-4": { waitingOn: "ABS-2" } };

export function Abs4Transcript({ moment }: { readonly moment: "waiting" | "answered" }) {
  const waiting = moment === "waiting";
  const reply: PeerMsg = MSG["pm_4"]!;
  const waitSince = at("13:47:02");
  return (
    <div className={`screen ${s.session}`}>
      <div className="runScreen" data-view="chat" data-testid="run-screen">
        <SessionHeader session={session("ABS-4", "implementer", "running", "13:42", waiting ? "13:47:02" : "13:52:10", waiting ? 0.44 : 0.71,
          waiting ? <Badge size="sm" icon="clock">Waiting on ABS-2 · parks in 10 min</Badge> : undefined)} />
        <RunBar />
        <div className="runView">
          <div className="runChat">
            <ChatTranscript fill live revision={waiting ? 1 : 2} turns={waiting ? 12 : 18}
              footer={<ChatComposer mode="steer" placeholder="Steer the implementer…" onSubmit={() => true} sentAs="Márcio" landsHint={steerHint("ABS-2", waiting)} canInterrupt />}>
              <ChatMessage role="system" name="His Dudeness" avatar={<DudeFace />} intent="prompt" content={PROMPT_4} startedAt={at("13:42:05")} />
              <ChatMessage role="implementer" content="Before I touch the form I'll see what my peers have: the panel calls ABS-2's endpoints." startedAt={at("13:42:40")} />
              <ChatAside>
                <ToolCallCard name="list_tasks" icon="layers" status="completed" summary={<><code>dude task list --peers</code> · 3 peers, all on {EPIC}, 0 messages for you</>} durationMs={140}
                  result={<PeerList variant="tool" label="Peers of ABS-4" self="ABS-4" peers={KEYS.map((k) => PEERS[k])} overrides={PEERS_THEN} />} />
                <ToolCallCard name="run_diff" icon="file-diff" status="completed" summary={<><code>dude diff --task ABS-2</code> · 1 file +6 −1, no routes yet</>} durationMs={210}
                  result={"backend/src/engine/client.ts  +6 −1\n(no file under backend/src/routes/metrics/ yet)"} />
                <ThinkingBlock text="Note 2 gives ABS-2's paths but not the body or the response, and ABS-2 has not written the routes. Only ABS-2 can say. Ask now; the panel's layout and the chart don't depend on the answer, so build those from mock data and wait only when I reach the API call." />
              </ChatAside>
              <ChatMessage role="implementer" content="Note 2 has the paths, not the body or response. Asking ABS-2 now, and building the panel and the chart from mock data meanwhile." startedAt={at("13:43:12")} />
              <ChatAside>
                <ToolCallCard name="message" icon="message" status="completed" durationMs={90}
                  summary={<><code>dude message ABS-2</code> · sent · carrying on; the reply lands at your next step</>}
                  result={'{"id":"pm_1","to":"ABS-2","lands":"next_step"}'} />
              </ChatAside>
              <PeerMessage msg={MSG["pm_1"]!} viewer="ABS-4" carryingOn />
              <ChatAside>
                <ToolCallCard name="write" status="completed" summary="frontend/src/metrics/PreviewPanel.tsx · history above, histogram below, sample note" durationMs={70} />
                <ToolCallCard name="write" status="completed" summary="frontend/src/metrics/previewMock.ts · 30 days, 20 bins" durationMs={40} />
                <ToolCallCard name="write" status="completed" summary="frontend/src/metrics/HistogramChart.tsx" durationMs={60} />
                <ToolCallCard name="bash" status="completed" summary="bun test frontend/src/metrics · 6 passed" durationMs={4_200} />
              </ChatAside>
              <ChatMessage role="implementer" content="The panel and the chart render from mock data and their tests pass. Next is `previewClient.ts`, the API call, which needs ABS-2's body and columns: I can't write it without the answer, so I'm waiting here." startedAt={at("13:46:58")} />
              <ChatAside>
                <ToolCallCard name="wait_for_reply" icon="clock" status="completed" durationMs={20}
                  summary={<><code>dude wait pm_1</code> · ends this turn: waiting on ABS-2</>}
                  result={'{"waiting_on":"ABS-2","message":"pm_1","parks_at":"13:57:02"}'} />
              </ChatAside>
              {waiting ? (
                <PeerWaitLine on="ABS-2" msg="pm_1" how="wait" since={waitSince} parksAt={at("13:57:02")} />
              ) : (
                <>
                  <PeerWaitLine on="ABS-2" msg="pm_1" how="wait" since={waitSince} parksAt={at("13:57:02")} answered={at("13:49:06")} />
                  <EpicNoteNotice change="added" note={NOTE_CONTRACT_1} read={{ at: at("13:49:06") }} />
                  <PeerMessage msg={reply} viewer="ABS-4" replyToText={MSG["pm_1"]!.text} />
                  <ChatMessage role="implementer" content="ABS-2 answered and pinned it as note 3. Writing the client to that contract and swapping the mock out of the panel: body `draft`, columns `metric_<id>_mean|_var|_min|_bins`, and `sample_fraction`." startedAt={at("13:49:20")} />
                  <ChatAside>
                    <ToolCallCard name="write" status="completed" summary="frontend/src/metrics/previewClient.ts" durationMs={60} />
                    <ToolCallCard name="edit" status="completed" summary="frontend/src/metrics/PreviewPanel.tsx · previewMock → previewClient" durationMs={50} />
                  </ChatAside>
                  <EpicNoteNotice change="replaced" note={NOTE_CONTRACT} was={NOTE_CONTRACT_1} changed={{ now: "_variance", was: "_var" }} read={{ at: at("13:51:24"), after: "Edit" }} />
                  <ChatMessage role="implementer" content="Note 4 renames `_var` to `_variance`. Changing the client's column to match." startedAt={at("13:51:30")} />
                  <ChatAside>
                    <ToolCallCard name="edit" status="completed" summary="frontend/src/metrics/previewClient.ts · _var → _variance" durationMs={50} />
                  </ChatAside>
                  <ChatMessage role="implementer" activity="streaming" content="Now the client's tests against note 4's columns, and" streaming />
                </>
              )}
            </ChatTranscript>
            <Rail me="ABS-4" role="implementer" model="claude-opus-5-5" notes={waiting ? 2 : 3} peersOverride={waiting ? PEERS_WAITING : undefined} />
          </div>
        </div>
      </div>
    </div>
  );
}

// --------------------------------------------------------------------------- Screen 2, 14:33: ABS-4's api reviewer

const REVIEW_BRIEFING = `**ABS-4 · Frontend: preview panel in the metric form** · review: api

Review the client and panel the implementer wrote for how they call the backend. Report findings; don't change code.

## Epic notes (3)

Check the code against these: they are what the tasks on this epic agreed.

- **Note 4 · Preview API body and response (ABS-2).** Both routes take \`{draft, unit_type_id, applications, from, sample, period, cumulative, bin_count, include_non_achievers}\`; \`draft\` is the form's value, not a saved metric. Columns \`metric_<id>_mean\`, \`_variance\`, \`_min\`, \`_bins\`, and \`sample_fraction\`. Engine call: \`POST /v1/query/estimate/histogram/metrics\` (ABS-1). — ABS-2's implementer, 13:51, replaces note 3
- **Note 2 · Preview API (ABS-2).** ABS-2 serves \`POST /v1/metrics/preview/history\` and \`/histogram\`; ABS-4 calls them. The engine's estimate histogram is ABS-1's: ABS-2 calls it and never builds it. — from a brainstorm session, filed by Márcio, 13:40
- **Note 1.** Read \`.ai/personal/marcio/docs/llm-agent/INDEX.md\` for how to run tests here. — from a brainstorm session, filed by Márcio, 13:40

\`dude task list --peers\` and \`dude diff --task KEY\` read the other tasks; you can't message them.`;

export function Abs4Reviewer() {
  return (
    <div className={`screen ${s.session}`}>
      <div className="runScreen" data-view="chat" data-testid="run-screen">
        <SessionHeader session={session("ABS-4", "reviewer", "running", "14:31", "14:34:50", 0.21)} />
        <RunBar />
        <div className="runView">
          <div className="runChat">
            <ChatTranscript fill live revision={1} turns={6}
              footer={<ChatComposer mode="steer" placeholder="Steer the reviewer…" onSubmit={() => true} sentAs="Márcio" landsHint={steerHint("ABS-4", false)} canInterrupt />}>
              <ChatMessage role="system" name="His Dudeness" avatar={<DudeFace />} intent="prompt" content={REVIEW_BRIEFING} startedAt={at("14:31:02")} />
              <ChatMessage role="reviewer" content="Checking the client against note 4, then ABS-2's routes against the client." startedAt={at("14:31:30")} />
              <ChatAside>
                <ToolCallCard name="read" status="completed" summary="frontend/src/metrics/previewClient.ts" durationMs={30} />
                <ToolCallCard name="run_diff" icon="file-diff" status="completed" durationMs={240}
                  summary={<><code>dude diff --task ABS-2 backend/src/routes/metrics/preview.ts</code> · +188, reads <code>body.draft</code>, returns <code>_variance</code></>} />
              </ChatAside>
              <ChatMessage role="reviewer" content={"**api: no findings.** The client posts `draft` and reads `metric_<id>_mean`, `_variance`, `_min`, `_bins` and `sample_fraction`, as note 4 says; ABS-2's routes read and return the same. The engine call is ABS-1's path, as notes 2 and 4 say."} startedAt={at("14:34:40")} />
            </ChatTranscript>
            <Rail me="ABS-4" role="reviewer" model="claude-opus-5-5"
              peersOverride={{ "ABS-4": { waitingOn: undefined }, "ABS-2": { status: "review", phase: "review", role: "reviewer" } }} />
          </div>
        </div>
      </div>
    </div>
  );
}

// --------------------------------------------------------------------------- Screen 3: ABS-2

const PROMPT_2 = `**ABS-2 · Backend: metric preview endpoints**

Add \`/v1/metrics/preview/history\` and \`/histogram\`: compute a draft metric's history and histogram from a sample, through the engine's estimate path.

${onThisEpic("ABS-1 (engine: estimate histogram endpoint), ABS-3 (custom SQL), ABS-4 (frontend panel that calls yours)", NOTES_1, "JERV-2 (Metric preview in the experiment setup, project jervasion) depends on this task")}`;

export function Abs2Transcript() {
  return (
    <div className={`screen ${s.session}`}>
      <div className="runScreen" data-view="chat" data-testid="run-screen">
        <SessionHeader session={session("ABS-2", "implementer", "running", "13:42", "13:51:40", 0.81)} />
        <RunBar />
        <div className="runView">
          <div className="runChat">
            <ChatTranscript fill live revision={1} turns={15}
              footer={<ChatComposer mode="steer" placeholder="Steer the implementer…" onSubmit={() => true} sentAs="Jonas" landsHint={steerHint("ABS-2", false)} canInterrupt />}>
              <ChatMessage role="system" name="Duder" avatar={<DudeFace />} intent="prompt" content={PROMPT_2} startedAt={at("13:42:02")} />
              <ChatAside>
                <ToolCallCard name="bash" status="completed" summary="cat .ai/personal/marcio/docs/llm-agent/INDEX.md" durationMs={40} />
              </ChatAside>
              <PeerMessage msg={MSG["pm_1"]!} viewer="ABS-2" />
              <ChatMessage role="implementer" content="ABS-4 asked for my contract and is carrying on meanwhile. The histogram needs ABS-1's engine endpoint, so I'll check what ABS-1 has before I fix anything." startedAt={at("13:44:05")} />
              <ChatAside>
                <ToolCallCard name="list_tasks" icon="layers" status="completed" durationMs={120} defaultExpanded
                  summary={<><code>dude task list --peers</code> · 4 peers, 1 linked · 1 message for you</>}
                  result={<PeerList variant="tool" label="Peers of ABS-2" self="ABS-2" peers={[...KEYS, "JERV-2" as const].map((k) => PEERS[k])}
                    overrides={{ "ABS-4": { forYou: { count: 1, waits: false } } }} />} />
                <ToolCallCard name="run_diff" icon="file-diff" status="completed" summary={<><code>dude diff --task ABS-1</code> · EstimateMetricsHistogramQueryHandler.java +214, Routes.java +6 −1</>} durationMs={260} />
                <ThinkingBlock text="Note 2 says the engine histogram is ABS-1's and I call it. ABS-1 has a histogram handler, but which path it will keep isn't in the diff or a note; only ABS-1 knows. Ask it and keep writing the routes; I only need the path when I wire the engine call." />
                <ToolCallCard name="message" icon="message" status="completed" durationMs={80}
                  summary={<><code>dude message ABS-1</code> · sent · carrying on</>} />
              </ChatAside>
              <PeerMessage msg={MSG["pm_2"]!} viewer="ABS-2" carryingOn />
              <ChatAside>
                <ToolCallCard name="edit" status="completed" summary="backend/src/routes/metrics/preview.ts" durationMs={70} />
              </ChatAside>
              <PeerMessage msg={MSG["pm_3"]!} viewer="ABS-2" replyToText={MSG["pm_2"]!.text} />
              <ChatMessage role="implementer" content="ABS-1 has the handler on its branch, so I won't add one here: `PREVIEW_HISTOGRAM_URL` points at its path. Pinning the contract for the epic and answering ABS-4." startedAt={at("13:47:10")} />
              <ChatAside>
                <ToolCallCard name="note" icon="layers" status="completed" durationMs={70}
                  summary={<><code>dude note</code> · note 3, told to 3 agents at their next step</>} />
                <EpicNoteRow note={NOTE_CONTRACT_1} />
                <ToolCallCard name="message" icon="message" status="completed" durationMs={60}
                  summary={<><code>dude message ABS-4 --reply-to pm_1</code> · ABS-4 is waiting: the reply resumes it</>} />
              </ChatAside>
              <PeerMessage msg={MSG["pm_4"]!} viewer="ABS-2" replyToText={MSG["pm_1"]!.text} />
              <ChatMessage role="implementer" content="Writing the route tests against ABS-1's response. Checking its column names first." startedAt={at("13:49:40")} />
              <ChatAside>
                <ToolCallCard name="run_diff" icon="file-diff" status="completed" durationMs={180}
                  summary={<><code>dude diff --task ABS-1 HistogramBins.java</code> · columns <code>_mean</code>, <code>_variance</code>, <code>_min</code>, <code>_bins</code></>} />
              </ChatAside>
              <ChatMessage role="implementer" content="The engine says `_variance`, not `_var`. Renaming the column in the routes and replacing note 3: every agent on the epic is told at its next step, so ABS-4 needs no message." startedAt={at("13:50:55")} />
              <ChatAside>
                <ToolCallCard name="edit" status="completed" summary="backend/src/routes/metrics/preview.ts · _var → _variance" durationMs={60} />
                <ToolCallCard name="note" icon="layers" status="completed" durationMs={70}
                  summary={<><code>dude note --replaces 3</code> · note 4, told to 3 agents at their next step</>} />
                <EpicNoteRow note={NOTE_CONTRACT} />
              </ChatAside>
              <ChatMessage role="implementer" activity="streaming" streaming content="Back to the route tests, one per column, and" />
            </ChatTranscript>
            <Rail me="ABS-2" role="implementer" model="claude-opus-5-5" linked={["JERV-2"]} />
          </div>
        </div>
      </div>
    </div>
  );
}

// --------------------------------------------------------------------------- Screen 4: ABS-2's conductor

const WAKE_BRIEFING = `Conductor, dude woke you about ABS-2, "Backend: metric preview endpoints": a decision is yours. You are this task's conductor. Below is what dude knows about the task, in short; read more with your tools.

## The task

ABS-2 · wi_0mv5auwan3e9310013429faaa · status running

## Runs (2)

- run_7c… implementer · completed 14:20 · 3 commits · b71e04d

## Since you last looked, on this epic (13:44 → 14:20)

Messages to or from ABS-2:
- 13:43 ABS-4 → ABS-2 (ABS-4 carried on, waited from 13:47): "What body and response will /v1/metrics/preview/* use? I'll code to it." Answered 13:49.
- 13:45 ABS-2 → ABS-1: "Which path does your estimate histogram use?" ABS-1 answered 13:46: \`POST /v1/query/estimate/histogram/metrics\`.

Epic notes added or replaced:
- 13:48 note 3, ABS-2's implementer: Preview API body and response.
- 13:51 note 4, ABS-2's implementer, replaces note 3: … \`_variance\` (was \`_var\`) … \`sample_fraction\`. Engine call: \`POST /v1/query/estimate/histogram/metrics\` (ABS-1).

Peers that changed:
- ABS-1 moved to review at 14:12; head 3f9a2c1 → 9d04e7a.
- ABS-3 is in review.

## Decisions

The implementer finished: what next is yours (review, or Deliver's next).`;

export function ConductorChat({ open }: { readonly open: boolean }) {
  const key: TaskKey = "ABS-2";
  return (
    <div className="screen">
      <div className={s.taskTop}>
        <span className="ds-mono" style={{ color: "var(--ds-color-text-muted)", fontSize: "var(--ds-text-sm)" }}>ABS-2 · {EPIC}</span>
        <h1 className={s.taskTitle}>{PEERS[key].title}</h1>
      </div>
      <Tabs value="chat" onValueChange={() => undefined}>
        <TabList aria-label="Task" className="tabsInset">
          <Tab value="chat">Chat</Tab>
          <Tab value="overview">Overview</Tab>
          <Tab value="sessions" count={3}>Sessions</Tab>
          <Tab value="activity">Activity</Tab>
        </TabList>
      </Tabs>
      <div className="taskChat" data-testid="task-chat">
        <div className="runScreen" data-view="chat">
          <div className="runChat">
            <ChatTranscript fill revision={1} turns={9}
              pinned={<TaskHistory lead="Conducted by its conductor, with Jonas" steps={["implement", "review"]} facts={["3 commits", "$2.31"]} />}
              footer={<>
                <DeciderLine decider="conductor" waiting="what follows the implementer" />
                <ChatComposer mode="chat" placeholder="Ask about this task…" onSubmit={() => true} to={<>To <b>Conductor</b></>} sentAs="Jonas" />
              </>}>
              <ChatMessage role="human" person={P["jonas"]} intent="message" content="Take this through. The engine histogram is ABS-1's, so call it, don't build it." startedAt={at("13:36:10")} />
              <ChatMessage role="conductor" content="Starting the implementer with that. I'll let it ask ABS-1 for the path instead of guessing." startedAt={at("13:37:02")} />
              <ChatRunLine role="implementer" status="completed" what="the preview routes" facts={["8m 20s", "$1.12"]} onOpen={() => undefined} />
              <ChatNotice kind="parked" by="Duder" text="Parked while nobody is writing" at={at("13:44:00")} />
              <ChatNotice kind="decision" by="Duder" text="The implementer finished: what follows it is the conductor's decision" at={at("14:20:05")} />
              <SinceDigest key={String(open)} defaultOpen={open} since={at("13:44")} woke={at("14:20")}
                counts="2 threads · 2 note changes · ABS-1 in review"
                groups={[
                  { title: "Messages with ABS-2", items: [
                    { id: "d1", lead: <TaskChip task="ABS-4" />, text: <>asked for the preview body and response, carried on, waited from 13:47 · <b>answered 13:49</b></>, at: at("13:43") },
                    { id: "d2", lead: <TaskChip task="ABS-1" />, text: <>answered the engine path: <code>POST /v1/query/estimate/histogram/metrics</code></>, at: at("13:46") },
                  ] },
                  { title: "Epic notes added or replaced", items: [
                    { id: "d3", lead: <PinGlyph />, text: <>Note 3, then note 4 in its place: Preview API body and response (ABS-2), <code>{"{draft, …}"}</code>, <code>_variance</code> (was <code>_var</code>), <code>sample_fraction</code></>, at: at("13:51") },
                  ] },
                  { title: "Peers that changed", items: [
                    { id: "d4", lead: <TaskChip task="ABS-1" status="review" role="reviewer" />, text: "moved to review; head 3f9a2c1 → 9d04e7a", at: at("14:12") },
                  ] },
                ]} />
              <ChatMessage role="system" name="Duder" avatar={<DudeFace />} intent="briefing" content={WAKE_BRIEFING} startedAt={at("14:20:06")} />
              <ChatMessage role="conductor" content="The implementer coded to ABS-1's path and didn't add an engine handler, so there's nothing to reconcile with ABS-1's branch. The contract ABS-4 builds on is pinned as note 4. Starting review on the routes and the engine client." startedAt={at("14:20:40")} />
              <ChatRunLine role="reviewer" status="running" what="correctness, api" facts={["1m 10s"]} onOpen={() => undefined} />
            </ChatTranscript>
          </div>
        </div>
      </div>
    </div>
  );
}

// --------------------------------------------------------------------------- Screen 5: the rate limit

const EARLIER: readonly PeerMsg[] = [
  { id: "pp1", from: "ABS-4", fromRole: "implementer", to: "ABS-2", text: "Should `bin_count` default to 20 when the form leaves it empty, or is it required?", sentAt: at("15:01:10"), delivery: { kind: "read", at: at("15:01:30") } },
  { id: "pp2", from: "ABS-2", fromRole: "implementer", to: "ABS-4", text: "Required. Send 20 from the form.", sentAt: at("15:02:00"), delivery: { kind: "read", at: at("15:02:01") }, replyTo: "pp1" },
];

export function GuardsTranscript() {
  return (
    <div className={`screen ${s.session}`}>
      <div className="runScreen" data-view="chat" data-testid="run-screen">
        <SessionHeader session={session("ABS-4", "implementer", "running", "14:52", "15:06:30", 1.12)} />
        <RunBar />
        <div className="runView">
          <div className="runChat">
            <ChatTranscript fill live revision={1} turns={9}
              footer={<ChatComposer mode="steer" placeholder="Steer the implementer…" onSubmit={() => true} sentAs="Márcio" landsHint={steerHint("ABS-4", false)} canInterrupt />}>
              {EARLIER.map((m) => <PeerMessage key={m.id} msg={m} viewer="ABS-4" replyToText={m.replyTo ? EARLIER.find((x) => x.id === m.replyTo)?.text : undefined} />)}
              <ChatMessage role="implementer" content="I need to know what ABS-2's 400 body looks like before I write the form's error state." startedAt={at("15:05:50")} />
              <ChatAside>
                <ToolCallCard name="message" icon="message" status="failed" durationMs={30}
                  summary={<code>dude message ABS-2 "What does the 400's body…"</code>}
                  error={"Refused: this Run has sent 5 messages in the last hour, the most a Run may send. The next is allowed at 15:12. Until then, `dude task list --peers`, `dude diff --task ABS-2` and the epic notes may already answer it; if they don't, carry on and ask then."} />
                <ToolCallCard name="run_diff" icon="file-diff" status="completed" durationMs={190}
                  summary={<><code>dude diff --task ABS-2 backend/src/routes/metrics/preview.ts</code> · 400 is <code>{"{field, message}"}</code></>} />
              </ChatAside>
              <ChatMessage role="implementer" activity="streaming" streaming content="ABS-2's routes answer a 400 with `{field, message}`. Showing the message under that field; writing the error state now, and" />
            </ChatTranscript>
            <Rail me="ABS-4" role="implementer" model="claude-opus-5-5" />
          </div>
        </div>
      </div>
    </div>
  );
}

// --------------------------------------------------------------------------- Screen 5: a peer with no Run going

const NO_RUN_Q: PeerMsg = {
  id: "pm_9", from: "ABS-4", fromRole: "implementer", to: "ABS-3",
  text: "Do custom SQL metrics come back with the same `metric_<id>_bins` column as built-in ones, or can it be missing?",
  sentAt: at("15:08:40"), delivery: { kind: "inbox" },
};
const NO_RUN_ASSUMED: PeerMsg = {
  id: "pm_10", from: "ABS-4", fromRole: "implementer", to: "ABS-3",
  text: "Not waiting on this: going ahead assuming `_bins` can be missing for custom SQL. The panel hides the histogram when it is. Tell me if it is always there.",
  sentAt: at("15:09:30"), delivery: { kind: "inbox" }, replyTo: "pm_9",
};

export function NoRunTranscript() {
  return (
    <div className={`screen ${s.session}`}>
      <div className="runScreen" data-view="chat" data-testid="run-screen">
        <SessionHeader session={session("ABS-4", "implementer", "running", "14:52", "15:09:50", 1.18)} />
        <RunBar />
        <div className="runView">
          <div className="runChat">
            <ChatTranscript fill live revision={1} turns={9}
              footer={<ChatComposer mode="steer" placeholder="Steer the implementer…" onSubmit={() => true} sentAs="Márcio" landsHint={steerHint("ABS-4", false)} canInterrupt />}>
              <ChatMessage role="implementer" content="Custom SQL metrics come through the same preview path. Whether they always carry `_bins` isn't in note 4 or in ABS-3's diff; asking ABS-3." startedAt={at("15:08:20")} />
              <ChatAside>
                <ToolCallCard name="message" icon="message" status="completed" durationMs={70}
                  summary={<><code>dude message ABS-3</code> · sent · ABS-3 has no Run going</>}
                  result={'{"id":"pm_9","to":"ABS-3","lands":"next_run","notice":"ABS-3 has no Run going; it will see this when its next Run starts."}'} />
              </ChatAside>
              <PeerMessage msg={NO_RUN_Q} viewer="ABS-4" />
              <ChatAside>
                <ThinkingBlock text="ABS-3 is in review with no Run going, so no answer is coming soon. The panel can handle both cases: hide the histogram when `_bins` is missing. Carry on with that, and say so in the thread so ABS-3 can correct me." />
                <ToolCallCard name="message" icon="message" status="completed" durationMs={60}
                  summary={<><code>dude message ABS-3 --reply-to pm_9</code> · your own question answered with an assumption: no wait stays open on it</>}
                  result={'{"id":"pm_10","to":"ABS-3","lands":"next_run","reply_to":"pm_9"}'} />
              </ChatAside>
              <PeerMessage msg={NO_RUN_ASSUMED} viewer="ABS-4" replyToText={NO_RUN_Q.text} />
              <ChatAside>
                <ToolCallCard name="edit" status="completed" summary="frontend/src/metrics/PreviewPanel.tsx · no histogram without _bins" durationMs={50} />
              </ChatAside>
              <ChatMessage role="implementer" activity="streaming" streaming content="The panel hides the histogram when `_bins` is missing. Adding a test for a custom SQL metric, and" />
            </ChatTranscript>
            <Rail me="ABS-4" role="implementer" model="claude-opus-5-5"
              peersOverride={{ "ABS-3": { live: false, lastSaid: "Review done; waiting for Márcio's approval." } }} />
          </div>
        </div>
      </div>
    </div>
  );
}

// --------------------------------------------------------------------------- Screen 5: ten notes

const RETIRED_2: Note = { ...NOTE_PATHS, retired: { by: P["marcio"]!, at: at("15:26:00") } };
const NOTE_FALLBACK: Note = {
  id: "en_14", n: 14, by: { kind: "agent", task: "ABS-4", role: "implementer", for: P["marcio"]! }, at: at("15:24:30"), readBy: 1, replaces: 9,
  text: "**Preview panel states (ABS-4).** Empty sample: \"No data in this sample\". 400: the message under its field. No `_bins` (custom SQL): the histogram is hidden, the history stays.",
};

export function NotesLimitTranscript() {
  return (
    <div className={`screen ${s.session}`}>
      <div className="runScreen" data-view="chat" data-testid="run-screen">
        <SessionHeader session={session("ABS-4", "implementer", "running", "14:52", "15:26:20", 1.43)} />
        <RunBar />
        <div className="runView">
          <div className="runChat">
            <ChatTranscript fill live revision={1} turns={11}
              footer={<ChatComposer mode="steer" placeholder="Steer the implementer…" onSubmit={() => true} sentAs="Márcio" landsHint={steerHint("ABS-4", false)} canInterrupt />}>
              <ChatMessage role="implementer" content="The panel's empty, error and no-histogram states are settled, and ABS-2's routes produce each of them. The reviewers on this epic should check against them: that's a note." startedAt={at("15:23:50")} />
              <ChatAside>
                <ToolCallCard name="note" icon="layers" status="failed" durationMs={30}
                  summary={<code>dude note "Preview panel states (ABS-4) …"</code>}
                  error={"This epic has 10 notes. Replace one (`dude note --replaces N`) instead."} />
                <ThinkingBlock text="Note 9 is my own, the panel's empty state; the new note covers it and more. Replace it." />
                <ToolCallCard name="note" icon="layers" status="completed" durationMs={70}
                  summary={<><code>dude note --replaces 9</code> · note 14, told to 3 agents at their next step · 10 of 10</>} />
                <EpicNoteRow note={NOTE_FALLBACK} />
              </ChatAside>
              <ChatMessage role="implementer" content="Note 14 replaces note 9. Adding a test for a custom SQL metric." startedAt={at("15:24:40")} />
              <ChatAside>
                <ToolCallCard name="write" status="completed" summary="frontend/src/metrics/PreviewPanel.test.tsx · custom SQL, no _bins" durationMs={60} />
              </ChatAside>
              <EpicNoteNotice change="retired" note={RETIRED_2} read={{ at: at("15:26:04"), after: "Write" }} />
              <ChatMessage role="implementer" activity="streaming" streaming content="Note 2 is retired; note 4 has the paths too, so nothing changes here. Running the panel's tests, and" />
            </ChatTranscript>
            <Rail me="ABS-4" role="implementer" model="claude-opus-5-5" notes={9} />
          </div>
        </div>
      </div>
    </div>
  );
}
