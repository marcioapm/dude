/*
 * The new-session welcome mockup: the app's shell with a session just made
 * by New session, three ways its first screen could look, and today's for
 * comparison. A bar above it switches the option, the theme and whether a
 * project is linked yet; it is not part of the proposal. Sending a message
 * shows what follows: the session as it is today.
 */

import { StrictMode, useState } from "react";
import { createRoot } from "react-dom/client";
import "@dude/design-system/tokens.css";
import "@dude/design-system/base.css";
import "../../src/app.css";
import { ThemeProvider, useTheme, type ThemePreference } from "@dude/design-system";
import { Segmented, Sidebar, SidebarLink, SidebarProfile, SidebarSessions, SidebarToggle } from "@dude/design-system/components";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import { navProjects } from "../../../../packages/design-system/src/gallery/navFixtures.ts";
import { NewSession, Today, type Option } from "./WelcomePage.tsx";
import { P } from "./data.ts";

type Screen = "today" | Option;

const NOTES: Record<Screen, string> = {
  today: "Today: an empty transcript with one muted line at the top, the composer at the foot, the rail full of empty blocks.",
  centred: "A · Centred. dude's face, a greeting, the composer in the middle of the page with what it reads as chips in it, four starters under it. The rail waits until there is a conversation.",
  starters: "B · Starters. The brainstorm's face and what it does, four starters as tiles with a sentence each, your recent sessions; the composer stays at the foot, where it will be.",
  context: "C · From your work. Option A, plus what dude already knows is worth talking through: the question waiting on you, a task that keeps failing, an epic in review, yesterday's session.",
};

function Mock() {
  const theme = useTheme();
  const [screen, setScreen] = useState<Screen>(() => (new URLSearchParams(location.search).get("o") as Screen | null) ?? "centred");
  const [navOpen, setNavOpen] = useState(false);
  const [linked, setLinked] = useState<"none" | "two">(() => (new URLSearchParams(location.search).get("linked") === "two" ? "two" : "none"));
  return (
    <div style={{ display: "flex", flexDirection: "column", height: "100%" }}>
      <div className="mockbar">
        <b>dude · A new session's first screen (mockup)</b>
        <Segmented size="sm" label="Option" value={screen} onChange={setScreen} options={[
          { value: "today", label: "Today" },
          { value: "centred", label: "A · Centred" },
          { value: "starters", label: "B · Starters" },
          { value: "context", label: "C · From your work" },
        ]} />
        <Segmented size="sm" label="Linked" value={linked} onChange={setLinked} options={[{ value: "none", label: "Nothing linked" }, { value: "two", label: "Two linked" }]} />
        <Segmented size="sm" label="Theme" value={theme.resolved} onChange={(t) => theme.setPreference(t as ThemePreference)} options={[{ value: "dark", label: "Dark" }, { value: "light", label: "Light" }]} />
      </div>
      <div className="mocknote">{NOTES[screen]}</div>
      <div className="shell" style={{ flex: 1, minHeight: 0 }}>
        <Sidebar
          projects={navProjects}
          title="dude"
          selected={null}
          collapsible
          open={navOpen}
          onOpenChange={setNavOpen}
          sessions={<SidebarSessions selected="new" onSelect={() => undefined} onNew={() => undefined} onOpenList={() => undefined}
            sessions={[{ id: "new", title: "New session" }, { id: "s1", title: "Usage-based billing" }, { id: "s2", title: "Q4 cleanup ideas", shared: true, owner: P["ana"] }]} />}
          footer={
            <>
              <SidebarLink icon="building">Organisation settings</SidebarLink>
              <SidebarProfile person={P["marcio"]!} detail="marcio@acme.dev" onOpen={() => undefined} />
            </>
          }
        />
        <main className="main flush">
          <SidebarToggle open={navOpen} onOpenChange={setNavOpen} size="sm" />
          {screen === "today" ? <Today /> : <NewSession key={`${screen}-${linked}`} option={screen} startLinked={linked === "two"} />}
        </main>
      </div>
    </div>
  );
}

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <ThemeProvider defaultPreference={(new URLSearchParams(location.search).get("theme") as ThemePreference | null) ?? "dark"} storageKey={null} densityStorageKey={null}>
      <TooltipProvider>
        <ToastProvider>
          <Mock />
        </ToastProvider>
      </TooltipProvider>
    </ThemeProvider>
  </StrictMode>,
);
