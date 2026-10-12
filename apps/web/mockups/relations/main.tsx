/*
 * The task relations mockup: the app's shell (the design system's Sidebar,
 * as apps/web lays it out) around the proposed screens — the board with
 * blocked and blocking cards, a blocked task's page with its Relations,
 * Start anyway, the relation editor, a brainstorm's proposal with
 * relations, an epic's order, and the history lines. A bar above switches
 * screen, theme and density; it is not part of the proposal.
 *
 * The URL picks the state, so each screenshot is one address:
 * ?s=board|board-hover|board-blocked|task|failed-blocker|override|editor|cycle|proposal|proposal-refused|epic|history|history-chat|history-override
 * &theme=dark|light &density=comfortable|compact
 */

import { StrictMode, useState } from "react";
import { createRoot } from "react-dom/client";
import "@dude/design-system/tokens.css";
import "@dude/design-system/base.css";
import "../../src/app.css";
import { ThemeProvider, useTheme, type ThemePreference } from "@dude/design-system";
import { Segmented, Sidebar, SidebarLink, SidebarProfile, SidebarToggle } from "@dude/design-system/components";
import { Select, ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import type { NavRef } from "../../../../packages/design-system/src/util/navModel.ts";
import { BoardPage, type BoardFilter } from "./BoardPage.tsx";
import { ProposalPage } from "./ProposalPage.tsx";
import { TaskPage, type EditorState, type TaskView } from "./TaskPage.tsx";
import { YOU, keyOfId, setScenario, taskId, world } from "./data.ts";

const SCREENS = {
  board: "1 · Project board",
  "board-hover": "1 · Board, a card's relations lit",
  "board-blocked": "1 · Board, blocked only",
  task: "2 · Blocked task",
  "failed-blocker": "2 · Blocked by a failed task",
  override: "3 · Start anyway",
  editor: "4 · Add a relation",
  cycle: "4 · A loop refused",
  proposal: "5 · Proposal with relations",
  "proposal-refused": "5 · Proposal, item can't be filed",
  epic: "6 · Epic order",
  history: "7 · History (Activity)",
  "history-chat": "7 · History (Chat)",
  "history-override": "7 · History, started anyway",
} as const;
type Screen = keyof typeof SCREENS;

const params = new URLSearchParams(location.search);
const first = (params.get("s") as Screen | null) ?? "board";

type Place =
  | { view: "board"; scope: "project" | "epic"; hover: string | null; filter: BoardFilter }
  | { view: "task"; key: string; tab: TaskView; override: boolean; editor: EditorState }
  | { view: "proposal"; refused: boolean };

function placeOf(s: Screen): Place {
  switch (s) {
    case "board": return { view: "board", scope: "project", hover: null, filter: "all" };
    case "board-hover": return { view: "board", scope: "project", hover: "CP-41", filter: "all" };
    case "board-blocked": return { view: "board", scope: "project", hover: null, filter: "blocked" };
    case "epic": return { view: "board", scope: "epic", hover: null, filter: "all" };
    case "task": return { view: "task", key: "CP-44", tab: "overview", override: false, editor: "closed" };
    case "failed-blocker": return { view: "task", key: "CP-49", tab: "overview", override: false, editor: "closed" };
    case "override": return { view: "task", key: "CP-44", tab: "overview", override: true, editor: "closed" };
    case "editor": return { view: "task", key: "CP-44", tab: "overview", override: false, editor: "open" };
    case "cycle": return { view: "task", key: "CP-44", tab: "overview", override: false, editor: "cycle" };
    case "history": return { view: "task", key: "CP-42", tab: "activity", override: false, editor: "closed" };
    case "history-chat": return { view: "task", key: "CP-42", tab: "chat", override: false, editor: "closed" };
    case "history-override": return { view: "task", key: "CP-49", tab: "activity", override: false, editor: "closed" };
    case "proposal": return { view: "proposal", refused: false };
    case "proposal-refused": return { view: "proposal", refused: true };
  }
}

function screenOf(p: Place): Screen | "" {
  if (p.view === "proposal") return p.refused ? "proposal-refused" : "proposal";
  if (p.view === "board") return p.scope === "epic" ? "epic" : p.hover ? "board-hover" : p.filter === "blocked" ? "board-blocked" : "board";
  if (p.override) return "override";
  if (p.editor !== "closed") return p.editor === "cycle" ? "cycle" : "editor";
  if (p.key === "CP-42") return p.tab === "chat" ? "history-chat" : "history";
  if (p.key === "CP-49") return p.tab === "activity" ? "history-override" : "failed-blocker";
  return p.key === "CP-44" ? "task" : "";
}

function Mock() {
  const theme = useTheme();
  const [place, setPlace] = useState<Place>(() => placeOf(first in SCREENS ? first : "board"));
  const [navOpen, setNavOpen] = useState(false);
  const go = (p: Place) => { setPlace(p); setNavOpen(false); };
  // CP-42's history is told the morning CP-41 merged, CP-49's an hour after Tom started it anyway; every other screen is before both.
  setScenario(place.view === "task" && place.tab !== "overview"
    ? (place.key === "CP-42" ? "cp41-merged" : place.key === "CP-49" ? "cp49-overridden" : "now") : "now");
  const openTask = (key: string) => go({ view: "task", key, tab: "overview", override: false, editor: "closed" });
  const toBoard = () => go({ view: "board", scope: "project", hover: null, filter: "all" });

  const selected: NavRef | null = place.view === "task" ? { kind: "task", id: taskId(place.key) }
    : place.view === "board" ? (place.scope === "epic" ? { kind: "epic", id: "e_billing" } : { kind: "project", id: "p_control-plane" }) : null;

  let main;
  if (place.view === "board") {
    main = <BoardPage key={`${place.scope}`} scope={place.scope} hover={place.hover} filter={place.filter}
      onFilter={(filter) => go({ ...place, filter, hover: null })} onOpen={openTask} />;
  } else if (place.view === "task") {
    const p = place;
    main = <TaskPage key={p.key} k={p.key} view={p.tab} onView={(tab) => go({ ...p, tab })}
      override={p.override} onOverride={(override) => go({ ...p, override })}
      editor={p.editor} onEditor={(editor) => go({ ...p, editor })} onOpen={openTask} onBoard={toBoard} />;
  } else {
    main = <ProposalPage state={place.refused ? "refused" : "ready"} />;
  }

  const current = screenOf(place);
  const nav = world().nav;
  return (
    <div style={{ display: "flex", flexDirection: "column", height: "100%" }}>
      <div className="mockbar">
        <b>dude · Task relations (mockup)</b>
        <Select size="sm" aria-label="Screen" value={current || undefined} placeholder="Another task"
          onValueChange={(s) => go(placeOf(s as Screen))}
          options={(Object.keys(SCREENS) as Screen[]).map((s) => ({ value: s, label: SCREENS[s] }))} />
        <Segmented size="sm" label="Density" value={theme.density} onChange={(d) => theme.setDensity(d)}
          options={[{ value: "comfortable", label: "Comfortable" }, { value: "compact", label: "Compact" }]} />
        <Segmented size="sm" label="Theme" value={theme.resolved} onChange={(t) => theme.setPreference(t as ThemePreference)}
          options={[{ value: "dark", label: "Dark" }, { value: "light", label: "Light" }]} />
      </div>
      <div className="mocknote">
        Hover or focus a card to light its relations. A blocked card says what it waits on; open it (CP-44) for its Relations, the refused start and Start anyway.
      </div>
      <div className="shell" style={{ flex: 1, minHeight: 0 }}>
        <Sidebar
          projects={nav}
          title="dude"
          you={YOU.id}
          selected={selected}
          onSelect={(ref) => {
            if (ref.kind === "task") openTask(keyOfId(ref.id));
            else if (ref.kind === "epic" && ref.id === "e_billing") go({ view: "board", scope: "epic", hover: null, filter: "all" });
            else toBoard();
          }}
          collapsible
          open={navOpen}
          onOpenChange={setNavOpen}
          footer={
            <>
              <SidebarLink icon="building">Organisation settings</SidebarLink>
              <SidebarProfile person={YOU} detail="marcio@acme.dev" onOpen={() => undefined} />
            </>
          }
        />
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
