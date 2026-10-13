/*
 * Accounts across organisations, as a mockup: first sign-in (one
 * invitation, two), no organisation, the organisation switcher in its two
 * places, your settings with keys by organisation, an organisation's
 * members with invitations, and the super-admin's organisations. The bar
 * above switches screen, variant, switcher place, sidebar, theme and
 * density; it is not part of the proposal. Every choice is also a URL
 * parameter, for the screenshots: ?screen=switcher&place=B&rail=1&menu=1.
 */

import { StrictMode, useEffect, useState } from "react";
import { createRoot } from "react-dom/client";
import "@dude/design-system/tokens.css";
import "@dude/design-system/base.css";
import "../../src/app.css";
import { ThemeProvider, useTheme, type ThemePreference } from "@dude/design-system";
import { Segmented } from "@dude/design-system/components";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import { ACME, CHLOE_EMAIL, HOST, MEMBERSHIPS, MY_INVITATIONS, NORTHWIND, P, type Org } from "./data.ts";
import { AdminPage, FirstOne, FirstTwo, MembersPage, NoOrg, OrgWelcome, YourSettings } from "./Pages.tsx";
import { Shell, type SwitcherPlace, type SwitchProps } from "./Shell.tsx";

type Screen = "first" | "none" | "switcher" | "settings" | "members" | "admin";

const VARIANTS: Record<Screen, readonly { value: string; label: string }[]> = {
  first: [{ value: "one", label: "One invitation" }, { value: "two", label: "Two invitations" }],
  none: [{ value: "plain", label: "No invitation" }, { value: "expired", label: "Expired invitation" }],
  switcher: [{ value: "before", label: "In Acme" }, { value: "after", label: "After switching" }],
  settings: [{ value: "keys", label: "Profile and keys" }, { value: "reveal", label: "New key, shown once" }],
  members: [{ value: "list", label: "Members" }, { value: "invite", label: "Invite (bulk paste)" }, { value: "remove", label: "Remove" }],
  admin: [{ value: "list", label: "Organisations" }, { value: "create", label: "Create" }, { value: "org", label: "One organisation" }],
};

const SCREENS: readonly { value: Screen; label: string }[] = [
  { value: "first", label: "1 First sign-in" },
  { value: "none", label: "2 No organisation" },
  { value: "switcher", label: "3 Switcher" },
  { value: "settings", label: "4 Your settings" },
  { value: "members", label: "5 Members" },
  { value: "admin", label: "6 Super-admin" },
];

const NOTES: Record<Screen, string> = {
  first: "Chloé signs in with Cloudflare Access for the first time. One invitation: signing in accepts it and she lands in Acme. Two: she chooses.",
  none: "Access let them in, but no organisation on this dude has their email. No key prompt: an honest page, who to ask, Sign out.",
  switcher: "Open the switcher (click, or Tab to it and press Enter; arrows move, Escape closes). Pick Northwind: the URL, the sidebar and Waiting on you change; another tab stays where it was.",
  settings: "Your profile is the same in every organisation; email comes from Access. Keys are grouped by organisation: New key for Acme.",
  members: "Invite by email, many at once; no secret is shown. Pending invitations expire, can be resent or revoked.",
  admin: "Outside every organisation: Márcio's email is in auth.super_admins. Reached from the switcher's All organisations, or #/admin.",
};

const params = new URLSearchParams(location.search);
const param = <T extends string>(k: string, ok: readonly T[], d: T): T => (ok.includes(params.get(k) as T) ? (params.get(k) as T) : d);

