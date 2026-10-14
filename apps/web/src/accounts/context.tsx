/**
 * Who is signed in across organisations, for the screens inside one: the
 * organisations you are in (role, what waits on you), the invitations
 * waiting for you, the one this tab is in, and the ways out of it (switch,
 * join, all organisations, sign out). The gatekeeper (Gatekeeper.tsx)
 * provides it once it has mounted an organisation; screens mounted
 * without it (tests of one screen) read `null` and show one organisation.
 */

import { createContext, useContext } from "react";
import type { OrgInvitation, OrgMembership, Organisation } from "@dude/design-system/components";
import type { ApiClient, MembershipInfo, InvitationInfo } from "../api/client.ts";
import { untilWords } from "./words.ts";

export interface Account {
  /** The credential with no organisation named: account routes (keys across organisations). */
  base: ApiClient;
  email: string | null;
  authMethod: "api_key" | "cloudflare_access";
  memberships: readonly MembershipInfo[];
  invitations: readonly InvitationInfo[];
  current: MembershipInfo;
  superAdmin: boolean;
  /** Go to another organisation's home. */
  switchTo: (organizationId: string) => void;
  /** Open an invitation's accept page. */
  join: (code: string) => void;
  /** The super-admin's page. */
  admin: () => void;
  /** Read `/v1/me` again: after joining, or when an organisation's list changed. */
  refresh: () => void;
}

export const AccountContext = createContext<Account | null>(null);

export function useAccount(): Account | null {
  return useContext(AccountContext);
}

export const orgOf = (m: Pick<MembershipInfo, "id" | "name" | "slug">): Organisation => ({ id: m.id, name: m.name, slug: m.slug });

export function switcherMemberships(memberships: readonly MembershipInfo[]): OrgMembership[] {
  return memberships.map((m) => ({ org: orgOf(m), role: m.role, waiting: m.waitingOnYou }));
}

export function switcherInvitations(invitations: readonly InvitationInfo[], now = Date.now()): OrgInvitation[] {
  return invitations.map((i) => ({ code: i.code, org: orgOf(i.organization), inviter: i.inviter, expires: untilWords(i.expiresAt, now) }));
}
