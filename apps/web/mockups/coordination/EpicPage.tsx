/*
 * Screen 1 (and 7, at phone width): an epic's page with its proposed
 * second view, Coordination — the epic notes, then the threads between its
 * tasks' agents, and its tasks as `list_tasks --epic mine` sees them.
 * Screen 6: the project's page, its epic card carrying the waiting mark.
 *
 * The epic page is today's epic board (Board with the epic) with a
 * Board | Coordination switch in its header line; nothing else moves.
 */

import { Board, Breadcrumb, EpicCard, ScreenHeader, Segmented, type EpicSummary } from "@dude/design-system/components";
import { Button, RowMenu, Section, Textarea } from "@dude/design-system/primitives";
import { EpicNote, EpicNoteHistory, PeerList, PeerMessage, PeerThread, PeerWaitMark } from "./Peer.tsx";
import { EPIC, EPIC_ID, KEYS, MSG, NOTES, P, PEERS, PLANNED, PROJECT, REPLACED, THREADS, at, navAbs, type TaskKey } from "./data.ts";
import s from "./screens.module.css";

export type EpicView = "board" | "coordination";

function crumbs(onProject: () => void) {
  return [
    { id: "p", label: PROJECT, onSelect: onProject },
    { id: "e", label: EPIC, icon: "layers" as const },
  ];
}

/** Whether ABS-4's wait on ABS-2 is still open (before 13:49) or answered. */
export type Moment = "waiting" | "answered";

const THREAD_FOOT = <span>Between ABS-4's and ABS-2's agents. Everyone on the epic can read it; only the two agents write in it.</span>;

export function EpicPage({ view, onView, moment, historyOpen, onProject, onOpenTask }: {
  readonly view: EpicView;
  readonly onView: (v: EpicView) => void;
  readonly moment: Moment;
  /** The replaced notes, unfolded. */
  readonly historyOpen: boolean;
  readonly onProject: () => void;
  readonly onOpenTask: (k: TaskKey) => void;
}) {
  const project = navAbs(moment === "waiting");
  const epic = project.epics![0]!;
  const open = moment === "waiting" ? 1 : 0;
  const notes = moment === "waiting" ? PLANNED : NOTES;
  return (
    <div className="screen">
      <ScreenHeader className={s.headerPad}
        lead={<Breadcrumb items={crumbs(onProject)} />}
        meta={<Segmented size="sm" label="Epic view" tabs="epic" value={view} onChange={onView}
          options={[
            { value: "board", label: "Board" },
            { value: "coordination", label: <>Coordination{open ? <span className="ds-tnum" style={{ color: "var(--ds-color-text-muted)" }}> {open}</span> : null}</> },
          ]} />}
        actions={<>
          <Button size="sm" variant="quiet" leadingIcon="edit">Edit epic</Button>
          <Button size="sm" variant="primary" leadingIcon="plus">New task</Button>
        </>} />
      {view === "board" ? (
        <div className={s.boardWrap} id="epic-board-panel" role="tabpanel" aria-labelledby="epic-board-tab">
          <Board project={project} epic={epic} hideHeader onSelect={(ref) => {
            const k = KEYS.find((key) => PEERS[key].id === ref.id);
            if (k) onOpenTask(k);
          }} />
        </div>
      ) : (
        <div className={s.coord} id="epic-coordination-panel" role="tabpanel" aria-labelledby="epic-coordination-tab" data-testid="coordination">
          <div className={s.coordGrid}>
            <div className={s.coordMain}>
              <Section title="Epic notes" count={notes.length}
                actions={<Button size="sm" variant="secondary" leadingIcon="plus">Add note</Button>}>
                <p className={s.lead}>Working facts every agent on this epic must follow. A note is never edited: a new one replaces it. Every agent is told at its next step when one is added or replaced, and every Run that starts here has the current ones in its briefing.</p>
                <ul className={s.notes}>
                  {notes.map((n) => (
                    <EpicNote key={n.id} note={n} live={4} isNew={n.replaces !== undefined} onReplace={() => undefined} />
                  ))}
                </ul>
                {moment === "answered" ? <EpicNoteHistory key={String(historyOpen)} notes={REPLACED} live={4} defaultOpen={historyOpen} /> : null}
                <div className={s.noteForm}>
                  <Textarea label="New note" rows={2} maxRows={6} placeholder="A contract, a decision, where the docs are…" />
                  <div className={s.noteFormActions}>
                    <span>As Márcio · every agent on the epic is told at its next step. Text only: it can't steer or start anything.</span>
                    <Button size="sm" variant="secondary">Pin for the epic</Button>
                  </div>
                </div>
              </Section>
              <Section title="Between tasks" count={THREADS.length}>
                <div className={s.threads}>
                  <PeerThread a="ABS-4" b="ABS-2" subject="Preview API body and response"
                    state={moment === "waiting" ? { kind: "waiting", on: "ABS-2", since: at("13:43:20") } : { kind: "answered", at: at("13:49:05") }}
                    foot={THREAD_FOOT}>
                    <PeerMessage compact msg={MSG["pm_1"]!} waiting={moment === "waiting" ? { since: at("13:43:20"), parksIn: "Parks at 13:53 if no reply." } : undefined} />
                    {moment === "answered" ? <PeerMessage compact msg={MSG["pm_4"]!} replyToText={MSG["pm_1"]!.text} /> : null}
                  </PeerThread>
                  <PeerThread a="ABS-2" b="ABS-1" subject="Engine path for the estimate histogram"
                    state={moment === "waiting" ? { kind: "open", on: "ABS-1" } : { kind: "answered", at: at("13:46:02") }}
                    foot={<span>Between ABS-2's and ABS-1's agents. Everyone on the epic can read it; only the two agents write in it.</span>}>
                    <PeerMessage compact msg={MSG["pm_2"]!} />
                    {moment === "answered" ? <PeerMessage compact msg={MSG["pm_3"]!} replyToText={MSG["pm_2"]!.text} /> : null}
                  </PeerThread>
                </div>
              </Section>
            </div>
            <Section title="Tasks on this epic" count={KEYS.length}>
              <PeerList peers={KEYS.map((k) => PEERS[k])} onOpen={onOpenTask}
                overrides={moment === "answered"
                  ? { "ABS-4": { waitingOn: undefined, lastSaid: "Note 4 renames `_var` to `_variance`; changed the client's column to match. Writing the panel.", head: "a41d0e2", files: [{ path: "frontend/src/metrics/previewClient.ts", add: 76, del: 0 }] } }
                  : { "ABS-2": { head: "c02b9e8", files: [{ path: "backend/src/engine/client.ts", add: 6, del: 1 }], lastSaid: "ABS-4 is waiting on my contract. Checking what ABS-1 has before I fix anything; asked it for its engine path." } }} />
            </Section>
          </div>
        </div>
      )}
    </div>
  );
}

