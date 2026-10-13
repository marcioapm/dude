/*
 * The app shell for this mockup: the design system's Sidebar (drawer under
 * 1000px), a rail drawn here when collapsed (the welcome mockup's rail
 * styles), dude's brand at the top as today, and the organisation switcher
 * in the profile band: your face, your name and the organisation under it
 * open one menu for you: your organisations, invitations to join, your
 * settings, All organisations for a super-admin, and Sign out.
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
import type { Invitation, Org, Role } from "./data.ts";
import s from "./accounts.module.css";

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
  /** Each organisation's waiting count beside it. */
  counts: boolean;
  onAdmin: () => void;
  onSettings: () => void;
  onJoin: (i: Invitation) => void;
  memberships: readonly Membership[];
  invitations: readonly Invitation[];
  /** A super-admin's menu has All organisations. */
  superAdmin: boolean;
}

export interface Membership { readonly org: Org; readonly role: Role; readonly waiting: number }

function menuItems(p: SwitchProps): RowMenuItem[] {
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
      id: i.id, label: `Join ${i.org.name}…`, leading: <OrgFace org={i.org} size={16} />,
      description: `Invited by ${i.by?.name ?? "the operator"} · expires ${i.expires}`, onSelect: () => p.onJoin(i),
    })),
    ...(p.invitations.length ? [{ kind: "separator" } as const] : []),
    { id: "settings", label: "Your settings", icon: "settings", onSelect: p.onSettings },
  ];
  if (p.superAdmin) items.push({ id: "admin", label: "All organisations", icon: "building", description: "Super-admin: this dude's organisations", onSelect: p.onAdmin });
  items.push({ kind: "separator" }, { id: "signout", label: "Sign out", icon: "arrow-right", description: "Of Cloudflare Access, on this browser" });
  return items;
}

/** A plain button that forwards its ref: RowMenu's trigger slot needs one. */
const Trigger = forwardRef<HTMLButtonElement, ButtonHTMLAttributes<HTMLButtonElement>>(function Trigger(props, ref) {
  return <button ref={ref} type="button" {...props} />;
});

/** You, in the band, with the organisation under your name. */
export function SwitcherBand(p: SwitchProps & { you: Person }) {
  const m = p.memberships.find((x) => x.org.id === p.current.id);
  return (
    <RowMenu items={menuItems(p)} open={p.open} onOpenChange={p.onOpenChange} align="start" side="top" label="You and your organisations"
      trigger={
        <Trigger className={s.bandTrigger} aria-label={`${p.you.name}, ${p.current.name}: switch organisation, your settings, sign out`} data-testid="switcher">
          <PersonAvatar person={p.you} size={40} aria-hidden title="" />
          <span className={s.orgText}>
            <span className={s.bandName}>{p.you.name}</span>
            <span className={s.bandOrg}><OrgFace org={p.current} size={16} />{p.current.name}{m ? ` · ${roleWord(m.role)}` : ""}</span>
          </span>
          {p.invitations.length ? <span className={s.bandDot} aria-label={`${p.invitations.length} invitation`} /> : null}
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

/** The sidebar folded to a rail: your face at the bottom opens the same menu. */
export function Rail({ org, you, home, onHome, onExpand, sw }: {
  org: Org;
  you: Person;
  home: boolean;
  onHome: () => void;
  onExpand: () => void;
  sw: SwitchProps;
}) {
  const waiting = globalCounts(org.projects, you.id).needs_you;
  return (
    <nav className={rail.rail} aria-label="Navigation" data-testid="sidebar-rail">
      <div className={rail.top}>
        <RailButton label="Home" current={home} onClick={onHome}><Mark size={28} /></RailButton>
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
        <RowMenu items={menuItems(sw)} open={sw.open} onOpenChange={sw.onOpenChange} side="right" align="end" label={`${you.name}, ${org.name}: switch organisation`}
          trigger={
            <Trigger className={`${rail.item} ${s.railFace}`} aria-label={`${you.name}, ${org.name}: switch organisation`} data-testid="switcher">
              <PersonAvatar person={you} size={28} aria-hidden title="" />
              <span className={s.railOrg}><OrgFace org={org} size={14} /></span>
            </Trigger>
          } />
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
          <Rail org={p.org} you={p.you} home={p.home} onHome={p.onHome} onExpand={() => p.onCollapsedChange(false)} sw={railSw} />
        </div>
      ) : null}
      <div className={p.collapsed ? "sidebarSlot isCollapsed" : "sidebarSlot"}>
        <Sidebar
          projects={p.org.projects}
          you={p.you.id}
          title={
            <button type="button" className="brandHome" aria-label="El Duderino, home" onClick={p.onHome}>
              <span className="brand"><Mark size={30} />El Duderino</span>
            </button>
          }
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
              <SwitcherBand {...sideSw} you={p.you} />
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
