/*
 * The screens of the accounts mockup, composed from the design system's
 * components. What is laid out here and not in the design system is in
 * accounts.module.css, on tokens only, and named in docs/design/accounts.md
 * where it should become a component.
 */

import { useState, type ReactNode } from "react";
import {
  ChatComposer, EntityLine, PersonAvatar, PersonAvatarStack, ProjectAvatar, RecentSessions, SettingsHeader, SettingsLayout,
  SettingsNote, SettingsSection, StarterPills, WELCOME_STARTERS, Welcome, type SettingsNavItem,
} from "@dude/design-system/components";
import { Icon, type IconName } from "@dude/design-system";
import {
  Badge, Button, Callout, Card, CardBody, CardFooter, CardHeader, Dialog, FormStack, IconButton, Input, KeyValueList, Select, Table, TBody, Td, Textarea, Th, THead, Tr,
} from "@dude/design-system/primitives";
import { projectCounts } from "../../../../packages/design-system/src/util/navModel.ts";
import type { Person } from "../../../../packages/design-system/src/components/PersonAvatar.tsx";
import {
  ACME, ACME_MEMBERS, ACME_PENDING, ALL_ORGS, CHLOE_EMAIL, CHLOE_INVITES, HOST, HOW, MEMBERSHIPS, MY_KEYS, NEW_KEY, NORTHWIND_MEMBERS, NORTHWIND_PENDING,
  OPERATOR, P, PASTE_EXAMPLE, parseInvites, type Invitation, type Member, type Org, type Pending, type Role,
} from "./data.ts";
import { Mark, OrgFace, roleWord } from "./Shell.tsx";
import s from "./accounts.module.css";

const ROLES = [{ value: "member", label: "Member" }, { value: "admin", label: "Admin" }];

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

