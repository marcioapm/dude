/*
 * The screens of the accounts mockup, composed from the design system's
 * components. What is laid out here and not in the design system is in
 * accounts.module.css, on tokens only, and named in docs/design/accounts.md
 * where it should become a component (EmailChipsInput, InvitationLinkList).
 */

import { useRef, useState, type KeyboardEvent, type ReactNode } from "react";
import {
  ChatComposer, EntityLine, PersonAvatar, PersonAvatarStack, ProjectAvatar, RecentSessions, Segmented, SettingRow, SettingsHeader, SettingsLayout,
  SettingsNote, SettingsSection, StarterPills, Switch, WELCOME_STARTERS, Welcome, type SettingsNavItem,
} from "@dude/design-system/components";
import { Icon, type IconName } from "@dude/design-system";
import {
  Badge, Button, Callout, Card, CardBody, CardFooter, CardHeader, Dialog, FormStack, IconButton, Input, KeyValueList, Select, Table, TableEmpty, TBody, Td, Th, THead, Tr, useToast,
} from "@dude/design-system/primitives";
import { projectCounts } from "../../../../packages/design-system/src/util/navModel.ts";
import type { Person } from "../../../../packages/design-system/src/components/PersonAvatar.tsx";
import {
  ACME, ACME_INVITATIONS, ACME_MEMBERS, ALL_ORGS, CHIP_EXAMPLE, CHLOE_EMAIL, CHLOE_INVITES, CHLOE_OTHER, HOST, HOW, MEMBERSHIPS, MY_INVITATIONS, MY_KEYS, NEW_CODES,
  NEW_KEY, NORTHWIND, NORTHWIND_INVITATIONS, NORTHWIND_MEMBERS, OPERATOR, P, chipStates, inviteUrl, parsePaste, pendingOf,
  type ChipState, type Entry, type Invitation, type InvitationRow, type InvitationStatus, type MadeLink, type Member, type Org, type Role,
} from "./data.ts";
import { Mark, OrgFace, roleWord } from "./Shell.tsx";
import s from "./accounts.module.css";

const ROLES = [{ value: "member", label: "Member" }, { value: "admin", label: "Admin" }];

/** Clipboard where the page may write it (a file:// page in a test browser may not); the toast says it either way. */
function useCopy() {
  const toast = useToast();
  return (text: string, what: string) => {
    void navigator.clipboard?.writeText(text).catch(() => undefined);
    toast.toast({ title: `${what} copied`, tone: "success" });
  };
}

/** Your face and name, editable where you first see them: name and photo came from Access. */
function ProfileStrip({ person, email, compact }: { person: Person; email: string; compact?: boolean }) {
  const [name, setName] = useState(person.name);
  return (
    <div className={s.profileStrip} data-testid="profile-strip">
      <div className={s.profileFace}>
        <PersonAvatar person={{ ...person, name }} size={56} ring={false} />
        <Button size="sm" variant="quiet" leadingIcon="upload">Change photo</Button>
      </div>
      <div className={s.profileFields}>
        <Input label="Your name" value={name} onChange={(e) => setName(e.target.value)} hint="How everyone sees you, in every organisation." />
        {compact ? null : <KeyValueList items={[{ label: "Email", value: <span>{email} <span className="muted">· from Cloudflare Access</span></span> }]} />}
      </div>
    </div>
  );
}

function HowItWorks() {
  return (
    <ul className={s.how} aria-label="How dude works">
      {HOW.map((h) => (
        <li key={h.title} className={s.howItem}>
          <Icon name={h.icon as IconName} size={16} className={s.howIcon} />
          <span><b>{h.title}.</b> <span className="muted">{h.body}</span></span>
        </li>
      ))}
    </ul>
  );
}

/** Screen 1: first sign-in, after Join on Gus's link: a page inside Northwind. */
export function FirstOne({ invitation, onStart }: { invitation: Invitation; onStart: () => void }) {
  const org = invitation.org;
  const projects = org.projects.map((p) => ({ p, c: projectCounts(p, P.chloe.id) }));
  return (
    <div className={s.page} data-testid="first-one">
      <div className={s.column}>
        <header className={s.hero}>
          <OrgFace org={org} size={48} />
          <h1 className={s.heroTitle}>Welcome to {org.name}, Chloé</h1>
          <p className={s.heroLine}>You joined from {invitation.by?.name ?? "the operator"}'s invitation link, as {invitation.role === "admin" ? "an admin" : "a member"}. You're in Acme too: everyone who signs in to this dude is.</p>
        </header>
        <Card>
          <CardHeader title="You" />
          <CardBody><ProfileStrip person={P.chloe} email={CHLOE_EMAIL} /></CardBody>
        </Card>
        <Card>
          <CardHeader title={`${org.name}'s projects`} actions={<span className="muted">{projects.length} projects · 4 people</span>} />
          <CardBody padding="flush">
            <ul className={s.rows}>
              {projects.map(({ p, c }) => (
                <li key={p.id} className={s.row}>
                  <EntityLine lead={<ProjectAvatar project={{ id: p.id, name: p.name }} size={24} />} name={p.name}
                    detail={[c.active ? `${c.active} running` : null, c.ready ? `${c.ready} ready` : null].filter(Boolean).join(" · ") || "Nothing running"} />
                </li>
              ))}
            </ul>
          </CardBody>
        </Card>
        <section className={s.howBlock}>
          <h2 className="ds-label">How dude works</h2>
          <HowItWorks />
        </section>
        <div className={s.next}>
          <Button variant="primary" size="lg" leadingIcon="brainstorm" onClick={onStart} data-testid="start">Start your first session</Button>
          <span className="muted">Or open a project in the sidebar.</span>
        </div>
      </div>
    </div>
  );
}

/** A page outside every organisation: dude's face, no sidebar. */
function Outside({ children, testId }: { children: ReactNode; testId: string }) {
  return (
    <div className={s.outside} data-testid={testId}>
      <div className={s.outsideColumn}>
        <div className={s.outsideMark}><Mark size={72} /></div>
        {children}
      </div>
    </div>
  );
}

function SignedInAs({ email }: { email: string }) {
  return <p className={s.fine}>Signed in as <b>{email}</b> with Cloudflare Access. <button type="button" className={s.link}>Sign out</button></p>;
}

