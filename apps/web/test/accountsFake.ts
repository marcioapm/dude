/**
 * The accounts world for the web tests: the fixture client (one
 * organisation's tasks and people) with what `/v1/me`, invitations, keys
 * and the super-admin's routes answer, kept in memory and changed by the
 * calls the screens make. Every call is recorded with the organisation
 * the client named, so a test can say what was asked, and where.
 */

import type { PersonRole } from "@dude/domain";
import { FixtureClient } from "../src/fixtures/client.ts";
import { ORG, PEOPLE, YOU } from "../src/fixtures/data.ts";
import {
  ApiError, type AdminOrganization, type AdminPerson, type ApiClient, type ApiKeyInfo, type InvitationCounts, type InvitationRow, type InvitationStatus,
  type InvitationView, type InviteResult, type Located, type MeAnswer, type Member, type MembershipInfo, type InvitationInfo,
} from "../src/api/client.ts";

const DAY = 86_400_000;
const iso = (offsetDays: number) => new Date(Date.now() + offsetDays * DAY).toISOString();

export const ACME: MembershipInfo = { id: ORG.id, name: "Acme", slug: "acme", role: "admin", personId: YOU, waitingOnYou: 2 };
export const NORTHWIND: MembershipInfo = { id: "org_northwind", name: "Northwind", slug: "northwind", role: "member", personId: "per_nw_marcio", waitingOnYou: 1 };
export const GLOBEX = { id: "org_globex", name: "Globex", slug: "globex" };

export function invitation(over: Partial<InvitationRow> & Pick<InvitationRow, "id" | "code" | "email">): InvitationRow {
  return { name: null, organization: { id: ACME.id, name: "Acme", slug: "acme" }, role: "member", inviter: "Ana Ribeiro", createdAt: iso(-4),
    expiresAt: iso(10), status: "pending", acceptedAt: null, acceptedPersonId: null, revokedAt: null, renewedAt: null, ...over };
}

export const ACME_INVITATIONS: InvitationRow[] = [
  invitation({ id: "inv_jo", code: "Jo00000000000000000000", email: "jo.becker@acme.dev", name: "Jo Becker" }),
  invitation({ id: "inv_jules", code: "Ju00000000000000000000", email: "jules@acme.dev", name: "Jules Moreau", createdAt: iso(0) }),
  invitation({ id: "inv_kai", code: "Ka00000000000000000000", email: "kai.nakamura@acme.dev", name: "Kai Nakamura", createdAt: iso(-16), expiresAt: iso(-2), status: "expired" }),
  invitation({ id: "inv_lin", code: "Li00000000000000000000", email: "lin@example.com", name: "Lin Zhao", status: "accepted", acceptedAt: iso(-21), acceptedPersonId: "u_lin" }),
  invitation({ id: "inv_priya", code: "Pr00000000000000000000", email: "priya.raman@gmail.com", name: "Priya Raman", status: "revoked", revokedAt: iso(-5) }),
];

export const GLOBEX_INVITE: InvitationInfo = {
  id: "inv_gx", code: "Gx4tR8mQ2vLp9sKd3wNb7a", email: "marcio@example.com", name: null, organization: GLOBEX, role: "member",
  inviter: "Lin Zhao", createdAt: iso(-4), expiresAt: iso(9),
};

export interface Call {
  method: string;
  /** The organisation the client named; null for one naming none. */
  org: string | null;
  args: unknown[];
}

export interface World {
  me: MeAnswer;
  invitations: InvitationRow[];
  /** `/v1/invitations/:code` per code; an ApiError is the refusal it answers with. */
  codes: Map<string, InvitationView | ApiError>;
  keys: ApiKeyInfo[];
  located: Map<string, Located>;
  orgs: AdminOrganization[];
  orgPeople: Map<string, AdminPerson[]>;
  calls: Call[];
  /** What Join answers: the organisation it made you a member of. */
  onAccept?: (code: string) => void;
  /** Rows per page of an invitations list (the API's is 50); a shorter page answers with a cursor. */
  pageSize?: number;
  /** Holds an invitations list read until the promise it returns settles; undefined answers at once. */
  holdList?: (params: { status?: InvitationStatus | "all"; q?: string; cursor?: string }) => Promise<void> | undefined;
  /** Addresses already pending on the server but not in `invitations`: inviting one answers `existing`. */
  pendingUnlisted?: Set<string>;
  /** An organisation's people, where it differs from the fixtures'; a promise holds the read until it settles. */
  people?: Map<string, { people: Member[]; you: string } | Promise<{ people: Member[]; you: string }>>;
  /** This browser's push registrations, by the organisation they are in. */
  push: Map<string, Set<string>>;
}

