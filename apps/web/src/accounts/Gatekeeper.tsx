/**
 * Between signing in and the app: the sign-in gate's answer (`GET /v1/me`,
 * docs/design/accounts.md, *The sign-in gate*) and the address decide what
 * mounts, before any organisation's data is read.
 *
 * - member: the organisation the URL names (`#/o/<slug>/…`), else the
 *   last one this browser was in, else the first; an older link without
 *   `/o/` is found with `/v1/locate/:id` and rewritten. The tenant app
 *   gets a client that names that organisation on every request.
 * - invited: You're invited, or the link's accept page.
 * - super_admin: the super-admin's page.
 * - none: No organisation; an invitation link still says why it can't be used.
 *
 * Each tab mounts its own organisation: switching is navigation, so two
 * tabs can be in two organisations at once.
 */

import { Fragment, useCallback, useEffect, useLayoutEffect, useMemo, useState, type ReactNode } from "react";
import { FrontDoor, FrontDoorMessage } from "@dude/design-system/components";
import { Button, Card, CardBody, CardFooter, Spinner } from "@dude/design-system/primitives";
import type { ApiClient, InvitationInfo, MeAnswer, MembershipInfo } from "../api/client.ts";
import { DudeMark } from "../DudeMark.tsx";
import { inOrganization, setPlaceOrganization } from "../place.ts";
import { AdminPage } from "./AdminPage.tsx";
import { AccountContext, type Account } from "./context.tsx";
import { AcceptInvitation, NoOrganisation, YoureInvited } from "./FrontDoors.tsx";
import { JOINED_FROM } from "./JoinedScreen.tsx";
import { adminHash, inviteHash, LAST_ORGANIZATION, parseRoute, type Route } from "./route.ts";
import type { AuthMethod } from "../auth.ts";

export interface GatekeeperProps {
  /** The signed-in credential, naming no organisation. */
  client: ApiClient;
  me: MeAnswer;
  authMethod: AuthMethod;
  /** Read `/v1/me` again (after joining, Check again); resolves with the new answer. */
  reread: () => Promise<MeAnswer | null>;
  onSignOut: () => void;
  /** Mounts the tenant app for one organisation; keyed by it, so a switch remounts what it read. */
  children: (client: ApiClient, membership: MembershipInfo) => ReactNode;
}

/** Your organisations, from `/v1/me`: a key's own one when the answer lists none (API-key mode). */
export function membershipsOf(me: MeAnswer): MembershipInfo[] {
  if ("organizations" in me && me.organizations?.length) return me.organizations;
  if (me.person && me.organization) {
    return [{ id: me.organization.id, name: me.organization.name, slug: me.organization.slug ?? me.organization.id,
      role: me.person.role, personId: me.person.id, waitingOnYou: 0 }];
  }
  return [];
}

export function emailOf(me: MeAnswer): string | null {
  if ("email" in me && typeof me.email === "string") return me.email;
  if ("account" in me && me.account) return me.account.email;
  return me.person?.email ?? null;
}

function useHash(): string {
  const [hash, setHash] = useState(() => window.location.hash);
  useEffect(() => {
    const follow = () => setHash(window.location.hash);
    window.addEventListener("hashchange", follow);
    window.addEventListener("popstate", follow);
    return () => {
      window.removeEventListener("hashchange", follow);
      window.removeEventListener("popstate", follow);
    };
  }, []);
  return hash;
}

/** Replace the address without a history step, and say so to whoever follows it. */
function replaceHash(hash: string) {
  window.history.replaceState(null, "", hash);
  window.dispatchEvent(new HashChangeEvent("hashchange"));
}

/** Go to an address as a step Back can undo. */
function goHash(hash: string) {
  if (window.location.hash === hash) return;
  window.location.hash = hash;
}