/**
 * Screen 1, second variant: Chloé signs in at the bare address while two
 * invitations wait for her email (she lost the links, or opened one and the
 * other was sent too). She is already in Acme, which is open to everyone;
 * the other two wait for her answer.
 */
export function FirstTwo({ onJoin, onContinue }: { onJoin: (org: Org) => void; onContinue: () => void }) {
  const [state, setState] = useState<Record<string, "joined" | "later">>({});
  return (
    <Outside testId="first-two">
      <header className={s.hero}>
        <h1 className={s.heroTitle}>You're invited, Chloé</h1>
        <p className={s.heroLine}>Two organisations on this dude sent you an invitation link. Join either here; the other waits in your menu.</p>
      </header>
      <Callout tone="info">You're already in Acme: everyone who signs in to this dude joins it as a member.</Callout>
      <Card>
        <CardHeader title="You" />
        <CardBody><ProfileStrip person={P.chloe} email={CHLOE_EMAIL} /></CardBody>
      </Card>
      <ul className={s.invites}>
        {CHLOE_INVITES.map((i) => <InviteCard key={i.id} i={i} state={state[i.id]} onJoin={() => { setState((x) => ({ ...x, [i.id]: "joined" })); onJoin(i.org); }}
          onLater={() => setState((x) => ({ ...x, [i.id]: "later" }))} />)}
      </ul>
      <div className={s.next}><Button variant="secondary" trailingIcon="arrow-right" onClick={onContinue}>Continue to Acme</Button></div>
      <SignedInAs email={CHLOE_EMAIL} />
    </Outside>
  );
}

function InviteCard({ i, state, onJoin, onLater }: { i: Invitation; state?: "joined" | "later" | undefined; onJoin: () => void; onLater: () => void }) {
  return (
    <li>
      <Card variant="raised">
        <div className={s.invite}>
          <OrgFace org={i.org} size={40} />
          <div className={s.inviteText}>
            <b className={s.inviteName}>{i.org.name}</b>
            <span className="muted">{roleWord(i.role)} · {i.by?.name ?? "The operator"} sent you this link on {i.sent} · expires {i.expires}</span>
            <span className="muted">{i.org.projects.length ? `${i.org.projects.length} projects` : "New: no projects yet"}</span>
          </div>
          <div className={s.inviteActions}>
            {state === "later" ? <span className="muted">In your menu for later</span> : (
              <>
                <Button variant="quiet" onClick={onLater}>Not now</Button>
                <Button variant="primary" onClick={onJoin} data-testid={`join-${i.org.slug}`}>Join {i.org.name}</Button>
              </>
            )}
          </div>
        </div>
      </Card>
    </li>
  );
}

/** Screen 2: signed in, in no organisation: none is open to everyone and no invitation waits. */
export function NoOrg({ expired }: { expired: boolean }) {
  return (
    <Outside testId="no-org">
      <header className={s.hero}>
        <h1 className={s.heroTitle}>Your email isn't in any organisation on this dude</h1>
        <p className={s.heroLine}>You signed in as <b>{CHLOE_EMAIL}</b>. Ask an admin of the organisation you work with for an invitation link.</p>
      </header>
      {expired ? (
        <Callout tone="attention">Your invitation to Northwind expired on 2 October. Ask Gus Lindqvist for a new link.</Callout>
      ) : null}
      <Card>
        <CardHeader title="Who to ask" />
        <CardBody>
          <FormStack>
            <p className="muted">An admin of the organisation can invite you from its Members page and send you the link. If you don't know who that is, ask whoever runs this dude:</p>
            <EntityLine lead={<PersonAvatar person={OPERATOR.person} size={32} />} name={OPERATOR.person.name} detail={OPERATOR.email} />
            <p className="muted">Signed in with the wrong address? Sign out, then sign in with the one you were invited with.</p>
          </FormStack>
        </CardBody>
        <CardFooter>
          <Button variant="secondary" leadingIcon="retry">Check again</Button>
          <Button variant="quiet" leadingIcon="arrow-right">Sign out</Button>
        </CardFooter>
      </Card>
    </Outside>
  );
}

/** After switching: the organisation's welcome, its sidebar, its URL. */
export function OrgWelcome({ org, greeting = "Afternoon, Márcio" }: { org: Org; greeting?: string }) {
  return (
    <Welcome data-testid="org-welcome" mark={<Mark size="fill" />} greeting={greeting} line={`What are we working out in ${org.name} today?`}
      composer={<ChatComposer mode="chat" variant="stage" placeholder="Start a session: an idea, a question, a plan…" onSubmit={() => true} to={<>To <b>Brainstorm</b></>} />}
      starters={<StarterPills starters={WELCOME_STARTERS} onPick={() => undefined} />}
      footer={<RecentSessions sessions={org.id === ACME.id
        ? [{ id: "s1", title: "Usage-based billing", summary: "Filed 1 epic, 4 tasks", age: "yesterday" }, { id: "s2", title: "Q4 cleanup ideas", summary: "Nothing filed yet", age: "3 days ago" }]
        : org.id === NORTHWIND.id ? [{ id: "n1", title: "Portal invoice VAT", summary: "Filed 2 tasks", age: "2 days ago" }] : []} onOpen={() => undefined} onAll={() => undefined} />} />
  );
}

/* -- Screen 7: the accept page, #/invite/<code> -- */

export type AcceptState = "join" | "first" | "wrong" | "member" | "invalid";

/**
 * Every state of `#/invite/<code>`, outside any organisation. Chloé's link
 * is Gus's, to Northwind; Ana's older one is to Acme, which Chloé joined on
 * her first sign-in because Acme is open to everyone.
 */
