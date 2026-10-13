/*
 * The mockup's world: Márcio, admin of Acme, member of Northwind and the
 * deployment's super-admin; Chloé, new, in no organisation, invited to
 * Northwind by Gus's link and to Globex by Lin's; and the people, keys and
 * invitations each organisation's pages show. Deterministic.
 */

import type { Person } from "../../../../packages/design-system/src/components/PersonAvatar.tsx";
import type { NavProject, NavTask } from "../../../../packages/design-system/src/util/navModel.ts";
import { navProjects, navProjectsQuiet } from "../../../../packages/design-system/src/gallery/navFixtures.ts";
import ana from "../../../../docs/design/mockups/photos/ana.jpg?url";
import cy from "../../../../docs/design/mockups/photos/cy.jpg?url";
import eli from "../../../../docs/design/mockups/photos/eli.jpg?url";
import gus from "../../../../docs/design/mockups/photos/gus.jpg?url";

export type Role = "admin" | "member";

export const HOST = "dude.acme.dev";

export const inviteUrl = (code: string) => `https://${HOST}/#/invite/${code}`;

/** People by face. Márcio's id is the gallery's, so the sidebar's "Mine" and needs-you are his. */
export const P = {
  marcio: { id: "u_marcio", name: "Márcio Martins", online: true },
  ana: { id: "u_ana", name: "Ana Ribeiro", photoUrl: ana, online: true },
  chloe: { id: "u_chloe", name: "Chloé Durand", photoUrl: cy, online: true },
  eli: { id: "u_eli", name: "Eli Haddad", photoUrl: eli, online: false },
  gus: { id: "u_gus", name: "Gus Lindqvist", photoUrl: gus, online: true },
  tom: { id: "u_tom", name: "Tom Okafor", online: false },
  lin: { id: "u_lin", name: "Lin Zhao", online: false },
} satisfies Record<string, Person>;

export interface Org {
  readonly id: string;
  readonly name: string;
  readonly slug: string;
  /** An identity slot for its face: organisations have no image yet. */
  readonly colorSlot: number;
  readonly projects: readonly NavProject[];
  readonly created: string;
}

let n = 7100;
function task(title: string, status: NavTask["status"], people: Person[]): NavTask {
  n += 1;
  return { id: `wi_${n}`, key: `PORT-${n - 7100}`, title, status, people };
}

/** The deployment's first organisation. */
export const ACME: Org = { id: "org_acme", name: "Acme", slug: "acme", colorSlot: 2, projects: navProjects, created: "March 2026" };

export const NORTHWIND: Org = {
  id: "org_northwind",
  name: "Northwind",
  slug: "northwind",
  colorSlot: 5,
  created: "September 2026",
  projects: [
    {
      id: "p_portal",
      name: "customer-portal",
      tasks: [
        task("Invoice PDFs render the VAT line twice", "running", [P.gus, P.marcio]),
        task("Order history paginates past the last page", "awaiting_input", [P.marcio, P.gus]),
        task("Move the address form to the shared validator", "review", [P.eli]),
        task("Dark mode for the account pages", "queued", [P.gus]),
      ],
    },
    ...navProjectsQuiet,
  ],
};

export const GLOBEX: Org = { id: "org_globex", name: "Globex", slug: "globex", colorSlot: 0, projects: [], created: "October 2026" };

/** Márcio's memberships, as the switcher lists them. */
export const MEMBERSHIPS: readonly { org: Org; role: Role; waiting: number }[] = [
  { org: ACME, role: "admin", waiting: 2 },
  { org: NORTHWIND, role: "member", waiting: 1 },
];

/** An invitation to someone, as the invitee sees it. */
export interface Invitation {
  readonly id: string;
  readonly code: string;
  readonly org: Org;
  readonly role: Role;
  readonly by: Person | null;
  readonly sent: string;
  readonly expires: string;
}

/** One Márcio has not taken up yet: the profile menu's Join… */
export const MY_INVITATIONS: readonly Invitation[] = [
  { id: "inv_globex", code: "Gx4tR8mQ2vLp9sKd3wNb7a", org: GLOBEX, role: "member", by: P.lin, sent: "4 October", expires: "in 9 days" },
];

export const CHLOE_EMAIL = "chloe.durand@hey.com";
/** The address Chloé is signed in with in the wrong-email state. */
export const CHLOE_OTHER = "chloe.d@gmail.com";

/**
 * Chloé's: Gus's link to Northwind, Lin's to Globex. She is in no
 * organisation until she joins one of them.
 */
export const CHLOE_INVITES: readonly Invitation[] = [
  { id: "inv_2", code: "Nw5hT2cJ8rFq6zMe1uVd4x", org: NORTHWIND, role: "member", by: P.gus, sent: "11 October", expires: "in 12 days" },
  { id: "inv_3", code: "Gc8wQ3eR6tY9uI2oP5aS1d", org: GLOBEX, role: "member", by: P.lin, sent: "12 October", expires: "in 13 days" },
];

