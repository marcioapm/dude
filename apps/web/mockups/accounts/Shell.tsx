/*
 * The app shell for this mockup: the design system's Sidebar (drawer under
 * 1000px), a rail drawn here when collapsed (the welcome mockup's rail
 * styles, with the switcher in it), and the organisation switcher in one of
 * its two proposed places:
 *
 *   A  the sidebar's top, where "El Duderino" is: dude's face stays the way
 *      home, the organisation's name beside it opens the menu.
 *   B  the profile band: your face, your name and the organisation under it
 *      open one menu for you (organisations, your settings, sign out).
 *
 * The menu is RowMenu (Radix: arrows, Enter, Escape, the current one
 * ticked). Its radio items take no face, so each organisation's role is its
 * description; docs/design/accounts.md names the OrgSwitcher component.
 */

import { forwardRef, useEffect, useState, type ButtonHTMLAttributes, type ReactNode } from "react";
import { NeedsYouCount, PersonAvatar, ProjectAvatar, Sidebar, SidebarLink, SidebarSessions, SidebarToggle } from "@dude/design-system/components";
import { Icon } from "@dude/design-system";
import { IconButton, RowMenu, Tooltip, type RowMenuItem } from "@dude/design-system/primitives";
import type { Person } from "../../../../packages/design-system/src/components/PersonAvatar.tsx";
import { globalCounts, projectCounts } from "../../../../packages/design-system/src/util/navModel.ts";
import rail from "../welcome/rail.module.css";
import dudeSvg from "../../public/dude.svg?url";
import dudeOutlinedSvg from "../../public/dude-outlined.svg?url";
import type { MY_INVITATIONS, Org, Role } from "./data.ts";
import s from "./accounts.module.css";

export type SwitcherPlace = "A" | "B";

export function Mark({ size }: { size: number | "fill" }) {
  const box = size === "fill" ? { width: "100%", height: "100%" } : { width: size, height: size };
  return (
    <span className="dudeMark" style={box} aria-hidden="true">
      <img className="dudeMarkLight" src={dudeSvg} alt="" style={box} />
      <img className="dudeMarkDark" src={dudeOutlinedSvg} alt="" style={box} />
    </span>
  );
}

/** An organisation's face: initials on an identity colour, as a project's (organisations have no image yet). */
export function OrgFace({ org, size = 24 }: { org: Org; size?: number }) {
  return <ProjectAvatar project={{ id: org.id, name: org.name, colorSlot: org.colorSlot }} size={size} />;
}

export const roleWord = (r: Role) => (r === "admin" ? "Admin" : "Member");

export interface SwitchProps {
  current: Org;
  onSwitch: (org: Org) => void;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** Each organisation's waiting count beside it (open question 4). */
  counts: boolean;
  onAdmin: () => void;
  onSettings: () => void;
  memberships: readonly Membership[];
  invitations: typeof MY_INVITATIONS;
  /** A super-admin's menu ends with All organisations. */
  superAdmin: boolean;
}

export interface Membership { readonly org: Org; readonly role: Role; readonly waiting: number }

function menuItems(p: SwitchProps, place: SwitcherPlace): RowMenuItem[] {
  const items: RowMenuItem[] = [
    {
      kind: "radio", id: "orgs", label: "Your organisations", value: p.current.id,
      onValueChange: (id) => { const m = p.memberships.find((x) => x.org.id === id); if (m) p.onSwitch(m.org); },
      items: p.memberships.map((m) => ({
        id: m.org.id, label: m.org.name,
        description: p.counts && m.waiting ? `${roleWord(m.role)} · ${m.waiting} waiting on you` : roleWord(m.role),
      })),
    },
    { kind: "separator" },
    ...p.invitations.map((i): RowMenuItem => ({
      id: i.id, label: `Join ${i.org.name}`, leading: <OrgFace org={i.org} size={16} />,
      description: `Invited by ${i.by.name} · expires ${i.expires}`,
    })),
    ...(p.invitations.length ? [{ kind: "separator" } as const] : []),
  ];
  if (place === "B") items.push({ id: "settings", label: "Your settings", icon: "settings", onSelect: p.onSettings });
  if (p.superAdmin) items.push({ id: "admin", label: "All organisations", icon: "building", description: "Super-admin: this dude's organisations", onSelect: p.onAdmin });
  if (place === "B") items.push({ id: "signout", label: "Sign out", icon: "arrow-right" });
  while (items.at(-1)?.kind === "separator") items.pop();
  return items;
}

