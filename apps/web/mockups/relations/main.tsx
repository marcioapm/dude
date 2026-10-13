/*
 * The task relations mockup: the app's shell (the design system's Sidebar,
 * as apps/web lays it out) around the proposed screens — the board with
 * blocked and blocking cards and their start, a blocked task's page with
 * its Relations, Start when unblocked, Start anyway, the relation editor,
 * a brainstorm's proposal with relations, an epic's order, and the history
 * lines. A bar above switches screen, theme and density; it is not part of
 * the proposal.
 *
 * The URL picks the state, so each screenshot is one address:
 * ?s=<a key of SCREENS> &theme=dark|light &density=comfortable|compact
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
import { TaskPage, type EditorState, type OverrideState, type TaskView } from "./TaskPage.tsx";
import { P, YOU, keyOfId, setAutoExtra, setScenario, taskId, world } from "./data.ts";

const SCREENS = {
  board: "1 · Project board",
  "board-menu": "1 · Board, a blocked card's start menu",
  "board-hover": "1 · Board, a card's relations lit",
  "board-blocked": "1 · Board, blocked only",
  task: "2 · Blocked task",
  "task-auto": "2 · Blocked task, starts when unblocked",
  "task-other": "2 · Someone else's task",
  "unmerged-blocker": "2 · Blocker done, nothing merged",
  "failed-blocker": "2 · Blocked by a failed task",
  "auto-dialog": "3 · Start when unblocked",
  override: "3 · Start anyway",
  "override-comms": "3 · Start anyway — Later: with agent comms",
  editor: "4 · Add a relation",
  cycle: "4 · A loop refused",
  proposal: "5 · Proposal with relations",
  "proposal-refused": "5 · Proposal, item can't be filed",
  epic: "6 · Epic order",
  history: "7 · History, started when unblocked (Activity)",
  "history-chat": "7 · History, started when unblocked (Chat)",
  "history-override": "7 · History, started anyway",
  "history-unmerged": "7 · History, done with nothing merged",
} as const;
type Screen = keyof typeof SCREENS;

const params = new URLSearchParams(location.search);
const first = (params.get("s") as Screen | null) ?? "board";

type Place =
  | { view: "board"; scope: "project" | "epic"; hover: string | null; menu: string | null; filter: BoardFilter }
  | { view: "task"; key: string; tab: TaskView; override: OverrideState; auto: boolean; autoSet: boolean; editor: EditorState }
  | { view: "proposal"; refused: boolean };

const board = (o: Partial<Extract<Place, { view: "board" }>> = {}): Place => ({ view: "board", scope: "project", hover: null, menu: null, filter: "all", ...o });
const page = (key: string, o: Partial<Extract<Place, { view: "task" }>> = {}): Place =>
  ({ view: "task", key, tab: "overview", override: "closed", auto: false, autoSet: false, editor: "closed", ...o });

function placeOf(s: Screen): Place {
  switch (s) {
    case "board": return board();
    case "board-menu": return board({ menu: "CP-44" });
    case "board-hover": return board({ hover: "CP-41" });
    case "board-blocked": return board({ filter: "blocked" });
    case "epic": return board({ scope: "epic" });
    case "task": return page("CP-44");
    case "task-auto": return page("CP-44", { autoSet: true });
    case "task-other": return page("CP-42");
    case "unmerged-blocker": return page("CP-51");
    case "failed-blocker": return page("CP-49");
    case "auto-dialog": return page("CP-44", { auto: true });
    case "override": return page("CP-44", { override: "open" });
    case "override-comms": return page("CP-42", { override: "comms" });
    case "editor": return page("CP-44", { editor: "open" });
    case "cycle": return page("CP-44", { editor: "cycle" });
    case "history": return page("CP-42", { tab: "activity" });
    case "history-chat": return page("CP-42", { tab: "chat" });
    case "history-override": return page("CP-49", { tab: "activity" });
    case "history-unmerged": return page("CP-51", { tab: "activity" });
    case "proposal": return { view: "proposal", refused: false };
    case "proposal-refused": return { view: "proposal", refused: true };
  }
}

function screenOf(p: Place): Screen | "" {
  if (p.view === "proposal") return p.refused ? "proposal-refused" : "proposal";
  if (p.view === "board") return p.scope === "epic" ? "epic" : p.menu ? "board-menu" : p.hover ? "board-hover" : p.filter === "blocked" ? "board-blocked" : "board";
  if (p.override !== "closed") return p.override === "comms" ? "override-comms" : "override";
  if (p.auto) return "auto-dialog";
  if (p.editor !== "closed") return p.editor === "cycle" ? "cycle" : "editor";
  if (p.key === "CP-42") return p.tab === "chat" ? "history-chat" : p.tab === "activity" ? "history" : "task-other";
  if (p.key === "CP-49") return p.tab === "activity" ? "history-override" : "failed-blocker";
  if (p.key === "CP-51") return p.tab === "activity" ? "history-unmerged" : "unmerged-blocker";
  if (p.key === "CP-44") return p.autoSet ? "task-auto" : "task";
  return "";
}

function Mock() {
  const theme = useTheme();
  const [place, setPlace] = useState<Place>(() => placeOf(first in SCREENS ? first : "board"));
  const [navOpen, setNavOpen] = useState(false);
  const go = (p: Place) => { setPlace(p); setNavOpen(false); };
  // CP-42's history is told the morning CP-41 merged and it started, CP-49's an hour after Tom started it anyway; every other screen is before both.
  setScenario(place.view === "task" && place.tab !== "overview"
    ? (place.key === "CP-42" ? "cp41-merged" : place.key === "CP-49" ? "cp49-overridden" : "now") : "now");
  setAutoExtra(place.view === "task" && place.autoSet ? { "CP-44": { how: "deliver", by: P.marcio, ago: "10m" } } : {});
  const openTask = (key: string) => go(page(key));
  const toBoard = () => go(board());

  const selected: NavRef | null = place.view === "task" ? { kind: "task", id: taskId(place.key) }
    : place.view === "board" ? (place.scope === "epic" ? { kind: "epic", id: "e_billing" } : { kind: "project", id: "p_control-plane" }) : null;

  let main;
  if (place.view === "board") {
    main = <BoardPage key={`${place.scope}/${place.menu ?? ""}`} scope={place.scope} hover={place.hover} menu={place.menu} filter={place.filter}
      onFilter={(filter) => go({ ...place, filter, hover: null, menu: null })} onOpen={openTask} />;
  } else if (place.view === "task") {
    const p = place;
    main = <TaskPage key={`${p.key}/${p.autoSet}`} k={p.key} view={p.tab} onView={(tab) => go({ ...p, tab })}
      override={p.override} onOverride={(override) => go({ ...p, override })}
      auto={p.auto} onAuto={(auto) => go({ ...p, auto })}
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
        Hover or focus a card to light its relations. A Backlog card has its own Start beside it; a blocked one's menu offers Start when unblocked and Start anyway. Open CP-44 for its Relations and the refused start.
      </div>
      <div className="shell" style={{ flex: 1, minHeight: 0 }}>
        <Sidebar
          projects={nav}
          title="dude"
          you={YOU.id}
          selected={selected}
          onSelect={(ref) => {
            if (ref.kind === "task") openTask(keyOfId(ref.id));
            else if (ref.kind === "epic" && ref.id === "e_billing") go(board({ scope: "epic" }));
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