export function AcceptPage({ state, onJoin, onOpen }: { state: AcceptState; onJoin: (org: Org) => void; onOpen: (org: Org) => void }) {
  const gus = CHLOE_INVITES[0]!;
  if (state === "invalid") {
    return (
      <Outside testId="accept-invalid">
        <Card>
          <CardBody>
            <div className={s.accept}>
              <span className={s.acceptIcon}><Icon name="link" size={20} /></span>
              <h1 className={s.acceptTitle}>This invitation can't be used</h1>
              <p className={s.acceptLine}>Ask whoever sent it for a new link.</p>
            </div>
          </CardBody>
          <CardFooter><Button variant="secondary" leadingIcon="arrow-right">Open dude</Button></CardFooter>
        </Card>
        <SignedInAs email={CHLOE_EMAIL} />
      </Outside>
    );
  }
  if (state === "wrong") {
    return (
      <Outside testId="accept-wrong">
        <Card>
          <CardBody>
            <div className={s.accept}>
              <OrgFace org={NORTHWIND} size={48} />
              <h1 className={s.acceptTitle}>This invitation is for another address</h1>
              <p className={s.acceptLine}>Gus Lindqvist invited <b>{CHLOE_EMAIL}</b> to Northwind. You're signed in as <b>{CHLOE_OTHER}</b>.</p>
              <p className={s.acceptLine}>If both are yours, sign out and sign in again with the invited one. If not, ask Gus for a link for {CHLOE_OTHER}.</p>
            </div>
          </CardBody>
          <CardFooter>
            <Button variant="primary" leadingIcon="arrow-right" data-testid="switch-account">Sign out and use another account</Button>
          </CardFooter>
        </Card>
        <SignedInAs email={CHLOE_OTHER} />
      </Outside>
    );
  }
  if (state === "member") {
    return (
      <Outside testId="accept-member">
        <Card>
          <CardBody>
            <div className={s.accept}>
              <OrgFace org={ACME} size={48} />
              <h1 className={s.acceptTitle}>You're already in Acme</h1>
              <p className={s.acceptLine}>Ana Ribeiro's invitation is marked accepted; you joined Acme when you first signed in. Your role stays Member.</p>
            </div>
          </CardBody>
          <CardFooter><Button variant="primary" onClick={() => onOpen(ACME)} data-testid="open-org">Open Acme</Button></CardFooter>
        </Card>
        <SignedInAs email={CHLOE_EMAIL} />
      </Outside>
    );
  }
  const i = gus;
  return (
    <Outside testId={state === "first" ? "accept-first" : "accept-join"}>
      <Card>
        <CardBody>
          <div className={s.accept}>
            <OrgFace org={i.org} size={48} />
            <h1 className={s.acceptTitle}>Join {i.org.name} as {roleWord(i.role).toLowerCase()}</h1>
            <p className={s.acceptLine}>Invited by {i.by?.name ?? "the operator"} on {i.sent}.</p>
            <KeyValueList className={s.acceptFacts} items={[
              { label: "For", value: <span>{CHLOE_EMAIL} <span className="muted">· you</span></span> },
              { label: "Role", value: roleWord(i.role) },
              { label: "Expires", value: i.expires },
            ]} />
          </div>
        </CardBody>
        {state === "first" ? (
          <CardBody>
            <Callout tone="info">You're in Acme already: anyone who signs in to this dude joins it as a member. Not now leaves you in Acme only, with this invitation in your menu.</Callout>
          </CardBody>
        ) : null}
        <CardFooter>
          <Button variant="quiet" data-testid="not-now">Not now</Button>
          <Button variant="primary" onClick={() => onJoin(i.org)} data-testid="accept-join-button">Join {i.org.name}</Button>
        </CardFooter>
      </Card>
      <SignedInAs email={CHLOE_EMAIL} />
    </Outside>
  );
}

/* -- Screen 4: Your settings -- */

