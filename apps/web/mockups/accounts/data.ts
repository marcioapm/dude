/*
 * The mockup's world: Márcio, admin of Acme, member of Northwind and the
 * deployment's super-admin; Chloé, new, invited to Acme (and, in the second
 * variant, to Northwind too); and the people, keys and invitations each
 * organisation's pages show. Deterministic.
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

export const ORGS = [ACME, NORTHWIND] as const;

/** Márcio's memberships, as the switcher lists them. */
export const MEMBERSHIPS: readonly { org: Org; role: Role; waiting: number }[] = [
  { org: ACME, role: "admin", waiting: 2 },
  { org: NORTHWIND, role: "member", waiting: 1 },
];

/** An invitation Márcio has not taken up yet. */
export const MY_INVITATIONS = [{ id: "inv_globex", org: GLOBEX, role: "member" as Role, by: P.lin, expires: "in 9 days" }];

export interface Invitation {
  readonly id: string;
  readonly org: Org;
  readonly role: Role;
  readonly by: Person | null;
  readonly sent: string;
  readonly expires: string;
}

/** Chloé's, on her first sign-in. */
export const CHLOE_INVITES: readonly Invitation[] = [
  { id: "inv_1", org: ACME, role: "member", by: P.ana, sent: "9 October", expires: "in 10 days" },
  { id: "inv_2", org: NORTHWIND, role: "member", by: P.gus, sent: "11 October", expires: "in 12 days" },
];

export const CHLOE_EMAIL = "chloe.durand@hey.com";

/** The first super-admin in the configuration: who the No organisation page names. */
export const OPERATOR = { person: P.marcio, email: "marcio@acme.dev" };

export interface Member {
  readonly person: Person;
  readonly email: string | null;
  readonly role: Role;
  readonly seen: string;
}

export const ACME_MEMBERS: readonly Member[] = [
  { person: P.marcio, email: "marcio@acme.dev", role: "admin", seen: "online" },
  { person: P.ana, email: "ana@acme.dev", role: "admin", seen: "online" },
  { person: P.tom, email: "tom@acme.dev", role: "member", seen: "active yesterday on CP-41" },
  { person: P.lin, email: "lin@acme.dev", role: "member", seen: "active 3 days ago" },
  { person: P.eli, email: "eli@northwind.io", role: "member", seen: "active last week" },
];

export interface Pending {
  readonly id: string;
  readonly email: string;
  readonly name?: string;
  readonly role: Role;
  readonly by: Person | null;
  readonly expires: string;
  readonly expired?: boolean;
}

export const ACME_PENDING: readonly Pending[] = [
  { id: "p1", email: "chloe.durand@hey.com", name: "Chloé Durand", role: "member", by: P.ana, expires: "Expires in 10 days" },
  { id: "p2", email: "sam@acme.dev", role: "admin", by: P.marcio, expires: "Expires in 13 days" },
  { id: "p3", email: "kai.nakamura@acme.dev", name: "Kai Nakamura", role: "member", by: P.ana, expires: "Expired 3 days ago", expired: true },
];

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
  { person: P.gus, email: "gus@northwind.io", role: "admin", seen: "online" },
  { person: P.marcio, email: "marcio@acme.dev", role: "member", seen: "active 2 days ago" },
  { person: P.eli, email: "eli@northwind.io", role: "member", seen: "active yesterday" },
  // Seeded before people had emails: works through its keys, cannot sign in with a browser.
  { person: { id: "u_ops", name: "ops-bot" }, email: null, role: "member", seen: "key used 4 hours ago" },
];

export const NORTHWIND_PENDING: readonly Pending[] = [
  { id: "n1", email: "chloe.durand@hey.com", name: "Chloé Durand", role: "member", by: P.gus, expires: "Expires in 12 days" },
];

/** What "how dude works" says, once, to someone new. */
export const HOW = [
  { icon: "conductor", title: "Talk it through", body: "On a task: you and the conductor work it out in Chat, and it runs the agents." },
  { icon: "git-pr", title: "Deliver", body: "On a task: dude implements, reviews and opens a pull request on its own, and stops there." },
  { icon: "brainstorm", title: "Sessions", body: "Before there is a task: a brainstorm reads what you link and proposes work you file." },
] as const;

/** One line of a bulk invite, as the dialog reads it. */
export interface Parsed {
  readonly line: string;
  readonly name?: string;
  readonly email?: string;
  readonly problem?: "unreadable" | "member" | "pending" | "duplicate";
}

const EMAIL = /^[^\s<>@,]+@[^\s<>@,]+\.[^\s<>@,]+$/;

/** `Name <email>`, `"Name" <email>` or a bare email; one per line or comma. */
export function parseInvites(text: string, members: readonly Member[], pending: readonly Pending[]): Parsed[] {
  const seen = new Set<string>();
  return text.split(/[\n,;]+/).map((l) => l.trim()).filter(Boolean).map((line): Parsed => {
    const m = /^"?([^"<]*?)"?\s*<([^>]+)>$/.exec(line);
    const email = (m ? m[2]! : line).trim().toLowerCase();
    const name = m ? m[1]!.trim() || undefined : undefined;
    if (!EMAIL.test(email)) return { line, problem: "unreadable" };
    const base = { line, email, ...(name ? { name } : {}) };
    if (seen.has(email)) return { ...base, problem: "duplicate" };
    seen.add(email);
    if (members.some((x) => x.email === email)) return { ...base, problem: "member" };
    if (pending.some((x) => x.email === email)) return { ...base, problem: "pending" };
    return base;
  });
}

export const PASTE_EXAMPLE = `Priya Raman <priya@acme.dev>
"Jules Moreau" <jules@acme.dev>
lin@acme.dev
tomás@
kai.nakamura@acme.dev`;
