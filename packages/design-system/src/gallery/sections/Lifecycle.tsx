import { useState, type ReactNode } from "react";
import { Block, Caption, Col, Label, Panes, Row, Section, type PaneMode } from "../Frame.tsx";
import { ArchiveDialog, ArchiveEpicDialog, ArchivedMark, CloseDialog, ResolutionMark, lifecycleMenuItems, type LifecycleTask, type LifecycleTaskRef } from "../../components/Lifecycle.tsx";
import { Board } from "../../components/Board.tsx";
import { RelationList } from "../../components/Relations.tsx";
import { StatusMark } from "../../components/StatusMark.tsx";
import { Button } from "../../primitives/Button.tsx";
import { RowMenu } from "../../primitives/RowMenu.tsx";
import type { NavProject } from "../../util/navModel.ts";
import { people } from "../navFixtures.ts";

const ana = people["ana"]!;

const CP63: LifecycleTaskRef = { key: "CP-63", title: "Export usage as CSV, quoting every field", status: "received" };
const CP62: LifecycleTaskRef = { key: "CP-62", title: "CSV export drops rows when a name has a comma", status: "received" };
const CP47: LifecycleTaskRef = { key: "CP-47", title: "Export usage as CSV", status: "received" };
const CP44: LifecycleTaskRef = { key: "CP-44", title: "Billing page: usage this month and projected invoice", status: "received" };
const CP46: LifecycleTaskRef = { key: "CP-46", title: "Usage alerts by email", status: "aborted" };
const CP64: LifecycleTaskRef = { key: "CP-64", title: "CSV export loses rows", status: "aborted", duplicateOf: "CP-62" };
const find = async (q: string) => [CP62, CP47, CP63, CP64].filter((t) => !q || `${t.key} ${t.title}`.toLowerCase().includes(q.toLowerCase()));
const NOT_YOURS = "Only Ana, the owner, or an admin can do this";

/** A dialog opened from its button, in the pane's theme and density (the portal follows the trigger's pane). */
function Opens({ label, children }: { readonly label: string; readonly children: (open: boolean, set: (o: boolean) => void) => ReactNode }) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <Button variant="secondary" size="sm" onClick={() => setOpen(true)}>{label}</Button>
      {children(open, setOpen)}
    </>
  );
}

/** The menu drawn open, in place, in its pane's theme. */
function MenuOpen({ theme, task, notYours }: { readonly theme: string; readonly task: LifecycleTask; readonly notYours?: string | undefined }) {
  const [host, setHost] = useState<HTMLElement | null>(null);
  return (
    <div ref={setHost} data-theme={theme} style={{ minHeight: 120, position: "relative" }}>
      {host ? <RowMenu label="Task actions" open forceMount container={host} align="start"
        items={lifecycleMenuItems(task, { notYours, onClose: () => undefined, onReopen: () => undefined, onArchive: () => undefined, onUnarchive: () => undefined })} /> : null}
    </div>
  );
}

const MENU_STATES: ReadonlyArray<readonly [string, LifecycleTask, string | undefined]> = [
  ["Backlog, the owner", { status: "received" }, undefined],
  ["Running", { status: "running" }, undefined],
  ["In review", { status: "review" }, undefined],
  ["Done, nothing to merge", { status: "done", resolution: "nothing_to_merge" }, undefined],
  ["Closed", { status: "aborted", resolution: "wont_do" }, undefined],
  ["Closed and archived", { status: "aborted", resolution: "wont_do", archived: true }, undefined],
  ["A member", { status: "received" }, NOT_YOURS],
];

const boardProject: NavProject = {
  id: "p_lc", name: "control-plane", archivedCount: 3,
  tasks: [
    { id: "a", key: "CP-46", title: "Usage alerts by email", status: "aborted", resolution: "wont_do", people: [ana] },
    { id: "b", key: "CP-63", title: "Export usage as CSV, quoting every field", status: "aborted", resolution: "duplicate", duplicateOf: "CP-62", people: [ana] },
    { id: "c", key: "CP-59", title: "Meter drifts after a restart", status: "aborted", resolution: "cant_reproduce", people: [ana] },
    { id: "d", key: "CP-38", title: "Spike: Stripe Tax for EU VAT", status: "aborted", people: [ana] },
    { id: "e", key: "CP-37", title: "Spike: price per unit or per tier?", status: "done", resolution: "nothing_to_merge", people: [ana] },
    { id: "f", key: "CP-62", title: "CSV export drops rows when a name has a comma", status: "received", people: [ana] },
  ],
};

