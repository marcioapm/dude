import { useMemo, useState, type ReactNode } from "react";
import { Block, Caption, Col, Panes, Section, type PaneMode } from "../Frame.tsx";
import { Board } from "../../components/Board.tsx";
import { Sidebar } from "../../components/Sidebar.tsx";
import { StatusBadge } from "../../components/StatusBadge.tsx";
import { Button, IconButton } from "../../primitives/Button.tsx";
import { RowMenu } from "../../primitives/RowMenu.tsx";
import { EmptyState } from "../../primitives/Feedback.tsx";
import { WORK_ITEM_STATUSES } from "../../tokens/status.ts";
import { BOARD_COLUMN_FOR_STATUS, BOARD_COLUMN_KINDS, BOARD_COLUMN_SPECS, boardScope } from "../../util/boardModel.ts";
import type { NavRef } from "../../util/navModel.ts";
import { navProjectEmpty, navProjectEverything, navProjects, navProjectsQuiet } from "../navFixtures.ts";

const CONTROL = navProjects[0]!;
const WEBHOOKS = CONTROL.epics![0]!;

export function BoardSection({ mode }: { readonly mode: PaneMode }) {
  return (
    <Section
      id="board"
      title="Board: the overview for a project or an epic"
      intro="What the main pane shows when the sidebar selection is a project or an epic rather than a work item. The same view model as the tree, laid out by lifecycle stage instead of by hierarchy: five lanes, always all five, cards sorted needs-you first. Nothing drags — every move between lanes belongs to the workflow, and the two a person makes are decisions taken in the transcript. A card is a way in, not a handle."
    >
      <Block
        id="board-columns"
        title="Status → lane"
        note="Eleven work item statuses fold into five lanes, keyed on the domain union so a new status is a compile error before it is a blank column. Needs-you is not a lane: it can strike in Intake (confirm a plan) or In progress (an agent asks), so it is a card treatment and a sort order, as it is a row treatment in the tree. Closed holds done, failed and aborted together — failed sorts first and keeps its danger mark; aborted is a decision and stays neutral."
      >
        <Panes mode={mode} surface>
          <div style={{ display: "grid", gridTemplateColumns: "repeat(5, max-content)", gap: "6px 24px", alignItems: "start" }}>
            {BOARD_COLUMN_KINDS.map((k) => (
              <Col key={k} style={{ gap: 4 }}>
                <span style={{ fontSize: 11, fontWeight: 600, letterSpacing: "0.06em", textTransform: "uppercase", color: "var(--ds-color-text-secondary)" }}>{BOARD_COLUMN_SPECS[k].label}</span>
                {WORK_ITEM_STATUSES.filter((s) => BOARD_COLUMN_FOR_STATUS[s] === k).map((s) => (
                  <StatusBadge key={s} status={s} size="sm" />
                ))}
              </Col>
            ))}
          </div>
        </Panes>
      </Block>

      <Block
        id="board-project"
        title="Project board — realistic"
        note="control-plane: three epics and three loose items, twenty cards. A card is three lines: status mark, key, epic and time in lane; the title, clamped to two lines; then who is on it and what it cost. A running card shows the working roles and the deepest live activity; a needs-you card shows the asker and the question in attention ink, with the tree row's wash and bar, and clicking it lands on the asking session. Everything else is quiet: hairline, mono key, muted numbers. Try ↑↓ ←→ Home End Enter."
      >
        <Panes mode={mode}>
          <ProjectDemo />
        </Panes>
      </Block>

      <Block
        id="board-epic"
        title="Epic board"
        note="The same board narrowed to one epic. The header carries the project and the layers glyph so the scope is never ambiguous; cards drop their epic line because it would say the same thing six times."
      >
        <Panes mode={mode}>
          <EpicDemo />
        </Panes>
      </Block>

      <Block
        id="board-swimlanes"
        title="Group by epic — swimlanes"
        note="The project board read by epic: a row per epic in the project's order, then 'No epic', each with the same five lanes under one shared head row. The lane header is the epic's title with the layers glyph, a count, the roll-up and the spend, and folds the row to 28px; an epic with nothing in it keeps its row so the order the operator set is visible. Cards drop their epic line here — the row says it. ↑↓ walk a column across rows; ←→ stay in the row. The header's '…' is the app's RowMenu. The toggle in the header is the app's; the board takes groupBy."
      >
        <Panes mode={mode}>
          <SwimlaneDemo />
        </Panes>
      </Block>

      <Block
        id="board-composed"
        title="Beside the sidebar"
        note="How the app composes it: boardScope maps the sidebar selection to a project or epic board; a work item or session selection opens the transcript instead. Selecting a card on the board follows into the tree, and the tree's selection marks the card, so the two never disagree about where you are."
      >
        <Panes mode={mode}>
          <ComposedDemo />
        </Panes>
      </Block>

      <Block
        id="board-states"
        title="Quiet, overflowing, empty, loading, narrow"
        note="A quiet epic has no attention ink anywhere and folds its empty lanes to labelled rails. Past twelve cards a lane shows 'N more' — urgent cards sort to the top, so what folds is only ever the calm tail. Empty is one line and a hint; loading keeps the five labels and shimmers the cards. Below 720px the lanes stop sharing the width and scroll sideways at 220px each; below 480px the epic line and header roll-up go."
      >
        <Panes mode={mode}>
          <div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fit, minmax(280px, 1fr))", gap: 12 }}>
            {(
              [
                ["quiet", <Board project={navProjectsQuiet[0]!} hideHeader />],
                ["overflow (cap 6)", <Board project={navProjectEverything} cap={6} hideHeader />],
                ["empty", <Board project={navProjectEmpty} />],
                ["loading", <Board project={navProjectEmpty} loading />],
                ["narrow (360px)", <div style={{ width: 360, height: "100%" }}><Board project={CONTROL} /></div>],
              ] as const
            ).map(([label, el]) => (
              <Col key={label} style={{ gap: 4 }}>
                <Caption>{label}</Caption>
                <div style={{ height: 340, overflow: "hidden", border: "1px solid var(--ds-color-border-subtle)", borderRadius: 6, display: "flex" }}>{el}</div>
              </Col>
            ))}
          </div>
        </Panes>
      </Block>
    </Section>
  );
}

