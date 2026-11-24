import { StrictMode, useState } from "react";
import { createRoot } from "react-dom/client";
import "@dude/design-system/tokens.css";
import "@dude/design-system/base.css";
import "../../src/app.css";
import { ThemeProvider, useTheme, type ThemePreference } from "@dude/design-system";
import { Sidebar, SidebarLink, SidebarProfile, SidebarToggle, SidebarSessions } from "@dude/design-system/components";
import { Button, Select, TooltipProvider } from "@dude/design-system/primitives";
import { ProjectSettings } from "./ProjectSettings";
import { TaskOverview, Conversation, TaskServers, DesignNotes } from "./TaskPages";
import { SCREENS, PERSON, type Screen } from "./data";
import s from "./styles.module.css";

const params = new URLSearchParams(location.search);
const first = params.get("screen") as Screen;
function Mock() {
 const theme = useTheme();
 const [screen, setScreen] = useState<Screen>(first in SCREENS ? first : "project-off");
 const [projectDefault, setProjectDefault] = useState(first === "project-on" || first === "project-missing" || first === "project-empty");
 const [open, setOpen] = useState(false);
 const [query, setQuery] = useState("");
 const go = (v: Screen) => { setScreen(v); if (v.startsWith("project-")) setProjectDefault(["project-on", "project-missing", "project-empty"].includes(v)); setOpen(false); };
 const projects = [{ id: "acme", name: "Acme", tasks: [{ id: "t142", key: "ACME-142", title: "Fix background queue retry", status: "running" as const, people: [PERSON] }] }];
 return <div className={s.app}><div className={s.harness}><strong>Preview automation <span>· design mockup</span></strong><Select aria-label="Screen" size="sm" value={screen} onValueChange={v => go(v as Screen)} options={Object.entries(SCREENS).map(([value, label]) => ({ value, label }))} /><div className={s.harnessButtons}><button aria-label="Comfortable density" className={theme.density === "comfortable" ? s.active : ""} onClick={() => theme.setDensity("comfortable")}>Comfortable</button><button aria-label="Compact density" className={theme.density === "compact" ? s.active : ""} onClick={() => theme.setDensity("compact")}>Compact</button><button className={theme.resolved === "dark" ? s.active : ""} onClick={() => theme.setPreference("dark")}>Dark</button><button className={theme.resolved === "light" ? s.active : ""} onClick={() => theme.setPreference("light")}>Light</button><Button size="sm" variant="quiet" onClick={() => go("notes")}>Design notes</Button></div></div><div className={s.shell}><Sidebar title="dude · Acme" projects={projects} you={PERSON.id} selected={screen.startsWith("project") ? { kind: "project", id: "acme" } : screen === "brainstorm-ready" || screen === "notes" ? null : { kind: "task", id: "t142" }} onSelect={ref => go(ref.kind === "task" ? "task-inherit" : "project-off")} query={query} onQueryChange={setQuery} sessions={<div className={s.sidebarSession}><small>SESSIONS</small><SidebarLink icon="agent" onClick={() => go("brainstorm-ready")}>Explore queue behavior</SidebarLink></div>} collapsible open={open} onOpenChange={setOpen} footer={<><SidebarLink icon="globe" onClick={() => go("servers-dormant")}>Project servers</SidebarLink><SidebarLink icon="settings" onClick={() => go("project-off")}>Project settings</SidebarLink><SidebarProfile person={PERSON} detail="marcio@example.com" onOpen={() => undefined} /></>} /><main className={s.main}><SidebarToggle open={open} onOpenChange={setOpen} size="sm" />{screen.startsWith("project-") ? <ProjectSettings key={screen} screen={screen} enabled={projectDefault} onEnabled={setProjectDefault} go={go} /> : screen.startsWith("task-") ? <TaskOverview key={screen} explicitOff={screen === "task-off"} projectDefault={projectDefault} setProjectDefault={setProjectDefault} go={go} /> : screen.startsWith("conductor-") || screen === "brainstorm-ready" ? <Conversation key={screen} screen={screen} go={go} /> : screen.startsWith("servers-") ? <TaskServers key={screen} reused={screen === "servers-reused"} go={go} /> : <DesignNotes />}</main></div></div>;
}
createRoot(document.getElementById("root")!).render(<StrictMode><ThemeProvider defaultPreference={(params.get("theme") as ThemePreference) ?? "dark"} defaultDensity={params.get("density") === "compact" ? "compact" : "comfortable"} storageKey={null} densityStorageKey={null}><TooltipProvider><Mock /></TooltipProvider></ThemeProvider></StrictMode>);
