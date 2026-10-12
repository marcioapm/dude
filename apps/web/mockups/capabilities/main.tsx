/*
 * The agent capabilities mockup: the app's shell (the design system's
 * Sidebar and SettingsLayout, as apps/web lays them out) with each role's
 * Capabilities, the Agents → Capabilities matrix and the Network page, in
 * Acme's settings and in a project's; and a brainstorm session and a Run
 * showing what their agent may do. A bar above switches scope, view, theme
 * and density; it is not part of the proposal. URL parameters set the
 * same (?scope=control-plane&page=implementer&theme=light&density=compact).
 */

import { StrictMode, useState } from "react";
import { createRoot } from "react-dom/client";
import "@dude/design-system/tokens.css";
import "@dude/design-system/base.css";
import "../../src/app.css";
import { ThemeProvider, useTheme, type ThemePreference } from "@dude/design-system";
import { AgentAvatar, ProjectAvatar, Segmented, SettingsLayout, SettingsNote, Sidebar, SidebarLink, SidebarProfile, SidebarToggle, type SettingsNavItem } from "@dude/design-system/components";
import { EmptyState, ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import { navProjects } from "../../../../packages/design-system/src/gallery/navFixtures.ts";
import { CapabilitiesMatrix, NetworkPage, RolePage, type Store, type Where } from "./SettingsPages.tsx";
import { RunScreen, SessionScreen } from "./LiveScreens.tsx";
import { ORG, P, PROJECTS, PROJECT_OVERRIDES, ROLES, ROLE_LABEL, faceOf, resolve, type Overrides, type ProjectName, type Role } from "./data.ts";

type Scope = "org" | ProjectName;
type Page = Role | "capabilities" | "network" | "images" | "session" | "run";

const params = new URLSearchParams(location.search);
const isRole = (p: string): p is Role => (ROLES as readonly string[]).includes(p);

function Mock() {
  const theme = useTheme();
  const [scope, setScope] = useState<Scope>(() => (PROJECTS as readonly string[]).includes(params.get("scope") ?? "") ? (params.get("scope") as ProjectName) : "org");
  const [page, setPage] = useState<Page>(() => (params.get("page") as Page | null) ?? "brainstorm");
  const [overrides, setOverrides] = useState<Record<ProjectName, Overrides>>(() => structuredClone(PROJECT_OVERRIDES));
  const [navOpen, setNavOpen] = useState(false);
  const store: Store = {
    overrides,
    setOverride: (project, role, change) => setOverrides((o) => ({ ...o, [project]: { ...o[project], [role]: change ?? undefined } })),
  };
  const where: Where = scope === "org" ? { kind: "org" } : { kind: "project", project: scope };
  const own = scope === "org" ? null : overrides[scope];
  const changed = (r: Role) => {
    if (!own) return false;
    const c = resolve(r, own);
    return [c.internet, c.webSearch, c.containers, ...Object.values(c.acts)].some((l) => l?.source === "project");
  };
  const beyond = (r: Role) => {
    if (!own) return false;
    const c = resolve(r, own);
    return [c.internet, c.webSearch, c.containers, ...Object.values(c.acts)].some((l) => l?.wider);
  };
  const changedCount = ROLES.filter(changed).length;
  const agentsNav: SettingsNavItem = {
    id: "agents",
    label: "Agents",
    icon: "agent",
    note: own && changedCount ? `${changedCount} changed` : undefined,
    items: [
      { id: "capabilities", label: "Capabilities", icon: "list-check" },
      ...ROLES.map((r) => ({
        id: r,
        label: ROLE_LABEL[r],
        leading: <AgentAvatar role={faceOf(r)} size="sm" />,
        note: beyond(r) ? `beyond ${ORG}` : changed(r) ? "changed" : undefined,
      })),
    ],
  };
  const items: SettingsNavItem[] = scope === "org"
    ? [
        { id: "members", label: "Members", icon: "human" },
        { id: "github", label: "GitHub", icon: "git-branch" },
        { id: "general", label: "General", icon: "settings" },
        agentsNav,
        { id: "network", label: "Network", icon: "globe" },
        { id: "models", label: "Models", icon: "sparkle" },
        { id: "machines", label: "Machines", icon: "chip" },
        { id: "images", label: "Images", icon: "cube" },
        { id: "delivery", label: "Delivery", icon: "list-check" },
      ]
    : [
        { id: "general", label: "General", icon: "settings" },
        { id: "repositories", label: "Repositories", icon: "git-branch" },
        agentsNav,
        { id: "network", label: "Network", icon: "globe" },
        { id: "delivery", label: "Delivery", icon: "list-check" },
      ];
  const go = (p: Page) => { setPage(p); setNavOpen(false); };
  const live = page === "session" || page === "run";

  let body;
  if (isRole(page)) body = <RolePage key={`${scope}-${page}`} role={page} where={where} store={store} onImages={() => go("images")} onCapabilities={() => go("capabilities")} />;
  else if (page === "capabilities") body = <CapabilitiesMatrix where={where} store={store} onRole={go} />;
  else if (page === "network") body = <NetworkPage where={where} store={store} onRole={go} />;
  else body = <EmptyState icon="cube" title="Images" description="Not part of this mockup: here an admin marks an image “can run containers”." />;

  return (
    <div style={{ display: "flex", flexDirection: "column", height: "100%" }}>
      <div className="mockbar">
        <b>dude · Agent capabilities (mockup)</b>
        <Segmented size="sm" label="View" value={live ? page : "settings"} onChange={(v) => go(v === "settings" ? "brainstorm" : v)}
          options={[{ value: "settings", label: "Settings" }, { value: "session", label: "A session" }, { value: "run", label: "A Run" }]} />
        <Segmented size="sm" label="Scope" value={scope} onChange={(s) => { setScope(s); if (live) go("capabilities"); }}
          options={[{ value: "org", label: ORG }, { value: "control-plane", label: "control-plane" }, { value: "billing", label: "billing" }]} />
        <Segmented size="sm" label="Theme" value={theme.resolved} onChange={(t) => theme.setPreference(t as ThemePreference)} options={[{ value: "dark", label: "Dark" }, { value: "light", label: "Light" }]} />
        <Segmented size="sm" label="Density" value={theme.density} onChange={theme.setDensity} options={[{ value: "comfortable", label: "Comfortable" }, { value: "compact", label: "Compact" }]} />
      </div>
      <div className="mocknote">
        Capabilities sit on each agent’s page, under its image; Agents → Capabilities is every role at once. A project may go narrower or wider than {ORG}: wider is marked “Beyond {ORG}”. Try control-plane’s Implementer, or turn a switch.
      </div>
      <div className="shell" style={{ flex: 1, minHeight: 0 }}>
        <Sidebar
          projects={navProjects}
          title="dude"
          collapsible
          open={navOpen}
          onOpenChange={setNavOpen}
          footer={
            <>
              <SidebarLink icon="building" current={!live && scope === "org"} onClick={() => { setScope("org"); go("capabilities"); }}>Organisation settings</SidebarLink>
              <SidebarProfile person={P["marcio"]!} detail="marcio@acme.dev" onOpen={() => undefined} />
            </>
          }
        />
        <main className={live ? "main flush" : "main"}>
          <SidebarToggle open={navOpen} onOpenChange={setNavOpen} size="sm" />
          {page === "session" ? <SessionScreen /> : page === "run" ? <RunScreen /> : (
            <SettingsLayout
              scope={scope === "org"
                ? { title: ORG, subtitle: "Organisation settings", leading: <AgentAvatar role="conductor" size="lg" /> }
                : { title: scope, subtitle: "Project settings", leading: <ProjectAvatar project={{ id: `p_${scope}`, name: scope }} size={32} /> }}
              items={items}
              current={page}
              onSelect={(id) => (isRole(id) || id === "capabilities" || id === "network" || id === "images") && go(id as Page)}
            >
              {page === "capabilities" ? null : (
                <SettingsNote icon="info">
                  {scope === "org"
                    ? `Only Acme’s admins change these. Each project follows them unless it sets its own.`
                    : `Values set here apply to ${scope} only, narrower or wider than ${ORG}’s; the rest are ${ORG}’s, marked “From ${ORG}”.`}
                </SettingsNote>
              )}
              {body}
            </SettingsLayout>
          )}
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
