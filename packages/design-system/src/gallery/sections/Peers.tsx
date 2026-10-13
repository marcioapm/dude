import { Block, Col, Label, Panes, Section, States, type PaneMode } from "../Frame.tsx";
import { ChatAside, ChatMessage } from "../../components/ChatMessage.tsx";
import { ChatNotice } from "../../components/ChatNotice.tsx";
import { ToolCallCard } from "../../components/ToolCallCard.tsx";
import {
  EpicNote, EpicNoteCount, EpicNoteHistory, EpicNoteNotice, EpicNoteRow, PeerList, PeerMessage, PeerThread, PeerWaitLine, PeerWaitMark,
  SinceDigest, TaskChip, type EpicNoteData, type PeerMessageData, type PeerRow, type PeerTask,
} from "../../components/Peer.tsx";
import { Section as PageSection } from "../../primitives/Layout.tsx";
import { Icon } from "../../icons/index.tsx";
import { ALL_TASK_STATUSES } from "@dude/domain";
import { AGENT_ROLE_NAMES } from "../../tokens/palette.ts";
import { people } from "../navFixtures.ts";

/*
 * Agents on an epic: the epic "Metric preview in the metric form", its four
 * tasks and a peer from another project linked by "depends on", as
 * docs/design/agent-coordination.md tells it.
 */

const at = (hm: string) => {
  const [h, m, s] = hm.split(":").map(Number);
  return new Date(2026, 9, 12, h ?? 0, m ?? 0, s ?? 0);
};

const T: Record<string, PeerTask & { readonly title: string }> = {
  "ABS-1": { key: "ABS-1", title: "Engine: estimate histogram endpoint", status: "running", role: "implementer", live: true },
  "ABS-2": { key: "ABS-2", title: "Backend: metric preview endpoints", status: "running", role: "implementer", live: true },
  "ABS-3": { key: "ABS-3", title: "Custom SQL on the estimate path", status: "review", role: "reviewer", live: true },
  "ABS-4": { key: "ABS-4", title: "Frontend: preview panel in the metric form", status: "running", role: "implementer", live: true },
};

const ASK: PeerMessageData = {
  id: "pm_1", from: T["ABS-4"]!, to: T["ABS-2"]!, text: "What body and response will `/v1/metrics/preview/*` use? I'll code to it.",
  sentAt: at("13:43:20"), delivery: { kind: "read", at: at("13:43:52"), after: "Bash" },
};
const ANSWER: PeerMessageData = {
  id: "pm_4", from: T["ABS-2"]!, to: T["ABS-4"]!, replyTo: "pm_1", sentAt: at("13:49:05"), delivery: { kind: "read", at: at("13:49:06") },
  text: "Both are `POST` under `/v1/metrics/preview/`. Body `{draft, unit_type_id, from, sample}`; columns `metric_<id>_mean`, `_var`, `_min`, `_bins`, plus `sample_fraction`. Pinned it as an epic note.",
};
const PATH_Q: PeerMessageData = {
  id: "pm_2", from: T["ABS-2"]!, to: T["ABS-1"]!, text: "Which path does your estimate histogram use?", sentAt: at("13:45:10"),
  delivery: { kind: "read", at: at("13:45:31"), after: "Edit" },
};
const NO_RUN: PeerMessageData = {
  id: "pm_9", from: T["ABS-4"]!, to: { ...T["ABS-3"]!, live: false }, sentAt: at("15:08:40"), delivery: { kind: "inbox", notice: "ABS-3 has no Run going; it will see this when its next Run starts." },
  text: "Do custom SQL metrics come back with the same `metric_<id>_bins` column, or can it be missing?",
};

const MARCIO = people["marcio"]!;
const CONTRACT = (v: string) =>
  `**Preview API body and response (ABS-2).** Both routes take \`{draft, unit_type_id, from, sample}\`. Columns \`metric_<id>_mean\`, \`${v}\`, \`_min\`, \`_bins\`, and \`sample_fraction\`.`;
