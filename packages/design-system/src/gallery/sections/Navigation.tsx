import { useMemo, useState, type ReactNode } from "react";
import { Block, Caption, Col, Label, Panes, Row, Section, States, type PaneMode } from "../Frame.tsx";
import { NavTree, NavTreeRow } from "../../components/NavTree.tsx";
import { Sidebar, SidebarSessions, type SidebarSession } from "../../components/Sidebar.tsx";
import { ProfileMenu } from "../../components/OrgSwitcher.tsx";
import { DocOutline, DocSearchResults, type DocOutlinePage, type DocSearchResult } from "../../components/DocOutline.tsx";
import { SearchField } from "../../primitives/SearchField.tsx";
import { WaitingList, WaitingRow } from "../../components/WaitingList.tsx";
import { PersonAvatar } from "../../components/PersonAvatar.tsx";
import { Button } from "../../primitives/Button.tsx";
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
      intro="Persistent chrome beside the transcript. It has to answer four questions without a click — what needs me, what is active, what is ready, who is working on what — and stay calm at fifty tasks. The mechanism is triage: every status rolls up into one of six buckets, and collapsed parents show the counted ones."
    >
      <Block
        id="nav-triage"
        title="Triage vocabulary"
        note="Every domain status lands in one of six buckets; four are counted. Needs-you keeps its diamond and ring and is the only count in attention ink. Waiting and done are never counted: the calm majority stays silent."
      >
        <Panes mode={mode} surface>
          <Col>
            <Label>Status → bucket</Label>
            <div style={{ display: "grid", gridTemplateColumns: "repeat(3, max-content)", gap: "4px 24px", alignItems: "center" }}>
              {ALL_STATUSES.map((s) => (
                <TriageMapRow key={s} status={s} />
              ))}
            </div>
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
        note="Project → Epic → Task → Session, rendered flat with aria-level so keyboard movement is index arithmetic. Each level has its own row grammar: projects are sticky small-caps headers, epics lead with the layers glyph and carry a total, tasks lead with a status dot and a mono key and trail with who is working (agent squares) and who is involved (human circles), sessions sit on a guide line behind a role avatar. Earlier Runs fold into one 'Attempt n' row each. Click a chevron or use ←→ to fold; a needs-you row opens by default down to the asking session, and a collapsed parent keeps its roll-up. Try ↑↓ → ← Home End Enter and / ."
      >
        <Panes mode={mode} surface>
          <TreeDemo />
        </Panes>
      </Block>

      <Block
        id="nav-tree-epics"
        title="NavTree epic rows"
        note="An epic leads with the layers glyph after its chevron, 14px, muted, lifted to secondary on hover and selection (muted falls under 4.5:1 on the washes); a task row never draws it. Open, folded with a needs-you count, selected, and one to hover; then the tree at 220px, where the people drop."
      >
        <Panes mode={mode} surface>
          <EpicRowsDemo />
        </Panes>
      </Block>

      <Block
        id="nav-tree-menus"
        title="NavTree — row menus"
        note="Projects, epics and tasks get a '…' menu from the app via menuItems (or a menu render prop for full control); the tree knows nothing about the actions. The trigger reserves no space on any row: on hover or focus it takes the trailing people's, epic count's or session plan's place. Nested rows show a border-colour guide per ancestor level through the chevrons. The trigger is visible on hover and focus and stays out of the tab order: with a row focused, Shift+F10 or the context-menu key opens it, as does right-click, and focus returns to the row when it closes so ↑↓ keep working. Rows that return no items draw nothing. Try: focus a row, Shift+F10, ↓, Enter."
      >
        <Panes mode={mode} surface>
          <TreeMenuDemo />
        </Panes>
      </Block>

      <Block
        id="nav-sidebar"
        title="Sidebar — realistic"
        note="The whole thing at its default 304px with three projects and fifty tasks. The brand row: the brand (its words truncate first), the others online (three faces and +N; hover names everyone), collapse. Search (/ from the tree, ↓ into it), Waiting on you, Sessions (latest three, folding), then the tree. The profile band opens Your settings · Organisation settings · Help · Sign out. Select a row to see the selection follow into the tree."
      >
        <Panes mode={mode}>
          <SidebarDemo />
        </Panes>
      </Block>

      <Block id="nav-waiting-list" title="WaitingList" note="Waiting's tab list: yours carry the attention tint and a primary action; others' are plain with Take over.">
        <Panes mode={mode} surface>
          <WaitingList aria-label="Waiting rows">
            <WaitingRow mine face={<PersonAvatar person={people["marcio"]!} size={32} agent="implementer" />} ask="Which wallet should checkout use?"
              where="WC-205 · Checkout" age="12m" action={<Button size="sm" variant="primary">Answer</Button>} onOpen={() => {}} />
            <WaitingRow face={<PersonAvatar person={people["ana"]!} size={32} />} ask="Confirm the plan" where="API-10 · Ana's task"
              age="2h" action={<Button size="sm" variant="quiet">Take over</Button>} onOpen={() => {}} />
          </WaitingList>
        </Panes>
      </Block>

      <Block id="nav-sidebar-sessions" title="SidebarSessions — folded, unfolded, more than three" note="The latest three by last activity, newest first. The heading: a fold chevron (the app keeps it), Sessions (the whole list), and + for a new session, as Projects has for a project. No New session row.">
        <Panes mode={mode}>
          <SessionStates />
        </Panes>
      </Block>

      <Block id="nav-sidebar-states" title="Sidebar — quiet, narrow, empty, loading" note="A quiet project shows no roll-ups, no pinned section and no attention ink anywhere. A search forces the ancestors open and greys the chevrons; Mine only opens them, and its chevrons still fold. Below 340px the chips other than Needs you drop their labels and keep mark + count. Empty and loading are one line and skeleton rows respectively.">
        <Panes mode={mode}>
          <div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fit, minmax(190px, 1fr))", gap: 12 }}>
            {(
              [
                ["quiet org", <Sidebar projects={navProjectsQuiet} title="quiet org" width="100%" />],
                ["narrow (200px)", <Sidebar projects={navProjects} title="narrow" width={200} />],
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

      <Block id="nav-guide" title="SearchField / DocOutline / DocSearchResults"
        note="The guide's column (Help): SearchField at the top (the sidebar's Find work box, one component), then DocOutline — the pages, and under the current one its sections, the one in view in bold — or, while there are words, DocSearchResults in its place: N sections, each its heading, its page and a snippet with the words marked. ↑↓ move, Enter opens, Escape clears.">
        <Panes mode={mode} surface>
          <GuideDemo />
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

/** Epic rows in each state, as single rows, then a narrow tree. */
function EpicRowsDemo() {
  const epic = navProjects[0]!.epics![0]!;
  const row = (expanded: boolean, needsYou: number): NavRow => ({
    key: `epic:${epic.id}:${expanded}:${needsYou}`, ref: { kind: "epic", id: epic.id }, depth: 1, parentKey: null, expandable: true, expanded,
    forced: false, node: epic, counts: { needs_you: needsYou, active: 1, ready: 0, failed: 0, waiting: 0, done: 0 }, triage: null, projectId: navProjects[0]!.id,
  });
  const states: ReadonlyArray<readonly [string, NavRow, boolean]> = [
    ["open", row(true, 0), false],
    ["folded, 2 need you", row(false, 2), false],
    ["selected", row(false, 0), true],
    ["hover me: the glyph lifts to secondary", row(false, 0), false],
  ];
  return (
    <Row top>
      <Col style={{ width: 300 }}>
        {states.map(([label, r, selected]) => (
          <div key={label}>
            <Caption>{label}</Caption>
            <div role="tree" aria-label={label}>
              <NavTreeRow row={r} selected={selected} tabIndex={-1} onFocus={() => undefined} onKeyDown={() => undefined} onClick={() => undefined} onToggle={() => undefined} />
            </div>
          </div>
        ))}
      </Col>
      <Col style={{ width: 220 }}>
        <Caption>220px: the people drop, the glyph stays</Caption>
        <div style={{ border: "1px solid var(--ds-color-border-subtle)", borderRadius: 6, maxHeight: 360, overflow: "auto", width: 220, containerType: "inline-size" }}>
          <NavTree projects={navProjects.slice(0, 1)} />
        </div>
      </Col>
    </Row>
  );
}

function menuItemsFor(row: NavRow, act: (label: string) => void): ReadonlyArray<RowMenuItem> | null {
  const on = (label: string) => () => act(label);
  switch (row.ref.kind) {
    case "project":
      return [
        { id: "settings", label: "Settings", icon: "settings", onSelect: on("Settings") },
        { id: "new-epic", label: "New epic", icon: "layers", onSelect: on("New epic") },
        { id: "new", label: "New task", icon: "plus", onSelect: on("New task") },
        { kind: "separator" },
        { id: "archive", label: "Archive", icon: "folder", tone: "danger", onSelect: on("Archive") },
      ];
    case "epic":
      return [
        { id: "edit", label: "Edit", icon: "edit", onSelect: on("Edit epic") },
        { id: "new", label: "New task", icon: "plus", onSelect: on("New task") },
        { kind: "separator" },
        { id: "up", label: "Move up", icon: "arrow-up", onSelect: on("Move up") },
        { id: "down", label: "Move down", icon: "arrow-down", onSelect: on("Move down") },
        { kind: "separator" },
        { id: "delete", label: "Delete", icon: "cross", tone: "danger", disabled: true, disabledReason: "Move its tasks out first" },
      ];
    case "task":
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
  const [selected, setSelected] = useState<NavRef | null>({ kind: "task", id: "wi_2402" });
  const [last, setLast] = useState<string | null>(null);
  return (
    <Row top>
      <Col>
        <Caption>Hover a task: the ⋯ takes its people's place. Guides mark each level.</Caption>
        <div style={{ border: "1px solid var(--ds-color-border-subtle)", borderRadius: 6, maxHeight: 420, overflow: "auto", width: 320, background: "var(--ds-color-surface)" }}>
          <NavTree projects={navProjectsQuiet.concat(navProjects.slice(0, 1))} selected={selected} onSelect={setSelected} menuItems={(row) => menuItemsFor(row, setLast)} />
        </div>
        <Caption>last action: {last ?? "none"}</Caption>
      </Col>
      <Col>
        <Caption>Nested guides, everything open (no menus)</Caption>
        <div style={{ border: "1px solid var(--ds-color-border-subtle)", borderRadius: 6, maxHeight: 420, overflow: "auto", width: 320, background: "var(--ds-color-surface)" }}>
          <NavTree projects={navProjects.slice(0, 1)} />
        </div>
      </Col>
    </Row>
  );
}

function SidebarDemo() {
  const [selected, setSelected] = useState<NavRef | null>({ kind: "task", id: "wi_2402" });
  const [waiting, setWaiting] = useState(false);
  const [folded, setFolded] = useState(false);
  // You and four others: the brand row shows three of the others and "+1".
  const online = [people["marcio"]!, people["ana"]!, people["tom"]!, people["lin"]!, people["kai"]!].map((p) => ({ ...p, online: true }));
  return (
    <Col>
      <div style={{ display: "flex", height: 760, overflow: "hidden" }}>
        <Sidebar
          projects={navProjects}
          selected={waiting ? null : selected}
          onSelect={(ref) => {
            setWaiting(false);
            setSelected(ref);
          }}
          you="u_marcio"
          online={online}
          onWaitingSelect={() => setWaiting(true)}
          waitingSelected={waiting}
          title="El Duderino"
          onCollapsedChange={() => undefined}
          sessions={<SidebarSessions sessions={SESSIONS} onSelect={() => undefined} onNew={() => undefined} onOpenList={() => undefined}
            folded={folded} onFoldedChange={setFolded} />}
          treeActions={<IconButton icon="plus" label="New project" size="sm" />}
          footer={<ProfileMenu person={{ ...people["marcio"]!, online: true }} detail="marcio@example.com" onSettings={() => undefined}
            onOrgSettings={() => undefined} onHelp={() => undefined} onSignOut={() => undefined} signOutNote="Forget this browser's key" />}
        />
        <div style={{ flex: 1, display: "grid", placeItems: "center", background: "var(--ds-color-surface)", color: "var(--ds-color-text-muted)", fontSize: 12 }}>
          {waiting ? "Waiting on you" : selected ? `${selected.kind} · ${selected.id}` : "transcript goes here"}
        </div>
      </div>
    </Col>
  );
}

const GUIDE: DocOutlinePage[] = [
  { slug: "accounts", title: "Accounts and organisations", sections: [{ anchor: "keys", heading: "Keys" }] },
  { slug: "delivery", title: "Delivering a task", sections: [
    { anchor: "the-pipeline", heading: "The pipeline" }, { anchor: "which-reviewers-run", heading: "Which reviewers run" },
    { anchor: "which-findings-block", heading: "Which findings block" }, { anchor: "when-the-loop-stops", heading: "When the loop stops" },
  ] },
  { slug: "github", title: "GitHub", sections: [] },
  { slug: "memory", title: "Memory", sections: [] },
];
const GUIDE_FOUND: DocSearchResult[] = [
  { page: "delivery", anchor: "which-reviewers-run", heading: "Which reviewers run", pageTitle: "Delivering a task",
    snippet: "API joins for /routes/ or /api/. Documentation never runs by default. Tick it under Reviewers every delivery runs to have each round check the docs." },
  { page: "why", anchor: "a-reviewer-didnt-run", heading: "A reviewer didn't run", pageTitle: "Why is dude doing this?",
    snippet: "Documentation runs only where an organisation or project makes it a required reviewer. See Delivering a task." },
];

/** The guide's column: search, then the outline or what the search found. */
function GuideDemo() {
  const [query, setQuery] = useState("");
  const [section, setSection] = useState("which-reviewers-run");
  const [active, setActive] = useState(0);
  const found = query.trim() ? (query.includes("zz") ? [] : GUIDE_FOUND) : null;
  const openResult = (result: DocSearchResult) => {
    setSection(result.anchor);
    setQuery("");
  };
  const column = (body: ReactNode, label: string) => (
    <Col style={{ width: 248 }}>
      <Caption>{label}</Caption>
      {body}
    </Col>
  );
  return (
    <Row top>
      {column(
        <>
          <SearchField value={query} label="Search the guide" placeholder="Search the guide" onValueChange={setQuery}
            onKeyDown={(e) => {
              if (e.key === "Escape") setQuery("");
              if (e.key === "ArrowDown" && found?.length) setActive((active + 1) % found.length);
              if (e.key === "ArrowUp" && found?.length) setActive((active - 1 + found.length) % found.length);
              if (e.key === "Enter") {
                e.preventDefault();
                const result = found?.[active];
                if (result) openResult(result);
              }
            }} />
          {found ? (
            <DocSearchResults results={found} query={query} active={active} onActiveChange={setActive} onOpen={openResult} listId="gallery-guide" />
          ) : (
            <DocOutline pages={GUIDE} current="delivery" currentSection={section} href={(p, s) => `#${p}${s ? `/${s}` : ""}`}
              onPage={() => undefined} onSection={(_, s) => setSection(s)} />
          )}
        </>, "live: type, or pick a section",
      )}
      {column(
        <>
          <SearchField value="documentation reviewer" label="Search the guide" onValueChange={() => undefined} />
          <DocSearchResults results={GUIDE_FOUND} query="documentation reviewer" active={0} onActiveChange={() => undefined} onOpen={() => undefined} listId="gallery-guide-found" />
        </>, "searching: 2 sections",
      )}
      {column(
        <>
          <SearchField value="zebra" label="Search the guide" onValueChange={() => undefined} />
          <DocSearchResults results={[]} query="zebra" active={0} onActiveChange={() => undefined} onOpen={() => undefined} listId="gallery-guide-none" />
        </>, "nothing found",
      )}
    </Row>
  );
}

const minutesAgo = (m: number) => new Date(Date.UTC(2026, 9, 22, 12) - m * 60_000).toISOString();
/** Five sessions out of order: the sidebar shows the latest three by activity. */
const SESSIONS: SidebarSession[] = [
  { id: "s_retry", title: "Retry policy for webhooks", lastActivityAt: minutesAgo(300) },
  { id: "s_csv", title: "Invoice export as CSV", lastActivityAt: minutesAgo(5) },
  { id: "s_copy", title: "Pricing page copy", lastActivityAt: minutesAgo(900) },
  { id: "s_reopen", title: "Why do reviewers re-open fixed findings?", lastActivityAt: minutesAgo(40), shared: true },
  { id: "s_onboard", title: "Onboarding checklist ideas", lastActivityAt: minutesAgo(2000), shared: true, owner: people["ana"]! },
];

function SessionStates() {
  const [folded, setFolded] = useState(true);
  const pane = (label: string, el: ReactNode) => (
    <div key={label}>
      <Caption>{label}</Caption>
      <div style={{ width: 280, background: "var(--ds-color-chrome)", padding: 8 }}>{el}</div>
    </div>
  );
  return (
    <Row top>
      {pane("unfolded: 5 sessions, the latest 3", <SidebarSessions sessions={SESSIONS} onSelect={() => undefined} onNew={() => undefined} onOpenList={() => undefined} />)}
      {pane("folded (click the chevron)", <SidebarSessions sessions={SESSIONS} onSelect={() => undefined} onNew={() => undefined} onOpenList={() => undefined}
        folded={folded} onFoldedChange={setFolded} />)}
      {pane("limit 5", <SidebarSessions sessions={SESSIONS} limit={5} selected="s_reopen" onSelect={() => undefined} onNew={() => undefined} />)}
    </Row>
  );
}
