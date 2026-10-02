/*
 * The recovery mockup: the app's shell with a stopped task, the three ways
 * to pick it back up, and the page after each. A bar above it walks the
 * screens and switches how it stopped and the theme; it is not part of the
 * proposal.
 */

import { StrictMode, useState } from "react";
import { createRoot } from "react-dom/client";
import "@dude/design-system/tokens.css";
import "@dude/design-system/base.css";
import "../../src/app.css";
import { ThemeProvider, useTheme, type ThemePreference } from "@dude/design-system";
import { Segmented, Sidebar, SidebarLink, SidebarProfile } from "@dude/design-system/components";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import { navProjects } from "../../../../packages/design-system/src/gallery/navFixtures.ts";
import { After, Meaning, PickUpDialog, StoppedSession, StoppedTask, type Screen, type Way } from "./RecoveryPage.tsx";
import { P, type Stop } from "./data.ts";

function Mock() {
  const theme = useTheme();
  const [screen, setScreen] = useState<Screen>("meaning");
  const [stop, setStop] = useState<Stop>("aborted");
  const [kept, setKept] = useState<"kept" | "gone">("kept");
  const [who, setWho] = useState<"owner" | "other">("owner");
  const [dialog, setDialog] = useState<Way | null>(null);
  const [way, setWay] = useState<Way>("resume");
  const [afterTab, setAfterTab] = useState<"overview" | "sessions" | "activity">("sessions");
  const [earlierOpen, setEarlierOpen] = useState(false);
  const [openOld, setOpenOld] = useState(false);
  const history = screen === "history";
  const expired = kept === "gone";
  const choose = (w: Way) => setDialog(expired && w === "resume" ? "retry" : w);
  return (
    <div style={{ display: "flex", flexDirection: "column", height: "100%" }}>
      <div className="mockbar">
        <b>dude · Picking a stopped task back up (mockup)</b>
        <Segmented size="sm" label="Screen" value={screen} onChange={(s) => {
          setScreen(s);
          setDialog(null);
          // The old attempt's history: after Start over, folded open, its session to hand.
          if (s === "history") { setWay("restart"); setAfterTab("overview"); setEarlierOpen(true); setOpenOld(true); }
          else { setEarlierOpen(false); setOpenOld(false); }
        }} options={[
          { value: "meaning", label: "1 · What they mean" },
          { value: "task", label: "2 · Stopped task" },
          { value: "session", label: "3 · Its session" },
          { value: "after", label: "4 · After" },
          { value: "history", label: "5 · Attempt 1" },
        ]} />
        <Segmented size="sm" label="Theme" value={theme.resolved} onChange={(t) => theme.setPreference(t as ThemePreference)} options={[{ value: "dark", label: "Dark" }, { value: "light", label: "Light" }]} />
      </div>
      {screen !== "meaning" ? (
        <div className="mockbar">
          <span style={{ marginRight: "auto" }}>Scenario</span>
          <Segmented size="sm" label="How it stopped" value={stop} onChange={setStop} options={[{ value: "aborted", label: "Aborted by Ana" }, { value: "failed", label: "Failed (host lost)" }]} />
          {screen === "after" || history ? (
            <>
              <Segmented size="sm" label="Picked up by" value={way} onChange={setWay} options={[{ value: "resume", label: "Resume" }, { value: "retry", label: "Try again" }, { value: "restart", label: "Start over" }]} />
              <Segmented size="sm" label="Tab" value={afterTab} onChange={setAfterTab} options={[{ value: "overview", label: "Overview" }, { value: "sessions", label: "Sessions" }, { value: "activity", label: "Activity" }]} />
            </>
          ) : (
            <>
              <Segmented size="sm" label="Workspace" value={kept} onChange={setKept} options={[{ value: "kept", label: "Still kept" }, { value: "gone", label: "Past retention" }]} />
              {screen === "task" ? (
                <Segmented size="sm" label="Seen by" value={who} onChange={setWho} options={[{ value: "owner", label: "Seen by Márcio (owner)" }, { value: "other", label: "Seen by someone else" }]} />
              ) : null}
            </>
          )}
        </div>
      ) : null}
      <div className="shell" style={{ flex: 1, minHeight: 0 }}>
        <Sidebar
          projects={navProjects}
          title="dude"
          footer={
            <>
              <SidebarLink icon="building">Organisation settings</SidebarLink>
              <SidebarProfile person={P["marcio"]!} detail="marcio@acme.dev" onOpen={() => undefined} />
            </>
          }
        />
        <main className={screen === "meaning" ? "main" : "main flush"}>
          {screen === "meaning" ? <Meaning /> : null}
          {screen === "task" ? <StoppedTask stop={stop} expired={expired} you={who === "owner"} onChoose={choose} /> : null}
          {screen === "session" ? <StoppedSession stop={stop} expired={expired} onChoose={choose} /> : null}
          {screen === "after" || history ? (
            <After stop={stop} way={way} tab={afterTab} earlierOpen={earlierOpen} onEarlierOpen={setEarlierOpen} openOld={openOld} onOpenOld={setOpenOld} />
          ) : null}
          {dialog ? (
            <PickUpDialog stop={stop} expired={expired} way={dialog} onWay={setDialog} onClose={() => setDialog(null)}
              onDone={(w) => { setDialog(null); setWay(w); setAfterTab(w === "restart" ? "overview" : "sessions"); setScreen("after"); }} />
          ) : null}
        </main>
      </div>
    </div>
  );
}

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <ThemeProvider defaultPreference="dark" storageKey={null} densityStorageKey={null}>
      <TooltipProvider>
        <ToastProvider>
          <Mock />
        </ToastProvider>
      </TooltipProvider>
    </ThemeProvider>
  </StrictMode>,
);
