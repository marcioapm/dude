/*
 * Screen 8: the brainstorm session that planned the epic, and its proposal
 * card, the app's own ProposalCard. The epic item carries two notes under
 * its description: they are pinned on the epic when a person files it, and
 * only then. Nothing a brainstorm proposes takes effect before that.
 */

import { useState } from "react";
import {
  Capabilities, ChatComposer, ChatMessage, ChatTranscript, LinkedProjects, ProposalCard, ScreenHeader, Segmented, SessionPeople,
  SessionRail, SessionRailBlock, SessionTitle, type ProposalCardItem,
} from "@dude/design-system/components";
import { Markdown } from "@dude/design-system/components";
import { PinGlyph } from "./Peer.tsx";
import { EPIC, P, PEERS, PLANNED, SESSION, at, type TaskKey } from "./data.ts";
import ps from "./peer.module.css";

const ABS = { key: "ABS", name: "abs" };

/** The notes a proposed epic carries, under its description: what every agent on it will be told once it is filed. */
function ProposedNotes() {
  return (
    <div className={ps.proposedNotes}>
      <div className={ps.proposedNotesHead}><PinGlyph size={12} />{PLANNED.length} epic notes, pinned when it is filed</div>
      <ul className={ps.proposedNotesList}>
        {[...PLANNED].reverse().map((n) => (
          <li key={n.id}><Markdown source={n.text} unmeasured /></li>
        ))}
      </ul>
    </div>
  );
}

const TASK_GOALS: Record<Exclude<TaskKey, "JERV-2">, string> = {
  "ABS-1": "Add POST /v1/query/estimate/histogram/metrics to the engine: binned values of a metric over a sample.",
  "ABS-2": "Add /v1/metrics/preview/history and /histogram: a draft metric's history and histogram through the estimate path.",
  "ABS-3": "Let custom SQL metrics go through the estimate path's sampling, as built-in ones do.",
  "ABS-4": "A preview panel in the metric form: the draft's history and histogram, from a sample.",
};

const ITEMS: ProposalCardItem[] = [
  {
    kind: "epic", title: EPIC, project: ABS, canFile: true,
    detail: <>
      <div>Preview a metric's history and histogram in the metric form, from a sample, before saving it.</div>
      <ProposedNotes />
    </>,
  },
  ...(["ABS-1", "ABS-2", "ABS-3", "ABS-4"] as const).map((k): ProposalCardItem => ({
    kind: "task", child: true, title: PEERS[k].title, detail: TASK_GOALS[k], project: ABS, canFile: true,
  })),
];

const ANSWER = `Four tasks under one epic. ABS-2 sits between the engine and the form, so the two things every task must hold to from the start are notes on the epic rather than lines in the description:

- **who serves and who calls the preview API**, so nobody builds ABS-1's engine endpoint a second time; ABS-2 replaces it with the body and response once it settles them;
- **where the docs are**, which you'd otherwise tell each Run by hand.

The description stays the what and why, for people. Filing pins both notes; nothing starts.`;

export function BrainstormProposal() {
  const [selected, setSelected] = useState<Set<number>>(() => new Set(ITEMS.map((_, i) => i)));
  return (
    <div className="screen sessionScreen" data-testid="session-screen">
      <ScreenHeader fillTitle title={<SessionTitle title={SESSION.title} onRename={async () => undefined} />}
        meta={<span>Brainstorm · claude-opus-5-5</span>} />
      <div className="runScreen" data-view="chat">
        <div className="runBar">
          <Segmented size="sm" label="Show" value="chat" onChange={() => undefined}
            options={[{ value: "chat", label: "Conversation" }, { value: "events", label: "Events" }]} />
        </div>
        <div className="runView">
          <div className="runChat">
            <ChatTranscript fill revision={1} turns={3}
              footer={<ChatComposer mode="chat" placeholder="Message the brainstorm…" onSubmit={() => true} to={<>To <b>Brainstorm</b></>}
                sentAs="Márcio · everyone in the session sees it" />}>
              <ChatMessage role="human" person={P["marcio"]} intent="message" startedAt={at("13:31:10")}
                content="Plan the metric preview for abs: engine histogram, backend preview routes, custom SQL on the estimate path, and the panel in the form. Agents must read .ai/personal/marcio/docs/llm-agent/INDEX.md for tests." />
              <ChatMessage role="brainstorm" content={ANSWER} startedAt={at("13:36:40")} />
              <ProposalCard items={ITEMS} selected={selected} filingAs="Márcio"
                onToggle={(i) => setSelected((s) => { const n = new Set(s); if (n.has(i)) n.delete(i); else n.add(i); return n; })}
                onFile={() => undefined} />
            </ChatTranscript>
            <SessionRail className="runRail" aria-label="The session">
              <SessionRailBlock label="People">
                <SessionPeople members={[{ person: P["marcio"]!, role: "owner", open: true, you: true }, { person: P["jonas"]!, role: "chat" }]} />
              </SessionRailBlock>
              <SessionRailBlock label="Linked">
                <LinkedProjects projects={[{ key: "ABS", name: "abs", repositories: [{ name: "abs", defaultBranch: "main" }] }]} />
              </SessionRailBlock>
              <SessionRailBlock label="It can">
                <Capabilities can={["Read the linked repositories", "Search memory", "Propose epics, tasks, edits, comments and epic notes"]}
                  cannot={["Pin a note, or file anything, itself", "Start a task"]} />
              </SessionRailBlock>
            </SessionRail>
          </div>
        </div>
      </div>
    </div>
  );
}