/** A plain button that forwards its ref: RowMenu's trigger slot needs one. */
const Trigger = forwardRef<HTMLButtonElement, ButtonHTMLAttributes<HTMLButtonElement>>(function Trigger(props, ref) {
  return <button ref={ref} type="button" {...props} />;
});

/** A: the organisation's name in the sidebar's top, beside dude's face. */
export function SwitcherTop(p: SwitchProps & { onHome: () => void }) {
  const m = p.memberships.find((x) => x.org.id === p.current.id);
  return (
    <span className={s.top}>
      <button type="button" className="brandHome" aria-label="Home" onClick={p.onHome}><Mark size={30} /></button>
      <RowMenu items={menuItems(p, "A")} open={p.open} onOpenChange={p.onOpenChange} align="start" label="Switch organisation"
        trigger={
          <Trigger className={s.orgTrigger} aria-label={`${p.current.name}, switch organisation`} data-testid="switcher">
            <span className={s.orgText}>
              <span className={s.orgName}>{p.current.name}</span>
              {m ? <span className={s.orgRole}>{roleWord(m.role)}</span> : null}
            </span>
            <Icon name="chevron-down" size={14} className={s.chev} />
          </Trigger>
        } />
    </span>
  );
}

/** B: you, in the band, with the organisation under your name. */
export function SwitcherBand(p: SwitchProps & { you: Person; email: string }) {
  const m = p.memberships.find((x) => x.org.id === p.current.id);
  return (
    <RowMenu items={menuItems(p, "B")} open={p.open} onOpenChange={p.onOpenChange} align="start" side="top" label="You and your organisations"
      trigger={
        <Trigger className={s.bandTrigger} aria-label={`${p.you.name}, ${p.current.name}: switch organisation, your settings, sign out`} data-testid="switcher">
          <PersonAvatar person={p.you} size={40} aria-hidden title="" />
          <span className={s.orgText}>
            <span className={s.bandName}>{p.you.name}</span>
            <span className={s.bandOrg}><OrgFace org={p.current} size={16} />{p.current.name}{m ? ` · ${roleWord(m.role)}` : ""}</span>
          </span>
          <Icon name="sort" size={14} className={s.chev} />
        </Trigger>
      } />
  );
}

function RailButton({ label, current, onClick, children }: { label: ReactNode; current?: boolean; onClick?: () => void; children: ReactNode }) {
  return (
    <Tooltip content={label} side="right">
      <button type="button" className={rail.item} data-current={current ? "true" : undefined} aria-current={current ? "page" : undefined}
        aria-label={typeof label === "string" ? label : undefined} onClick={onClick}>
        {children}
      </button>
    </Tooltip>
  );
}

/** The sidebar folded to a rail, with the switcher where its place puts it. */
export function Rail({ place, org, you, home, onHome, onExpand, sw }: {
  place: SwitcherPlace;
  org: Org;
  you: Person;
  home: boolean;
  onHome: () => void;
  onExpand: () => void;
  sw: SwitchProps;
}) {
  const waiting = globalCounts(org.projects, you.id).needs_you;
  const railTrigger = (children: ReactNode, label: string) => (
    <RowMenu items={menuItems(sw, place)} open={sw.open} onOpenChange={sw.onOpenChange} side="right" align={place === "A" ? "start" : "end"} label={label}
      trigger={<Trigger className={rail.item} aria-label={label} data-testid="switcher">{children}</Trigger>} />
  );
  return (
    <nav className={rail.rail} aria-label="Navigation" data-testid="sidebar-rail">
      <div className={rail.top}>
        <RailButton label="Home" current={home} onClick={onHome}><Mark size={28} /></RailButton>
        {place === "A" ? railTrigger(<OrgFace org={org} size={24} />, `${org.name}: switch organisation`) : null}
        <RailButton label="Expand sidebar" onClick={onExpand}><Icon name="chevron-right" size={16} /></RailButton>
      </div>
      <div className={rail.group}>
        <RailButton label="Find work"><Icon name="search" size={16} /></RailButton>
        <RailButton label={waiting ? `Waiting on you · ${waiting}` : "Nothing waiting on you"}>
          {waiting ? <NeedsYouCount count={waiting} /> : <Icon name="inbox" size={16} />}
        </RailButton>
        <RailButton label="New session" onClick={onHome}><Icon name="plus" size={16} /></RailButton>
        <RailButton label="Sessions"><Icon name="brainstorm" size={16} /></RailButton>
      </div>
      <div className={rail.projects} aria-label="Projects">
        {org.projects.map((p) => (
          <RailButton key={p.id} label={p.name}>
            <ProjectAvatar project={{ id: p.id, name: p.name, imageUrl: p.imageUrl, colorSlot: p.colorSlot }} size={24} />
            {projectCounts(p, you.id).needs_you ? <span className={rail.mark} aria-hidden="true" /> : null}
          </RailButton>
        ))}
      </div>
      <div className={rail.band}>
        <RailButton label="Organisation settings"><Icon name="building" size={16} /></RailButton>
        {place === "B" ? railTrigger(<PersonAvatar person={you} size={28} aria-hidden title="" />, `${you.name}, ${org.name}: switch organisation`)
          : <RailButton label={`${you.name} · your settings`} onClick={sw.onSettings}><PersonAvatar person={you} size={28} /></RailButton>}
      </div>
    </nav>
  );
}

