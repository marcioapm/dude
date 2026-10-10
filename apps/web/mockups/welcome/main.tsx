/*
 * The welcome page mockup: dude as it opens. The sidebar works — the brand
 * and New session go to the welcome, a session opens it, a project opens a
 * stand-in for its board — and sending from the welcome makes a session
 * with that message and what it reads. A bar above switches theme,
 * whether you have sessions yet, and today's new session for comparison;
 * it is not part of the proposal.
 */

import { StrictMode, useEffect, useState } from "react";
import { createRoot } from "react-dom/client";
import "@dude/design-system/tokens.css";
import "@dude/design-system/base.css";
import "../../src/app.css";
import { ThemeProvider, useTheme, type ThemePreference } from "@dude/design-system";
import { Segmented, Sidebar, SidebarLink, SidebarProfile, SidebarSessions, SidebarToggle } from "@dude/design-system/components";
import { EmptyState, IconButton, ToastProvider, Tooltip, TooltipProvider } from "@dude/design-system/primitives";
import { SidebarRail } from "./SidebarRail.tsx";
import type { NavRef } from "../../../../packages/design-system/src/util/navModel.ts";
import { navProjects } from "../../../../packages/design-system/src/gallery/navFixtures.ts";
import { Session, Today, Welcome } from "./WelcomePage.tsx";
import { P, RECENT, type Project, type Recent } from "./data.ts";
import dudeSvg from "../../public/dude.svg?url";
import dudeOutlinedSvg from "../../public/dude-outlined.svg?url";

type Place = { view: "welcome" } | { view: "session"; id: string } | { view: "tree"; ref: NavRef } | { view: "today" };

/** A session made from the welcome: no title until its agent names it. */
interface Made { id: string; title: string | null; first: string; linked: readonly Project[] }

const params = new URLSearchParams(location.search);

/** For the mockup only: what the agent might call it. The real name is the agent's. */
function nameFor(text: string): string {
  const rest = text.replace(/^(I want to plan an epic for|Help me write a task for|How does|Go through)\s*/i, "").replace(/[?.!].*$/, "").trim();
  const words = rest.split(/\s+/).slice(0, 6).join(" ");
  return words ? words[0]!.toUpperCase() + words.slice(1) : "New session";
}

function Brand() {
  return (
    <span className="brand">
      <span className="dudeMark" style={{ width: 30, height: 30 }} aria-hidden="true">
        <img className="dudeMarkLight" src={dudeSvg} alt="" style={{ width: 30, height: 30 }} />
        <img className="dudeMarkDark" src={dudeOutlinedSvg} alt="" style={{ width: 30, height: 30 }} />
      </span>
      El Duderino
    </span>
  );
}