/** Screen 1: first sign-in with one invitation, already accepted: a page inside Acme. */
export function FirstOne({ onStart }: { onStart: () => void }) {
  const projects = ACME.projects.map((p) => ({ p, c: projectCounts(p, P.chloe.id), yours: p.id === "p_control" ? 2 : 0 }))
    .sort((a, b) => b.yours - a.yours);
  return (
    <div className={s.page} data-testid="first-one">
      <div className={s.column}>
        <header className={s.hero}>
          <OrgFace org={ACME} size={48} />
          <h1 className={s.heroTitle}>Welcome to Acme, Chloé</h1>
          <p className={s.heroLine}>Ana Ribeiro invited you as a member. You're in.</p>
        </header>
        <Card>
          <CardHeader title="You" />
          <CardBody><ProfileStrip person={P.chloe} email={CHLOE_EMAIL} /></CardBody>
        </Card>
        <Card>
          <CardHeader title="Acme's projects" actions={<span className="muted">{projects.length} projects · 5 people</span>} />
          <CardBody padding="flush">
            <ul className={s.rows}>
              {projects.map(({ p, c, yours }) => (
                <li key={p.id} className={s.row}>
                  <EntityLine lead={<ProjectAvatar project={{ id: p.id, name: p.name }} size={24} />} name={p.name}
                    detail={[yours ? `${yours} tasks of yours` : null, c.active ? `${c.active} running` : null, c.ready ? `${c.ready} ready` : null].filter(Boolean).join(" · ") || "Nothing running"} />
                  {yours ? <Badge tone="info">You're on it</Badge> : null}
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

/** Screen 1, second variant: two invitations, nothing joined yet. */
export function FirstTwo({ onJoin }: { onJoin: (org: Org) => void }) {
  const [state, setState] = useState<Record<string, "joined" | "later">>({});
  return (
    <Outside testId="first-two">
      <header className={s.hero}>
        <h1 className={s.heroTitle}>You're invited, Chloé</h1>
        <p className={s.heroLine}>Two organisations on this dude asked you to join. Join one to start; the other waits in the switcher.</p>
      </header>
      <Card>
        <CardHeader title="You" />
        <CardBody><ProfileStrip person={P.chloe} email={CHLOE_EMAIL} /></CardBody>
      </Card>
      <ul className={s.invites}>
        {CHLOE_INVITES.map((i) => <InviteCard key={i.id} i={i} state={state[i.id]} onJoin={() => { setState((x) => ({ ...x, [i.id]: "joined" })); onJoin(i.org); }}
          onLater={() => setState((x) => ({ ...x, [i.id]: "later" }))} />)}
      </ul>
      <p className={s.fine}>Signed in as {CHLOE_EMAIL} with Cloudflare Access. <button type="button" className={s.link}>Sign out</button></p>
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
            <span className="muted">{roleWord(i.role)} · invited by {i.by?.name ?? "the operator"} on {i.sent} · expires {i.expires}</span>
            <span className="muted">{i.org.projects.length} projects</span>
          </div>
          <div className={s.inviteActions}>
            {state === "later" ? <span className="muted">In the switcher for later</span> : (
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

/** Screen 2: signed in, in no organisation. */
export function NoOrg({ expired }: { expired: boolean }) {
  return (
    <Outside testId="no-org">
      <header className={s.hero}>
        <h1 className={s.heroTitle}>Your email isn't in any organisation on this dude</h1>
        <p className={s.heroLine}>You signed in as <b>{CHLOE_EMAIL}</b>. Someone has to invite that address before you can join.</p>
      </header>
      {expired ? (
        <Callout tone="attention">Your invitation to Northwind expired on 2 October. Ask Gus Lindqvist to send it again.</Callout>
      ) : null}
      <Card>
        <CardHeader title="Who to ask" />
        <CardBody>
          <FormStack>
            <p className="muted">An admin of the organisation you work with can invite you from its Members page. If you don't know who that is, ask whoever runs this dude:</p>
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
export function OrgWelcome({ org }: { org: Org }) {
  return (
    <Welcome data-testid="org-welcome" mark={<Mark size="fill" />} greeting="Afternoon, Márcio" line={`What are we working out in ${org.name} today?`}
      composer={<ChatComposer mode="chat" variant="stage" placeholder="Start a session: an idea, a question, a plan…" onSubmit={() => true} to={<>To <b>Brainstorm</b></>} />}
      starters={<StarterPills starters={WELCOME_STARTERS} onPick={() => undefined} />}
      footer={<RecentSessions sessions={org.id === ACME.id
        ? [{ id: "s1", title: "Usage-based billing", summary: "Filed 1 epic, 4 tasks", age: "yesterday" }, { id: "s2", title: "Q4 cleanup ideas", summary: "Nothing filed yet", age: "3 days ago" }]
        : [{ id: "n1", title: "Portal invoice VAT", summary: "Filed 2 tasks", age: "2 days ago" }]} onOpen={() => undefined} onAll={() => undefined} />} />
  );
}

/** Screen 4: Your settings. */
export function YourSettings({ reveal, onReveal }: { reveal: Org | null; onReveal: (org: Org | null) => void }) {
  const [appearance, setAppearance] = useState(false);
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
              <EntityLine lead={<OrgFace org={ALL_ORGS[2]!.org} size={24} />} name="Globex" detail="Invited by Lin Zhao · expires in 9 days" />
              <span className={s.rowActions}><Button size="sm" variant="quiet">Decline</Button><Button size="sm" variant="secondary">Join</Button></span>
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

function PendingTable({ pending, onRevoke }: { pending: readonly Pending[]; onRevoke?: (p: Pending) => void }) {
  return (
    <Table density="comfortable" data-testid="pending">
      <THead><Tr><Th width="45%">Invited</Th><Th>Role</Th><Th>Expires</Th><Th align="right"><span className="ds-sr-only">Actions</span></Th></Tr></THead>
      <TBody>
        {pending.map((p) => (
          <Tr key={p.id}>
            <Td><EntityLine lead={<span className={s.envelope}><Icon name="message" size={14} /></span>} name={p.name ?? p.email}
              detail={[p.name ? p.email : null, p.by ? `by ${p.by.name}` : "by the operator"].filter(Boolean).join(" · ")} /></Td>
            <Td>{p.role === "admin" ? <Badge>Admin</Badge> : <span className="muted">Member</span>}</Td>
            <Td>{p.expired ? <Badge tone="attention" icon="clock" title={p.expires}>Expired</Badge> : <span className="muted">{p.expires.replace("Expires ", "")}</span>}</Td>
            <Td align="right" fit>
              <span className={s.rowActions}>
                <IconButton size="sm" icon="copy" label={`Copy the sign-in link for ${p.email}`} />
                <Button size="sm" variant={p.expired ? "secondary" : "quiet"}>Resend</Button>
                <Button size="sm" variant="quiet" onClick={() => onRevoke?.(p)}>Revoke…</Button>
              </span>
            </Td>
          </Tr>
        ))}
      </TBody>
    </Table>
  );
}

function MembersTable({ members, you, admin, onRemove, keyOnly }: { members: readonly Member[]; you?: string; admin: boolean; onRemove?: (m: Member) => void; keyOnly?: boolean }) {
  return (
    <Table density="comfortable" data-testid="members">
      <THead><Tr><Th width="45%">Person</Th><Th>Role</Th><Th align="right"><span className="ds-sr-only">Actions</span></Th></Tr></THead>
      <TBody>
        {members.map((m) => {
          const me = m.person.id === you;
          return (
            <Tr key={m.person.id}>
              <Td><EntityLine lead={<PersonAvatar person={m.person} size={32} aria-hidden title="" />} name={m.person.name}
                detail={[me ? "you" : null, m.email ?? "no email: signs in only with a key", m.seen].filter(Boolean).join(" · ")} /></Td>
              <Td>{admin && !me ? <Select size="sm" aria-label={`Role of ${m.person.name}`} value={m.role} options={ROLES} /> : m.role === "admin" ? <Badge>Admin</Badge> : <span className="muted">Member</span>}</Td>
              <Td align="right" fit>
                {keyOnly && !m.email ? <Button size="sm" variant="secondary">Set email…</Button>
                  : admin && !me ? <Button size="sm" variant="quiet" onClick={() => onRemove?.(m)}>Remove…</Button> : null}
              </Td>
            </Tr>
          );
        })}
      </TBody>
    </Table>
  );
}

const ORG_NAV: SettingsNavItem[] = [
  { id: "members", label: "Members", icon: "human", note: "3 pending" },
  { id: "general", label: "General", icon: "settings" },
  { id: "github", label: "GitHub", icon: "git-branch" },
  { id: "agents", label: "Agents", icon: "agent" },
  { id: "network", label: "Network", icon: "globe" },
  { id: "models", label: "Models", icon: "sparkle" },
  { id: "machines", label: "Machines", icon: "chip" },
  { id: "delivery", label: "Delivery", icon: "list-check" },
  { id: "memory", label: "Memory", icon: "memory" },
];

/** Screen 5: Organisation settings → Members. */
export function MembersPage({ dialog, onDialog }: { dialog: "none" | "invite" | "remove"; onDialog: (d: "none" | "invite" | "remove") => void }) {
  const [removing, setRemoving] = useState<Member | null>(ACME_MEMBERS[2]!);
  return (
    <SettingsLayout data-testid="org-members" scope={{ title: "Acme", subtitle: "Organisation settings", leading: <OrgFace org={ACME} size={32} /> }}
      items={ORG_NAV} current="members" onSelect={() => undefined}>
      <SettingsNote>Everyone here can create, deliver and steer tasks. Admins manage the organisation: its members and its settings.</SettingsNote>
      <SettingsHeader title="Members" description="Who is in Acme. An invitation is matched by email when they sign in: nothing secret to pass on."
        actions={<Button variant="primary" leadingIcon="plus" onClick={() => onDialog("invite")} data-testid="invite">Invite people</Button>} />
      <SettingsSection title={`Pending · ${ACME_PENDING.length}`}>
        <PendingTable pending={ACME_PENDING} />
      </SettingsSection>
      <SettingsSection title={`Members · ${ACME_MEMBERS.length}`}>
        <MembersTable members={ACME_MEMBERS} you={P.marcio.id} admin onRemove={(m) => { setRemoving(m); onDialog("remove"); }} />
      </SettingsSection>
      {dialog === "invite" ? <InviteDialog org={ACME} onClose={() => onDialog("none")} members={ACME_MEMBERS} pending={ACME_PENDING} /> : null}
      {dialog === "remove" && removing ? (
        <Dialog open onOpenChange={(o) => !o && onDialog("none")} tone="danger" size="sm" title={`Remove ${removing.person.name} from Acme?`}
          description="Their keys for Acme stop working at once. Tasks they own become nobody's; what they did keeps their name. They stay signed in to dude for their other organisations."
          footer={<><Button variant="quiet" onClick={() => onDialog("none")}>Cancel</Button><Button variant="danger" solid>Remove {removing.person.name.split(" ")[0]}</Button></>} />
      ) : null}
    </SettingsLayout>
  );
}

function InviteDialog({ org, onClose, members, pending, admin }: { org: Org; onClose: () => void; members: readonly Member[]; pending: readonly Pending[]; admin?: boolean }) {
  const [text, setText] = useState(admin ? "" : PASTE_EXAMPLE);
  const [role, setRole] = useState<Role>(admin ? "admin" : "member");
  const parsed = parseInvites(text, members, pending);
  const ok = parsed.filter((p) => !p.problem);
  const why = { unreadable: "not an email address: left out", member: `already in ${org.name}`, pending: "already invited: Resend it instead", duplicate: "twice in the list" } as const;
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} size="md" title={`Invite people to ${org.name}`}
      description="Emails, one per line or separated by commas. Pasting “Name <email>” lines from your mail keeps the names."
      footer={<><Button variant="quiet" onClick={onClose}>Cancel</Button><Button variant="primary" disabled={!ok.length} data-testid="invite-submit">{ok.length === 1 ? "Invite 1 person" : `Invite ${ok.length} people`}</Button></>}>
      <FormStack>
        <Textarea label="Who" rows={4} maxRows={8} value={text} onChange={(e) => setText(e.target.value)} placeholder="ana@acme.dev, Tom Okafor <tom@acme.dev>" data-testid="invite-text" />
        {parsed.length ? (
          <ul className={s.parsed} aria-label="Will be invited" data-testid="parsed">
            {parsed.map((p, i) => (
              <li key={i} className={s.parsedRow} data-problem={p.problem ?? undefined}>
                <Icon name={p.problem ? (p.problem === "unreadable" ? "warning" : "minus") : "check"} size={14} className={p.problem ? s.parsedSkip : s.parsedOk} />
                <span className={s.parsedName}>{p.email ? (p.name ? <>{p.name} <span className="muted">{p.email}</span></> : p.email) : <span className="ds-mono">{p.line}</span>}</span>
                {p.problem ? <span className="muted">{why[p.problem]}</span> : null}
              </li>
            ))}
          </ul>
        ) : null}
        <Select label="Role" value={role} options={ROLES} onValueChange={(v) => setRole(v as Role)} />
        <p className="muted">They sign in at <b>{HOST}</b> with the email you invited and land in {org.name}. Invitations expire in 14 days; Resend extends them.</p>
      </FormStack>
    </Dialog>
  );
}

const ADMIN_NAV: SettingsNavItem[] = [
  { id: "orgs", label: "Organisations", icon: "building", note: String(ALL_ORGS.length) },
];

/** Screen 6: super-admin, outside any organisation. */
export function AdminPage({ view, onView, onBack }: { view: "list" | "create" | "org"; onView: (v: "list" | "create" | "org") => void; onBack: () => void }) {
  const [inviting, setInviting] = useState(false);
  return (
    <SettingsLayout data-testid="admin" className={s.admin}
      scope={{ title: "This dude", subtitle: "Super-admin", leading: <span className={s.adminMark}><Mark size={32} /></span> }}
      items={ADMIN_NAV} current="orgs" onSelect={() => onView("list")}
      footer={<div className={s.adminFoot}>
        <p className="muted">Outside every organisation: this page shows no organisation's work. You're here because your email is in <code>auth.super_admins</code>.</p>
        <Button size="sm" variant="secondary" leadingIcon="arrow-right" onClick={onBack}>Back to Acme</Button>
      </div>}>
      {view === "org" ? (
        <>
          <SettingsHeader title={<span className={s.inline}><OrgFace org={ALL_ORGS[1]!.org} size={24} />Northwind</span>}
            description={`${HOST}/#/o/northwind · made in September 2026 · 4 members`}
            actions={<><Button variant="quiet" onClick={() => onView("list")}>All organisations</Button><Button variant="primary" leadingIcon="plus" onClick={() => setInviting(true)}>Invite</Button></>} />
          <Callout tone="info">You can see who is in Northwind and invite people. Its projects, tasks and settings are its members'; to work in it, invite yourself.</Callout>
          <SettingsSection title="Pending · 1"><PendingTable pending={NORTHWIND_PENDING} /></SettingsSection>
          <SettingsSection title="Members · 4"><MembersTable members={NORTHWIND_MEMBERS} admin keyOnly /></SettingsSection>
          {inviting ? <InviteDialog org={ALL_ORGS[1]!.org} admin onClose={() => setInviting(false)} members={NORTHWIND_MEMBERS} pending={NORTHWIND_PENDING} /> : null}
        </>
      ) : (
        <>
          <SettingsHeader title="Organisations" description="Every organisation on this dude. Nobody makes their own: you make it and invite its first admin, who invites the rest."
            actions={<Button variant="primary" leadingIcon="plus" onClick={() => onView("create")} data-testid="create-org">Create organisation</Button>} />
          <Table density="comfortable" data-testid="orgs">
            <THead><Tr><Th>Organisation</Th><Th>Members</Th><Th>Admins</Th><Th>Made</Th><Th align="right"><span className="ds-sr-only">Open</span></Th></Tr></THead>
            <TBody>
              {ALL_ORGS.map(({ org, members, admins, pending }) => (
                <Tr key={org.id} interactive onClick={() => org.slug === "northwind" && onView("org")}>
                  <Td><EntityLine lead={<OrgFace org={org} size={28} />} name={org.name} detail={<span className="ds-mono">/o/{org.slug}</span>} /></Td>
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
  const derived = name.toLowerCase().normalize("NFD").replace(/[\u0300-\u036f]/g, "").replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "");
  const shown = slug ?? derived;
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} size="sm" title="Create organisation"
      description="It starts with dude's default model tiers and machine sizes. Its first admin gets an invitation and sets up the rest."
      footer={<><Button variant="quiet" onClick={onClose}>Cancel</Button><Button variant="primary" disabled={!name.trim() || !shown || !email.includes("@")}>Create and invite</Button></>}>
      <FormStack>
        <Input label="Name" autoFocus value={name} onChange={(e) => setName(e.target.value)} />
        <Input label="Slug" mono value={shown} onChange={(e) => setSlug(e.target.value)} hint={`In every link: ${HOST}/#/o/${shown || "…"}. It can't be changed later.`} />
        <Input label="First admin's email" type="email" value={email} onChange={(e) => setEmail(e.target.value)} hint="They join as admin when they sign in with it. Expires in 14 days." />
      </FormStack>
    </Dialog>
  );
}
