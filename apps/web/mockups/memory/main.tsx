/*
 * The memory mockup: the app's shell (the design system's Sidebar and
 * SettingsLayout, as apps/web lays them out) with the proposed Memory page
 * in the organisation's and a project's settings. A bar above it switches
 * scope and theme; it is not part of the proposal.
 */

import { StrictMode, useState } from "react";
import { createRoot } from "react-dom/client";
import "@dude/design-system/tokens.css";
import "@dude/design-system/base.css";
import "../../src/app.css";
import { ThemeProvider, useTheme, type ThemePreference } from "@dude/design-system";
import { AgentAvatar, ProjectAvatar, Segmented, SettingsLayout, SettingsNote, Sidebar, SidebarLink, SidebarProfile, type SettingsNavItem } from "@dude/design-system/components";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import { navProjects } from "../../../../packages/design-system/src/gallery/navFixtures.ts";
import { MemoryGlyph, MemoryPages, type MemoryPageId, type Where } from "./MemoryPage.tsx";
import { ORG, P } from "./data.ts";

function Mock() {
  const theme = useTheme();
  const [scope, setScope] = useState<"org" | "project">("org");
  const [page, setPage] = useState<MemoryPageId>("memory-search");
  const where: Where = scope === "org" ? { kind: "org" } : { kind: "project", project: "control-plane" };
  // A page with sub-pages, as Agents has its roles: the group opens on its first.
  const memoryNav: SettingsNavItem = {
    id: "memory",
    label: "Memory",
    leading: <MemoryGlyph className="navGlyph" />,
    items: [
      { id: "memory-search", label: "Search" },
      { id: "memory-list", label: "Memories" },
      { id: "memory-index", label: "Index", note: scope === "org" ? "2 failed" : undefined },
    ],
  };
  const items: SettingsNavItem[] =
    scope === "org"
      ? [
          { id: "members", label: "Members", icon: "human" },
          { id: "general", label: "General", icon: "settings" },
          { id: "github", label: "GitHub", icon: "git-branch" },
          { id: "agents", label: "Agents", icon: "agent" },
          { id: "delivery", label: "Delivery", icon: "list-check" },
          memoryNav,
        ]
      : [
          { id: "general", label: "General", icon: "settings" },
          { id: "repositories", label: "Repositories", icon: "git-branch" },
          { id: "agents", label: "Agents", icon: "agent" },
          { id: "delivery", label: "Delivery", icon: "list-check" },
          memoryNav,
        ];
  return (
    <div style={{ display: "flex", flexDirection: "column", height: "100%" }}>
      <div className="mockbar">
        <b>dude · Memory (mockup)</b>
        <Segmented size="sm" label="Scope" value={scope} onChange={setScope} options={[{ value: "org", label: "Organisation settings" }, { value: "project", label: "Project settings" }]} />
        <Segmented size="sm" label="Theme" value={theme.resolved} onChange={(t) => theme.setPreference(t as ThemePreference)} options={[{ value: "dark", label: "Dark" }, { value: "light", label: "Light" }]} />
        <Segmented size="sm" label="Density" value={theme.density} onChange={theme.setDensity} options={[{ value: "comfortable", label: "Comfortable" }, { value: "compact", label: "Compact" }]} />
      </div>
      <div className="shell" style={{ flex: 1, minHeight: 0 }}>
        <Sidebar
          projects={navProjects}
          title="dude"
          footer={
            <>
              <SidebarLink icon="building" current={scope === "org"} onClick={() => setScope("org")}>Organisation settings</SidebarLink>
              <SidebarProfile person={P["marcio"]!} detail="marcio@acme.dev" onOpen={() => undefined} />
            </>
          }
        />
        <main className="main">
          <SettingsLayout
            key={scope}
            scope={
              scope === "org"
                ? { title: ORG, subtitle: "Organisation settings", leading: <AgentAvatar role="orchestrator" size="lg" /> }
                : { title: "control-plane", subtitle: "Project settings", leading: <ProjectAvatar project={{ id: "p_control-plane", name: "control-plane" }} size={32} /> }
            }
            items={items}
            current={page}
            onSelect={(id) => id.startsWith("memory-") && setPage(id as MemoryPageId)}
          >
            <SettingsNote icon="info">
              {scope === "org"
                ? "Everyone in Acme can search and add memories; archiving another person’s is for organisation admins."
                : "Memories added here apply to control-plane only. Acme’s are shown too, marked “From Acme”, and are changed in Acme’s settings."}
            </SettingsNote>
            <MemoryPages key={scope} page={page} where={where} onPage={setPage} />
          </SettingsLayout>
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
