/*
 * The agent startup mockup: a brainstorm session and a task's Run while
 * their agent is being placed, started or resumed, in the app's shell. A
 * bar above switches the scenario, theme and density, and replays the
 * fresh start; it is not part of the proposal. URL parameters set the same
 * (?s=resume&theme=light&density=compact&open=1).
 */

import { StrictMode, useMemo, useState } from "react";
import { createRoot } from "react-dom/client";
import "@dude/design-system/tokens.css";
import "@dude/design-system/base.css";
import "../../src/app.css";
import { ThemeProvider, useTheme, type ThemePreference } from "@dude/design-system";
import { Segmented, Sidebar, SidebarLink, SidebarProfile, SidebarSessions, SidebarToggle } from "@dude/design-system/components";
import { Button, ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import { navProjects } from "../../../../packages/design-system/src/gallery/navFixtures.ts";
import { RunScreen, SessionScreen } from "./StartupPage.tsx";
import { useNow } from "./components.tsx";
import { FRESH_SCRIPT, P, SCENARIOS, SESSION, startFor, type Scenario, type StartStage, type StartState } from "./data.ts";

const params = new URLSearchParams(location.search);
const isScenario = (v: string | null): v is Scenario => SCENARIOS.some((x) => x.value === v);

/** Scenario 1 as it plays from `t0`: the stage the script has reached, and what passed before it. */
function freshAt(t0: number, now: number): StartState {
  const elapsed = now - t0;
  let stage: StartStage = "waiting";
  let since = t0;
  const done: Array<{ stage: StartStage; tookMs: number }> = [];
  for (const step of FRESH_SCRIPT) {
    if (elapsed < step.atMs) break;
    done.push({ stage, tookMs: t0 + step.atMs - since });
    stage = step.stage;
    since = t0 + step.atMs;
  }
  return { kind: "start", stage, stageSince: since, startedAt: t0, done };
}

function Mock() {
  const theme = useTheme();
  const [scenario, setScenario] = useState<Scenario>(() => (isScenario(params.get("s")) ? (params.get("s") as Scenario) : "fresh"));
  // A screenshot of scenario 1 at a moment: ?at=7 opens it 7 seconds in.
  const [t0, setT0] = useState(() => Date.now() - Number(params.get("at") ?? 0) * 1000);
  const [opened] = useState(() => Date.now());
  const [navOpen, setNavOpen] = useState(false);
  const now = useNow(250);
  const fixed = useMemo(() => startFor(scenario, opened), [scenario, opened]);
  const start = scenario === "fresh" ? freshAt(t0, now) : fixed;
  const note = SCENARIOS.find((x) => x.value === scenario)!.note;
  const sessionTitle = scenario === "resume" || scenario === "running" ? SESSION.title : "New session";

  return (
    <div style={{ display: "flex", flexDirection: "column", height: "100%" }}>
      <div className="mockbar">
        <b>dude · The agent is starting (mockup)</b>
        <Segmented size="sm" label="Scenario" value={scenario} onChange={(v) => { setScenario(v); setT0(Date.now()); }}
          options={SCENARIOS.map((x) => ({ value: x.value, label: x.label }))} />
        {scenario === "fresh" ? <Button size="sm" variant="quiet" leadingIcon="retry" onClick={() => setT0(Date.now())}>Replay</Button> : null}
        <Segmented size="sm" label="Theme" value={theme.resolved} onChange={(t) => theme.setPreference(t as ThemePreference)}
          options={[{ value: "dark", label: "Dark" }, { value: "light", label: "Light" }]} />
        <Segmented size="sm" label="Density" value={theme.density} onChange={theme.setDensity}
          options={[{ value: "comfortable", label: "Comfortable" }, { value: "compact", label: "Compact" }]} />
      </div>
      <div className="mocknote" data-testid="mocknote">{note}</div>
      <div className="shell" style={{ flex: 1, minHeight: 0 }}>
        <Sidebar
          projects={navProjects}
          title="dude"
          collapsible
          open={navOpen}
          onOpenChange={setNavOpen}
          selected={null}
          sessions={scenario === "run" ? undefined : (
            <SidebarSessions selected="s_billing" onSelect={() => undefined} onNew={() => undefined} onOpenList={() => undefined}
              sessions={[{ id: "s_billing", title: sessionTitle }, { id: "s_flags", title: "Feature flags per organisation" }]} />
          )}
          footer={
            <>
              <SidebarLink icon="building">Organisation settings</SidebarLink>
              <SidebarProfile person={P["marcio"]!} detail="marcio@acme.dev" onOpen={() => undefined} />
            </>
          }
        />
        <main className="main flush">
          <SidebarToggle open={navOpen} onOpenChange={setNavOpen} size="sm" />
          {scenario === "run" && start ? <RunScreen key="run" start={start} now={now} />
            : <SessionScreen key={scenario} start={start} now={now} sentAt={start ? start.startedAt - 400 : opened - 30_000}
              resumed={scenario === "resume" || scenario === "running"} />}
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