export function YourSettings({ reveal, onReveal }: { reveal: Org | null; onReveal: (org: Org | null) => void }) {
  const [appearance, setAppearance] = useState(false);
  const globex = MY_INVITATIONS[0]!;
  return (
    <div className={s.settingsPage} data-testid="your-settings">
      <SettingsHeader title="Your settings" description="Only you see these. Your profile and keys are yours in every organisation; how dude looks is this browser's." />
      <Card>
        <CardHeader title="Profile" />
        <CardBody>
          <FormStack>
            <p className="muted">The same in every organisation you're in: Acme and Northwind see this name and photo.</p>
            <ProfileStrip person={P.marcio} email="marcio@acme.dev" compact />
            <KeyValueList items={[
              { label: "Email", value: <span>marcio@acme.dev <span className="muted">· from Cloudflare Access, can't be changed here</span></span> },
              { label: "Signs in with", value: "Cloudflare Access" },
            ]} />
          </FormStack>
        </CardBody>
        <CardFooter><Button variant="primary" disabled>Save</Button></CardFooter>
      </Card>
      <Card data-testid="keys">
        <CardHeader title="Keys" />
        <CardBody>
          <FormStack>
            <p className="muted">For the dude CLI and scripts. A key acts as you in one organisation; make one in each you work in.</p>
            {MEMBERSHIPS.map(({ org, role }) => {
              const keys = MY_KEYS[org.id] ?? [];
              return (
                <section key={org.id} className={s.keyGroup} data-testid={`keys-${org.slug}`}>
                  <div className={s.keyHead}>
                    <OrgFace org={org} size={20} />
                    <b>{org.name}</b>
                    <span className="muted">{roleWord(role)}</span>
                    <span className={s.spacer} />
                    <Button size="sm" variant="secondary" leadingIcon="plus" onClick={() => onReveal(org)} data-testid={`new-key-${org.slug}`}>New key for {org.name}</Button>
                  </div>
                  {keys.length ? (
                    <Table>
                      <THead><Tr><Th>Name</Th><Th>Key</Th><Th>Last used</Th><Th align="right"><span className="ds-sr-only">Actions</span></Th></Tr></THead>
                      <TBody>
                        {keys.map((k) => (
                          <Tr key={k.id}><Td>{k.name}</Td><Td mono muted>{k.prefix}…</Td><Td muted>{k.used}</Td>
                            <Td align="right" fit><Button size="sm" variant="quiet">Revoke…</Button></Td></Tr>
                        ))}
                      </TBody>
                    </Table>
                  ) : <p className={s.none}>No keys for {org.name} yet.</p>}
                </section>
              );
            })}
          </FormStack>
        </CardBody>
      </Card>
      <Card>
        <CardHeader title="Organisations" />
        <CardBody padding="flush">
          <ul className={s.rows}>
            {MEMBERSHIPS.map(({ org, role }) => (
              <li key={org.id} className={s.row}>
                <EntityLine lead={<OrgFace org={org} size={24} />} name={org.name} detail={`${roleWord(role)} · ${HOST}/#/o/${org.slug}`} />
              </li>
            ))}
            <li className={s.row}>
              <EntityLine lead={<OrgFace org={globex.org} size={24} />} name={globex.org.name} detail={`Invited by ${globex.by?.name ?? "the operator"} · expires ${globex.expires}`} />
              <span className={s.rowActions}><Button size="sm" variant="quiet">Not now</Button><Button size="sm" variant="secondary">Join</Button></span>
            </li>
          </ul>
        </CardBody>
      </Card>
      <Card>
        <CardHeader title="Appearance and notifications" actions={<Button size="sm" variant="quiet" trailingIcon={appearance ? "chevron-up" : "chevron-down"} onClick={() => setAppearance((a) => !a)}>{appearance ? "Hide" : "Show"}</Button>} />
        {appearance ? <CardBody><p className="muted">Theme, density and this browser's notifications: unchanged from today.</p></CardBody> : null}
      </Card>
      {reveal ? <KeyReveal org={reveal} onClose={() => onReveal(null)} /> : null}
    </div>
  );
}

function KeyReveal({ org, onClose }: { org: Org; onClose: () => void }) {
  const [named, setNamed] = useState(false);
  const [name, setName] = useState("Laptop");
  if (!named) {
    return (
      <Dialog open onOpenChange={(o) => !o && onClose()} size="sm" title={`New key for ${org.name}`}
        description={`It acts as you in ${org.name} only. Name it for where it will live, so you know which to revoke.`}
        footer={<><Button variant="quiet" onClick={onClose}>Cancel</Button><Button variant="primary" onClick={() => setNamed(true)} data-testid="key-create">Create key</Button></>}>
        <Input label="Name" autoFocus value={name} onChange={(e) => setName(e.target.value)} placeholder="Laptop" />
      </Dialog>
    );
  }
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} size="md" title={`Key “${name}” for ${org.name}`} description="Copy it now: it is not shown again."
      footer={<Button variant="primary" onClick={onClose}>Done</Button>}>
      <FormStack>
        <Input label="Key" mono readOnly value={NEW_KEY} trailing={<Button size="sm" variant="quiet" leadingIcon="copy">Copy</Button>} data-testid="key-secret" />
        <div>
          <span className="ds-label">Use it with the CLI</span>
          <pre className={s.cli}><code>{`dude login https://${HOST}\n# paste the key; the CLI keeps it as "${org.slug}"\ndude whoami\n# Márcio Martins · ${org.name} · ${roleWord(MEMBERSHIPS.find((m) => m.org.id === org.id)?.role ?? "member")}`}</code></pre>
          <p className="muted">With keys for several organisations, the CLI uses the last one you logged in with; <code>--org {org.slug}</code> picks one.</p>
        </div>
      </FormStack>
    </Dialog>
  );
}

/* -- Screen 5: Members -- */

function MembersTable({ members, you, admin, onRemove, keyOnly }: { members: readonly Member[]; you?: string; admin: boolean; onRemove?: (m: Member) => void; keyOnly?: boolean }) {
  return (
    <Table density="comfortable" data-testid="members">
      <THead><Tr><Th width="45%">Person</Th><Th>Joined</Th><Th>Role</Th><Th align="right"><span className="ds-sr-only">Actions</span></Th></Tr></THead>
      <TBody>
        {members.map((m) => {
          const me = m.person.id === you;
          return (
            <Tr key={m.person.id}>
              <Td><EntityLine lead={<PersonAvatar person={m.person} size={32} aria-hidden title="" />} name={m.person.name}
                detail={[me ? "you" : null, m.email ?? "no email: signs in only with a key", m.seen].filter(Boolean).join(" · ")} /></Td>
              <Td wrap muted>{m.joined ?? "—"}</Td>
              <Td>{admin && !me ? <Select size="sm" aria-label={`Role of ${m.person.name}`} value={m.role} options={ROLES} /> : m.role === "admin" ? <Badge>Admin</Badge> : <span className="muted">Member</span>}</Td>
              <Td align="right" fit>
                <span className={s.rowActions}>
                  {keyOnly && !m.email ? <Button size="sm" variant="secondary">Set email…</Button> : null}
                  {admin && !me ? <Button size="sm" variant="quiet" onClick={() => onRemove?.(m)}>Remove…</Button> : null}
                </span>
              </Td>
            </Tr>
          );
        })}
      </TBody>
    </Table>
  );
}

const orgNav = (pending: number): SettingsNavItem[] => [
  { id: "members", label: "Members", icon: "human" },
  { id: "invitations", label: "Invitations", icon: "link", note: pending ? `${pending} pending` : undefined },
  { id: "general", label: "General", icon: "settings" },
  { id: "github", label: "GitHub", icon: "git-branch" },
  { id: "agents", label: "Agents", icon: "agent" },
  { id: "network", label: "Network", icon: "globe" },
  { id: "models", label: "Models", icon: "sparkle" },
  { id: "machines", label: "Machines", icon: "chip" },
  { id: "delivery", label: "Delivery", icon: "list-check" },
  { id: "memory", label: "Memory", icon: "memory" },
];

export type MembersDialog = "none" | "invite" | "links" | "remove";