export interface Member {
  readonly person: Person;
  readonly email: string | null;
  readonly role: Role;
  readonly seen: string;
  /** How they came in, read from the invitation accepted into the row. */
  readonly joined?: string;
}

export const ACME_MEMBERS: readonly Member[] = [
  { person: P.marcio, email: "marcio@acme.dev", role: "admin", seen: "online", joined: "first admin" },
  { person: P.ana, email: "ana@acme.dev", role: "admin", seen: "online", joined: "invited by Márcio" },
  { person: P.tom, email: "tom@acme.dev", role: "member", seen: "active yesterday on CP-41", joined: "joined before invitations" },
  { person: P.lin, email: "lin@acme.dev", role: "member", seen: "active 3 days ago", joined: "invited by Márcio" },
  { person: P.eli, email: "eli@northwind.io", role: "member", seen: "active last week", joined: "invited by Ana" },
];

export type InvitationStatus = "pending" | "expired" | "accepted" | "revoked";

/** One row of an organisation's Invitations page. */
export interface InvitationRow {
  readonly id: string;
  readonly code: string;
  readonly email: string;
  readonly name?: string;
  readonly role: Role;
  readonly by: Person | null;
  readonly created: string;
  readonly status: InvitationStatus;
  /** Pending: "in 10 days"; expired: "2 days ago". */
  readonly expires?: string;
  readonly renewed?: string;
  /** Accepted or revoked: by whom and when. */
  readonly closedBy?: Person;
  readonly closed?: string;
}

export const ACME_INVITATIONS: readonly InvitationRow[] = [
  { id: "i1", code: "k7Qm2vX9pL4sN8wR1tYb3c", email: "jo.becker@acme.dev", name: "Jo Becker", role: "member", by: P.ana, created: "4 days ago", status: "pending", expires: "in 10 days" },
  { id: "i2", code: "Sa3pW7nK1xD5gH9qT2mR6e", email: "sam@acme.dev", role: "admin", by: P.marcio, created: "yesterday", status: "pending", expires: "in 13 days" },
  { id: "i3", code: "Jm8bY4uC6vE2kP0wL5sF1z", email: "jules@acme.dev", name: "Jules Moreau", role: "member", by: P.ana, created: "today", status: "pending", expires: "in 14 days" },
  { id: "i4", code: "Kn2rZ6tB9yH3mQ7cX4vA8d", email: "kai.nakamura@acme.dev", name: "Kai Nakamura", role: "member", by: P.ana, created: "16 days ago", status: "expired", expires: "2 days ago" },
  { id: "i5", code: "Lz5cM1oP8eR4tS2uV6wG9h", email: "lin@acme.dev", name: "Lin Zhao", role: "member", by: P.marcio, created: "4 weeks ago", status: "accepted", closedBy: P.lin, closed: "3 weeks ago" },
  { id: "i6", code: "Eh7dN3qW5rT1yU9iO2pJ4k", email: "eli@northwind.io", name: "Eli Haddad", role: "member", by: P.ana, created: "6 weeks ago", status: "accepted", closedBy: P.eli, closed: "4 weeks ago", renewed: "5 weeks ago" },
  { id: "i7", code: "An9fG2hJ6kL3zX8cV1bN5m", email: "ana@acme.dev", name: "Ana Ribeiro", role: "admin", by: P.marcio, created: "March", status: "accepted", closedBy: P.ana, closed: "March" },
  { id: "i8", code: "Pr4iY7oU1tR5eW9qA3sD6f", email: "priya.raman@gmail.com", name: "Priya Raman", role: "member", by: P.ana, created: "9 days ago", status: "revoked", closedBy: P.ana, closed: "5 days ago" },
];

export const NORTHWIND_INVITATIONS: readonly InvitationRow[] = [
  { id: "n1", code: "Nw5hT2cJ8rFq6zMe1uVd4x", email: CHLOE_EMAIL, name: "Chloé Durand", role: "member", by: P.gus, created: "2 days ago", status: "pending", expires: "in 12 days" },
  { id: "n2", code: "Gs1aB4cD7eF0gH3iJ6kL9m", email: "gus@northwind.io", name: "Gus Lindqvist", role: "admin", by: null, created: "September", status: "accepted", closedBy: P.gus, closed: "September" },
];

export const pendingOf = (rows: readonly InvitationRow[]) => rows.filter((r) => r.status === "pending");

export interface Key {
  readonly id: string;
  readonly name: string;
  readonly prefix: string;
  readonly used: string;
}

export const MY_KEYS: Record<string, readonly Key[]> = {
  [ACME.id]: [
    { id: "k1", name: "Laptop", prefix: "dude_sk_Qm3vX9", used: "2 hours ago" },
    { id: "k2", name: "Release script", prefix: "dude_sk_a81LpE", used: "yesterday" },
  ],
  [NORTHWIND.id]: [],
};

export const NEW_KEY = "dude_sk_N7wq2c4ZkLr0sYb1pFhT8eXuAm5dJgVo";