const N4: EpicNoteData = { id: "en_4", n: 4, text: CONTRACT("_variance"), by: { kind: "agent", task: "ABS-2", role: "implementer", for: people["ana"] },
  at: at("13:51:20"), readBy: 3, replaces: 3 };
const N3: EpicNoteData = { ...N4, id: "en_3", n: 3, text: CONTRACT("_var"), at: at("13:48:40"), readBy: 4, replaces: null, replacedBy: { n: 4, at: at("13:51:20") } };
const N2: EpicNoteData = { id: "en_2", n: 2, text: "**Preview API (ABS-2).** ABS-2 serves `POST /v1/metrics/preview/history` and `/histogram`; ABS-4 calls them.",
  by: { kind: "person", person: MARCIO }, fromSession: true, at: at("13:40:30"), readBy: 4 };
const N1: EpicNoteData = { id: "en_1", n: 1, text: "Read `.ai/personal/marcio/docs/llm-agent/INDEX.md` for how to run tests here.",
  by: { kind: "person", person: MARCIO }, at: at("13:40:30"), readBy: 4 };
const N2_RETIRED: EpicNoteData = { ...N2, retired: { by: MARCIO.name, at: at("15:26:00") } };

const PEERS: PeerRow[] = [
  { ...T["ABS-1"]!, phase: "implement", run: "live", branch: "dude/wi_0mv5au1/attempt-1", head: "3f9a2c1",
    files: [{ path: "engine/src/EstimateMetricsHistogramQueryHandler.java", add: 214, del: 0 }, { path: "engine/src/Routes.java", add: 6, del: 1 }],
    lastSaid: "Added the handler at POST /v1/query/estimate/histogram/metrics; running the engine tests next." },
  { ...T["ABS-2"]!, phase: "implement", run: "live", branch: "dude/wi_0mv5auwan3/attempt-1", head: "b71e04d",
    files: [{ path: "backend/src/routes/metrics/preview.ts", add: 188, del: 0 }], lastSaid: "Replaced the contract note: `_var` is now `_variance`.",
    forYou: { count: 1, waits: true } },
  { ...T["ABS-3"]!, phase: "review", run: "parked", branch: "dude/wi_0mv5au3/attempt-1", head: "e20c9b7", files: [],
    lastSaid: "Implementation done; in review." },
  { ...T["ABS-4"]!, phase: "implement", run: "live", branch: "dude/wi_0mv5au4/attempt-1", head: null, files: [], waitingOn: "ABS-2",
    lastSaid: "Panel and chart render from mock data. Waiting on ABS-2's body." },
  { key: "JERV-2", title: "Metric preview in the experiment setup", status: "running", role: "implementer", phase: "implement", run: "live",
    branch: "dude/wi_0mv6jr2/attempt-1", head: "71ac3d0", files: [{ path: "web/src/setup/MetricStep.tsx", add: 42, del: 6 }],
    lastSaid: "Reusing ABS-2's preview endpoint.", link: { dependsOn: "ABS-2", project: "jervasion" } },
];

const FOOT = "Between ABS-4's and ABS-2's agents. Everyone on the epic can read it; only the two agents write in it.";