/** Where a bare address opens: the last organisation this browser was in, if still yours, else the first. */
export function homeOf(memberships: readonly MembershipInfo[], last = localStorage.getItem(LAST_ORGANIZATION)): MembershipInfo | undefined {
  return memberships.find((m) => m.slug === last) ?? memberships[0];
}

export function Gatekeeper({ client, me, authMethod, reread, onSignOut, children }: GatekeeperProps) {
  const hash = useHash();
  const route = useMemo<Route>(() => parseRoute(hash), [hash]);
  const memberships = useMemo(() => membershipsOf(me), [me]);
  const invitations: readonly InvitationInfo[] = ("invitations" in me ? me.invitations : undefined) ?? [];
  const superAdmin = me.superAdmin === true;
  const email = emailOf(me) ?? "";
  const gate = me.gate ?? "member";
  // API-key mode (development, the demo, the suites): the key names its one organisation, so the
  // addresses stay as they were (`#/task/…`); a `#/o/<slug>/…` link to that organisation opens too.
  const keyMode = authMethod === "api_key";
  // Joining: hold the page until `/v1/me` says member and the address names the organisation.
  const [joining, setJoining] = useState(false);
  // An older link being placed among your organisations.
  const [locating, setLocating] = useState<string | null>(null);

  const joined = useCallback(async (organization: { id: string; slug: string }, target: string, from?: { inviter: string; role: string }) => {
    setJoining(true);
    if (from) sessionStorage.setItem(`${JOINED_FROM}:${organization.id}`, JSON.stringify(from));
    localStorage.setItem(LAST_ORGANIZATION, organization.slug);
    try {
      await reread();
    } finally {
      goHash(target);
      setJoining(false);
    }
  }, [reread]);

  // A member at an address with no organisation in it: placed, then rewritten in place.
  useEffect(() => {
    if (gate === "super_admin" && route.kind !== "admin" && route.kind !== "invite") return replaceHash(adminHash());
    if (gate !== "member" || !memberships.length) return;
    // A key is one organisation's: its tab keeps the addresses it always had.
    if (keyMode) {
      if (route.kind === "admin" || route.kind === "invitations") replaceHash("#/");
      return;
    }
    if (route.kind === "home" || route.kind === "invitations") {
      const home = homeOf(memberships)!;
      replaceHash(inOrganization(home.slug, "#/"));
    } else if (route.kind === "admin" && !superAdmin) {
      replaceHash(inOrganization(homeOf(memberships)!.slug, "#/"));
    } else if (route.kind === "legacy") {
      const fallback = memberships.length === 1 ? memberships[0]! : homeOf(memberships)!;
      if (!route.id || memberships.length === 1) return replaceHash(inOrganization(fallback.slug, route.rest));
      let current = true;
      setLocating(route.id);
      client.locate(route.id).then(
        (found) => current && replaceHash(inOrganization(found.organization.slug, route.rest)),
        // Not yours anywhere: it opens in your home organisation, which says it is not there.
        () => current && replaceHash(inOrganization(fallback.slug, route.rest)),
      ).finally(() => current && setLocating(null));
      return () => {
        current = false;
      };
    }
  }, [gate, route, memberships, superAdmin, client, keyMode]);

  const toAdmin = useCallback(() => goHash(adminHash()), []);
  const join = useCallback((code: string) => goHash(inviteHash(code)), []);
  const switchTo = useCallback((id: string) => {
    const m = memberships.find((x) => x.id === id);
    if (!m) return;
    localStorage.setItem(LAST_ORGANIZATION, m.slug);
    goHash(inOrganization(m.slug, "#/"));
  }, [memberships]);

  const membership = route.kind === "tenant" ? memberships.find((m) => m.slug === route.slug)
    : keyMode && gate === "member" && (route.kind === "home" || route.kind === "legacy") ? memberships[0] : undefined;
  // The tab's places name its organisation from here on (over Access; a key's stay bare). A layout
  // effect: before the tenant app's effects and handlers write a place.
  const placeSlug = gate === "member" && membership && !keyMode ? membership.slug : null;
  useLayoutEffect(() => setPlaceOrganization(placeSlug), [placeSlug]);
  const membershipId = membership?.id;
  const membershipSlug = membership?.slug;
  useEffect(() => {
    if (membershipSlug) localStorage.setItem(LAST_ORGANIZATION, membershipSlug);
  }, [membershipSlug]);
  // Keyed on the id, not the object: re-reading `/v1/me` (a profile save) makes new membership
  // objects, and a new client would re-read the tenant app and reopen its stream.
  const scoped = useMemo(() => (membershipId ? client.inOrganization(membershipId) : null), [client, membershipId]);
  const account = useMemo<Account | null>(() => membership ? {
    base: client, email, authMethod, memberships, invitations, current: membership, superAdmin,
    switchTo, join, admin: toAdmin, refresh: () => void reread(),
  } : null, [client, email, authMethod, memberships, invitations, membership, superAdmin, switchTo, join, toAdmin, reread]);

  if (joining || locating) return <Waiting />;

  if (route.kind === "invite") {
    return (
      <AcceptInvitation key={route.code} client={client} code={route.code} email={email} memberships={memberships}
        onJoined={(organization, target, invitation) => void joined(organization, target, { inviter: invitation.inviter, role: invitation.role })}
        onNotNow={() => goHash("#/")} onHome={() => goHash("#/")} onSignOut={onSignOut} onSwitchAccount={onSignOut} />
    );
  }
  if (route.kind === "admin" && superAdmin) {
    const back = gate === "member" ? homeOf(memberships) : undefined;
    return (
      <AdminPage client={client} slug={route.slug} onOpen={(slug) => goHash(adminHash(slug))}
        back={back ? { name: back.name, go: () => goHash(inOrganization(back.slug, "#/")) } : null} />
    );
  }
  if (gate === "none") {
    return <NoOrganisation email={email} onCheck={async () => void (await reread().catch(() => null))} onSignOut={onSignOut} />;
  }
  if (gate === "invited") {
    return (
      <YoureInvited email={email} name={"name" in me ? me.name ?? null : null} photoUrl={"photoUrl" in me ? me.photoUrl ?? null : null}
        invitations={invitations} superAdmin={superAdmin} onAdmin={toAdmin} onSignOut={onSignOut}
        onJoin={async (invitation, name) => {
          const { organization } = await client.acceptInvitation(invitation.code, name || undefined);
          await joined(organization, inOrganization(organization.slug, "#/welcome"), { inviter: invitation.inviter, role: invitation.role });
        }} />
    );
  }
  if (gate === "super_admin") {
    // Nothing else is theirs: the super-admin's page is home (the effect above puts it in the address).
    return <AdminPage client={client} slug={null} onOpen={(slug) => goHash(adminHash(slug))} back={null} />;
  }
  if (route.kind !== "tenant" && !membership) return <Waiting />;
  if (!membership || !scoped || !account) {
    const home = homeOf(memberships);
    return (
      <FrontDoor data-testid="not-yours" mark={<DudeMark size={72} />}>
        <Card>
          <CardBody><FrontDoorMessage icon="building" title="You're not in an organisation at this address" line="Check the link, or ask whoever sent it." /></CardBody>
          {home ? <CardFooter><Button variant="primary" onClick={() => switchTo(home.id)} data-testid="open-home">Open {home.name}</Button></CardFooter> : null}
        </Card>
      </FrontDoor>
    );
  }
  return (
    <AccountContext.Provider value={account}>
      {/* Keyed by the organisation: a switch remounts the tenant app, so nothing read for one shows in another. */}
      <Fragment key={membership.id}>{children(scoped, membership)}</Fragment>
    </AccountContext.Provider>
  );
}

function Waiting() {
  return <div className="keyPrompt" role="status"><Spinner label="Opening…" /></div>;
}
