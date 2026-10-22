import { forwardRef, type ButtonHTMLAttributes, type ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { Icon } from "../icons/index.tsx";
import { RowMenu, type RowMenuItem } from "../primitives/RowMenu.tsx";
import { Tooltip } from "../primitives/Tooltip.tsx";
import { PersonAvatar, type Person } from "./PersonAvatar.tsx";
import { NeedsYouCount } from "./StatusMark.tsx";
import { OrgFace, type Organisation } from "./OrgFace.tsx";
import styles from "./OrgSwitcher.module.css";

export type OrgRole = "admin" | "member";

export interface OrgMembership {
  readonly org: Organisation;
  readonly role: OrgRole;
  /** What waits on you there; the switcher's only cross-organisation count. */
  readonly waiting: number;
}

/** An invitation waiting for you: the menu's "Join <org>…". */
export interface OrgInvitation {
  readonly code: string;
  readonly org: Organisation;
  /** Who sent it; "the operator" for a super-admin's. */
  readonly inviter: string;
  /** "in 9 days". */
  readonly expires: string;
}

export interface OrgSwitcherProps {
  readonly you: Person;
  /** The organisation this page is in. */
  readonly current: Organisation;
  readonly memberships: ReadonlyArray<OrgMembership>;
  readonly invitations?: ReadonlyArray<OrgInvitation> | undefined;
  /** Adds All organisations. */
  readonly superAdmin?: boolean | undefined;
  readonly onSwitch: (org: Organisation) => void;
  readonly onJoin?: ((invitation: OrgInvitation) => void) | undefined;
  readonly onSettings: () => void;
  readonly onOrgSettings?: (() => void) | undefined;
  readonly onHelp?: (() => void) | undefined;
  readonly onAdmin?: (() => void) | undefined;
  readonly onSignOut: () => void;
  /** What signing out does here: "Of Cloudflare Access, on this browser". */
  readonly signOutNote?: string | undefined;
  /** The settings item's label. */
  readonly settingsLabel?: string | undefined;
  /** The super-admin item's label, and the line under it. */
  readonly adminLabel?: string | undefined;
  readonly adminNote?: string | undefined;
  /** `band`: the expanded sidebar's profile band. `rail`: the collapsed rail's face. */
  readonly variant?: "band" | "rail" | undefined;
  readonly open?: boolean | undefined;
  readonly onOpenChange?: ((open: boolean) => void) | undefined;
  /** Gallery previews only: the menu drawn open inside a themed pane. */
  readonly forceMount?: true | undefined;
  readonly container?: HTMLElement | null | undefined;
  readonly "data-testid"?: string | undefined;
}

export const roleWord = (role: OrgRole) => (role === "admin" ? "Admin" : "Member");

/** The menu's rows, in the order the design names them. */
export function switcherItems(p: OrgSwitcherProps): RowMenuItem[] {
  const invitations = p.invitations ?? [];
  const items: RowMenuItem[] = [
    {
      kind: "radio", id: "orgs", label: "Your organisations", value: p.current.id,
      onValueChange: (id) => {
        const m = p.memberships.find((x) => x.org.id === id);
        if (m && id !== p.current.id) p.onSwitch(m.org);
      },
      items: p.memberships.map((m) => ({
        id: m.org.id, label: m.org.name, leading: <OrgFace org={m.org} size={20} aria-hidden />,
        description: m.waiting ? `${roleWord(m.role)} · ${m.waiting} waiting on you` : roleWord(m.role),
        trailing: m.waiting ? <NeedsYouCount count={m.waiting} aria-hidden /> : undefined,
      })),
    },
    { kind: "separator" },
  ];
  for (const i of invitations) {
    items.push({
      id: `join-${i.code}`, label: `Join ${i.org.name}…`, leading: <OrgFace org={i.org} size={16} aria-hidden />,
      description: `Invited by ${i.inviter} · expires ${i.expires}`, onSelect: () => p.onJoin?.(i),
    });
  }
  if (invitations.length) items.push({ kind: "separator" });
  const [settings, ...rest] = profileItems(p);
  items.push(settings!);
  if (p.superAdmin && p.onAdmin) {
    // All organisations sits with the places, before the separator and Sign out.
    const at = rest.findIndex((it) => it.kind === "separator");
    rest.splice(at, 0, { id: "admin", label: p.adminLabel ?? "All organisations", icon: "building", onSelect: p.onAdmin,
      ...(p.adminNote ? { description: p.adminNote } : {}) });
  }
  items.push(...rest);
  return items;
}

export interface ProfileMenuActions {
  readonly onSettings: () => void;
  readonly settingsLabel?: string | undefined;
  readonly onOrgSettings?: (() => void) | undefined;
  readonly onHelp?: (() => void) | undefined;
  readonly onSignOut: () => void;
  readonly signOutNote?: string | undefined;
}

/** Your settings · Organisation settings · Help, then Sign out: the profile menu in every sign-in mode. */
export function profileItems(p: ProfileMenuActions): RowMenuItem[] {
  const items: RowMenuItem[] = [{ id: "settings", label: p.settingsLabel ?? "Your settings", icon: "settings", onSelect: p.onSettings }];
  if (p.onOrgSettings) items.push({ id: "org-settings", label: "Organisation settings", icon: "building", onSelect: p.onOrgSettings });
  if (p.onHelp) items.push({ id: "help", label: "Help", icon: "book-open", onSelect: p.onHelp });
  items.push({ kind: "separator" }, {
    id: "signout", label: "Sign out", icon: "arrow-right", onSelect: p.onSignOut,
    ...(p.signOutNote ? { description: p.signOutNote } : {}),
  });
  return items;
}

/** A plain button that forwards its ref and Radix's handlers: RowMenu's trigger slot needs one. */
const Trigger = forwardRef<HTMLButtonElement, ButtonHTMLAttributes<HTMLButtonElement>>(function Trigger(props, ref) {
  return <button ref={ref} type="button" {...props} />;
});

export interface ProfileMenuProps extends ProfileMenuActions {
  /** Unknown while people load: the band says "Your settings" with no face. */
  readonly person?: Person | null | undefined;
  /** Under the name: an email. */
  readonly detail?: string | undefined;
  readonly open?: boolean | undefined;
  readonly onOpenChange?: ((open: boolean) => void) | undefined;
  readonly forceMount?: true | undefined;
  readonly container?: HTMLElement | null | undefined;
  readonly "data-testid"?: string | undefined;
}

/**
 * You at the foot of the sidebar when there are no organisations to switch
 * between (API-key sign-in): the band of `OrgSwitcher`, opening the same
 * profile items without the organisations.
 */
export function ProfileMenu(p: ProfileMenuProps) {
  const name = p.person ? `${p.person.name}: your settings, organisation settings, help, sign out` : "Your settings, organisation settings, help, sign out";
  return (
    <RowMenu items={profileItems(p)} label="You" side="top" align="start" className={styles["menu"]}
      trigger={
        <Trigger className={styles["band"]} aria-label={name} data-testid={p["data-testid"] ?? "profile-menu"}>
          {p.person ? <PersonAvatar person={p.person} size={40} aria-hidden title="" /> : null}
          <span className={styles["text"]}>
            <span className={styles["name"]}>{p.person?.name ?? (p.settingsLabel ?? "Your settings")}</span>
            {p.detail ? <span className={styles["org"]}><span className={styles["orgName"]}>{p.detail}</span></span> : null}
          </span>
          <Icon name="sort" size={14} className={styles["chevron"]} />
        </Trigger>
      }
      {...(p.open !== undefined ? { open: p.open } : {})} {...(p.onOpenChange ? { onOpenChange: p.onOpenChange } : {})}
      {...(p.forceMount ? { forceMount: p.forceMount } : {})} {...(p.container ? { container: p.container } : {})} />
  );
}

/**
 * You, at the foot of the sidebar, and the one menu for you: your
 * organisations (role and what waits on you there, the current one
 * ticked), Join… for each invitation waiting for you, Your settings, All
 * organisations for a super-admin, and Sign out. Radix's menu: Enter or
 * Space opens, arrows move, Enter picks, Escape closes and returns focus.
 *
 * `band` is the expanded sidebar's: your face, your name, the
 * organisation's face and name under it, a dot when an invitation waits,
 * a sort chevron. `rail` is your face with the organisation's in its
 * corner, named in a tooltip.
 */
export function OrgSwitcher(p: OrgSwitcherProps) {
  const variant = p.variant ?? "band";
  const membership = p.memberships.find((m) => m.org.id === p.current.id);
  const pending = p.invitations?.length ?? 0;
  const name = `${p.you.name}, ${p.current.name}${membership ? `, ${roleWord(membership.role).toLowerCase()}` : ""}: switch organisation, your settings${p.onOrgSettings ? ", organisation settings" : ""}${p.onHelp ? ", help" : ""}, sign out`;
  const items = switcherItems(p);
  const menu = (trigger: ReactNode, side: "top" | "right", align: "start" | "end") => (
    <RowMenu items={items} label="You and your organisations" trigger={trigger} side={side} align={align}
      className={styles["menu"]} {...(p.open !== undefined ? { open: p.open } : {})}
      {...(p.onOpenChange ? { onOpenChange: p.onOpenChange } : {})}
      {...(p.forceMount ? { forceMount: p.forceMount } : {})} {...(p.container ? { container: p.container } : {})} />
  );
  const invitationWords = pending === 1 ? "1 invitation waiting" : `${pending} invitations waiting`;
  if (variant === "rail") {
    return (
      <Tooltip content={`${p.you.name} · ${p.current.name}`} side="right">
        <span className={styles["railSlot"]}>
          {menu(
            <Trigger className={styles["railTrigger"]} aria-label={name} data-testid={p["data-testid"] ?? "switcher"}>
              <PersonAvatar person={p.you} size={28} aria-hidden title="" />
              <span className={styles["railOrg"]}><OrgFace org={p.current} size={14} aria-hidden /></span>
              {pending ? <span className={styles["railDot"]} role="img" aria-label={invitationWords} /> : null}
            </Trigger>,
            "right", "end",
          )}
        </span>
      </Tooltip>
    );
  }
  return menu(
    <Trigger className={styles["band"]} aria-label={name} data-testid={p["data-testid"] ?? "switcher"}>
      <PersonAvatar person={p.you} size={40} aria-hidden title="" />
      <span className={styles["text"]}>
        <span className={styles["name"]}>{p.you.name}</span>
        <span className={styles["org"]}>
          <OrgFace org={p.current} size={16} aria-hidden />
          <span className={styles["orgName"]}>{p.current.name}{membership ? ` · ${roleWord(membership.role)}` : ""}</span>
        </span>
      </span>
      {pending ? <span className={styles["dot"]} role="img" aria-label={invitationWords} /> : null}
      <Icon name="sort" size={14} className={cx(styles["chevron"])} />
    </Trigger>,
    "top", "start",
  );
}
