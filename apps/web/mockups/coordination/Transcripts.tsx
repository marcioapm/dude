/*
 * Screens 2–5: transcripts with peers in them, built from the Run screen's
 * own parts (SessionHeader, the Conversation / Changes / Events bar,
 * ChatTranscript, ChatAside, ToolCallCard, ThinkingBlock, ChatNotice,
 * SessionRail) and a task's Chat's (TaskHistory, ChatRunLine, DeciderLine).
 * What is new is PeerMessage, EpicNoteRow, SinceDigest and PeerList, from
 * Peer.tsx.
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
import { EpicNoteRow, PeerList, PeerMessage, PinGlyph, SinceDigest, TaskChip } from "./Peer.tsx";
import { EPIC, KEYS, MSG, NOTES, P, PEERS, at, type PeerMsg, type TaskKey } from "./data.ts";
import s from "./screens.module.css";

function DudeFace() {
  return (
    <span className="dudeMark" style={{ width: "100%", height: "100%" }} aria-hidden="true">
      <img className="dudeMarkLight" src={dudeSvg} alt="" style={{ width: "100%", height: "100%" }} />
      <img className="dudeMarkDark" src={dudeOutlinedSvg} alt="" style={{ width: "100%", height: "100%" }} />
    </span>
  );
}

const NOTE_CONTRACT = NOTES.find((n) => n.id === "en_2")!;

function RunBar() {
  return (
    <div className="runBar">
      <Segmented size="sm" label="View" tabs="run" value="chat" onChange={() => undefined}
        options={[{ value: "chat", label: "Conversation" }, { value: "changes", label: "Changes" }, { value: "events", label: "Events" }]} />
      <div className="runBarTools" />
    </div>
  );
}

/** The session rail with the proposed Peers block: the epic's other tasks, and how many notes it has. */
function Rail({ me, role, model, peersOverride }: { readonly me: TaskKey; readonly role: AgentRole; readonly model: string; readonly peersOverride?: Parameters<typeof PeerList>[0]["overrides"] }) {
  return (
    <SessionRail className="runRail" aria-label="This session">
      <SessionRailBlock label="Session">
        <SessionFacts facts={[{ label: "Model", value: model, mono: true }, { label: "Agent", value: "Claude Code" }, { label: "Attempt", value: 1 }]} />
      </SessionRailBlock>
      <SessionRailBlock label={<>Peers · {EPIC}</>}>
        <PeerList variant="rail" peers={KEYS.filter((k) => k !== me).map((k) => PEERS[k])} overrides={peersOverride} />
      </SessionRailBlock>
      <SessionRailBlock label="Epic notes">
        <span style={{ display: "inline-flex", alignItems: "center", gap: 6, color: "var(--ds-color-text-secondary)", fontSize: "var(--ds-text-sm)" }}>
          <PinGlyph />{NOTES.length} pinned · all read
        </span>
      </SessionRailBlock>
      <SessionRailBlock label="Role">
        <SessionFacts facts={[{ label: "Peers", value: role === "reviewer" ? "read only" : "read, message, note" }]} />
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

// --------------------------------------------------------------------------- Screen 2: ABS-4

const PROMPT_4 = `**ABS-4 · Frontend: preview panel in the metric form**

Add a preview panel to the metric form: the metric's history and its histogram, computed from a sample, for the form's current values.

## On this epic

*Metric preview in the metric form* has 3 other tasks: ABS-1 (engine), ABS-2 (backend preview endpoints), ABS-3 (custom SQL). Call \`dude peers\` before assuming anything about them; \`dude diff --task KEY\` reads their changes; \`dude message KEY\` asks them.

**Epic notes (1)**

- Read \`.ai/personal/marcio/docs/llm-agent/INDEX.md\` for how to run tests here. — Márcio, 13:41`;

const PEERS_THEN = { "ABS-2": { files: [{ path: "backend/src/engine/client.ts", add: 6, del: 1 }], head: "c02b9e8", lastSaid: "Reading the metric routes. Asking ABS-1 for its engine path before writing the preview handler." } };

export function Abs4Transcript({ moment }: { readonly moment: "waiting" | "answered" }) {
  const waiting = moment === "waiting";
  const reply: PeerMsg = MSG["pm_4"]!;
  return (
    <div className={`screen ${s.session}`}>
      <div className="runScreen" data-view="chat" data-testid="run-screen">
        <SessionHeader session={session("ABS-4", "implementer", "running", "13:42", waiting ? "13:47:40" : "13:52:10", waiting ? 0.31 : 0.58,
          waiting ? <Badge size="sm" icon="clock">Waiting on ABS-2</Badge> : undefined)} />
        <RunBar />
        <div className="runView">
          <div className="runChat">
            <ChatTranscript fill live revision={waiting ? 1 : 2} turns={waiting ? 7 : 11}
              footer={<ChatComposer mode="steer" placeholder="Steer the implementer…" onSubmit={() => true} sentAs="Márcio" landsHint={steerHint("ABS-2", waiting)} canInterrupt />}>
              <ChatMessage role="system" name="His Dudeness" avatar={<DudeFace />} intent="prompt" content={PROMPT_4} startedAt={at("13:42:05")} />
              <ChatMessage role="implementer" content="Before I touch the form I'll see what the other tasks on this epic have: the panel calls ABS-2's endpoints." startedAt={at("13:42:40")} />
              <ChatAside>
                <ToolCallCard name="peers" icon="layers" status="completed" summary={<><code>dude peers</code> · 3 peers, 0 messages for you</>} durationMs={140} defaultExpanded
                  result={<PeerList variant="tool" peers={["ABS-1", "ABS-2", "ABS-3"].map((k) => PEERS[k as TaskKey])} overrides={PEERS_THEN} />} />
                <ToolCallCard name="peer_diff" icon="file-diff" status="completed" summary={<><code>dude diff --task ABS-2</code> · 1 file +6 −1, no routes yet</>} durationMs={210}
                  result={"backend/src/engine/client.ts  +6 −1\n(no file under backend/src/routes/metrics/ yet)"} />
                <ThinkingBlock text="ABS-2 owns /v1/metrics/preview/* and has not written the routes. If I pick a body shape now, one of us rewrites later. Ask, and wait: there is nothing else in this task that doesn't depend on the contract." />
              </ChatAside>
              <ChatMessage role="implementer" content="ABS-2 hasn't written the preview routes yet. Rather than guess the body and response, I'm asking it and waiting for the answer." startedAt={at("13:43:12")} />
              <ChatAside>
                <ToolCallCard name="message" icon="message" status="completed" durationMs={90}
                  summary={<><code>dude message ABS-2 --wait</code> · lands at ABS-2's next step; this turn ends until it replies</>}
                  result={'{"id":"pm_1","to":"ABS-2","lands":"next_step","waiting":true}'} />
              </ChatAside>
              <PeerMessage msg={{ ...MSG["pm_1"]!, delivery: waiting ? MSG["pm_1"]!.delivery : MSG["pm_1"]!.delivery }} viewer="ABS-4"
                waiting={waiting ? { since: at("13:43:20"), parksIn: "Parks at 13:53 if no reply; the reply resumes it." } : undefined} />
              {waiting ? null : (
                <>
                  <PeerMessage msg={reply} viewer="ABS-4" replyToText={MSG["pm_1"]!.text} />
                  <ChatAside>
                    <EpicNoteRow note={NOTE_CONTRACT} />
                  </ChatAside>
                  <ChatMessage role="implementer" content="ABS-2 answered and pinned it. Coding the client to that contract: body `draft`, columns `metric_<id>_mean|_var|_min|_bins`, and `sample_fraction`." startedAt={at("13:49:20")} />
                  <ChatAside>
                    <ToolCallCard name="write" status="completed" summary="frontend/src/metrics/previewClient.ts" durationMs={60} />
                  </ChatAside>
                  <ChatMessage role="implementer" activity="streaming" content="Now the panel: history as a line, the histogram from `metric_<id>_bins`, and" streaming />
                </>
              )}
            </ChatTranscript>
            <Rail me="ABS-4" role="implementer" model="claude-opus-5-5" peersOverride={waiting ? PEERS_THEN : undefined} />
          </div>
        </div>
      </div>
    </div>
  );
}

// --------------------------------------------------------------------------- Screen 3: ABS-2

const PROMPT_2 = `**ABS-2 · Backend: metric preview endpoints**

Add \`/v1/metrics/preview/history\` and \`/histogram\`: compute a draft metric's history and histogram from a sample, through the engine's estimate path.

## On this epic

3 other tasks: ABS-1 (engine: estimate histogram endpoint), ABS-3 (custom SQL), ABS-4 (frontend panel that calls yours). \`dude peers\` before assuming anything about them.

**Epic notes (1)**

- Read \`.ai/personal/marcio/docs/llm-agent/INDEX.md\` for how to run tests here. — Márcio, 13:41`;

export function Abs2Transcript() {
  return (
    <div className={`screen ${s.session}`}>
      <div className="runScreen" data-view="chat" data-testid="run-screen">
        <SessionHeader session={session("ABS-2", "implementer", "running", "13:42", "13:50:30", 0.74)} />
        <RunBar />
        <div className="runView">
          <div className="runChat">
            <ChatTranscript fill live revision={1} turns={12}
              footer={<ChatComposer mode="steer" placeholder="Steer the implementer…" onSubmit={() => true} sentAs="Jonas" landsHint={steerHint("ABS-2", false)} canInterrupt />}>
              <ChatMessage role="system" name="Duder" avatar={<DudeFace />} intent="prompt" content={PROMPT_2} startedAt={at("13:42:02")} />
              <ChatAside>
                <ToolCallCard name="bash" status="completed" summary="cat .ai/personal/marcio/docs/llm-agent/INDEX.md" durationMs={40} />
              </ChatAside>
              <PeerMessage msg={MSG["pm_1"]!} viewer="ABS-2" />
              <ChatMessage role="implementer" content="ABS-4 is waiting on my contract. The histogram needs ABS-1's engine endpoint, so I'll check what ABS-1 has before I fix anything." startedAt={at("13:44:05")} />
              <ChatAside>
                <ToolCallCard name="peers" icon="layers" status="completed" summary={<><code>dude peers</code> · 3 peers, 1 message for you (ABS-4, waiting)</>} durationMs={120} />
                <ToolCallCard name="peer_diff" icon="file-diff" status="completed" summary={<><code>dude diff --task ABS-1</code> · EstimateMetricsHistogramQueryHandler.java +214, Routes.java +6 −1</>} durationMs={260} />
                <ToolCallCard name="message" icon="message" status="completed" durationMs={80}
                  summary={<><code>dude message ABS-1</code> · lands at ABS-1's next step; carrying on</>} />
              </ChatAside>
              <PeerMessage msg={MSG["pm_2"]!} viewer="ABS-2" />
              <ChatAside>
                <ToolCallCard name="edit" status="completed" summary="backend/src/routes/metrics/preview.ts" durationMs={70} />
              </ChatAside>
              <PeerMessage msg={MSG["pm_3"]!} viewer="ABS-2" replyToText={MSG["pm_2"]!.text} />
              <ChatMessage role="implementer" content="ABS-1 has the handler on its branch, so I won't add one here: `PREVIEW_HISTOGRAM_URL` points at its path. Pinning the contract for the epic and answering ABS-4." startedAt={at("13:47:10")} />
              <ChatAside>
                <ToolCallCard name="note" icon="layers" status="completed" durationMs={70}
                  summary={<><code>dude note</code> · pinned for 4 agents on {EPIC}</>} />
                <EpicNoteRow note={NOTE_CONTRACT} pinned />
                <ToolCallCard name="message" icon="message" status="completed" durationMs={60}
                  summary={<><code>dude message ABS-4 --reply-to pm_1</code> · ABS-4 is waiting: the reply resumes it</>} />
              </ChatAside>
              <PeerMessage msg={MSG["pm_4"]!} viewer="ABS-2" replyToText={MSG["pm_1"]!.text} />
              <ChatMessage role="implementer" activity="streaming" streaming content="Writing the route tests against the pinned shape: one per column, and" />
            </ChatTranscript>
            <Rail me="ABS-2" role="implementer" model="claude-opus-5-5" />
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
- 13:43 ABS-4 → ABS-2 (waited): "What body and response will /v1/metrics/preview/* use? I'll code to it." Answered 13:49.
- 13:45 ABS-2 → ABS-1: "Which path does your estimate histogram use?" ABS-1 answered 13:46: \`POST /v1/query/estimate/histogram/metrics\`.

New epic notes:
- 13:48 ABS-2's implementer: Preview API … \`sample_fraction\`. Engine call: \`POST /v1/query/estimate/histogram/metrics\` (ABS-1).

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
                counts="2 threads · 1 note · ABS-1 in review"
                groups={[
                  { title: "Messages with ABS-2", items: [
                    { id: "d1", lead: <TaskChip task="ABS-4" />, text: <>asked for the preview body and response, and waited · <b>answered 13:49</b></>, at: at("13:43") },
                    { id: "d2", lead: <TaskChip task="ABS-1" />, text: <>answered the engine path: <code>POST /v1/query/estimate/histogram/metrics</code></>, at: at("13:46") },
                  ] },
                  { title: "New epic notes", items: [
                    { id: "d3", lead: <PinGlyph />, text: <>Preview API (ABS-2): body <code>{"{draft, …}"}</code>, columns <code>{"metric_<id>_*"}</code>, <code>sample_fraction</code></>, at: at("13:48") },
                  ] },
                  { title: "Peers that changed", items: [
                    { id: "d4", lead: <TaskChip task="ABS-1" status="review" role="reviewer" />, text: "moved to review; head 3f9a2c1 → 9d04e7a", at: at("14:12") },
                  ] },
                ]} />
              <ChatMessage role="system" name="Duder" avatar={<DudeFace />} intent="briefing" content={WAKE_BRIEFING} startedAt={at("14:20:06")} />
              <ChatMessage role="conductor" content="The implementer coded to ABS-1's path and didn't add an engine handler, so there's nothing to reconcile with ABS-1's branch. The contract ABS-4 builds on is pinned. Starting review on the routes and the engine client." startedAt={at("14:20:40")} />
              <ChatRunLine role="reviewer" status="running" what="correctness, api" facts={["1m 10s"]} onOpen={() => undefined} />
            </ChatTranscript>
          </div>
        </div>
      </div>
    </div>
  );
}

// --------------------------------------------------------------------------- Screen 5: guards

const PING: readonly PeerMsg[] = [
  { id: "pp1", from: "ABS-4", fromRole: "implementer", to: "ABS-2", text: "Should `bin_count` default to 20 when the form leaves it empty, or is it required?", sentAt: at("15:01:10"), delivery: { kind: "read", at: at("15:01:30") } },
  { id: "pp2", from: "ABS-2", fromRole: "implementer", to: "ABS-4", text: "Required. Send 20 from the form.", sentAt: at("15:02:00"), delivery: { kind: "read", at: at("15:02:01") }, replyTo: "pp1" },
  { id: "pp3", from: "ABS-4", fromRole: "implementer", to: "ABS-2", text: "Then the form validates it: should a missing value be a 400 from you, or should I block the request?", sentAt: at("15:02:40"), delivery: { kind: "read", at: at("15:03:05") } },
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
              {PING.map((m) => <PeerMessage key={m.id} msg={m} viewer="ABS-4" replyToText={m.replyTo ? PING.find((x) => x.id === m.replyTo)?.text : undefined} />)}
              <PeerMessage viewer="ABS-4" replyToText={PING[2]!.text}
                msg={{ id: "pp4", from: "ABS-2", fromRole: "implementer", to: "ABS-4", text: "A 400 from me either way. Should the 400 carry the field name, or a code?", sentAt: at("15:03:40"), delivery: { kind: "read", at: at("15:03:41") }, replyTo: "pp3" }} />
              <ChatNotice kind="notice" by="His Dudeness" at={at("15:03:42")}
                text="These agents are going back and forth: ABS-4 and ABS-2 traded 4 messages in 3 minutes without either committing. Have a look; neither is stopped." />
              <ChatMessage role="implementer" content="Let me check again whether the 400 body is settled before I write the error state." startedAt={at("15:05:50")} />
              <ChatAside>
                <ToolCallCard name="message" icon="message" status="failed" durationMs={30}
                  summary={<code>dude message ABS-2 "Is the 400's body…"</code>}
                  error={"Refused: this Run has sent 10 messages in the last hour, the limit. The next is allowed at 15:12. If every peer needs it, pin it with `dude note`; otherwise carry on and ask later. The thread is in ABS-4's and ABS-2's Chat for people to see."} />
              </ChatAside>
              <ChatMessage role="implementer" activity="streaming" streaming content="Carrying on: I'll treat any 400 as a field error and show its message. Writing the error state now, and" />
            </ChatTranscript>
            <Rail me="ABS-4" role="implementer" model="claude-opus-5-5" />
          </div>
        </div>
      </div>
    </div>
  );
}
