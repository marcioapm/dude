import { useMemo, useState, type ReactNode } from "react";
import { Block, Caption, Col, Label, Panes, Row, Section, States, type PaneMode } from "../Frame.tsx";
import { HumanAvatar, HumanAvatarStack, identitySlot } from "../../components/HumanAvatar.tsx";
import { AgentAvatar } from "../../components/AgentAvatar.tsx";
import { TriageRollup } from "../../components/TriageRollup.tsx";
import { NavTree } from "../../components/NavTree.tsx";
import { Sidebar } from "../../components/Sidebar.tsx";
import { StatusBadge } from "../../components/StatusBadge.tsx";
import { IconButton } from "../../primitives/Button.tsx";
import type { RowMenuItem } from "../../primitives/RowMenu.tsx";
import type { NavRow } from "../../util/navModel.ts";
import { ALL_STATUSES, type Status } from "../../tokens/status.ts";
import { COUNTED_TRIAGE_KINDS, TRIAGE_FOR_STATUS, TRIAGE_KINDS, TRIAGE_SPECS, type TriageCounts } from "../../tokens/triage.ts";
import { globalCounts, type NavRef } from "../../util/navModel.ts";
import { navProjects, navProjectsQuiet, people } from "../navFixtures.ts";

const ALL_PEOPLE = Object.values(people);