export function memberMe(over: Partial<{ organizations: MembershipInfo[]; invitations: InvitationInfo[]; superAdmin: boolean }> = {}): MeAnswer {
  return { gate: "member", account: { id: "acc_m", email: "marcio@example.com", name: "Márcio Martins", photoUrl: null },
    organizations: over.organizations ?? [ACME, NORTHWIND], invitations: over.invitations ?? [GLOBEX_INVITE], organization: null,
    superAdmin: over.superAdmin ?? true, authMethod: "cloudflare_access" };
}

export function world(me: MeAnswer = memberMe()): World {
  return {
    me, invitations: ACME_INVITATIONS.map((i) => ({ ...i })), codes: new Map(), located: new Map(), orgPeople: new Map(), calls: [], push: new Map(),
    keys: [
      { id: "key_1", name: "Laptop", prefix: "dude_sk_Qm3vX9", createdAt: iso(-30), lastUsedAt: iso(-0.1), current: false, organizationId: ACME.id },
      { id: "key_2", name: "Release script", prefix: "dude_sk_a81LpE", createdAt: iso(-20), lastUsedAt: iso(-1), current: false, organizationId: ACME.id },
    ],
    orgs: [
      { id: ACME.id, name: "Acme", slug: "acme", createdAt: "2026-03-01T00:00:00Z", members: 6, admins: 1, pendingInvitations: 2 },
      { id: NORTHWIND.id, name: "Northwind", slug: "northwind", createdAt: "2026-09-01T00:00:00Z", members: 2, admins: 1, pendingInvitations: 0 },
    ],
  };
}

const status = (r: InvitationRow): InvitationStatus => r.status;