export function LifecycleSection({ mode }: { readonly mode: PaneMode }) {
  return (
    <Section id="lifecycle" title="Lifecycle: closed and archived work"
      intro="A person ends a task with a reason (Close), and puts work away (Archive). Both are neutral and muted: neither needs anyone. Status still says what delivery did; the resolution sits beside it.">
      <Block id="lifecycle-marks" title="ResolutionMark / ArchivedMark"
        note="Each resolution as its small neutral badge, a duplicate with its canonical key, beside the status it explains; ArchivedMark is the archive glyph and the word in muted ink.">
        <Panes mode={mode} surface>
          <Col>
            {(["wont_do", "duplicate", "cant_reproduce", "nothing_to_merge"] as const).map((r) => (
              <Row key={r}>
                <StatusMark status={r === "nothing_to_merge" ? "done" : "aborted"} size="sm" />
                <ResolutionMark resolution={r} of={r === "duplicate" ? "CP-62" : undefined} />
              </Row>
            ))}
            <Row><StatusMark status="aborted" size="sm" /><ResolutionMark resolution="wont_do" /><ArchivedMark /></Row>
          </Col>
        </Panes>
      </Block>

      <Block id="lifecycle-menu" title="The task menu's lifecycle"
        note="Close… / Reopen and Archive… / Unarchive, shown to everyone and disabled with the reason: who may not, agents working, a pull request open, already ended, archived. Archive… on a task not ended opens Close and archive.">
        <Panes mode={mode} surface>
          {(theme) => (
            <div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fill, minmax(260px, 1fr))", gap: 16 }}>
              {MENU_STATES.map(([label, task, notYours]) => (
                <Col key={label}><Caption>{label}</Caption><MenuOpen theme={theme} task={task} notYours={notYours} /></Col>
              ))}
            </div>
          )}
        </Panes>
      </Block>

      <Block id="lifecycle-close" title="CloseDialog"
        note="Four resolutions, Nothing to merge disabled with why (Mark done's), Can't reproduce where the caller says it isn't this task's. Won't do with something waiting: it keeps waiting, with Remove the relation. Duplicate: the picker (refusing this task and a duplicate), then what the handover does. A member: the primary disabled with whose decision it is. Press to open.">
        <Panes mode={mode}>
          <Row>
            <Opens label="Won't do, CP-47 waits">{(o, s) => <CloseDialog open={o} onOpenChange={s} task={CP63} waiting={[CP47]} initial="wont_do" find={find} onConfirm={() => s(false)} />}</Opens>
            <Opens label="Duplicate, picking">{(o, s) => <CloseDialog open={o} onOpenChange={s} task={CP63} waiting={[CP47]} initial="duplicate" find={find} onConfirm={() => s(false)} />}</Opens>
            <Opens label="Duplicate of CP-62">{(o, s) => <CloseDialog open={o} onOpenChange={s} task={CP63} waiting={[CP47]} initial="duplicate" initialDuplicate={CP62} find={find} onConfirm={() => s(false)} />}</Opens>
            <Opens label="Can't reproduce refused">{(o, s) => <CloseDialog open={o} onOpenChange={s} task={CP44} find={find} onConfirm={() => s(false)}
              cantReproduceReason="Only for a bug: there is nothing to reproduce in a feature" />}</Opens>
            <Opens label="A member">{(o, s) => <CloseDialog open={o} onOpenChange={s} task={CP63} initial="wont_do" find={find} onConfirm={() => s(false)} refused={NOT_YOURS} />}</Opens>
            <Opens label="Refused by the server">{(o, s) => <CloseDialog open={o} onOpenChange={s} task={CP63} initial="wont_do" find={find} onConfirm={() => s(false)}
              problem="Stop its agents first: an agent of CP-63 is still working." />}</Opens>
          </Row>
        </Panes>
      </Block>

      <Block id="lifecycle-close-archive" title="Close and archive"
        note="Archive… on a task that hasn't ended: the Close dialog titled Close and archive, nothing chosen, its primary disabled until a resolution is; a member sees it disabled with whose it is.">
        <Panes mode={mode}>
          <Row>
            <Opens label="Close and archive CP-44">{(o, s) => <CloseDialog open={o} onOpenChange={s} task={CP44} andArchive find={find} onConfirm={() => s(false)} />}</Opens>
            <Opens label="…as a member">{(o, s) => <CloseDialog open={o} onOpenChange={s} task={CP44} andArchive find={find} onConfirm={() => s(false)} refused={NOT_YOURS} />}</Opens>
          </Row>
        </Panes>
      </Block>

      <Block id="lifecycle-archive" title="ArchiveDialog / ArchiveEpicDialog"
        note="An ended task: the plain confirmation, Keep it holding the focus. An epic with unended tasks: refused, listing them, its one action archiving the ended ones; every task ended: archives the epic and its tasks.">
        <Panes mode={mode}>
          <Row>
            <Opens label="Archive CP-46">{(o, s) => <ArchiveDialog open={o} onOpenChange={s} task={CP46} onConfirm={() => s(false)} />}</Opens>
            <Opens label="…as a member">{(o, s) => <ArchiveDialog open={o} onOpenChange={s} task={CP46} onConfirm={() => s(false)} refused={NOT_YOURS} />}</Opens>
            <Opens label="Epic refused">{(o, s) => <ArchiveEpicDialog open={o} onOpenChange={s} epic="Webhook reliability" onConfirm={() => s(false)}
              unfinished={[{ ...CP47, owner: "Ana" }, { ...CP62, owner: "Ana" }]} ended={[CP46, { key: "CP-30", title: "Retry webhooks", status: "done" }]} />}</Opens>
            <Opens label="Epic, all ended">{(o, s) => <ArchiveEpicDialog open={o} onOpenChange={s} epic="Webhook reliability" onConfirm={() => s(false)}
              unfinished={[]} ended={[CP46, { key: "CP-30", title: "Retry webhooks", status: "done" }]} />}</Opens>
          </Row>
        </Panes>
      </Block>

      <Block id="lifecycle-board" title="Board: closed cards, archived hint"
        note="In Closed, a closed card says its resolution on its marks line and 'closed' in its foot; a stopped one still says 'stopped'; done with nothing merged says so as today. Archived cards are not on the board: the header's quiet '3 archived' is the way to them.">
        <Panes mode={mode}>
          <div style={{ height: 520, display: "flex" }}>
            <Board project={boardProject} archivedHint={<a href="#lifecycle-board">3 archived</a>} />
          </div>
        </Panes>
      </Block>

      <Block id="lifecycle-relations" title="RelationList: duplicates"
        note="On the canonical task: Duplicated by, with the handover under it; on the closed one: Duplicate of. The copy glyph; neither removable here (Reopen removes it).">
        <Panes mode={mode} surface>
          <Col style={{ gap: 24 }}>
            <Label>CP-62</Label>
            <RelationList onRemove={() => undefined} groups={[
              { kind: "duplicated-by", rows: [{ key: "CP-63", title: CP63.title, status: "aborted", owner: ana }],
                note: "CP-63's blocking came here with it: CP-47 now waits on CP-62." },
              { kind: "blocks", rows: [{ key: "CP-47", title: CP47.title, status: "received", owner: ana }] },
            ]} />
            <Label>CP-63</Label>
            <RelationList onRemove={() => undefined} groups={[
              { kind: "duplicate-of", rows: [{ key: "CP-62", title: CP62.title, status: "received", owner: ana }] },
              { kind: "blocks", rows: [{ key: "CP-47", title: CP47.title, status: "received", owner: ana }] },
            ]} />
          </Col>
        </Panes>
      </Block>
    </Section>
  );
}