export function NavigationSection({ mode }: { readonly mode: PaneMode }) {
  return (
    <Section
      id="navigation"
      title="Navigation: the other half of the screen"
      intro="Persistent chrome beside the transcript. It has to answer four questions without a click — what needs me, what is active, what is ready, who is working on what — and stay calm at fifty work items. The mechanism is triage: every status rolls up into one of six buckets, and collapsed parents show the counted ones."
    >
      <Block
        id="nav-human"
        title="HumanAvatar"
        note="A person: full circle with a ring, initials rather than a glyph, a muted identity colour. Three channels an agent avatar never shares, so the two are not confusable even in grayscale. Colour is hashed from the id, so the same person is the same colour on every screen without a profile record; an image URL replaces the initials later and nothing else changes. Stacks overlap and overflow to +N with the full list in the title."
      >
        <Panes mode={mode} surface>
          <Col>
            <Row style={{ gap: 12 }}>
              {ALL_PEOPLE.map((p) => (
                <HumanAvatar key={p.id} person={p} size="md" showName detail={`slot ${identitySlot(p)}`} />
              ))}
            </Row>
            <States
              items={[
                ["xs / sm / md / lg", <>{(["xs", "sm", "md", "lg"] as const).map((s) => <HumanAvatar key={s} person={people["marcio"]!} size={s} />)}</>],
                ["initials", <>{["Márcio Martins", "marcio", "sam.delgado", "priya@example.com", "Jules", "李 明"].map((n) => <HumanAvatar key={n} person={{ name: n }} size="md" showName />)}</>],
                ["stack of 2", <HumanAvatarStack people={ALL_PEOPLE.slice(0, 2)} size="sm" />],
                ["stack of 5, max 3", <HumanAvatarStack people={ALL_PEOPLE.slice(0, 5)} size="sm" max={3} />],
                ["stack of 8, xs", <HumanAvatarStack people={ALL_PEOPLE} size="xs" max={3} />],
                [
                  "human vs agent",
                  <Row style={{ gap: 16 }}>
                    <HumanAvatar person={people["ana"]!} size="md" showName />
                    <AgentAvatar role="reviewer" size="md" name="reviewer" />
                    <AgentAvatar role="orchestrator" size="md" name="orchestrator" />
                    <AgentAvatar role="human" size="md" name="human (event actor)" />
                  </Row>,
                ],
                [
                  "grayscale",
                  <span style={{ filter: "grayscale(1)", display: "inline-flex", gap: 8, alignItems: "center" }}>
                    <HumanAvatarStack people={ALL_PEOPLE.slice(0, 4)} size="sm" max={4} />
                    <AgentAvatar role="implementer" size="sm" />
                    <AgentAvatar role="orchestrator" size="sm" />
                  </span>,
                ],
              ]}
            />
          </Col>
        </Panes>
      </Block>

      <Block
        id="nav-triage"
        title="Triage vocabulary & TriageRollup"
        note="Every domain status lands in one of six buckets; four are counted. The roll-up draws a StatusBadge dot per non-empty bucket, most urgent first, so a collapsed project still says what is inside it. Needs-you keeps its diamond and ring and is the only count in attention ink. Waiting and done are never counted: the calm majority stays silent."
      >
        <Panes mode={mode} surface>
          <Col>
            <Label>Status → bucket</Label>
            <div style={{ display: "grid", gridTemplateColumns: "repeat(3, max-content)", gap: "4px 24px", alignItems: "center" }}>
              {ALL_STATUSES.map((s) => (
                <TriageMapRow key={s} status={s} />
              ))}
            </div>
            <Label>Roll-ups</Label>
            <States
              items={[
                ["2 need you · 5 active · 1 ready", <TriageRollup counts={{ needs_you: 2, active: 5, ready: 1, failed: 0, waiting: 12, done: 30 }} />],
                ["verbose", <TriageRollup counts={{ needs_you: 2, active: 5, ready: 1, failed: 1, waiting: 0, done: 0 }} verbose />],
                ["only active", <TriageRollup counts={{ needs_you: 0, active: 3, ready: 0, failed: 0, waiting: 4, done: 9 }} />],
                ["one failed", <TriageRollup counts={{ needs_you: 0, active: 0, ready: 0, failed: 1, waiting: 0, done: 20 }} />],
                ["quiet (renders nothing)", <TriageRollup counts={{ needs_you: 0, active: 0, ready: 0, failed: 0, waiting: 4, done: 9 }} />],
                ["all six", <TriageRollup counts={{ needs_you: 1, active: 1, ready: 1, failed: 1, waiting: 1, done: 1 }} only={TRIAGE_KINDS} verbose />],
              ]}
            />
            <div style={{ display: "grid", gridTemplateColumns: "max-content max-content max-content minmax(0, 1fr)", gap: "6px 16px", alignItems: "center" }}>
              {COUNTED_TRIAGE_KINDS.map((k) => (
                <FragmentRow key={k} cells={[<Caption>{k}</Caption>, <Caption>{TRIAGE_SPECS[k].tone}</Caption>, <StatusBadge status={TRIAGE_SPECS[k].status} size="sm" />, <span style={{ fontSize: 12, color: "var(--ds-color-text-secondary)" }}>{TRIAGE_SPECS[k].description}</span>]} />
              ))}
            </div>
          </Col>
        </Panes>
      </Block>

      <Block
        id="nav-tree"
        title="NavTree"
        note="Project → Epic → Work item → Session, rendered flat with aria-level so keyboard movement is index arithmetic. Each level has its own row grammar: projects are sticky small-caps headers, epics carry the layers glyph and a total, work items lead with a status dot and a mono key and trail with who is working (agent squares) and who is involved (human circles), sessions sit on a guide line behind a role avatar. Earlier Runs fold into one 'Attempt n' row each. Click a chevron or use ←→ to fold; a needs-you row opens by default down to the asking session, and a collapsed parent keeps its roll-up. Try ↑↓ → ← Home End Enter and / ."
      >
        <Panes mode={mode} surface>
          <TreeDemo />
        </Panes>
      </Block>

      <Block
        id="nav-tree-menus"
        title="NavTree — row menus"
        note="Projects, epics and work items get a '…' menu from the app via menuItems (or a menu render prop for full control); the tree knows nothing about the actions. The trigger is visible on hover and focus and stays out of the tab order: with a row focused, Shift+F10 or the context-menu key opens it, as does right-click, and focus returns to the row when it closes so ↑↓ keep working. Rows that return no items draw nothing. Try: focus a row, Shift+F10, ↓, Enter."
      >
        <Panes mode={mode} surface>
          <TreeMenuDemo />
        </Panes>
      </Block>

      <Block
        id="nav-sidebar"
        title="Sidebar — realistic"
        note="The whole thing at its default 304px with three projects and fifty work items. Header, search (/ from the tree, ↓ into it), four filter chips with global counts, the pinned Needs-you list across every project, then the tree. The pinned list is what makes 'what needs me' answerable without expanding anything: it names the work item, who is asking, who it waits on, and where it lives. Select a row to see the selection follow into the tree."
      >
        <Panes mode={mode}>
          <SidebarDemo />
        </Panes>
      </Block>

      <Block id="nav-sidebar-states" title="Sidebar — quiet, filtered, narrow, empty, loading" note="A quiet project shows no roll-ups, no pinned section and no attention ink anywhere. A filter forces the ancestors open and greys the chevrons. Below 296px the chips drop their labels and keep mark + count. Empty and loading are one line and skeleton rows respectively.">
        <Panes mode={mode}>
          <div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fit, minmax(190px, 1fr))", gap: 12 }}>
            {(
              [
                ["quiet org", <Sidebar projects={navProjectsQuiet} title="quiet org" width="100%" />],
                ["filtered: active", <Sidebar projects={navProjects} title="filtered" width="100%" triage="active" query="" />],
                ["narrow (200px)", <Sidebar projects={navProjects} title="narrow" width={200} hideAttention />],
                ["no matches", <Sidebar projects={navProjects} title="no matches" width="100%" query="zzzz" />],
                ["empty", <Sidebar projects={[]} title="empty" width="100%" />],
                ["loading", <Sidebar projects={[]} title="loading" width="100%" loading />],
              ] as const
            ).map(([label, el]) => (
              <div key={label} style={{ height: 360, overflow: "hidden", border: "1px solid var(--ds-color-border-subtle)", borderRadius: 6, display: "flex" }}>
                {el}
              </div>
            ))}
          </div>
        </Panes>
      </Block>
    </Section>
  );
}