export function PeersSection({ mode }: { readonly mode: PaneMode }) {
  return (
    <Section id="peers" title="Peers"
      intro="Agents on an epic read, ask and brief each other. A peer message wears the peer wash, a teal-cyan no other message background uses; its rail stays its sender's role colour and its face is its role face. Never the accent (a person's steer), never attention (needs you), never a person's face. Waiting on a peer and a note's notice are neutral ink.">
      <Block id="pe-chip" title="TaskChip"
        note="A task named inline: status mark, mono key, its live agent's face. A button that says what it opens, or an image with the same name. In every status and role.">
        <Panes mode={mode}>
          <States items={[
            ["as a button", <TaskChip task={T["ABS-2"]!} onOpen={() => undefined} />],
            ["as a name, with its role", <TaskChip task={T["ABS-4"]!} showRole />],
            ["every status", <>{ALL_TASK_STATUSES.map((s) => <TaskChip key={s} task={{ key: "ABS-2", status: s, role: "implementer" }} />)}</>],
            ["every role", <>{AGENT_ROLE_NAMES.map((r) => <TaskChip key={r} task={{ key: "ABS-2", status: "running", role: r, live: true }} showRole />)}</>],
            ["key only", <TaskChip task={{ key: "JERV-2" }} />],
          ]} />
        </Panes>
      </Block>

      <Block id="pe-message" title="PeerMessage"
        note="A message between two tasks' agents, as a turn on the peer wash (hover it for the hover step): the sender's face and a 2px rail in its role colour, dashed until it is read; 'ABS-4 Implementer → ABS-2'; a neutral Peer or Reply tag; its delivery in a steer's words. Between an implementer's own turn and a person's steer, so the three read apart by background alone.">
        <Panes mode={mode} surface>
          <Col>
            <Label>an implementer's own turn, a peer message read where the agent read it, a person's steer</Label>
            <ChatMessage role="implementer" content="Note 2 has the paths, not the body. Asking ABS-2 now." startedAt={at("13:43:12")} />
            <PeerMessage msg={ASK} viewer="ABS-2" />
            <ChatMessage role="human" name="Márcio" intent="steer" content="Keep the client in one file." startedAt={at("13:44:00")} deliveredAt={at("13:44:10")} read />
            <Label>in the sender's transcript: sent · carrying on</Label>
            <PeerMessage msg={ASK} viewer="ABS-4" carryingOn />
            <Label>queued: lands at the peer's next step</Label>
            <PeerMessage msg={{ ...PATH_Q, delivery: { kind: "queued" } }} />
            <Label>no Run going: held for the peer's next Run</Label>
            <PeerMessage msg={NO_RUN} viewer="ABS-4" carryingOn />
            <Label>a reply, quoting what it answers</Label>
            <PeerMessage msg={ANSWER} viewer="ABS-4" replyToText={ASK.text} />
            <Label>refused</Label>
            <PeerMessage msg={{ ...PATH_Q, delivery: { kind: "queued" } }} viewer="ABS-2"
              refused="this Run has sent 5 messages in the last hour, the most a Run may send. The next is allowed at 15:12 UTC." />
            <Label>compact, waiting (in a thread)</Label>
            <PeerMessage compact msg={ASK} waiting={{ since: at("13:47:02"), note: "Parks at 13:57 if no reply." }} />
          </Col>
        </Panes>
      </Block>

      <Block id="pe-wait" title="PeerWaitLine"
        note="Where a Run stopped to wait on a peer, in its own transcript: one quiet line on the text column with a clock. Waiting by `dude wait`, by a turn that ended with its question unanswered, answered, and the peer ending without a reply — next to an EpicNoteNotice and a ChatNotice, so the three read apart.">
        <Panes mode={mode} surface>
          <Col>
            <PeerWaitLine on="ABS-2" msg="pm_1" how="wait" since={at("13:47:02")} parksAt={at("13:57:02")} now={at("13:53:02")} />
            <PeerWaitLine on="ABS-2" msg="pm_1" how="turn-ended" since={at("13:47:02")} parksAt={at("13:57:02")} now={at("13:53:02")} />
            <PeerWaitLine on="ABS-2" msg="pm_1" how="wait" since={at("13:47:02")} parksAt={at("13:57:02")} ended={{ at: at("13:49:06"), by: "reply" }} />
            <PeerWaitLine on="ABS-2" msg="pm_1" how="wait" since={at("13:47:02")} ended={{ at: at("13:52:00"), by: "peer_ended", status: "aborted" }} />
            <EpicNoteNotice change="added" note={N3} read={{ at: at("13:49:06") }} />
            <ChatNotice kind="parked" by="Duder" text="Parked while it waits on a peer's reply" at={at("13:57:02")} />
          </Col>
        </Panes>
      </Block>

      <Block id="pe-note" title="EpicNote / EpicNoteCount / EpicNoteHistory / EpicNoteRow"
        note="A note pinned for every agent on the epic: the pin, the words, its number, who pinned it (a person, an agent on its person's face, or a person filing a brainstorm's proposal), when, what it replaces, how many live agents have read it, and a person's Replace and Retire. Replaced or retired, it is history: no actions. The count is the section's, against its bound.">
        <Panes mode={mode}>
          <Col>
            <PageSection title="Epic notes" count={3} limit={10} countNoun="notes">
              <ul style={{ display: "flex", flexDirection: "column", gap: 2, margin: 0, padding: 0, listStyle: "none" }}>
                <EpicNote note={N4} live={4} isNew onReplace={() => undefined} onRetire={() => undefined} />
                <EpicNote note={N2} live={4} onReplace={() => undefined} onRetire={() => undefined} />
                <EpicNote note={N1} live={4} onReplace={() => undefined} onRetire={() => undefined} />
              </ul>
              <EpicNoteHistory notes={[N3]} live={4} />
            </PageSection>
            <Label>the history, open: a replaced note and a retired one</Label>
            <EpicNoteHistory notes={[N3, N2_RETIRED]} live={4} defaultOpen />
            <Label>EpicNoteCount at 3 and at 10</Label>
            <span className="ds-label">Epic notes<EpicNoteCount count={3} limit={10} /></span>
            <span className="ds-label">Epic notes<EpicNoteCount count={10} limit={10} /></span>
            <Label>EpicNoteRow: the agent's own note call, pinning and replacing</Label>
            <ChatAside>
              <ToolCallCard name="note" icon="pin" status="completed" summary="dude note · note 3, told to 3 agents at their next step" durationMs={70} />
              <EpicNoteRow note={{ ...N3, replaces: null }} />
              <EpicNoteRow note={N4} />
            </ChatAside>
          </Col>
        </Panes>
      </Block>

      <Block id="pe-notice" title="EpicNoteNotice"
        note="What every agent on the epic is told at its next step: added, replaced (the words that changed, and what they were) and retired. One quiet line with the pin, never a face, a rail or a tag — between a PeerMessage and a ChatNotice so the three read apart.">
        <Panes mode={mode} surface>
          <Col>
            <PeerMessage msg={ANSWER} viewer="ABS-4" replyToText={ASK.text} />
            <EpicNoteNotice change="added" note={N3} read={{ at: at("13:49:06") }} />
            <EpicNoteNotice change="replaced" note={N4} was={N3} changed={{ now: "_variance", was: "_var" }} read={{ at: at("13:51:24"), after: "Edit" }} />
            <EpicNoteNotice change="retired" note={N2_RETIRED} read={{ at: at("15:26:04"), after: "Write" }} />
            <ChatNotice kind="notice" by="Duder" text="The agent compacted its context." at={at("15:30:00")} />
            <Label>at 360px</Label>
            <div style={{ width: 360 }}>
              <EpicNoteNotice change="replaced" note={N4} was={N3} changed={{ now: "_variance", was: "_var" }} read={{ at: at("13:51:24"), after: "Edit" }} />
            </div>
          </Col>
        </Panes>
      </Block>

      <Block id="pe-thread" title="PeerThread"
        note="Two tasks' agents talking: subject, the pair, state, the messages, and who writes. Read only for people: no composer, no Reply. Under 520px the chips drop their role words.">
        <Panes mode={mode}>
          <Col>
            <PeerThread a={T["ABS-4"]!} b={T["ABS-2"]!} subject="Preview API body and response" foot={FOOT}
              state={{ kind: "waiting", on: "ABS-2", since: at("13:47:02") }}>
              <PeerMessage compact msg={ASK} waiting={{ since: at("13:47:02"), note: "Asked at 13:43 and carried on. Parks at 13:57 if no reply." }} />
            </PeerThread>
            <PeerThread a={T["ABS-2"]!} b={T["ABS-1"]!} subject="Engine path for the estimate histogram" state={{ kind: "open", on: "ABS-1" }}
              foot="Between ABS-2's and ABS-1's agents. Everyone on the epic can read it; only the two agents write in it.">
              <PeerMessage compact msg={PATH_Q} />
            </PeerThread>
            <PeerThread a={T["ABS-4"]!} b={T["ABS-2"]!} subject="Preview API body and response" foot={FOOT}
              state={{ kind: "answered", at: at("13:49:05") }}>
              <PeerMessage compact msg={ASK} />
              <PeerMessage compact msg={ANSWER} replyToText={ASK.text} />
            </PeerThread>
            <Label>at 360px</Label>
            <div style={{ width: 360 }}>
              <PeerThread a={T["ABS-4"]!} b={T["ABS-2"]!} subject="Preview API body and response" foot={FOOT}
                state={{ kind: "answered", at: at("13:49:05") }}>
                <PeerMessage compact msg={ANSWER} replyToText={ASK.text} />
              </PeerThread>
            </div>
          </Col>
        </Panes>
      </Block>

      <Block id="pe-digest" title="SinceDigest"
        note="What a conductor was told of its epic when it woke for something else: one muted line just before the briefing that carried it, folded and open. Never a wake, never a tone.">
        <Panes mode={mode} surface>
          {() => {
            const groups = [
              { title: "Messages with ABS-2", items: [
                { id: "d1", lead: <TaskChip task={T["ABS-4"]!} />, text: "asked for the preview body and response · answered 13:49", at: at("13:43") },
                { id: "d2", lead: <TaskChip task={T["ABS-1"]!} />, text: <>answered the engine path: <code>POST /v1/query/estimate/histogram/metrics</code></>, at: at("13:46") },
              ] },
              { title: "Epic notes added, replaced or retired", items: [
                { id: "d3", lead: <Icon name="pin" size={14} />, text: "Note 4 replaces note 3: Preview API body and response", at: at("13:51") },
              ] },
              { title: "Peers that changed", items: [
                { id: "d4", lead: <TaskChip task={{ ...T["ABS-1"]!, status: "review", role: "reviewer" }} />, text: "is in review", at: at("14:12") },
              ] },
            ];
            return (
              <Col>
                <SinceDigest since={at("13:44")} woke={at("14:20")} counts="2 messages · 1 note change · 1 peer moved" groups={groups} />
                <SinceDigest since={at("13:44")} woke={at("14:20")} counts="2 messages · 1 note change · 1 peer moved" groups={groups} defaultOpen />
                <ChatMessage role="system" name="Duder" intent="briefing" content="Conductor, dude woke you about ABS-2: a decision is yours." startedAt={at("14:20:06")} />
              </Col>
            );
          }}
        </Panes>
      </Block>

      <Block id="pe-mark" title="PeerWaitMark"
        note="Agents on an epic waiting on each other: a clock and the words, neutral, every pair in its name. On the epic card, after the title.">
        <Panes mode={mode}>
          <States items={[
            ["one", <PeerWaitMark pairs={[["ABS-4", "ABS-2"]]} />],
            ["several", <PeerWaitMark pairs={[["ABS-4", "ABS-2"], ["ABS-2", "ABS-1"]]} />],
          ]} />
        </Panes>
      </Block>

      <Block id="pe-list" title="PeerList"
        note="What list_tasks --peers returns, as rows: in a tool call's result, on the Coordination view, and in a Run's rail. A peer linked by 'depends on' from another project carries a neutral link chip; a peer with messages for the caller, a neutral chip saying so.">
        <Panes mode={mode}>
          <Col>
            <Label>tool (ABS-2's call)</Label>
            <PeerList variant="tool" label="Peers of ABS-2" self="ABS-2" peers={PEERS.map((p) => (p.key === "ABS-4" ? { ...p, forYou: { count: 1, waits: true } } : p))} />
            <Label>page</Label>
            <PeerList label="Tasks on this epic" peers={PEERS} onOpen={() => undefined} />
            <Label>rail</Label>
            <div style={{ width: 280 }}>
              <PeerList variant="rail" label="Peers of ABS-4" peers={PEERS.filter((p) => p.key !== "ABS-4")} />
            </div>
          </Col>
        </Panes>
      </Block>
    </Section>
  );
}