const WIDE = "(min-width: 1000px)";

function useWide(): boolean {
  const [wide, setWide] = useState(() => window.matchMedia(WIDE).matches);
  useEffect(() => {
    const q = window.matchMedia(WIDE);
    const follow = () => setWide(q.matches);
    q.addEventListener("change", follow);
    return () => q.removeEventListener("change", follow);
  }, []);
  return wide;
}

export interface ShellProps {
  org: Org;
  you: Person;
  email: string;
  place: SwitcherPlace;
  collapsed: boolean;
  onCollapsedChange: (c: boolean) => void;
  navOpen: boolean;
  onNavOpenChange: (o: boolean) => void;
  sw: SwitchProps;
  home: boolean;
  onHome: () => void;
  orgSettings?: boolean;
  onOrgSettings: () => void;
  sessions: readonly { id: string; title: string }[];
  flush?: boolean;
  children: ReactNode;
}

export function Shell(p: ShellProps) {
  // Under 1000px the rail is never shown, so the sidebar's switcher is the one in use.
  const wide = useWide();
  const railed = p.collapsed && wide;
  const sideSw: SwitchProps = railed ? { ...p.sw, open: false } : p.sw;
  const railSw: SwitchProps = railed ? p.sw : { ...p.sw, open: false };
  return (
    <div className="shell" style={{ flex: 1, minHeight: 0 }}>
      {p.collapsed ? (
        <div className="railSlot">
          <Rail place={p.place} org={p.org} you={p.you} home={p.home} onHome={p.onHome} onExpand={() => p.onCollapsedChange(false)} sw={railSw} />
        </div>
      ) : null}
      <div className={p.collapsed ? "sidebarSlot isCollapsed" : "sidebarSlot"}>
        <Sidebar
          projects={p.org.projects}
          you={p.you.id}
          title={p.place === "A" ? <SwitcherTop {...sideSw} onHome={p.onHome} /> : (
            <button type="button" className="brandHome" aria-label="El Duderino, home" onClick={p.onHome}>
              <span className="brand"><Mark size={30} />El Duderino</span>
            </button>
          )}
          headerActions={
            <Tooltip content="Collapse sidebar" shortcut="[" side="right">
              <IconButton icon="chevron-left" label="Collapse sidebar" size="sm" className="collapseButton" onClick={() => p.onCollapsedChange(true)} />
            </Tooltip>
          }
          online={[p.you]}
          onWaitingSelect={() => undefined}
          collapsible
          open={p.navOpen}
          onOpenChange={p.onNavOpenChange}
          sessions={<SidebarSessions sessions={p.sessions} onSelect={() => undefined} onNew={p.onHome} onOpenList={() => undefined} />}
          footer={
            <>
              <SidebarLink icon="building" current={p.orgSettings} onClick={p.onOrgSettings}>Organisation settings</SidebarLink>
              {p.place === "B" ? <SwitcherBand {...sideSw} you={p.you} email={p.email} /> : (
                <button type="button" className={s.bandTrigger} onClick={p.sw.onSettings} aria-label="Your settings">
                  <PersonAvatar person={p.you} size={40} aria-hidden title="" />
                  <span className={s.orgText}>
                    <span className={s.bandName}>{p.you.name}</span>
                    <span className={s.bandOrg}>{p.email}</span>
                  </span>
                  <Icon name="settings" size={14} className={s.chev} />
                </button>
              )}
            </>
          }
        />
      </div>
      <main className={p.flush ? "main flush" : "main"}>
        <SidebarToggle open={p.navOpen} onOpenChange={p.onNavOpenChange} size="sm" />
        {p.children}
      </main>
    </div>
  );
}