function Frame({ height = 520, children }: { readonly height?: number | undefined; readonly children: ReactNode }) {
  return <div style={{ height, display: "flex", border: "1px solid var(--ds-color-border-subtle)", borderRadius: 6, overflow: "hidden" }}>{children}</div>;
}

function ProjectDemo() {
  const [selected, setSelected] = useState<NavRef | null>({ kind: "workItem", id: "wi_2402" });
  return (
    <Col>
      <Frame>
        <Board
          project={CONTROL}
          selected={selected}
          onSelect={setSelected}
          headerActions={
            <>
              <IconButton icon="plus" label="New task" size="sm" />
              <IconButton icon="more" label="More" size="sm" />
            </>
          }
        />
      </Frame>
      <Caption>selected: {selected ? `${selected.kind} ${selected.id}` : "none"}</Caption>
    </Col>
  );
}

function SwimlaneDemo() {
  const [selected, setSelected] = useState<NavRef | null>({ kind: "workItem", id: "wi_2402" });
  const [grouped, setGrouped] = useState(true);
  const [collapsed, setCollapsed] = useState<ReadonlySet<string>>(() => new Set(["epic:e_intervention"]));
  const withEmptyEpic = useMemo(() => ({ ...CONTROL, epics: [...(CONTROL.epics ?? []), { id: "e_later", title: "Q4 performance", workItems: [] }] }), []);
  return (
    <Col>
      <Frame height={640}>
        <Board
          project={withEmptyEpic}
          selected={selected}
          onSelect={setSelected}
          groupBy={grouped ? "epic" : null}
          collapsed={collapsed}
          onCollapsedChange={setCollapsed}
          laneMenu={(lane) =>
            lane.epic ? (
              <RowMenu
                label={`Actions for ${lane.title}`}
                items={[
                  { id: "edit", label: "Edit epic", icon: "edit" },
                  { id: "new", label: "New work item", icon: "plus" },
                  { kind: "separator" },
                  { id: "up", label: "Move up", icon: "arrow-up" },
                  { id: "down", label: "Move down", icon: "arrow-down" },
                ]}
              />
            ) : null
          }
          headerActions={
            <>
              <Button size="sm" variant={grouped ? "secondary" : "ghost"} leadingIcon="layers" aria-pressed={grouped} onClick={() => setGrouped((g) => !g)}>
                Group by epic
              </Button>
              <Button size="sm" variant="primary" leadingIcon="plus">
                New work item
              </Button>
            </>
          }
        />
      </Frame>
      <Caption>
        collapsed: {[...collapsed].join(", ") || "none"} · selected: {selected ? `${selected.kind} ${selected.id}` : "none"}
      </Caption>
    </Col>
  );
}

function EpicDemo() {
  const [selected, setSelected] = useState<NavRef | null>(null);
  return (
    <Frame height={400}>
      <Board project={CONTROL} epic={WEBHOOKS} selected={selected} onSelect={setSelected} />
    </Frame>
  );
}

function ComposedDemo() {
  const [selected, setSelected] = useState<NavRef | null>({ kind: "epic", id: "e_nav" });
  const scope = useMemo(() => boardScope(navProjects, selected), [selected]);
  return (
    <Frame height={600}>
      <Sidebar projects={navProjects} selected={selected} onSelect={setSelected} title="dude" width={260} />
      {scope ? (
        <Board project={scope.project} epic={scope.epic} selected={selected} onSelect={setSelected} />
      ) : (
        <div style={{ flex: 1, display: "flex", background: "var(--ds-color-canvas)" }}>
          <EmptyState icon="message" title="Transcript" description={selected ? `${selected.kind} · ${selected.id}` : "Select a project or an epic for its board"} />
        </div>
      )}
    </Frame>
  );
}
