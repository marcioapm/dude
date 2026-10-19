/*
 * The tracking-work mockup: the app's shell (the design system's Sidebar,
 * as apps/web lays it out) around the proposed screens for Lifecycle
 * (Close, Archive), Classification (kind, bug fields, priority) and
 * Finding things (the tasks list, Find work's tokens, the board's chips).
 * A bar above switches screen, theme, density and who is reading; it is
 * not part of the proposal.
 *
 * The URL picks the state, so each screenshot is one address:
 * ?screen=<a key of SCREENS> &theme=dark|light &density=comfortable|compact &role=owner|member
 */

import { StrictMode, useState, type ReactNode } from "react";
import { createRoot } from "react-dom/client";
import "@dude/design-system/tokens.css";
import "@dude/design-system/base.css";
import "../../src/app.css";
import { ThemeProvider, useTheme, type ThemePreference } from "@dude/design-system";
import { Segmented, Sidebar, SidebarLink, SidebarProfile, SidebarToggle } from "@dude/design-system/components";
import { Select, ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import type { NavOverrides, NavRef } from "../../../../packages/design-system/src/util/navModel.ts";
import { BoardPage, NO_FILTERS, type BoardFilters } from "./BoardPage.tsx";
import { ArchiveEpicDialog, TaskDialogBug } from "./Dialogs.tsx";
import { BriefPage, ProposalKindsPage } from "./Classification.tsx";
import { EpicGlyphs, EpicVariantsPage, SIDEBAR_EXPANDED, SidebarEpicsPage } from "./EpicGlyph.tsx";
import { FIND_QUERY, FindWorkPane, MeaningPage } from "./Finding.tsx";
import { ActivityLines, TaskPage, type Overlay, type TaskView } from "./TaskPage.tsx";
import { TasksList, type View } from "./TasksList.tsx";
import { ROLE, YOU, keyOf, world, type Resolution } from "./data.ts";
import pageStyles from "./pages.module.css";

export const SCREENS = {
  meaning: "0 · What the words mean",
  "close-dialog": "1 · Close: Won't do, and what waits on it (&why=duplicate: as a duplicate)",
  "closed-wontdo": "1 · Closed: Won't do",
  "closed-duplicate": "1 · Closed: Duplicate (both tasks)",
  "archive-task": "1 · Archive an ended task",
  "archive-open-task": "1 · Archive a task in Backlog: close and archive",
  "archive-epic-refused": "1 · Archive an epic: refused",
  "archived-view": "1 · Archived",
  "activity-lifecycle": "1 · Activity, every new line",
  "task-dialog-bug": "2 · New task, kind Bug",
  "bug-task": "2 · A bug, its cause confirmed",
  "introduced-by-proposed": "2 · A bug, its cause proposed",
  "board-kinds": "2 · Board: kinds and priorities",
  "proposal-card-kinds": "2 · Proposal with kinds",
  "brief-bug": "2 · What a kind tells the agents",
  "tasks-list": "3 · Tasks list",
  "tasks-list-view-bugs": "3 · Tasks list: Bugs",
  "tasks-list-archived": "3 · Tasks list: Archived",
  "find-work-tokens": "3 · Find work with tokens",
  "board-filters": "3 · Board filters",
  narrow: "4 · A bug at 390px",
  "sidebar-epics": "5 · Sidebar: epic rows with the layers glyph",
  "sidebar-epic-variants": "5 · Sidebar: epic row, what was considered",
} as const;
export type Screen = keyof typeof SCREENS;

const params = new URLSearchParams(location.search);
const first = (params.get("screen") ?? params.get("s") ?? "board-kinds") as Screen;

type Place =
  | { view: "board"; filters: BoardFilters; chips: boolean; overlay?: "archive-epic" | "task-dialog" | undefined }
  | { view: "task"; key: string; tab: TaskView; overlay: Overlay; split?: string | undefined; before?: boolean | undefined; closeAs?: Resolution | undefined }
  | { view: "list"; list: View }
  | { view: "activity" }
  | { view: "other"; screen: Screen };

const board = (o: Partial<Extract<Place, { view: "board" }>> = {}): Place => ({ view: "board", filters: NO_FILTERS, chips: true, ...o });
const page = (key: string, o: Partial<Extract<Place, { view: "task" }>> = {}): Place => ({ view: "task", key, tab: "overview", overlay: null, ...o });

function placeOf(s: Screen): Place {
  switch (s) {
    // `&why=duplicate` opens the same dialog on Duplicate, with its picker and the handover.
    case "close-dialog": return page("CP-63", { overlay: "close", before: true, closeAs: params.get("why") === "duplicate" ? "duplicate" : "wont_do" });
    case "closed-wontdo": return page("CP-46");
    case "closed-duplicate": return page("CP-63", { split: "CP-62" });
    case "archive-task": return page("CP-46", { overlay: "archive" });
    case "archive-open-task": return page("CP-44", { overlay: "close-archive" });
    case "archive-epic-refused": return board({ overlay: "archive-epic" });
    case "archived-view": return { view: "list", list: "archived" };
    case "activity-lifecycle": return { view: "activity" };
    case "task-dialog-bug": return board({ overlay: "task-dialog" });
    case "bug-task": return page("CP-61");
    case "introduced-by-proposed": return page("CP-60");
    case "board-kinds": return board({ chips: false });
    case "tasks-list": return { view: "list", list: "all" };
    case "tasks-list-view-bugs": return { view: "list", list: "bugs" };
    case "tasks-list-archived": return { view: "list", list: "archived" };
    case "find-work-tokens": return board();
    case "board-filters": return board({ filters: { ...NO_FILTERS, kind: "bug", mine: true } });
    case "narrow": return page("CP-62");
    default: return { view: "other", screen: s };
  }
}

function Mock() {
  const theme = useTheme();
  const [screen, setScreen] = useState<Screen>(first in SCREENS ? first : "board-kinds");
  const [place, setPlace] = useState<Place>(() => placeOf(screen));
  const [navOpen, setNavOpen] = useState(params.get("drawer") === "open");
  const [query, setQuery] = useState("");
  // The epic screens fold and open epics to show both states; elsewhere the tree opens by triage.
  const sidebarScreen = screen === "sidebar-epics" || screen === "sidebar-epic-variants";
  const [expanded, setExpanded] = useState<NavOverrides>(() => (sidebarScreen ? SIDEBAR_EXPANDED : new Map()));
  const finding = screen === "find-work-tokens";
  const go = (s: Screen) => { setScreen(s); setPlace(placeOf(s)); setNavOpen(false); if (s === "sidebar-epics" || s === "sidebar-epic-variants") setExpanded(SIDEBAR_EXPANDED); };

  let main: ReactNode = null;
  let selected: NavRef | null = { kind: "project", id: "p_cp" };
  const openTask = (key: string) => { setPlace(page(key)); setNavOpen(false); };
  const toBoard = () => setPlace(board());
  if (place.view === "board") {
    const p = place;
    main = (
      <>
        <BoardPage filters={p.filters} onFilters={(filters) => setPlace({ ...p, filters })}
          onOpen={openTask} onArchived={() => setPlace({ view: "list", list: "archived" })} showChips={p.chips} />
        <ArchiveEpicDialog open={p.overlay === "archive-epic"} onOpenChange={(o) => setPlace({ ...p, overlay: o ? "archive-epic" : undefined })} />
        <TaskDialogBug open={p.overlay === "task-dialog"} onOpenChange={(o) => setPlace({ ...p, overlay: o ? "task-dialog" : undefined })} />
      </>
    );
  } else if (place.view === "task") {
    const p = place;
    selected = { kind: "task", id: `t_${p.key}` };
    const one = (k: string) => (
      <TaskPage key={k} k={k} view={p.tab} onView={(tab) => setPlace({ ...p, tab })} overlay={k === p.key ? p.overlay : null} beforeClose={k === p.key && p.before} closeAs={k === p.key ? p.closeAs : undefined}
        onOverlay={(overlay) => setPlace({ ...p, overlay })} onOpen={openTask} onBoard={toBoard} />
    );
    main = p.split ? <div className={pageStyles["split"]}>{one(p.key)}{one(p.split)}</div> : one(p.key);
  } else if (place.view === "list") {
    const p = place;
    main = <TasksList view={p.list} onView={(list) => setPlace({ view: "list", list })} onOpen={openTask} />;
  } else if (place.view === "activity") {
    selected = { kind: "task", id: "t_CP-62" };
    main = <div className="screen taskScreen"><div className="taskPane"><h1 className="taskTitle" style={{ marginBottom: 16 }}>Activity: every new line</h1><ActivityLines k="CP-62" /></div></div>;
  } else if (place.screen === "proposal-card-kinds") {
    selected = null;
    main = <ProposalKindsPage />;
  } else if (place.screen === "brief-bug") {
    selected = null;
    main = <BriefPage />;
  } else if (place.screen === "meaning") {
    selected = null;
    main = <MeaningPage />;
  } else if (place.screen === "sidebar-epics") {
    selected = { kind: "epic", id: "e_billing" };
    main = <SidebarEpicsPage />;
  } else if (place.screen === "sidebar-epic-variants") {
    selected = { kind: "epic", id: "e_billing" };
    main = <EpicVariantsPage />;
  } else {
    selected = null;
  }

  return (
    <div style={{ display: "flex", flexDirection: "column", height: "100%" }}>
      <div className="mockbar">
        <b>dude · Tracking work (mockup)</b>
        <Select size="sm" aria-label="Screen" value={screen} onValueChange={(s) => go(s as Screen)}
          options={(Object.keys(SCREENS) as Screen[]).map((s) => ({ value: s, label: SCREENS[s] }))} />
        <Segmented size="sm" label="Reading as" value={ROLE} onChange={(r) => { const u = new URL(location.href); u.searchParams.set("role", r); u.searchParams.set("screen", screen); location.href = u.toString(); }}
          options={[{ value: "owner", label: "Ana (owner)" }, { value: "member", label: "Márcio (member)" }]} />
        <Segmented size="sm" label="Density" value={theme.density} onChange={(d) => theme.setDensity(d)}
          options={[{ value: "comfortable", label: "Comfortable" }, { value: "compact", label: "Compact" }]} />
        <Segmented size="sm" label="Theme" value={theme.resolved} onChange={(t) => theme.setPreference(t as ThemePreference)}
          options={[{ value: "dark", label: "Dark" }, { value: "light", label: "Light" }]} />
      </div>
      <div className="mocknote">
        Reading as {YOU.name} ({ROLE}). Close is a person's decision with a resolution; Archive takes ended work out of everyday views, closing it first when it hasn't ended. Epic rows in the sidebar carry the layers glyph.
      </div>
      <div className={`shell ${finding ? pageStyles["finding"] : ""}`} style={{ flex: 1, minHeight: 0 }}>
        <Sidebar projects={[world()]} title="dude" you={YOU.id} selected={selected}
          expanded={expanded} onExpandedChange={setExpanded}
          query={finding ? FIND_QUERY : query} onQueryChange={setQuery}
          sessions={finding ? <FindWorkPane /> : undefined}
          onSelect={(ref) => { if (ref.kind === "task") openTask(keyOf(ref.id)); else toBoard(); }}
          collapsible open={navOpen} onOpenChange={setNavOpen}
          footer={<><SidebarLink icon="building">Organisation settings</SidebarLink><SidebarProfile person={YOU} detail={ROLE === "owner" ? "ana@acme.dev" : "marcio@acme.dev"} onOpen={() => undefined} /></>} />
        <EpicGlyphs root={document.body} scope="aside" />
        <main className="main flush">
          <SidebarToggle open={navOpen} onOpenChange={setNavOpen} size="sm" />
          {main}
        </main>
      </div>
    </div>
  );
}

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <ThemeProvider defaultPreference={(params.get("theme") as ThemePreference | null) ?? "dark"}
      defaultDensity={params.get("density") === "compact" ? "compact" : "comfortable"} storageKey={null} densityStorageKey={null}>
      <TooltipProvider>
        <ToastProvider>
          <Mock />
        </ToastProvider>
      </TooltipProvider>
    </ThemeProvider>
  </StrictMode>,
);