/** Screen 5: Organisation settings → Members. */
export function MembersPage({ dialog, onDialog, onPage }: { dialog: MembersDialog; onDialog: (d: MembersDialog) => void; onPage: (id: string) => void }) {
  const [removing, setRemoving] = useState<Member>(ACME_MEMBERS[2]!);
  return (
    <SettingsLayout data-testid="org-members" scope={{ title: "Acme", subtitle: "Organisation settings", leading: <OrgFace org={ACME} size={32} /> }}
      items={orgNav(pendingOf(ACME_INVITATIONS).length)} current="members" onSelect={onPage}>
      <SettingsNote>Everyone here can create, deliver and steer tasks. Admins manage the organisation: its members, invitations and settings.</SettingsNote>
      <SettingsHeader title="Members" description="Who is in Acme. Anyone who signs in to this dude joins Acme as a member; invite someone to make them an admin, to bring back someone removed, or to send them a link."
        actions={<Button variant="primary" leadingIcon="plus" onClick={() => onDialog("invite")} data-testid="invite">Invite people</Button>} />
      <SettingsSection title={`Members · ${ACME_MEMBERS.length}`}>
        <MembersTable members={ACME_MEMBERS} you={P.marcio.id} admin onRemove={(m) => { setRemoving(m); onDialog("remove"); }} />
      </SettingsSection>
      {dialog === "invite" || dialog === "links" ? (
        <InviteDialog org={ACME} members={ACME_MEMBERS} invitations={ACME_INVITATIONS} done={dialog === "links"}
          onDone={(d) => onDialog(d ? "links" : "invite")} onClose={() => onDialog("none")} />
      ) : null}
      {dialog === "remove" ? (
        <Dialog open onOpenChange={(o) => !o && onDialog("none")} tone="danger" size="sm" title={`Remove ${removing.person.name} from Acme?`}
          description={<>
            Their keys for Acme stop working at once, and so does their next page in Acme. Tasks they own pass to the next person on them; what they did keeps their name.
            {" "}<b>Anyone who signs in joins Acme, but not someone removed:</b> only an invitation brings them back, and their old keys stay revoked.
          </>}
          footer={<><Button variant="quiet" onClick={() => onDialog("none")}>Cancel</Button><Button variant="danger" solid>Remove {removing.person.name.split(" ")[0]}</Button></>} />
      ) : null}
    </SettingsLayout>
  );
}

/* -- The invite dialog: email chips, then a link per invitee -- */

const CHIP: Record<ChipState["kind"], { tone: string; icon: IconName }> = {
  valid: { tone: "ok", icon: "check" },
  renew: { tone: "info", icon: "retry" },
  invalid: { tone: "bad", icon: "warning" },
  member: { tone: "skip", icon: "human" },
  invited: { tone: "info", icon: "link" },
  duplicate: { tone: "skip", icon: "minus" },
};

function chipWords(st: ChipState, org: Org): string | null {
  switch (st.kind) {
    case "valid": return null;
    case "renew": return `invitation expired ${st.row.expires}: Invite renews it`;
    case "invalid": return "not an email";
    case "member": return `already in ${org.name}`;
    case "invited": return `already invited ${st.row.created}`;
    case "duplicate": return "twice in the list";
  }
}

/**
 * Emails as chips. Enter, a comma, a space or leaving the field turn what is
 * typed into a chip; pasting splits by line, comma and spreadsheet cell and
 * keeps names. Backspace in the empty field takes the last chip back.
 */
function EmailChips({ entries, onChange, states, org, onCopyLink }: {
  entries: readonly Entry[];
  onChange: (e: Entry[]) => void;
  states: readonly ChipState[];
  org: Org;
  onCopyLink: (row: InvitationRow) => void;
}) {
  const [draft, setDraft] = useState("");
  const input = useRef<HTMLInputElement>(null);
  const commit = (text: string) => {
    const got = parsePaste(text);
    if (got.length) onChange([...entries, ...got]);
    setDraft("");
  };
  const onKey = (e: KeyboardEvent<HTMLInputElement>) => {
    if ((e.key === "Enter" || e.key === "," || e.key === " ") && draft.trim()) {
      e.preventDefault();
      commit(draft);
    } else if (e.key === "Backspace" && draft === "" && entries.length) {
      e.preventDefault();
      onChange(entries.slice(0, -1));
    }
  };
  return (
    <div className={s.chipField}>
      <label className="ds-label" htmlFor="invite-emails">Emails</label>
      <div className={s.chipBox} onClick={() => input.current?.focus()} data-testid="chips">
        {entries.map((e, i) => {
          const st = states[i]!;
          const words = chipWords(st, org);
          return (
            <span key={e.key} className={s.chip} data-tone={CHIP[st.kind].tone} data-state={st.kind} title={e.email ?? e.raw}>
              <Icon name={CHIP[st.kind].icon} size={12} className={s.chipIcon} />
              <span className={s.chipText}>
                {e.name ? <><b>{e.name}</b> <span className={s.chipMail}>{e.email}</span></> : <span className={s.chipMail}>{e.email ?? e.raw}</span>}
              </span>
              {words ? <span className={s.chipWhy}>{words}</span> : null}
              {st.kind === "invited" ? (
                <button type="button" className={s.chipAction} onClick={(ev) => { ev.stopPropagation(); onCopyLink(st.row); }}>Copy their link</button>
              ) : null}
              <IconButton size="sm" icon="close" label={`Remove ${e.email ?? e.raw}`} className={s.chipX}
                onClick={(ev) => { ev.stopPropagation(); onChange(entries.filter((x) => x.key !== e.key)); }} />
            </span>
          );
        })}
        <input ref={input} id="invite-emails" className={s.chipInput} value={draft} placeholder={entries.length ? "" : "ana@acme.dev, Tom Okafor <tom@acme.dev>"}
          onChange={(e) => setDraft(e.target.value)} onKeyDown={onKey} onBlur={() => draft.trim() && commit(draft)}
          onPaste={(e) => { e.preventDefault(); commit(draft + e.clipboardData.getData("text")); }} data-testid="chip-input" />
      </div>
      <small className="muted">Type or paste. Enter, comma or space adds one; pasting “Name &lt;email&gt;” or a column from a spreadsheet keeps the names.</small>
    </div>
  );
}

