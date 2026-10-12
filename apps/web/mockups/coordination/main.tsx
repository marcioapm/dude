/*
 * The agent-coordination mockup: dude's shell (the design system's Sidebar)
 * with the ABS epic as it would have gone with peers, messages and epic
 * notes. The bar above switches screen, moment, theme and density; it is
 * not part of the proposal. URL: ?screen=1..6&m=…&theme=…&density=…
 */

import { StrictMode, useState } from "react";
import { createRoot } from "react-dom/client";
import "@dude/design-system/tokens.css";
import "@dude/design-system/base.css";
import "../../src/app.css";
import { ThemeProvider, useTheme, type ThemePreference } from "@dude/design-system";
import { Segmented, Sidebar, SidebarLink, SidebarProfile, SidebarSessions, SidebarToggle } from "@dude/design-system/components";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import type { NavRef } from "../../../../packages/design-system/src/util/navModel.ts";
import { EpicPage, ProjectPage, type EpicView } from "./EpicPage.tsx";
import { BrainstormProposal } from "./Proposal.tsx";
import { Abs2Transcript, Abs4Reviewer, Abs4Transcript, ConductorChat, GuardsTranscript, NoRunTranscript, NotesLimitTranscript } from "./Transcripts.tsx";
import { EPIC_ID, KEYS, P, PEERS, SESSION, navAbs, type TaskKey } from "./data.ts";

type Screen = "1" | "2" | "3" | "4" | "5" | "6" | "8";

const SCREENS: ReadonlyArray<{ value: Screen; label: string }> = [
  { value: "1", label: "1 Epic · Coordination" },
  { value: "2", label: "2 ABS-4 asks" },
  { value: "3", label: "3 ABS-2 answers" },
  { value: "4", label: "4 Conductor" },
  { value: "5", label: "5 Limits" },
  { value: "6", label: "6 Board" },
  { value: "8", label: "8 Brainstorm" },
];

/** What each screen's Moment switch offers. */
const MOMENTS: Partial<Record<Screen, ReadonlyArray<{ value: string; label: string }>>> = {
  "1": [{ value: "answered", label: "13:52 answered" }, { value: "waiting", label: "13:48 waiting" }, { value: "history", label: "Replaced notes" }, { value: "board", label: "Board view" }],
  "2": [{ value: "waiting", label: "13:47 waiting" }, { value: "answered", label: "13:52 answered" }, { value: "reviewer", label: "14:34 reviewer" }],
  "4": [{ value: "collapsed", label: "Digest folded" }, { value: "open", label: "Digest open" }],
  "5": [{ value: "rate", label: "Rate limit" }, { value: "no-run", label: "No Run going" }, { value: "notes", label: "10 notes" }],
};

const params = new URLSearchParams(location.search);

function Mock() {
  const theme = useTheme();
  const [screen, setScreen] = useState<Screen>((params.get("screen") as Screen | null) ?? "1");
  const [moments, setMoments] = useState<Record<string, string>>(() => {
    const m = params.get("m");
    return m ? { [params.get("screen") ?? "1"]: m } : {};
  });
  const [navOpen, setNavOpen] = useState(false);
  const moment = moments[screen] ?? MOMENTS[screen]?.[0]?.value ?? "";
  const setMoment = (v: string) => setMoments((m) => ({ ...m, [screen]: v }));
  const go = (sc: Screen, m?: string) => {
    setScreen(sc);
    if (m) setMoments((x) => ({ ...x, [sc]: m }));
    setNavOpen(false);
  };
  const openTask = (k: TaskKey) => go(k === "ABS-2" ? "3" : k === "ABS-4" ? "2" : screen);

  const selected: NavRef = screen === "1" ? { kind: "epic", id: EPIC_ID }
    : screen === "6" ? { kind: "project", id: "p_abs" }
    : { kind: "task", id: PEERS[screen === "3" || screen === "4" ? "ABS-2" : "ABS-4"].id };

  let main;
  switch (screen) {
    case "1": {
      const view: EpicView = moment === "board" ? "board" : "coordination";
      main = <EpicPage view={view} onView={(v) => setMoment(v === "board" ? "board" : "answered")} moment={moment === "waiting" ? "waiting" : "answered"}
        historyOpen={moment === "history"} onProject={() => go("6")} onOpenTask={openTask} />;
      break;
    }
    case "2": main = moment === "reviewer" ? <Abs4Reviewer /> : <Abs4Transcript key={moment} moment={moment === "waiting" ? "waiting" : "answered"} />; break;
    case "3": main = <Abs2Transcript />; break;
    case "4": main = <ConductorChat key={moment} open={moment === "open"} />; break;
    case "5": main = moment === "no-run" ? <NoRunTranscript /> : moment === "notes" ? <NotesLimitTranscript /> : <GuardsTranscript />; break;
    case "6": main = <ProjectPage onOpenEpic={() => go("1", "answered")} onOpenTask={openTask} />; break;
    case "8": main = <BrainstormProposal />; break;
  }

  return (
    <div style={{ display: "flex", flexDirection: "column", height: "100%" }}>
      <div className="mockbar">
        <b>dude · Agents on an epic (mockup)</b>
        <Segmented size="sm" label="Screen" value={screen} onChange={(v) => go(v)} options={SCREENS} />
        {MOMENTS[screen] ? <Segmented size="sm" label="Moment" value={moment} onChange={setMoment} options={MOMENTS[screen]!} /> : null}
        <Segmented size="sm" label="Density" value={theme.density} onChange={(d) => theme.setDensity(d)}
          options={[{ value: "comfortable", label: "Comfortable" }, { value: "compact", label: "Compact" }]} />
        <Segmented size="sm" label="Theme" value={theme.resolved} onChange={(t) => theme.setPreference(t as ThemePreference)}
          options={[{ value: "dark", label: "Dark" }, { value: "light", label: "Light" }]} />
      </div>
      <div className="shell" style={{ flex: 1, minHeight: 0 }}>
        <Sidebar
          projects={[navAbs(screen === "6" || (screen === "1" && moment === "waiting") || (screen === "2" && moment === "waiting"))]}
          title="dude"
          selected={screen === "8" ? null : selected}
          sessions={<SidebarSessions sessions={[{ id: SESSION.id, title: SESSION.title, shared: true }]} selected={screen === "8" ? SESSION.id : null}
            onSelect={() => go("8")} onNew={() => undefined} />}
          onSelect={(ref) => {
            if (ref.kind === "project") go("6");
            else if (ref.kind === "epic") go("1");
            else {
              const k = KEYS.find((key) => PEERS[key].id === ref.id);
              if (k) openTask(k);
            }
          }}
          collapsible
          open={navOpen}
          onOpenChange={setNavOpen}
          footer={<>
            <SidebarLink icon="building">Organisation settings</SidebarLink>
            <SidebarProfile person={P["marcio"]!} detail="marcio@example.com" onOpen={() => undefined} />
          </>}
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