/** The fixture client, answering the accounts routes from `w`; `org` is what it names (null: none). */
export class AccountsClient extends FixtureClient {
  constructor(readonly w: World, readonly org: string | null = null) {
    super("a");
  }
  #log(method: string, ...args: unknown[]) {
    this.w.calls.push({ method, org: this.org, args });
  }
  override get organizationId() {
    return this.org ?? undefined;
  }
  override inOrganization(organizationId: string): ApiClient {
    this.#log("inOrganization", organizationId);
    return new AccountsClient(this.w, organizationId);
  }
  // A new answer each time, as the server's is: a re-read never hands back the objects already held.
  override gate(): Promise<MeAnswer> {
    this.#log("gate");
    return Promise.resolve(structuredClone(this.w.me));
  }
  override subscribePush(subscription: PushSubscriptionJSON) {
    this.#log("subscribePush", subscription.endpoint);
    this.#pushIn().add(subscription.endpoint!);
    return Promise.resolve({ subscribed: true });
  }
  override unsubscribePush(endpoint: string) {
    this.#log("unsubscribePush", endpoint);
    this.#pushIn().delete(endpoint);
    return Promise.resolve();
  }
  override pushSubscribed(endpoint: string) {
    this.#log("pushSubscribed", endpoint);
    return Promise.resolve({ subscribed: this.#pushIn().has(endpoint) });
  }
  /** The endpoints registered in this client's organisation ("" for one naming none). */
  #pushIn(): Set<string> {
    const org = this.org ?? "";
    let set = this.w.push.get(org);
    if (!set) this.w.push.set(org, (set = new Set()));
    return set;
  }
  override updateMe(changes: { name?: string }) {
    this.#log("updateMe", changes);
    return this.me().then((r) => ({ person: { ...r.person, ...changes } }));
  }
  override markWelcomed(organizationId: string) {
    this.#log("markWelcomed", organizationId);
    const welcomedAt = iso(0);
    const me = this.w.me;
    if ("organizations" in me && me.organizations) {
      me.organizations = me.organizations.map((m) => (m.id === organizationId && !m.welcomedAt ? { ...m, welcomedAt } : m));
    }
    return Promise.resolve({ welcomedAt });
  }
  override pushKey() {
    this.#log("pushKey");
    return Promise.resolve({ publicKey: "BPk" });
  }
  override listPeople() {
    this.#log("listPeople");
    const own = this.org ? this.w.people?.get(this.org) : undefined;
    return own ? Promise.resolve(own) : super.listPeople();
  }
  override navigation() {
    this.#log("navigation");
    return super.navigation();
  }
  override locate(id: string): Promise<Located> {
    this.#log("locate", id);
    const found = this.w.located.get(id);
    return found ? Promise.resolve(found) : Promise.reject(new ApiError(404, "not_found", "not found"));
  }
  override invitation(code: string): Promise<InvitationView> {
    this.#log("invitation", code);
    const v = this.w.codes.get(code) ?? new ApiError(404, "invitation_unusable", "invitation_unusable");
    return v instanceof ApiError ? Promise.reject(v) : Promise.resolve(v);
  }
  override acceptInvitation(code: string, name?: string) {
    this.#log("acceptInvitation", code, name);
    const v = this.w.codes.get(code);
    if (!v || v instanceof ApiError) return Promise.reject(v ?? new ApiError(404, "invitation_unusable", "invitation_unusable"));
    this.w.onAccept?.(code);
    return Promise.resolve({ person: { id: "per_new", role: v.role, name: name ?? "Chloé" }, organization: v.organization });
  }
  override listInvitations(params: { status?: InvitationStatus | "all"; q?: string; cursor?: string } = {}) {
    this.#log("listInvitations", params);
    const want = params.status ?? "pending";
    const q = (params.q ?? "").toLowerCase();
    const rows = this.w.invitations.filter((r) => (want === "all" || status(r) === want)
      && (!q || r.email.includes(q) || (r.name ?? "").toLowerCase().includes(q)));
    const start = params.cursor ? rows.findIndex((r) => r.id === params.cursor) + 1 : 0;
    const size = this.w.pageSize ?? 50;
    const page = rows.slice(start, start + size);
    const answer = { invitations: page, cursor: start + size < rows.length ? page.at(-1)!.id : null };
    const held = this.w.holdList?.(params);
    return held ? held.then(() => answer) : Promise.resolve(answer);
  }
  override invitationCounts(): Promise<InvitationCounts> {
    this.#log("invitationCounts");
    const counts: InvitationCounts = { pending: 0, expired: 0, accepted: 0, revoked: 0, all: this.w.invitations.length };
    for (const r of this.w.invitations) counts[status(r)]++;
    return Promise.resolve(counts);
  }
  override invite(invitations: Array<{ email: string; name?: string }>, role: PersonRole) {
    this.#log("invite", invitations, role);
    const results: InviteResult[] = invitations.map((e, i) => {
      if (this.w.pendingUnlisted?.has(e.email)) {
        const code = `Old${i}`.padEnd(22, "0");
        return { email: e.email, result: "existing", invitation: { id: `inv_old${i}`, code, role, expiresAt: iso(9) }, link: `#/invite/${code}` };
      }
      const old = this.w.invitations.find((r) => r.email === e.email && (r.status === "pending" || r.status === "expired"));
      if (old?.status === "expired") {
        old.status = "pending";
        old.expiresAt = iso(14);
        return { email: e.email, result: "renewed", invitation: { id: old.id, code: old.code, role: old.role, expiresAt: old.expiresAt }, link: `#/invite/${old.code}` };
      }
      const code = `New${i}`.padEnd(22, "0");
      this.w.invitations.push(invitation({ id: `inv_new${i}`, code, email: e.email, name: e.name ?? null, role, createdAt: iso(0), expiresAt: iso(14) }));
      return { email: e.email, result: "created", invitation: { id: `inv_new${i}`, code, role, expiresAt: iso(14) }, link: `#/invite/${code}` };
    });
    return Promise.resolve({ results });
  }
  override renewInvitation(id: string) {
    this.#log("renewInvitation", id);
    const row = this.w.invitations.find((r) => r.id === id)!;
    row.status = "pending";
    row.expiresAt = iso(14);
    return Promise.resolve({ code: row.code });
  }
  override revokeInvitation(id: string) {
    this.#log("revokeInvitation", id);
    const row = this.w.invitations.find((r) => r.id === id)!;
    row.status = "revoked";
    row.revokedAt = iso(0);
    return Promise.resolve();
  }
  override listMyKeys() {
    this.#log("listMyKeys");
    return Promise.resolve({ keys: [...this.w.keys] });
  }
  override createMyKey(name: string, organizationId = this.org ?? undefined) {
    this.#log("createMyKey", name, organizationId);
    const key = `dude_sk_new${this.w.keys.length}`;
    this.w.keys.push({ id: `key_${this.w.keys.length + 1}`, name, prefix: key.slice(0, 14), createdAt: iso(0), lastUsedAt: null, current: false,
      ...(organizationId ? { organizationId } : {}) });
    return Promise.resolve({ id: "key_new", name, prefix: key.slice(0, 14), key });
  }
  override adminOrganizations() {
    this.#log("adminOrganizations");
    return Promise.resolve({ organizations: [...this.w.orgs] });
  }
  override createOrganization(input: { name: string; slug: string; email: string }) {
    this.#log("createOrganization", input);
    const id = `org_${input.slug}`;
    this.w.orgs.push({ id, name: input.name, slug: input.slug, createdAt: iso(0), members: 0, admins: 0, pendingInvitations: 1 });
    return Promise.resolve({ organization: { id, name: input.name, slug: input.slug }, email: input.email, result: "created" as const,
      invitation: { id: "inv_first", code: "First00000000000000000", role: "admin" as const, expiresAt: iso(14) }, link: "#/invite/First00000000000000000" });
  }
  override adminOrganization(id: string) {
    this.#log("adminOrganization", id);
    const o = this.w.orgs.find((x) => x.id === id)!;
    const people = this.w.orgPeople.get(id) ?? PEOPLE.slice(0, 2).map((p) => ({ ...p, accountId: "acc", email: p.email }));
    return Promise.resolve({ organization: { id, name: o.name, slug: o.slug }, people: people as AdminPerson[], invitations: [], cursor: null });
  }
  override adminInvitations(id: string, params: { status?: InvitationStatus | "all"; q?: string; cursor?: string } = {}) {
    this.#log("adminInvitations", id, params);
    return this.listInvitations(params);
  }
  override adminInvitationCounts(id: string) {
    this.#log("adminInvitationCounts", id);
    return this.invitationCounts();
  }
  override adminInvite(id: string, invitations: Array<{ email: string; name?: string }>, role: PersonRole) {
    this.#log("adminInvite", id, invitations, role);
    return this.invite(invitations, role);
  }
  override adminUpdatePerson(personId: string, change: { email: string } | { role: PersonRole }) {
    this.#log("adminUpdatePerson", personId, change);
    return Promise.resolve({});
  }
  override adminRemovePerson(id: string, personId: string) {
    this.#log("adminRemovePerson", id, personId);
    return Promise.reject(new ApiError(409, "conflict", "the organization needs an admin; make someone else one first"));
  }
  override githubAuthorization() {
    this.#log("githubAuthorization");
    return Promise.resolve({ available: true, connection: null });
  }
  override connectGithub() {
    this.#log("connectGithub");
    return Promise.reject(new ApiError(400, "bad_request", "not in these tests"));
  }
}

export function invitationView(code: string, org: { id: string; name: string; slug: string }, state: "pending" | "member" = "pending",
  over: Partial<InvitationView> = {}): InvitationView {
  return { id: `inv_${code}`, code, email: "chloe.durand@hey.com", name: null, organization: org, role: "member", inviter: "Gus Lindqvist",
    createdAt: iso(-2), expiresAt: iso(12), state, personId: state === "member" ? "per_c" : null, ...over };
}
