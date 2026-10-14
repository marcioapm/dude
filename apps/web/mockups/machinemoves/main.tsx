/*
 * The machinemoves mockup: what an agent was told about its move, and one
 * notice per move. The app's shell with BILL-42's Runs, its Chat and a
 * brainstorm session, each from a ledger folded by the app's own code:
 * today's fold ("Before") or the proposed one ("After"). A bar above picks
 * the state, before or after, and the theme; it is not part of the
 * proposal. URL parameters set the same (?s=reset&v=before&theme=light).
 */

import { StrictMode, useMemo, useState } from "react";
import { createRoot } from "react-dom/client";
import "@dude/design-system/tokens.css";
import "@dude/design-system/base.css";
import "../../src/app.css";
import { ThemeProvider, useTheme, type ThemePreference } from "@dude/design-system";
import { Segmented, Sidebar, SidebarLink, SidebarProfile, SidebarSessions, SidebarToggle } from "@dude/design-system/components";
import { Select, ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import { navProjects } from "../../../../packages/design-system/src/gallery/navFixtures.ts";
import { ConductorChat, RunPage, SessionPage, type Variant } from "./Screens.tsx";
import { conductor, raise, refused, reset, session } from "./events.ts";
import { LARGE, PEOPLE, SESSION_TITLE, STANDARD, XL } from "./data.ts";

type State = "raise" | "raise-pool" | "reset" | "refused" | "conductor" | "session" | "reset-steer";

const STATES: ReadonlyArray<{ value: State; label: string; note: string }> = [
  { value: "raise", label: "1 · A raise in its pool", note: "The implementer moves Standard → Large. Before: the move's line counts only its park, and \"Resumed in …\" follows as a second completion; nothing shows what the agent was told. After: one line with the whole move's time (its stages on hover), then dude's words to the agent as a Told turn, before its first reply." },
  { value: "raise-pool", label: "2 · A raise to another pool", note: "Standard → XL in highmem: the wait for a machine there is in the move's time, and the agent is told the pool." },
  { value: "reset", label: "3 · A Reset with a note", note: "Ana resets the fixer (following the implementer) from Large back to Standard with a note, while it runs. The agent is told \"Moved back to Standard by Ana Ribeiro: <note>\"." },
  { value: "refused", label: "4 · lux refuses at resume", note: "The dry run passed, then lux refused XL at the real resume (platform_mismatch): the Run resumed on Standard. The refusal keeps its row and no timing row; the agent's keptInput words are shown." },
  { value: "conductor", label: "5 · The conductor moves", note: "BILL-42's Chat: the conductor moves itself Standard → Large. The same notice and Told turn, inside its Chat." },
  { value: "session", label: "6 · A session moves", note: "A brainstorm session Small → XL in highmem, on the Session screen: the same notice and Told turn, signed by the session's dude." },
  { value: "reset-steer", label: "7 · Reset + a person's message", note: "Ana's Reset with a note, then her steer queued while it moved. Two rows: dude's Told turn (the resume's input), then Ana's steer, read where the agent read it." },
];

const params = new URLSearchParams(location.search);
const isState = (v: string | null): v is State => STATES.some((s) => s.value === v);

function Screen({ state, variant }: { state: State; variant: Variant }) {
  const events = useMemo(() => {
    switch (state) {
      case "raise": return raise(false);
      case "raise-pool": return raise(true);
      case "reset": return reset(false);
      case "reset-steer": return reset(true);
      case "refused": return refused();
      case "conductor": return conductor();
      case "session": return session();
    }
  }, [state]);
  switch (state) {
    case "raise": return <RunPage events={events} variant={variant} role="implementer" title="Implement" machine={LARGE} raised runId="run_7f3k" />;
    case "raise-pool": return <RunPage events={events} variant={variant} role="implementer" title="Implement" machine={XL} raised runId="run_7f3k" />;
    case "reset":
    case "reset-steer": return <RunPage events={events} variant={variant} role="implementer" title="Fix · 2 findings" machine={STANDARD} raised={false} runId="run_9b2m" />;
    case "refused": return <RunPage events={events} variant={variant} role="implementer" title="Implement" machine={STANDARD} raised={false} runId="run_7f3k" />;
    case "conductor": return <ConductorChat events={events} variant={variant} />;
    case "session": return <SessionPage events={events} variant={variant} machine={XL} />;
  }
}

function Mock() {
  const theme = useTheme();
  const [state, setState] = useState<State>(() => (isState(params.get("s")) ? (params.get("s") as State) : "raise"));
  const [variant, setVariant] = useState<Variant>(params.get("v") === "before" ? "before" : "after");
  const [navOpen, setNavOpen] = useState(false);
  const note = STATES.find((s) => s.value === state)!.note;
  return (
    <div style={{ display: "flex", flexDirection: "column", height: "100%" }}>
      <div className="mockbar">
        <b>dude · Machine moves: what the agent was told, one notice per move (mockup)</b>
        <Select aria-label="State" size="sm" value={state} onValueChange={(v) => setState(v as State)}
          options={STATES.map((s) => ({ value: s.value, label: s.label }))} data-testid="state" />
        <Segmented size="sm" label="Before or after" value={variant} onChange={setVariant}
          options={[{ value: "before", label: "Before (today)" }, { value: "after", label: "After" }]} />
        <Segmented size="sm" label="Theme" value={theme.resolved} onChange={(t) => theme.setPreference(t as ThemePreference)}
          options={[{ value: "dark", label: "Dark" }, { value: "light", label: "Light" }]} />
      </div>
      <div className="mocknote" data-testid="mocknote">{variant === "before" ? `Before (today): ${note}` : note}</div>
      <div className="shell" style={{ flex: 1, minHeight: 0 }}>
        <Sidebar
          projects={navProjects}
          title="dude"
          collapsible
          open={navOpen}
          onOpenChange={setNavOpen}
          selected={null}
          sessions={
            <SidebarSessions selected={state === "session" ? "s_archive" : null} onSelect={() => undefined} onNew={() => undefined} onOpenList={() => undefined}
              sessions={[{ id: "s_archive", title: SESSION_TITLE }, { id: "s_flags", title: "Feature flags per organisation" }]} />
          }
          footer={<>
            <SidebarLink icon="building">Organisation settings</SidebarLink>
            <SidebarProfile person={PEOPLE.marcio} detail="marcio@acme.dev" onOpen={() => undefined} />
          </>}
        />
        <main className="main flush">
          <SidebarToggle open={navOpen} onOpenChange={setNavOpen} size="sm" />
          <Screen key={`${state}-${variant}`} state={state} variant={variant} />
        </main>
      </div>
    </div>
  );
}

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <ThemeProvider defaultPreference={(params.get("theme") as ThemePreference | null) ?? "dark"} defaultDensity="comfortable" storageKey={null} densityStorageKey={null}>
      <TooltipProvider>
        <ToastProvider>
          <Mock />
        </ToastProvider>
      </TooltipProvider>
    </ThemeProvider>
  </StrictMode>,
);