function TriageMapRow({ status }: { readonly status: Status }) {
  const k = TRIAGE_FOR_STATUS[status];
  return (
    <>
      <StatusBadge status={status} size="sm" />
      <Caption>→</Caption>
      <span style={{ fontSize: 12, color: k === "needs_you" ? "var(--ds-tone-attention-fg)" : "var(--ds-color-text-secondary)" }}>{TRIAGE_SPECS[k].label}</span>
    </>
  );
}

function FragmentRow({ cells }: { readonly cells: ReadonlyArray<ReactNode> }) {
  return (
    <>
      {cells.map((c, i) => (
        <span key={i}>{c}</span>
      ))}
    </>
  );
}

function CountsLine({ counts }: { readonly counts: TriageCounts }) {
  return (
    <Caption>
      {COUNTED_TRIAGE_KINDS.map((k) => `${TRIAGE_SPECS[k].countLabel(counts[k])}`).join(" · ")} · {counts.waiting} waiting · {counts.done} done
    </Caption>
  );
}

function TreeDemo() {
  const [selected, setSelected] = useState<NavRef | null>({ kind: "session", id: "s_2401-orc" });
  const counts = useMemo(() => globalCounts(navProjects), []);
  return (
    <Col>
      <CountsLine counts={counts} />
      <div style={{ border: "1px solid var(--ds-color-border-subtle)", borderRadius: 6, maxHeight: 520, overflow: "auto", width: 320, background: "var(--ds-color-surface)" }}>
        <NavTree projects={navProjects} selected={selected} onSelect={setSelected} />
      </div>
      <Caption>selected: {selected ? `${selected.kind} ${selected.id}` : "none"}</Caption>
    </Col>
  );
}