function InviteDialog({ org, members, invitations, done, onDone, onClose, admin }: {
  org: Org;
  members: readonly Member[];
  invitations: readonly InvitationRow[];
  done: boolean;
  onDone: (done: boolean) => void;
  onClose: () => void;
  admin?: boolean;
}) {
  const [entries, setEntries] = useState<Entry[]>(() => (admin ? parsePaste("Ingrid Svensson <ingrid@northwind.io>") : parsePaste(CHIP_EXAMPLE)));
  const [role, setRole] = useState<Role>(admin ? "admin" : "member");
  const copy = useCopy();
  const states = chipStates(entries, members, invitations);
  const take = entries.map((e, i) => ({ e, st: states[i]! })).filter(({ st }) => st.kind === "valid" || st.kind === "renew");
  const counts = states.reduce<Record<string, number>>((a, st) => ({ ...a, [st.kind]: (a[st.kind] ?? 0) + 1 }), {});
  const made: MadeLink[] = take.map(({ e, st }, i) => ({
    email: e.email!, role, ...(e.name ? { name: e.name } : {}),
    ...(st.kind === "renew" ? { code: st.row.code, renewed: true } : { code: NEW_CODES[i % NEW_CODES.length]! }),
  }));
  if (done) {
    return (
      <Dialog open onOpenChange={(o) => !o && onClose()} size="lg" title={made.length === 1 ? `1 invitation to ${org.name}` : `${made.length} invitations to ${org.name}`}
        description="dude sends no email. Send each person their link: it works only for their address, for 14 days. Cloudflare Access must let them in too."
        footer={<>
          <Button variant="secondary" leadingIcon="copy" onClick={() => copy(made.map((m) => [m.name, m.email, inviteUrl(m.code)].filter(Boolean).join(", ")).join("\n"), "All links")} data-testid="copy-all">Copy all</Button>
          <Button variant="primary" onClick={onClose}>Done</Button>
        </>}>
        <InvitationLinkList links={made} onCopy={(m) => copy(inviteUrl(m.code), `${m.name ?? m.email}'s link`)} />
        <p className="muted">They're on the Invitations page too, with their links, until they join or the link expires.</p>
      </Dialog>
    );
  }
  const summary = [
    counts.valid ? `${counts.valid} new` : null,
    counts.renew ? `${counts.renew} renewed` : null,
    counts.invalid ? `${counts.invalid} not an email` : null,
    counts.member ? `${counts.member} already in ${org.name}` : null,
    counts.invited ? `${counts.invited} already invited` : null,
    counts.duplicate ? `${counts.duplicate} duplicate` : null,
  ].filter(Boolean).join(" · ");
  const skipped = entries.length - take.length;
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} size="lg" title={`Invite people to ${org.name}`}
      description="You get a link for each person to send them yourself. A link works only for its address."
      footer={<><Button variant="quiet" onClick={onClose}>Cancel</Button>
        <Button variant="primary" disabled={!take.length} onClick={() => onDone(true)} data-testid="invite-submit">{take.length === 1 ? "Invite 1 person" : `Invite ${take.length} people`}</Button></>}>
      <FormStack>
        <EmailChips entries={entries} onChange={setEntries} states={states} org={org}
          onCopyLink={(row) => copy(inviteUrl(row.code), `${row.name ?? row.email}'s link`)} />
        {summary ? <p className={s.chipSummary} data-testid="chip-summary">{summary}.{skipped ? ` The other ${skipped} ${skipped === 1 ? "is" : "are"} left out.` : ""}</p> : null}
        <Select label="Role" value={role} options={ROLES} onValueChange={(v) => setRole(v as Role)} hint="For everyone in this invitation. Change one person's role on Members once they've joined." />
      </FormStack>
    </Dialog>
  );
}

/** Each invitee with their link and Copy. */
function InvitationLinkList({ links, onCopy }: { links: readonly MadeLink[]; onCopy: (m: MadeLink) => void }) {
  return (
    <ul className={s.linkList} data-testid="links">
      {links.map((m) => (
        <li key={m.email} className={s.linkRow}>
          <span className={s.linkWho}>
            <b>{m.name ?? m.email}</b>
            <span className="muted">{[m.name ? m.email : null, roleWord(m.role), m.renewed ? "renewed, same link" : null].filter(Boolean).join(" · ")}</span>
          </span>
          <code className={s.linkUrl}>{inviteUrl(m.code)}</code>
          <Button size="sm" variant="secondary" leadingIcon="copy" onClick={() => onCopy(m)}>Copy</Button>
        </li>
      ))}
    </ul>
  );
}

/* -- Screen 6: Invitations -- */

export type Filter = InvitationStatus | "all";

const STATUS: Record<InvitationStatus, { label: string; tone: "info" | "attention" | "success" | "neutral" }> = {
  pending: { label: "Pending", tone: "info" },
  expired: { label: "Expired", tone: "attention" },
  accepted: { label: "Accepted", tone: "success" },
  revoked: { label: "Revoked", tone: "neutral" },
};

/** Server-side in the product: the filter and search are query parameters, so a pending invitation never hides past a page. */
function filtered(rows: readonly InvitationRow[], filter: Filter, q: string) {
  const needle = q.trim().toLowerCase();
  return rows.filter((r) => (filter === "all" || r.status === filter)
    && (!needle || r.email.includes(needle) || (r.name ?? "").toLowerCase().includes(needle)));
}

