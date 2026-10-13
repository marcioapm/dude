/*
 * Accounts across organisations, as a mockup: first sign-in, no
 * organisation, the organisation switcher in the profile band, your
 * settings with keys by organisation, an organisation's members with the
 * email-chip invite and its links, the Invitations page, the accept page of
 * an invitation link in every state, and the super-admin's organisations.
 * The bar above switches screen, variant, sidebar, theme and density; it is
 * not part of the proposal. Every choice is also a URL parameter, for the
 * screenshots: ?screen=switcher&rail=1&menu=1.
 */

import { StrictMode, useEffect, useState } from "react";
import { createRoot } from "react-dom/client";
import "@dude/design-system/tokens.css";
import "@dude/design-system/base.css";
import "../../src/app.css";
import { ThemeProvider, useTheme, type ThemePreference } from "@dude/design-system";
import { Segmented } from "@dude/design-system/components";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import { ACME, CHLOE_INVITES, HOST, MEMBERSHIPS, MY_INVITATIONS, NORTHWIND, P, type Org } from "./data.ts";
import {
  AcceptPage, AdminPage, FirstOne, FirstTwo, InvitationsPage, MembersPage, NoOrg, OrgWelcome, YourSettings,
  type AcceptState, type AdminView, type InvitationsView, type MembersDialog,
} from "./Pages.tsx";
import { Shell, type Membership, type SwitchProps } from "./Shell.tsx";

type Screen = "first" | "none" | "switcher" | "settings" | "members" | "invitations" | "accept" | "admin";

const VARIANTS: Record<Screen, readonly { value: string; label: string }[]> = {
  first: [{ value: "one", label: "Joined from a link" }, { value: "two", label: "Two links waiting" }],
  none: [{ value: "plain", label: "No invitation" }, { value: "expired", label: "Expired invitation" }],
  switcher: [{ value: "before", label: "In Acme" }, { value: "after", label: "After switching" }],
  settings: [{ value: "keys", label: "Profile and keys" }, { value: "reveal", label: "New key, shown once" }],
  members: [{ value: "list", label: "Members" }, { value: "invite", label: "Invite: chips" }, { value: "links", label: "Invite: links" }, { value: "remove", label: "Remove" }],
  invitations: [{ value: "pending", label: "Pending" }, { value: "all", label: "All" }, { value: "search", label: "Search" }, { value: "revoke", label: "Revoke" }],
  accept: [
    { value: "join", label: "Join" }, { value: "first", label: "First sign-in" }, { value: "wrong", label: "Other email" },
    { value: "member", label: "Already in" }, { value: "invalid", label: "Can't be used" },
  ],
  admin: [{ value: "list", label: "Organisations" }, { value: "create", label: "Create" }, { value: "org", label: "One organisation" }],
};

const SCREENS: readonly { value: Screen; label: string }[] = [
  { value: "first", label: "1 First sign-in" },
  { value: "none", label: "2 No organisation" },
  { value: "switcher", label: "3 Switcher" },
  { value: "settings", label: "4 Your settings" },
  { value: "members", label: "5 Members" },
  { value: "invitations", label: "6 Invitations" },
  { value: "accept", label: "7 Accept" },
  { value: "admin", label: "8 Super-admin" },
];

const NOTES: Record<Screen, string> = {
  first: "Chloé signs in for the first time, in no organisation, holding invitations: dude lets her in only to answer them. From Gus's link she joins Northwind and its welcome opens; at the bare address, every invitation waiting for her email is listed.",
  none: "Access proved who they are, but they are in no organisation, hold no pending invitation and are not a super-admin. dude lets them in nowhere and records nothing: who to ask, Sign out.",
  switcher: "Your face at the bottom of the sidebar opens the menu (click, or Tab to it and press Enter; arrows move, Escape closes). Each organisation shows what waits on you there. Pick Northwind: the URL, the sidebar and Waiting on you change.",
  settings: "Your profile is the same in every organisation; email comes from Access. Keys are grouped by organisation: New key for Acme.",
  members: "People join by invitation only. Invite people: type or paste emails; each chip says what will happen to it. Invite gives a link per person to send them yourself.",
  invitations: "Every invitation and what became of it. Filtering by status and search run on the server, so a pending one never hides past a page.",
  accept: "What #/invite/<code> shows. A link works only for the email it was made for, which Cloudflare Access has proved.",
  admin: "Outside every organisation: Márcio's email is in auth.super_admins. Reached from your menu's All organisations, or #/admin; a super-admin in no organisation lands here.",
};

const params = new URLSearchParams(location.search);
const param = <T extends string>(k: string, ok: readonly T[], d: T): T => (ok.includes(params.get(k) as T) ? (params.get(k) as T) : d);