// --------------------------------------------------------------------------- Screen 6

const EPIC_SUMMARY: EpicSummary = {
  id: EPIC_ID,
  title: EPIC,
  description: "Preview a metric's history and histogram in the metric form, from a sample, before saving it.",
  lanes: { done: 0, review: 1, progress: 3, backlog: 0 },
  tasks: 4,
  prs: { open: 0 },
  people: [P["marcio"]!, P["jonas"]!],
  costUsd: 8.09,
  machineUsd: 0.64,
  when: "updated 1m ago",
};

/** A project's page: its epics above its board, the epic card with the waiting mark, and ABS-4's card saying whom it waits on. */
export function ProjectPage({ onOpenEpic, onOpenTask }: { readonly onOpenEpic: () => void; readonly onOpenTask: (k: TaskKey) => void }) {
  const project = navAbs(true);
  return (
    <div className="screen">
      <Board project={project} groupBy={null}
        onSelect={(ref) => {
          const k = KEYS.find((key) => PEERS[key].id === ref.id);
          if (k) onOpenTask(k);
        }}
        overview={
          <Section title="In progress" count={1}>
            <div className={s.epicCards}>
              <EpicCard epic={EPIC_SUMMARY} onOpen={onOpenEpic}
                actions={<span className={s.cardActions}>
                  <PeerWaitMark pairs={[["ABS-4", "ABS-2"]]} />
                  <RowMenu label={`State of ${EPIC}`} size="sm" items={[{ id: "coord", label: "Open Coordination", icon: "comments", onSelect: onOpenEpic }]} />
                </span>} />
            </div>
          </Section>
        }
        headerActions={<Button size="sm" variant="primary" leadingIcon="plus">New task</Button>} />
    </div>
  );
}