function InvitationsList({ org, rows, filter, onFilter, query, onQuery, onRevoke, onInvite }: {
  org: Org;
  rows: readonly InvitationRow[];
  filter: Filter;
  onFilter: (f: Filter) => void;
  query: string;
  onQuery: (q: string) => void;
  onRevoke: (r: InvitationRow) => void;
  onInvite?: () => void;
}) {
  const copy = useCopy();
  const toast = useToast();
  const count = (st: InvitationStatus) => rows.filter((r) => r.status === st).length;
  const shown = filtered(rows, filter, query);
  return (
    <>
      <div className={s.filters}>
        <Segmented size="sm" label="Status" value={filter} onChange={onFilter} data-testid="status-filter"
          options={[
            { value: "pending", label: `Pending ${count("pending")}` },
            { value: "expired", label: `Expired ${count("expired")}` },
            { value: "accepted", label: `Accepted ${count("accepted")}` },
            { value: "revoked", label: `Revoked ${count("revoked")}` },
            { value: "all", label: `All ${rows.length}` },
          ]} />
        <Input size="sm" aria-label="Search invitations" placeholder="Search email or name" value={query} onChange={(e) => onQuery(e.target.value)}
          leading={<Icon name="search" size={14} />} className={s.search} data-testid="invite-search" />
      </div>
      <Table density="comfortable" data-testid="invitations">
        <THead><Tr><Th width="36%">Invited</Th><Th width="26%">Status</Th><Th>Role</Th><Th>Expires</Th><Th align="right"><span className="ds-sr-only">Actions</span></Th></Tr></THead>
        <TBody>
          {shown.length ? shown.map((r) => (
            <Tr key={r.id} data-status={r.status}>
              <Td><EntityLine lead={<span className={s.envelope}><Icon name="link" size={14} /></span>} name={r.name ?? r.email}
                detail={[r.name ? r.email : null, `by ${r.by ? r.by.name.split(" ")[0] : "the operator"}, ${r.created}`].filter(Boolean).join(" · ")} /></Td>
              <Td wrap>
                <span className={s.statusCell}>
                  <Badge tone={STATUS[r.status].tone} dot>{STATUS[r.status].label}</Badge>
                  {r.status === "accepted" && r.closedBy ? (
                    <span className="muted">by <button type="button" className={s.link}>{r.closedBy.name}</button>, {r.closed}</span>
                  ) : r.status === "revoked" && r.closedBy ? <span className="muted">by {r.closedBy.name.split(" ")[0]}, {r.closed}</span> : null}
                </span>
              </Td>
              <Td fit>{r.role === "admin" ? <Badge>Admin</Badge> : <span className="muted">Member</span>}</Td>
              <Td fit muted>{r.status === "pending" ? r.expires : r.status === "expired" ? `expired ${r.expires}` : "—"}</Td>
              <Td align="right" fit>
                <span className={s.rowActions}>
                  {r.status === "pending" ? <>
                    <Button size="sm" variant="secondary" leadingIcon="copy" onClick={() => copy(inviteUrl(r.code), `${r.name ?? r.email}'s link`)}>Copy link</Button>
                    <Button size="sm" variant="quiet" onClick={() => onRevoke(r)}>Revoke…</Button>
                  </> : r.status === "expired" ? (
                    <Button size="sm" variant="secondary" leadingIcon="retry" onClick={() => toast.toast({ title: `Renewed for 14 days`, description: `${r.name ?? r.email}'s link works again: the same one you sent.`, tone: "success" })}>Renew</Button>
                  ) : r.status === "revoked" ? (
                    <Button size="sm" variant="quiet" leadingIcon="plus" onClick={onInvite}>Invite again</Button>
                  ) : null}
                </span>
              </Td>
            </Tr>
          )) : <TableEmpty colSpan={5}>{query ? `No invitation matches “${query}”.` : `No ${filter === "all" ? "" : `${filter} `}invitations to ${org.name}.`}</TableEmpty>}
        </TBody>
      </Table>
    </>
  );
}

export type InvitationsView = "pending" | "all" | "search" | "revoke";

export function InvitationsPage({ view, onView, onPage }: { view: InvitationsView; onView: (v: InvitationsView) => void; onPage: (id: string) => void }) {
  const [filter, setFilter] = useState<Filter>(view === "all" ? "all" : "pending");
  const [query, setQuery] = useState(view === "search" ? "kai" : "");
  const [revoking, setRevoking] = useState<InvitationRow>(ACME_INVITATIONS[1]!);
  const [inviting, setInviting] = useState<false | "invite" | "links">(false);
  return (
    <SettingsLayout data-testid="org-invitations" scope={{ title: "Acme", subtitle: "Organisation settings", leading: <OrgFace org={ACME} size={32} /> }}
      items={orgNav(pendingOf(ACME_INVITATIONS).length)} current="invitations" onSelect={onPage}>
      <SettingsHeader title="Invitations" description="Every invitation to Acme and what became of it. A link works only for its address, for 14 days; Renew gives an expired one 14 more."
        actions={<Button variant="primary" leadingIcon="plus" onClick={() => setInviting("invite")}>Invite people</Button>} />
      <InvitationsList org={ACME} rows={ACME_INVITATIONS} filter={view === "search" ? "all" : filter} onFilter={(f) => { setFilter(f); if (view === "search") onView(f === "all" ? "all" : "pending"); }}
        query={query} onQuery={setQuery} onRevoke={(r) => { setRevoking(r); onView("revoke"); }} onInvite={() => setInviting("invite")} />
      {view === "revoke" ? (
        <Dialog open onOpenChange={(o) => !o && onView("pending")} tone="danger" size="sm" title={`Revoke ${revoking.name ?? revoking.email}'s invitation?`}
          description={`The link stops working at once; opening it says it can't be used. If they already joined, this changes nothing. You can invite ${revoking.email} again later, with a new link.`}
          footer={<><Button variant="quiet" onClick={() => onView("pending")}>Cancel</Button><Button variant="danger" solid onClick={() => onView("pending")}>Revoke</Button></>} />
      ) : null}
      {inviting ? <InviteDialog org={ACME} members={ACME_MEMBERS} invitations={ACME_INVITATIONS} done={inviting === "links"}
        onDone={(d) => setInviting(d ? "links" : "invite")} onClose={() => setInviting(false)} /> : null}
    </SettingsLayout>
  );
}

/* -- Screen 8: super-admin, outside any organisation -- */

const ADMIN_NAV: SettingsNavItem[] = [
  { id: "orgs", label: "Organisations", icon: "building", note: String(ALL_ORGS.length) },
];

export type AdminView = "list" | "create" | "org" | "open";