function Mock() {
  const theme = useTheme();
  const [screen, setScreen] = useState<Screen>(() => param("screen", SCREENS.map((x) => x.value), "first"));
  const [variant, setVariant] = useState<string>(() => param("v", VARIANTS[screen].map((x) => x.value), VARIANTS[screen][0]!.value));
  const [collapsed, setCollapsed] = useState(params.get("rail") === "1");
  const [menu, setMenu] = useState(params.get("menu") === "1");
  const [navOpen, setNavOpen] = useState(params.get("drawer") === "1");
  const [org, setOrg] = useState<Org>(variant === "after" ? NORTHWIND : ACME);
  // Chloé, after Join on a link or on the two-invitations page; null: still deciding.
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
  const pick = (v: string) => {
    setVariant(v);
    setJoined(null);
    setMenu(false);
    if (screen === "switcher") setOrg(v === "after" ? NORTHWIND : ACME);
  };

  // Chloé on the first-sign-in and accept screens, Márcio everywhere else.
  // Chloé is in no organisation until she joins one; "Joined from a link" starts in Northwind.
  const chloe = screen === "first" || screen === "accept";
  const gus = CHLOE_INVITES[0]!;
  const chloeIn = joined ?? (screen === "first" && variant === "one" ? NORTHWIND : null);
  const chloeOrgs: Membership[] = chloeIn ? [{ org: chloeIn, role: "member", waiting: 0 }] : [];
  const chloeCurrent = chloeIn ?? NORTHWIND;
  const sw: SwitchProps = {
    current: chloe ? chloeCurrent : org,
    onSwitch: (o) => {
      setOrg(o);
      setMenu(false);
      if (screen === "switcher") setVariant(o.id === NORTHWIND.id ? "after" : "before");
    },
    open: menu,
    onOpenChange: setMenu,
    onAdmin: () => go("admin"),
    onSettings: () => go("settings"),
    onJoin: () => go("accept", "join"),
    memberships: chloe ? chloeOrgs : MEMBERSHIPS,
    invitations: chloe ? CHLOE_INVITES.filter((i) => i.org.id !== chloeIn?.id) : MY_INVITATIONS,
    superAdmin: !chloe,
  };

  const acceptOutside = screen === "accept" && !joined;
  const outside = (screen === "first" && variant === "two" && !joined) || screen === "none" || screen === "admin" || acceptOutside;
  const current = sw.current;
  let hash: string;
  if (screen === "none") hash = "#/";
  else if (screen === "admin") hash = variant === "list" || variant === "create" ? "#/admin" : "#/admin/o/northwind";
  else if (acceptOutside) hash = `#/invite/${variant === "invalid" ? "Zr0bQ9xW1eC4vT7yN2mK5p" : gus.code}`;
  else if (screen === "first" && variant === "two" && !joined) hash = "#/invitations";
  else if (screen === "settings") hash = "#/me/settings";
  else if (screen === "members") hash = `#/o/${current.slug}/settings/members`;
  else if (screen === "invitations") hash = `#/o/${current.slug}/settings/invitations`;
  else if (chloe) hash = `#/o/${current.slug}/welcome`;
  else hash = `#/o/${current.slug}/`;

  const orgPage = (id: string) => (id === "members" ? go("members") : id === "invitations" ? go("invitations") : undefined);

  let body;
  if (screen === "none") body = <NoOrg expired={variant === "expired"} />;
  else if (screen === "admin") body = <AdminPage view={variant as AdminView} onView={(v) => setVariant(v)} onBack={() => go("switcher")} />;
  else if (acceptOutside) body = <AcceptPage key={variant} state={variant as AcceptState} onJoin={(o) => setJoined(o)} onOpen={(o) => setJoined(o)} />;
  else if (screen === "first" && variant === "two" && !joined) body = <FirstTwo onJoin={(o) => setJoined(o)} />;

  const shellChild = chloe
    ? chloeCurrent.id === NORTHWIND.id && !(screen === "accept" && variant === "member")
      ? <FirstOne invitation={gus} onStart={() => go("switcher")} />
      : <OrgWelcome org={chloeCurrent} greeting="Afternoon, Chloé" />
    : screen === "settings" ? <YourSettings reveal={variant === "reveal" ? ACME : null} onReveal={(o) => setVariant(o ? "reveal" : "keys")} />
    : screen === "members" ? <MembersPage dialog={variant === "list" ? "none" : variant as MembersDialog} onDialog={(d) => setVariant(d === "none" ? "list" : d)} onPage={orgPage} />
    : screen === "invitations" ? <InvitationsPage key={variant === "revoke" ? "pending" : variant} view={variant as InvitationsView} onView={(v) => setVariant(v)} onPage={orgPage} />
    : <OrgWelcome org={org} />;

  return (
    <div style={{ display: "flex", flexDirection: "column", height: "100%" }}>
      <div className="mockbar">
        <b>dude · Accounts across organisations (mockup)</b>
        <Segmented size="sm" label="Screen" value={screen} onChange={(v) => go(v)} options={SCREENS} />
        <Segmented size="sm" label="Variant" value={variant} onChange={pick} options={VARIANTS[screen]} />
        <Segmented size="sm" label="Sidebar" value={collapsed ? "rail" : "full"} onChange={(v) => setCollapsed(v === "rail")}
          options={[{ value: "full", label: "Expanded" }, { value: "rail", label: "Collapsed" }]} />
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
        <Shell org={current} you={chloe ? P.chloe : P.marcio} collapsed={collapsed}
          onCollapsedChange={setCollapsed} navOpen={navOpen} onNavOpenChange={setNavOpen} sw={sw}
          home={screen === "switcher"} onHome={() => go("switcher", org.id === NORTHWIND.id ? "after" : "before")}
          orgSettings={screen === "members" || screen === "invitations"} onOrgSettings={() => go("members")}
          sessions={chloe ? [] : current.id === ACME.id ? [{ id: "s1", title: "Usage-based billing" }, { id: "s2", title: "Q4 cleanup ideas" }] : [{ id: "n1", title: "Portal invoice VAT" }]}
          flush={screen === "first" || screen === "switcher" || screen === "accept"}>
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
