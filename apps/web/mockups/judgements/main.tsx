/*
 * The judgements mockup: organisation settings with the judgement tier in
 * two places — A, a section on the Models page; B, a Judgements page of its
 * own — each in four states. The bar above switches them; it is not part of
 * the proposal. Every choice is a URL parameter too, for the screenshots:
 * ?place=b&v=unset&detail=jdg_01&theme=light.
 */

import { StrictMode, useState } from "react";
import { createRoot } from "react-dom/client";
import "@dude/design-system/tokens.css";
import "@dude/design-system/base.css";
import "../../src/app.css";
import { ThemeProvider, useTheme, type ThemePreference } from "@dude/design-system";
import { AgentAvatar, Segmented, SettingsLayout, Sidebar, SidebarLink, SidebarProfile, type SettingsNavItem } from "@dude/design-system/components";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import { navProjects } from "../../../../packages/design-system/src/gallery/navFixtures.ts";
import { JudgementsPage, ModelsWithJudgements, OrgNote, type Variant } from "./JudgementsPage.tsx";
import { JUDGEMENTS, ORG, P } from "./data.ts";

type Place = "a" | "b";
const params = new URLSearchParams(location.search);
const pick = <T extends string>(k: string, ok: readonly T[], d: T): T => (ok.includes(params.get(k) as T) ? (params.get(k) as T) : d);

const VARIANTS: readonly { value: Variant; label: string }[] = [
  { value: "set", label: "Set, with history" },
  { value: "test", label: "Test question" },
  { value: "unset", label: "Tier names no model" },
  { value: "empty", label: "Nothing asked yet" },
];

const NOTES: Record<Place, string> = {
  a: "A — on the Models page, under the tiers: Judgements is one more user of a tier, so the Used by column says so. Least new surface; the log sits on a page about models.",
  b: "B — a Judgements page of its own, between Models and Machines: room to explain what a judgement is, and where per-kind settings (thresholds, a tier per kind) would go later.",
};

function Mock() {
  const theme = useTheme();
  const [place, setPlace] = useState<Place>(() => pick("place", ["a", "b"], "b"));
  const [variant, setVariant] = useState<Variant>(() => pick("v", VARIANTS.map((v) => v.value), "set"));
  const detail = JUDGEMENTS.find((j) => j.id === params.get("detail")) ?? null;
  const items: SettingsNavItem[] = [
    { id: "members", label: "Members", icon: "human" },
    { id: "general", label: "General", icon: "settings" },
    { id: "github", label: "GitHub", icon: "git-branch" },
    { id: "agents", label: "Agents", icon: "agent" },
    { id: "network", label: "Network", icon: "globe" },
    { id: "models", label: "Models", icon: "sparkle", note: 3 },
    ...(place === "b" ? [{ id: "judgements", label: "Judgements", icon: "list-check" as const, note: variant === "unset" ? "no model" : undefined }] : []),
    { id: "machines", label: "Machines", icon: "chip", note: 4 },
    { id: "images", label: "Images", icon: "cube" },
    { id: "delivery", label: "Delivery", icon: "list-check" },
    { id: "memory", label: "Memory", icon: "memory" },
  ];
  return (
    <div style={{ display: "flex", flexDirection: "column", height: "100%" }}>
      <div className="mockbar">
        <b>dude · Judgements (mockup)</b>
        <Segmented size="sm" label="Where" value={place} onChange={setPlace}
          options={[{ value: "a", label: "A · In Models" }, { value: "b", label: "B · Own page" }]} />
        <Segmented size="sm" label="State" value={variant} onChange={setVariant} options={VARIANTS} />
        <Segmented size="sm" label="Theme" value={theme.resolved} onChange={(t) => theme.setPreference(t as ThemePreference)}
          options={[{ value: "dark", label: "Dark" }, { value: "light", label: "Light" }]} />
      </div>
      <div className="mocknote"><span>{NOTES[place]} Click a row in Recent judgements to see the call.</span></div>
      <div className="shell" style={{ flex: 1, minHeight: 0 }}>
        <Sidebar projects={navProjects} title="dude"
          footer={<>
            <SidebarLink icon="building" current onClick={() => undefined}>Organisation settings</SidebarLink>
            <SidebarProfile person={P["marcio"]!} detail="marcio@acme.dev" onOpen={() => undefined} />
          </>} />
        <main className="main">
          <SettingsLayout key={place}
            scope={{ title: ORG, subtitle: "Organisation settings", leading: <AgentAvatar role="conductor" size="lg" /> }}
            items={items} current={place === "a" ? "models" : "judgements"}
            onSelect={(id) => (id === "models" ? setPlace("a") : id === "judgements" ? setPlace("b") : undefined)}>
            {place === "b" ? <OrgNote /> : null}
            {place === "a"
              ? <ModelsWithJudgements key={variant} variant={variant} onVariant={setVariant} detail={detail} />
              : <JudgementsPage key={variant} variant={variant} onVariant={setVariant} onManage={() => setPlace("a")} detail={detail} />}
          </SettingsLayout>
        </main>
      </div>
    </div>
  );
}

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <ThemeProvider defaultPreference={(params.get("theme") as ThemePreference | null) ?? "dark"} storageKey={null} densityStorageKey={null}>
      <TooltipProvider>
        <ToastProvider>
          <Mock />
        </ToastProvider>
      </TooltipProvider>
    </ThemeProvider>
  </StrictMode>,
);