export function AdminPage({ view, onView, onBack }: { view: AdminView; onView: (v: AdminView) => void; onBack: () => void }) {
  const [inviting, setInviting] = useState<false | "invite" | "links">(false);
  const [filter, setFilter] = useState<Filter>("pending");
  const [query, setQuery] = useState("");
  const [open, setOpen] = useState(false);
  const nw = NORTHWIND;
  return (
    <SettingsLayout data-testid="admin" className={s.admin}
      scope={{ title: "This dude", subtitle: "Super-admin", leading: <span className={s.adminMark}><Mark size={32} /></span> }}
      items={ADMIN_NAV} current="orgs" onSelect={() => onView("list")}
      footer={<div className={s.adminFoot}>
        <p className="muted">Outside every organisation: this page shows no organisation's work. You're here because your email is in <code>auth.super_admins</code>.</p>
        <Button size="sm" variant="secondary" leadingIcon="arrow-right" onClick={onBack}>Back to Acme</Button>
      </div>}>
      {view === "org" || view === "open" ? (
        <>
          <SettingsHeader title={<span className={s.inline}><OrgFace org={nw} size={24} />Northwind</span>}
            description={`${HOST}/#/o/northwind · made in September 2026 · 4 members`}
            actions={<><Button variant="quiet" onClick={() => onView("list")}>All organisations</Button><Button variant="primary" leadingIcon="plus" onClick={() => setInviting("invite")}>Invite people</Button></>} />
          <Callout tone="info">You can see who is in Northwind, invite and remove people. Its projects, tasks and settings are its members'; to work in it, invite yourself.</Callout>
          <SettingsSection title="Who can join">
            <SettingRow label="Anyone who signs in joins" htmlFor="open-to-everyone"
              help={<>Everyone Cloudflare Access lets in to this dude joins Northwind as a member on their next visit. People removed from Northwind stay out: only an invitation brings them back.</>}>
              <Switch id="open-to-everyone" checked={open} onCheckedChange={(c) => (c ? onView("open") : setOpen(false))} label={open ? "On" : "Off: by invitation only"} testId="open-switch" />
            </SettingRow>
          </SettingsSection>
          <SettingsSection title="Members · 4"><MembersTable members={NORTHWIND_MEMBERS} admin keyOnly /></SettingsSection>
          <SettingsSection title="Invitations">
            <InvitationsList org={nw} rows={NORTHWIND_INVITATIONS} filter={filter} onFilter={setFilter} query={query} onQuery={setQuery}
              onRevoke={() => undefined} onInvite={() => setInviting("invite")} />
          </SettingsSection>
          {view === "open" ? (
            <Dialog open onOpenChange={(o) => !o && onView("org")} size="sm" title="Let anyone who signs in join Northwind?"
              description="Everyone who has signed in to this dude (23 people) joins Northwind as a member on their next visit, and so does anyone Cloudflare Access lets in later. They can see all of Northwind's projects and tasks. People removed from Northwind stay out; turning this off later removes nobody."
              footer={<><Button variant="quiet" onClick={() => onView("org")}>Cancel</Button><Button variant="primary" onClick={() => { setOpen(true); onView("org"); }} data-testid="open-confirm">Let everyone join</Button></>} />
          ) : null}
          {inviting ? <InviteDialog org={nw} admin members={NORTHWIND_MEMBERS} invitations={NORTHWIND_INVITATIONS} done={inviting === "links"}
            onDone={(d) => setInviting(d ? "links" : "invite")} onClose={() => setInviting(false)} /> : null}
        </>
      ) : (
        <>
          <SettingsHeader title="Organisations" description="Every organisation on this dude. Nobody makes their own: you make it and send its first admin their link; they invite the rest."
            actions={<Button variant="primary" leadingIcon="plus" onClick={() => onView("create")} data-testid="create-org">Create organisation</Button>} />
          <Table density="comfortable" data-testid="orgs">
            <THead><Tr><Th>Organisation</Th><Th>Who can join</Th><Th>Members</Th><Th>Admins</Th><Th>Made</Th><Th align="right"><span className="ds-sr-only">Open</span></Th></Tr></THead>
            <TBody>
              {ALL_ORGS.map(({ org, members, admins, pending }) => (
                <Tr key={org.id} interactive onClick={() => org.slug === "northwind" && onView("org")}>
                  <Td><EntityLine lead={<OrgFace org={org} size={28} />} name={org.name} detail={<span className="ds-mono">/o/{org.slug}</span>} /></Td>
                  <Td>{org.open ? <Badge tone="info" icon="globe">Anyone who signs in</Badge> : <span className="muted">By invitation</span>}</Td>
                  <Td>{members}{pending ? <span className="muted"> · {pending} pending</span> : null}</Td>
                  <Td>{admins.length ? <span className={s.inline}><PersonAvatarStack people={admins} size={24} max={3} />{admins.map((a) => a.name.split(" ")[0]).join(", ")}</span>
                    : <Badge tone="attention" icon="warning">No admin yet</Badge>}</Td>
                  <Td muted>{org.created}</Td>
                  <Td align="right" fit><Button size="sm" variant="quiet" trailingIcon="chevron-right" onClick={() => org.slug === "northwind" && onView("org")}>Open</Button></Td>
                </Tr>
              ))}
            </TBody>
          </Table>
          {view === "create" ? <CreateOrg onClose={() => onView("list")} /> : null}
        </>
      )}
    </SettingsLayout>
  );
}

function CreateOrg({ onClose }: { onClose: () => void }) {
  const [name, setName] = useState("Initech");
  const [slug, setSlug] = useState<string | null>(null);
  const [email, setEmail] = useState("bill@initech.com");
  const [made, setMade] = useState(false);
  const copy = useCopy();
  const derived = name.toLowerCase().normalize("NFD").replace(/[\u0300-\u036f]/g, "").replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "");
  const shown = slug ?? derived;
  if (made) {
    const link: MadeLink = { email, role: "admin", code: NEW_CODES[0]! };
    return (
      <Dialog open onOpenChange={(o) => !o && onClose()} size="lg" title={`${name} is made`}
        description="Send its first admin this link. It works only for their address, for 14 days."
        footer={<Button variant="primary" onClick={onClose}>Done</Button>}>
        <InvitationLinkList links={[link]} onCopy={() => copy(inviteUrl(link.code), "Link")} />
      </Dialog>
    );
  }
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} size="sm" title="Create organisation"
      description="It starts with dude's default model tiers and machine sizes, by invitation only. Its first admin sets up the rest."
      footer={<><Button variant="quiet" onClick={onClose}>Cancel</Button><Button variant="primary" disabled={!name.trim() || !shown || !email.includes("@")} onClick={() => setMade(true)} data-testid="create-submit">Create and get the link</Button></>}>
      <FormStack>
        <Input label="Name" autoFocus value={name} onChange={(e) => setName(e.target.value)} />
        <Input label="Slug" mono value={shown} onChange={(e) => setSlug(e.target.value)} hint={`In every link: ${HOST}/#/o/${shown || "…"}. It can't be changed later.`} />
        <Input label="First admin's email" type="email" value={email} onChange={(e) => setEmail(e.target.value)} hint="You get their invitation link to send them." />
      </FormStack>
    </Dialog>
  );
}