/** The super-admin's list: every organisation on this dude. */
export const ALL_ORGS: readonly { org: Org; members: number; admins: readonly Person[]; pending: number }[] = [
  { org: ACME, members: 5, admins: [P.marcio, P.ana], pending: 3 },
  { org: NORTHWIND, members: 4, admins: [P.gus], pending: 1 },
  { org: GLOBEX, members: 0, admins: [], pending: 1 },
];

export const NORTHWIND_MEMBERS: readonly Member[] = [
  { person: P.gus, email: "gus@northwind.io", role: "admin", seen: "online", joined: "invited by the operator" },
  { person: P.marcio, email: "marcio@acme.dev", role: "member", seen: "active 2 days ago", joined: "invited by Gus" },
  { person: P.eli, email: "eli@northwind.io", role: "member", seen: "active yesterday", joined: "invited by Gus" },
  // Seeded before people had emails: works through its keys, cannot sign in with a browser.
  { person: { id: "u_ops", name: "ops-bot" }, email: null, role: "member", seen: "key used 4 hours ago" },
];

/** What "how dude works" says, once, to someone new. */
export const HOW = [
  { icon: "conductor", title: "Talk it through", body: "On a task: you and the conductor work it out in Chat, and it runs the agents." },
  { icon: "git-pr", title: "Deliver", body: "On a task: dude implements, reviews and opens a pull request on its own, and stops there." },
  { icon: "brainstorm", title: "Sessions", body: "Before there is a task: a brainstorm reads what you link and proposes work you file." },
] as const;

/* -- The invite dialog's email chips -- */

/** One address as typed or pasted: `raw` when it is not an email. */
export interface Entry {
  readonly key: string;
  readonly raw: string;
  readonly email?: string;
  readonly name?: string;
}

export type ChipState =
  | { readonly kind: "valid" }
  | { readonly kind: "renew"; readonly row: InvitationRow }
  | { readonly kind: "invalid" }
  | { readonly kind: "member"; readonly name: string }
  | { readonly kind: "invited"; readonly row: InvitationRow }
  | { readonly kind: "duplicate" };

const EMAIL = /^[^\s<>@,;"]+@[^\s<>@,;"]+\.[^\s<>@,;"]+$/;
let keys = 0;

/** `Name <email>`, `"Name" <email>` or a bare address; `name` from a spreadsheet's other cell. */
export function entryOf(text: string, name?: string): Entry {
  const raw = text.trim();
  const m = /^"?([^"<]*?)"?\s*<([^>]+)>$/.exec(raw);
  const email = (m ? m[2]! : raw).trim().toLowerCase();
  const named = (m ? m[1]!.trim() : "") || name?.trim() || undefined;
  keys += 1;
  return EMAIL.test(email) ? { key: `e${keys}`, raw, email, ...(named ? { name: named } : {}) } : { key: `e${keys}`, raw };
}

/**
 * Pasted text as entries: one per line, or per comma or semicolon outside
 * `<…>`. A line with tabs is a spreadsheet row: the cell with an `@` is the
 * address and the first other non-empty cell its name.
 */
export function parsePaste(text: string): Entry[] {
  const out: Entry[] = [];
  for (const line of text.split(/\r?\n/)) {
    if (!line.trim()) continue;
    if (line.includes("\t")) {
      const cells = line.split("\t").map((c) => c.trim());
      const at = cells.find((c) => c.includes("@"));
      if (at) out.push(entryOf(at, cells.find((c) => c && c !== at)));
      continue;
    }
    for (const part of line.split(/[,;](?![^<]*>)/)) if (part.trim()) out.push(entryOf(part));
  }
  return out;
}

/** What each chip says, in order: the first of an address wins, the rest are duplicates. */
export function chipStates(entries: readonly Entry[], members: readonly Member[], invitations: readonly InvitationRow[]): ChipState[] {
  const seen = new Set<string>();
  return entries.map((e): ChipState => {
    if (!e.email) return { kind: "invalid" };
    if (seen.has(e.email)) return { kind: "duplicate" };
    seen.add(e.email);
    const m = members.find((x) => x.email === e.email);
    if (m) return { kind: "member", name: m.person.name };
    const live = invitations.find((x) => x.email === e.email && (x.status === "pending" || x.status === "expired"));
    if (live?.status === "pending") return { kind: "invited", row: live };
    if (live) return { kind: "renew", row: live };
    return { kind: "valid" };
  });
}

/** What the dialog opens with: every chip state at once, as a pasted list would give. */
export const CHIP_EXAMPLE = `Priya Raman <priya@acme.dev>
Noah Berg\tnoah.berg@acme.dev
"Jules Moreau" <jules@acme.dev>
lin@acme.dev
tomás@
kai.nakamura@acme.dev, priya@acme.dev`;

/** The links the invite gives back, one per invitee. */
export interface MadeLink {
  readonly name?: string;
  readonly email: string;
  readonly role: Role;
  readonly code: string;
  readonly renewed?: boolean;
}

export const NEW_CODES = ["Rq6tY1uI4oP7aS0dF3gH8j", "Bn2mV5cX8zL1kJ4hG7fD0s", "Wt9eR2yU5iO8pA1sD4fG7h", "Mx3nB6vC9xZ2lK5jH8gF1d"];