function Mock() {
  const theme = useTheme();
  const [screen, setScreen] = useState<Screen>(() => param("screen", SCREENS.map((x) => x.value), "first"));
  const [variant, setVariant] = useState<string>(() => params.get("v") ?? VARIANTS[screen][0]!.value);
  const [place, setPlace] = useState<SwitcherPlace>(() => param("place", ["A", "B"] as const, "A"));
  const [collapsed, setCollapsed] = useState(params.get("rail") === "1");
  const [menu, setMenu] = useState(params.get("menu") === "1");
  const [counts, setCounts] = useState(params.get("counts") !== "0");
  const [navOpen, setNavOpen] = useState(params.get("drawer") === "1");
  const [org, setOrg] = useState<Org>(variant === "after" ? NORTHWIND : ACME);
  // Chloé, after joining from the two-invitations page.
  const [joined, setJoined] = useState<Org | null>(null);

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

  const go = (sc: Screen, v?: string) => {
    setScreen(sc);
    const nv = v ?? VARIANTS[sc][0]!.value;
    setVariant(nv);
    setMenu(false);
    setJoined(null);
    setOrg(sc === "switcher" && nv === "after" ? NORTHWIND : ACME);
  };

  const chloe = screen === "first";
  const sw: SwitchProps = {
    current: chloe ? (joined ?? ACME) : org,
    onSwitch: (o) => {
      setOrg(o);
      setMenu(false);
      if (screen === "switcher") setVariant(o.id === NORTHWIND.id ? "after" : "before");
    },
    open: menu,
    onOpenChange: setMenu,
    counts,
    onAdmin: () => go("admin"),
    onSettings: () => go("settings"),
    memberships: chloe ? [{ org: joined ?? ACME, role: "member", waiting: 0 }] : MEMBERSHIPS,
    invitations: chloe ? [] : MY_INVITATIONS,
    superAdmin: !chloe,
  };

  const outside = (screen === "first" && variant === "two" && !joined) || screen === "none" || screen === "admin";
  const current = sw.current;
  let hash: string;
  if (screen === "none") hash = "#/";
  else if (screen === "admin") hash = variant === "org" ? "#/admin/o/northwind" : "#/admin";
  else if (screen === "first" && variant === "two" && !joined) hash = "#/invitations";
  else if (screen === "settings") hash = "#/me/settings";
  else if (screen === "members") hash = `#/o/${current.slug}/settings/members`;
  else if (screen === "first") hash = `#/o/${current.slug}/welcome`;
  else hash = `#/o/${current.slug}/`;

  let body;
  if (screen === "none") body = <NoOrg expired={variant === "expired"} />;
  else if (screen === "admin") body = <AdminPage view={variant as "list" | "create" | "org"} onView={(v) => setVariant(v)} onBack={() => go("switcher")} />;
  else if (screen === "first" && variant === "two" && !joined) body = <FirstTwo onJoin={(o) => setJoined(o)} />;

  const shellChild = screen === "first"
    ? (joined && joined.id !== ACME.id ? <OrgWelcome org={joined} /> : <FirstOne onStart={() => go("switcher")} />)
    : screen === "settings" ? <YourSettings reveal={variant === "reveal" ? ACME : null} onReveal={(o) => setVariant(o ? "reveal" : "keys")} />
    : screen === "members" ? <MembersPage dialog={variant === "invite" ? "invite" : variant === "remove" ? "remove" : "none"} onDialog={(d) => setVariant(d === "none" ? "list" : d)} />
    : <OrgWelcome org={org} />;

  return (
    <div style={{ display: "flex", flexDirection: "column", height: "100%" }}>
      <div className="mockbar">
        <b>dude · Accounts across organisations (mockup)</b>
        <Segmented size="sm" label="Screen" value={screen} onChange={(v) => go(v)} options={SCREENS} />
        <Segmented size="sm" label="Variant" value={variant} onChange={(v) => {
          setVariant(v);
          setJoined(null);
          if (screen === "switcher") setOrg(v === "after" ? NORTHWIND : ACME);
        }} options={VARIANTS[screen]} />
        <Segmented size="sm" label="Switcher" value={place} onChange={setPlace}
          options={[{ value: "A", label: "A · Sidebar top" }, { value: "B", label: "B · Profile band" }]} />
        <Segmented size="sm" label="Sidebar" value={collapsed ? "rail" : "full"} onChange={(v) => setCollapsed(v === "rail")}
          options={[{ value: "full", label: "Expanded" }, { value: "rail", label: "Collapsed" }]} />
        <Segmented size="sm" label="Counts in menu" value={counts ? "on" : "off"} onChange={(v) => setCounts(v === "on")}
          options={[{ value: "on", label: "Counts" }, { value: "off", label: "No counts" }]} />
        <Segmented size="sm" label="Density" value={theme.density} onChange={(d) => theme.setDensity(d)}
          options={[{ value: "comfortable", label: "Comfortable" }, { value: "compact", label: "Compact" }]} />
        <Segmented size="sm" label="Theme" value={theme.resolved} onChange={(t) => theme.setPreference(t as ThemePreference)}
          options={[{ value: "dark", label: "Dark" }, { value: "light", label: "Light" }]} />
      </div>
      <div className="mocknote">
        <span className="mockurl" data-testid="mock-url">{HOST}/{hash}</span>
        <span>{NOTES[screen]}</span>
      </div>
      {outside ? <div style={{ flex: 1, minHeight: 0, display: "flex" }}>{body}</div> : (
        <Shell org={current} you={chloe ? P.chloe : P.marcio} email={chloe ? CHLOE_EMAIL : "marcio@acme.dev"} place={place} collapsed={collapsed}
          onCollapsedChange={setCollapsed} navOpen={navOpen} onNavOpenChange={setNavOpen} sw={sw}
          home={screen === "switcher"} onHome={() => go("switcher", org.id === NORTHWIND.id ? "after" : "before")}
          orgSettings={screen === "members"} onOrgSettings={() => go("members")}
          sessions={chloe ? [] : current.id === ACME.id ? [{ id: "s1", title: "Usage-based billing" }, { id: "s2", title: "Q4 cleanup ideas" }] : [{ id: "n1", title: "Portal invoice VAT" }]}
          flush={screen === "first" || screen === "switcher"}>
          {shellChild}
        </Shell>
      )}
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