function menuItemsFor(row: NavRow, act: (label: string) => void): ReadonlyArray<RowMenuItem> | null {
  const on = (label: string) => () => act(label);
  switch (row.ref.kind) {
    case "project":
      return [
        { id: "settings", label: "Settings", icon: "settings", onSelect: on("Settings") },
        { id: "new-epic", label: "New epic", icon: "layers", onSelect: on("New epic") },
        { id: "new", label: "New work item", icon: "plus", onSelect: on("New work item") },
        { kind: "separator" },
        { id: "archive", label: "Archive", icon: "folder", tone: "danger", onSelect: on("Archive") },
      ];
    case "epic":
      return [
        { id: "edit", label: "Edit", icon: "edit", onSelect: on("Edit epic") },
        { id: "new", label: "New work item", icon: "plus", onSelect: on("New work item") },
        { kind: "separator" },
        { id: "up", label: "Move up", icon: "arrow-up", onSelect: on("Move up") },
        { id: "down", label: "Move down", icon: "arrow-down", onSelect: on("Move down") },
        { kind: "separator" },
        { id: "delete", label: "Delete", icon: "cross", tone: "danger", disabled: true, disabledReason: "Move its work items out first" },
      ];
    case "workItem":
      return [
        { id: "edit", label: "Edit", icon: "edit", onSelect: on("Edit") },
        { kind: "submenu", id: "move", label: "Move to epic", icon: "layers", items: [{ id: "e1", label: "Webhook reliability", onSelect: on("Move → Webhook reliability") }, { id: "e2", label: "Human intervention", onSelect: on("Move → Human intervention") }, { kind: "separator" }, { id: "none", label: "No epic", onSelect: on("Move → No epic") }] },
        { id: "split", label: "Split", icon: "simplifier", onSelect: on("Split") },
        { kind: "separator" },
        { id: "delete", label: "Delete", icon: "cross", tone: "danger", disabled: true, disabledReason: "It has run; abort it instead." },
      ];
    default:
      return null;
  }
}

function TreeMenuDemo() {
  const [selected, setSelected] = useState<NavRef | null>({ kind: "workItem", id: "wi_2402" });
  const [last, setLast] = useState<string | null>(null);
  return (
    <Col>
      <div style={{ border: "1px solid var(--ds-color-border-subtle)", borderRadius: 6, maxHeight: 420, overflow: "auto", width: 320, background: "var(--ds-color-surface)" }}>
        <NavTree projects={navProjectsQuiet.concat(navProjects.slice(0, 1))} selected={selected} onSelect={setSelected} menuItems={(row) => menuItemsFor(row, setLast)} />
      </div>
      <Caption>last action: {last ?? "none"}</Caption>
    </Col>
  );
}

function SidebarDemo() {
  const [selected, setSelected] = useState<NavRef | null>({ kind: "workItem", id: "wi_2402" });
  return (
    <Col>
      <div style={{ display: "flex", height: 640, border: "1px solid var(--ds-color-border-subtle)", borderRadius: 6, overflow: "hidden" }}>
        <Sidebar
          projects={navProjects}
          selected={selected}
          onSelect={(ref) => setSelected(ref)}
          title="dude"
          headerActions={
            <>
              <IconButton icon="plus" label="New task" size="sm" />
              <IconButton icon="more" label="More" size="sm" />
            </>
          }
          footer={
            <>
              <HumanAvatar person={people["marcio"]!} size="xs" showName />
              <span style={{ marginLeft: "auto", display: "inline-flex", alignItems: "center", gap: 6 }}>
                <StatusBadge status="running" variant="dot" iconOnly />
                <span>connected</span>
              </span>
            </>
          }
        />
        <div style={{ flex: 1, display: "grid", placeItems: "center", background: "var(--ds-color-canvas)", color: "var(--ds-color-text-muted)", fontSize: 12 }}>
          {selected ? `${selected.kind} · ${selected.id}` : "transcript goes here"}
        </div>
      </div>
    </Col>
  );
}
