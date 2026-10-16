/*
 * The tracking-work mockup: the app's shell (the design system's Sidebar,
 * as apps/web lays it out) around the proposed screens for Lifecycle
 * (Later, Close, Archive), Classification (kind, bug fields, priority) and
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
import type { NavRef } from "../../../../packages/design-system/src/util/navModel.ts";
import { BoardPage, NO_FILTERS, type BoardFilters } from "./BoardPage.tsx";
import { ROLE, YOU, keyOf, world } from "./data.ts";

export const SCREENS = {
  meaning: "0 · What the words mean",
  "board-later": "1 · Board with the Later shelf",
} as const;
export type Screen = keyof typeof SCREENS;

const params = new URLSearchParams(location.search);
const first = (params.get("screen") ?? params.get("s") ?? "board-later") as Screen;

type Place =
  | { view: "board"; filters: BoardFilters; shelfOpen: boolean; chips: boolean }
  | { view: "other"; screen: Screen };

function placeOf(s: Screen): Place {
  switch (s) {
    case "board-later": return { view: "board", filters: NO_FILTERS, shelfOpen: true, chips: false };
    default: return { view: "other", screen: s };
  }
}

function Mock() {
  const theme = useTheme();
  const [screen, setScreen] = useState<Screen>(first in SCREENS ? first : "board-later");
  const [place, setPlace] = useState<Place>(() => placeOf(screen));
  const [navOpen, setNavOpen] = useState(false);
  const go = (s: Screen) => { setScreen(s); setPlace(placeOf(s)); setNavOpen(false); };

  let main: ReactNode = null;
  let selected: NavRef | null = { kind: "project", id: "p_cp" };
  if (place.view === "board") {
    const p = place;
    main = <BoardPage filters={p.filters} onFilters={(filters) => setPlace({ ...p, filters })} shelfOpen={p.shelfOpen}
      onShelfOpen={(shelfOpen) => setPlace({ ...p, shelfOpen })} onOpen={() => undefined} onArchived={() => undefined} showChips={p.chips} />;
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
        Reading as {YOU.name} ({ROLE}). Later is a shelf, not a status; Close is a person's decision with a resolution; Archive takes finished work out of everyday views.
      </div>
      <div className="shell" style={{ flex: 1, minHeight: 0 }}>
        <Sidebar projects={[world(undefined, true)]} title="dude" you={YOU.id} selected={selected}
          onSelect={(ref) => { if (ref.kind === "task") keyOf(ref.id); }}
          collapsible open={navOpen} onOpenChange={setNavOpen}
          footer={<><SidebarLink icon="building">Organisation settings</SidebarLink><SidebarProfile person={YOU} detail={ROLE === "owner" ? "ana@acme.dev" : "marcio@acme.dev"} onOpen={() => undefined} /></>} />
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
