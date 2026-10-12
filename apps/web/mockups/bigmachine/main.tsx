/*
 * The bigmachine mockup: an agent moves itself to a larger machine.
 * The app's shell (the design system's Sidebar and main pane) with BILL-42's
 * Chat and Overview, its Runs' headers and a brainstorm session. A bar
 * above switches scenario, theme and density; it is not part of the
 * proposal. URL parameters set the same
 * (?s=move-pool&theme=light&density=compact).
 */

import { StrictMode, useState } from "react";
import { createRoot } from "react-dom/client";
import "@dude/design-system/tokens.css";
import "@dude/design-system/base.css";
import "../../src/app.css";
import { ThemeProvider, useTheme, type ThemePreference } from "@dude/design-system";
import { Segmented, Sidebar, SidebarLink, SidebarProfile, SidebarSessions, SidebarToggle } from "@dude/design-system/components";
import { Select, ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import { navProjects } from "../../../../packages/design-system/src/gallery/navFixtures.ts";
import { ConductorChat, TaskChat, TaskOverview } from "./TaskScreens.tsx";
import { RunPage, SessionPage } from "./RunAndSession.tsx";
import { P, SCENARIOS, SESSION, type Scenario } from "./data.ts";

const params = new URLSearchParams(location.search);
const isScenario = (v: string | null): v is Scenario => SCENARIOS.some((x) => x.value === v);

function Screen({ scenario }: { scenario: Scenario }) {
  switch (scenario) {
    case "move": return <TaskChat otherPool={false} />;
    case "move-pool": return <TaskChat otherPool />;
    case "refused-platform": return <RunPage view="refused-platform" />;
    case "refused-largest": return <RunPage view="refused-largest" />;
    case "refused-moves": return <RunPage view="refused-moves" />;
    case "conductor": return <ConductorChat />;
    case "run": return <RunPage view="fixer" />;
    case "start-over": return <RunPage view="start-over" />;
    case "task": return <TaskOverview />;
    case "reset": return <TaskOverview confirm />;
    case "session": return <SessionPage />;
  }
}

function Mock() {
  const theme = useTheme();
  const [scenario, setScenario] = useState<Scenario>(() => (isScenario(params.get("s")) ? (params.get("s") as Scenario) : "move"));
  const [navOpen, setNavOpen] = useState(false);
  const note = SCENARIOS.find((x) => x.value === scenario)!.note;
  // ?bar=0 leaves the switcher out, for a phone's screenshot.
  const bar = params.get("bar") !== "0";

  return (
    <div style={{ display: "flex", flexDirection: "column", height: "100%" }}>
      {bar ? <div className="mockbar">
        <b>dude · An agent asks for a larger machine (mockup)</b>
        <Select aria-label="Scenario" size="sm" value={scenario} onValueChange={(v) => setScenario(v as Scenario)}
          options={SCENARIOS.map((x) => ({ value: x.value, label: x.label }))} data-testid="scenario" />
        <Segmented size="sm" label="Theme" value={theme.resolved} onChange={(t) => theme.setPreference(t as ThemePreference)}
          options={[{ value: "dark", label: "Dark" }, { value: "light", label: "Light" }]} />
        <Segmented size="sm" label="Density" value={theme.density} onChange={theme.setDensity}
          options={[{ value: "comfortable", label: "Comfortable" }, { value: "compact", label: "Compact" }]} />
      </div> : null}
      <div className="mocknote" data-testid="mocknote" hidden={!bar}>{note}</div>
      <div className="shell" style={{ flex: 1, minHeight: 0 }}>
        <Sidebar
          projects={navProjects}
          title="dude"
          collapsible
          open={navOpen}
          onOpenChange={setNavOpen}
          selected={null}
          sessions={
            <SidebarSessions selected={scenario === "session" ? "s_archive" : null} onSelect={() => undefined} onNew={() => undefined} onOpenList={() => undefined}
              sessions={[{ id: "s_archive", title: SESSION.title }, { id: "s_flags", title: "Feature flags per organisation" }]} />
          }
          footer={
            <>
              <SidebarLink icon="building">Organisation settings</SidebarLink>
              <SidebarProfile person={P["marcio"]!} detail="marcio@acme.dev" onOpen={() => undefined} />
            </>
          }
        />
        <main className="main flush">
          <SidebarToggle open={navOpen} onOpenChange={setNavOpen} size="sm" />
          <Screen key={scenario} scenario={scenario} />
        </main>
      </div>
    </div>
  );
}

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <ThemeProvider defaultPreference={(params.get("theme") as ThemePreference | null) ?? "dark"} defaultDensity={params.get("density") === "compact" ? "compact" : "comfortable"} storageKey={null} densityStorageKey={null}>
      <TooltipProvider>
        <ToastProvider>
          <Mock />
        </ToastProvider>
      </TooltipProvider>
    </ThemeProvider>
  </StrictMode>,
);