function Mock() {
  const theme = useTheme();
  const [place, setPlace] = useState<Place>(() => (params.get("o") === "today" ? { view: "today" } : { view: "welcome" }));
  const [hasSessions, setHasSessions] = useState<"some" | "none">(params.get("sessions") === "none" ? "none" : "some");
  const [made, setMade] = useState<Made[]>([]);
  const [navOpen, setNavOpen] = useState(false);
  // Collapsed to the rail: remembered per person (localStorage in the app), wide screens only.
  const [collapsed, setCollapsed] = useState(params.get("rail") === "1");
  const [findOnExpand, setFindOnExpand] = useState(false);
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const t = e.target as HTMLElement | null;
      if (e.key === "[" && !e.metaKey && !e.ctrlKey && !e.altKey && !(t && (t.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(t.tagName)))) {
        e.preventDefault();
        setCollapsed((c) => !c);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);
  useEffect(() => {
    if (!collapsed && findOnExpand) {
      setFindOnExpand(false);
      requestAnimationFrame(() => (document.querySelector('[data-testid="sidebar"] input, nav input') as HTMLInputElement | null)?.focus());
    }
  }, [collapsed, findOnExpand]);
  const recent: Recent[] = hasSessions === "none" && made.length === 0 ? [] : [
    ...made.map((m) => ({ id: m.id, title: m.title ?? "New session", summary: "Nothing filed yet", age: "just now", first: m.first, reply: "", linked: m.linked })),
    ...(hasSessions === "some" ? RECENT : []),
  ];
  const go = (p: Place) => { setPlace(p); setNavOpen(false); };
  const start = (text: string, linked: readonly Project[]) => {
    const id = `new-${made.length + 1}`;
    setMade((m) => [{ id, title: null, first: text, linked }, ...m]);
    go({ view: "session", id });
    // Its agent names it once the subject is clear (name_session, at most 60 characters).
    setTimeout(() => setMade((m) => m.map((x) => (x.id === id ? { ...x, title: nameFor(text) } : x))), 2500);
  };
  const open = place.view === "session" ? recent.find((r) => r.id === place.id) : undefined;
  const openMade = place.view === "session" ? made.find((m) => m.id === place.id) : undefined;

  let main;
  if (place.view === "today") main = <Today />;
  else if (place.view === "welcome") {
    main = <Welcome key={hasSessions} recent={recent} startLinked={false} onStart={start}
      onOpen={(id) => go({ view: "session", id })} onAll={() => undefined} />;
  } else if (place.view === "session" && open) {
    main = <Session key={open.id} title={openMade ? openMade.title : open.title} first={open.first} reply={openMade ? undefined : open.reply}
      linked={open.linked} live={Boolean(openMade)} />;
  } else {
    main = <div className="centered"><EmptyState icon="layers" title="The project's board" description="Not part of this mockup: the brand or New session goes back to the welcome." /></div>;
  }

  return (
    <div style={{ display: "flex", flexDirection: "column", height: "100%" }}>
      <div className="mockbar">
        <b>dude · The welcome page (mockup)</b>
        <Segmented size="sm" label="Compare" value={place.view === "today" ? "today" : "new"} onChange={(v) => go(v === "today" ? { view: "today" } : { view: "welcome" })}
          options={[{ value: "new", label: "Proposed" }, { value: "today", label: "Today's new session" }]} />
        <Segmented size="sm" label="Sessions" value={hasSessions} onChange={(v) => { setHasSessions(v); go({ view: "welcome" }); }}
          options={[{ value: "some", label: "Has sessions" }, { value: "none", label: "First time" }]} />
        <Segmented size="sm" label="Density" value={theme.density} onChange={(d) => theme.setDensity(d)}
          options={[{ value: "comfortable", label: "Comfortable" }, { value: "compact", label: "Compact" }]} />
        <Segmented size="sm" label="Sidebar" value={collapsed ? "rail" : "full"} onChange={(v) => setCollapsed(v === "rail")}
          options={[{ value: "full", label: "Sidebar" }, { value: "rail", label: "Collapsed" }]} />
        <Segmented size="sm" label="Theme" value={theme.resolved} onChange={(t) => theme.setPreference(t as ThemePreference)} options={[{ value: "dark", label: "Dark" }, { value: "light", label: "Light" }]} />
      </div>
      <div className="mocknote">
        dude opens here, and New session comes here: nothing is made until you send. Try a starter, link a project, send, open a recent session, or press El Duderino to come back. Collapse the sidebar with the chevron by its title, or press [ anywhere.
      </div>
      <div className="shell" style={{ flex: 1, minHeight: 0 }}>
        {collapsed ? (
          <div className="railSlot">
            <SidebarRail projects={navProjects} selected={place.view === "tree" ? place.ref : null} you={P["marcio"]!}
              sessions={recent.map((r) => ({ id: r.id, title: r.title }))}
              home={place.view === "welcome"} sessionsOpen={place.view === "session"}
              onHome={() => go({ view: "welcome" })} onExpand={() => setCollapsed(false)}
              onSearch={() => { setFindOnExpand(true); setCollapsed(false); }}
              onWaiting={() => go({ view: "tree", ref: { kind: "task", id: "wi_2401" } })}
              onNew={() => go({ view: "welcome" })} onSessions={() => recent[0] && go({ view: "session", id: recent[0].id })}
              onProject={(id) => go({ view: "tree", ref: { kind: "project", id } })} />
          </div>
        ) : null}
        <div className={collapsed ? "sidebarSlot isCollapsed" : "sidebarSlot"}>
        <Sidebar
            projects={navProjects}
            title={<button type="button" className="brandHome" onClick={() => go({ view: "welcome" })} aria-label="Home"><Brand /></button>}
            selected={place.view === "tree" ? place.ref : null}
            onSelect={(ref) => go({ view: "tree", ref })}
            headerActions={<Tooltip content="Collapse sidebar" shortcut="[" side="right"><IconButton icon="chevron-left" label="Collapse sidebar" size="sm" className="collapseButton" onClick={() => setCollapsed(true)} data-testid="sidebar-collapse" /></Tooltip>}
            collapsible
            open={navOpen}
            onOpenChange={setNavOpen}
            sessions={<SidebarSessions selected={place.view === "session" ? place.id : null} onSelect={(id) => go({ view: "session", id })}
              onNew={() => go({ view: "welcome" })} onOpenList={() => undefined}
              sessions={recent.map((r) => ({ id: r.id, title: r.title, ...(r.shared ? { shared: true, owner: r.owner } : {}) }))} />}
            footer={
              <>
                <SidebarLink icon="building">Organisation settings</SidebarLink>
                <SidebarProfile person={P["marcio"]!} detail="marcio@acme.dev" onOpen={() => undefined} />
              </>
            }
          />
        </div>
        <main className={place.view === "tree" ? "main" : "main flush"}>
          <SidebarToggle open={navOpen} onOpenChange={setNavOpen} size="sm" />
          {main}
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
