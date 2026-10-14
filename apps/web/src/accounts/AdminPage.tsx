/**
 * The super-admin's page, outside every organisation (docs/design/
 * accounts.md, *Super-admin*): this dude's organisations, Create
 * organisation (and its first admin's link), and one organisation's
 * members and invitations, with Set email… for those who sign in only
 * with a key. It shows no organisation's work.
 */

import { useCallback, useEffect, useMemo, useState } from "react";
import { EntityLine, InvitationLinkList, OrgFace, PersonAvatar, SettingsHeader, SettingsLayout, SettingsSection, type SettingsNavItem } from "@dude/design-system/components";
import { Badge, Button, Callout, Dialog, FormStack, Input, Select, Spinner, Table, TableEmpty, TBody, Td, Th, THead, Tr } from "@dude/design-system/primitives";
import type { PersonRole } from "@dude/domain";
import type { AdminOrganization, AdminPerson, ApiClient } from "../api/client.ts";
import { errorText, useSave } from "../hooks/useSave.tsx";
import { DudeMark } from "../DudeMark.tsx";
import { InvitationsList } from "./InvitationsList.tsx";
import { InviteDialog, inviteLink, useCopy, type InvitationsApi } from "./InviteDialog.tsx";
import { RemoveDialog } from "../screens/MembersSection.tsx";
import { absolute, monthWords } from "./words.ts";

const ROLES = [{ value: "member", label: "Member" }, { value: "admin", label: "Admin" }];

/** A slug from a name: lower-case letters, digits and single hyphens, as the API takes. */
export function slugOf(name: string): string {
  return name.toLowerCase().normalize("NFD").replace(/[\u0300-\u036f]/g, "").replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "");
}

export interface AdminPageProps {
  client: ApiClient;
  /** `#/admin/o/<slug>`: one organisation; null: the list. */
  slug: string | null;
  onOpen: (slug: string | null) => void;
  /** Back to the organisation you came from, when you are in one. */
  back: { name: string; go: () => void } | null;
}

export function AdminPage({ client, slug, onOpen, back }: AdminPageProps) {
  const [orgs, setOrgs] = useState<AdminOrganization[] | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  const load = useCallback(() => {
    client.adminOrganizations().then((r) => setOrgs(r.organizations), (err: unknown) => setProblem(errorText(err)));
  }, [client]);
  useEffect(load, [load]);
  const nav: SettingsNavItem[] = [{ id: "orgs", label: "Organisations", icon: "building", note: orgs ? String(orgs.length) : undefined }];
  const open = slug ? orgs?.find((o) => o.slug === slug) : undefined;
  return (
    <SettingsLayout data-testid="admin" className="adminPage"
      scope={{ title: "This dude", subtitle: "Super-admin", leading: <span className="adminMark"><DudeMark size={32} /></span> }}
      items={nav} current="orgs" onSelect={() => onOpen(null)}
      footer={<div className="adminFoot">
        <p className="muted">Outside every organisation: this page shows no organisation's work. You're here because your email is in <code>auth.super_admins</code>.</p>
        {back ? <Button size="sm" variant="secondary" leadingIcon="arrow-right" onClick={back.go} data-testid="admin-back">Back to {back.name}</Button> : null}
      </div>}>
      {problem ? <Callout tone="danger">{problem}</Callout> : null}
      {slug ? (
        orgs === null ? <div className="centered"><Spinner label="Loading…" /></div>
          : open ? <AdminOrganizationPage key={open.id} client={client} org={open} onList={() => onOpen(null)} onChanged={load} />
          : <Callout tone="neutral">No organisation has the slug {slug}.</Callout>
      ) : (
        <>
          <SettingsHeader title="Organisations" description="Every organisation on this dude. Nobody makes their own: you make it and send its first admin their link; they invite the rest."
            actions={<Button variant="primary" leadingIcon="plus" onClick={() => setCreating(true)} data-testid="create-org">Create organisation</Button>} />
          <Table density="comfortable" data-testid="orgs">
            <THead><Tr><Th>Organisation</Th><Th>Members</Th><Th hideWhenNarrow>Admins</Th><Th hideWhenNarrow>Made</Th><Th align="right"><span className="ds-sr-only">Open</span></Th></Tr></THead>
            <TBody>
              {orgs === null ? null : orgs.length ? orgs.map((o) => (
                <Tr key={o.id} interactive onClick={() => onOpen(o.slug)} data-testid="admin-org" data-slug={o.slug}>
                  <Td><EntityLine lead={<OrgFace org={o} size={28} aria-hidden />} name={o.name} detail={<span className="ds-mono">/o/{o.slug}</span>} /></Td>
                  <Td>{o.members}{o.pendingInvitations ? <span className="muted"> · {o.pendingInvitations} pending</span> : null}</Td>
                  <Td hideWhenNarrow>{o.admins ? `${o.admins} ${o.admins === 1 ? "admin" : "admins"}` : <Badge tone="attention" icon="warning">No admin yet</Badge>}</Td>
                  <Td muted hideWhenNarrow>{monthWords(o.createdAt)}</Td>
                  <Td align="right" fit><Button size="sm" variant="quiet" trailingIcon="chevron-right" onClick={(e) => { e.stopPropagation(); onOpen(o.slug); }}>Open</Button></Td>
                </Tr>
              )) : <TableEmpty colSpan={5}>No organisations yet. Create the first one and invite its first admin.</TableEmpty>}
            </TBody>
          </Table>
          {creating ? <CreateOrganization client={client} onClose={() => setCreating(false)} onCreated={load} /> : null}
        </>
      )}
    </SettingsLayout>
  );
}

function CreateOrganization({ client, onClose, onCreated }: { client: ApiClient; onClose: () => void; onCreated: () => void }) {
  const [name, setName] = useState("");
  const [slug, setSlug] = useState<string | null>(null);
  const [email, setEmail] = useState("");
  const [made, setMade] = useState<{ name: string; email: string; url: string } | null>(null);
  const { busy, problem, save } = useSave();
  const copy = useCopy();
  const shown = slug ?? slugOf(name);
  if (made) {
    return (
      <Dialog open onOpenChange={(o) => !o && onClose()} size="lg" title={`${made.name} is made`}
        description="Send its first admin this link. It works only for their address, for 14 days."
        footer={<Button variant="primary" onClick={onClose} data-testid="create-done">Done</Button>}>
        <InvitationLinkList links={[{ email: made.email, role: "Admin", url: made.url }]} onCopy={(l) => copy(l.url, "Link")} data-testid="create-link" />
      </Dialog>
    );
  }
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} size="sm" title="Create organisation"
      description="It starts with dude's default model tiers and machine sizes. People join it by invitation; its first admin, who may be you, invites the rest."
      footerProblem={problem}
      footer={<>
        <Button variant="quiet" onClick={onClose}>Cancel</Button>
        <Button variant="primary" disabled={busy || !name.trim() || !shown || !email.includes("@")} data-testid="create-submit"
          onClick={() => {
            let url = "";
            void save(async () => {
              const r = await client.createOrganization({ name: name.trim(), slug: shown, email: email.trim() });
              url = r.invitation ? inviteLink(r.invitation.code) : absolute(r.link ?? "#/");
            }, () => {
              setMade({ name: name.trim(), email: email.trim().toLowerCase(), url });
              onCreated();
            });
          }}>Create and get the link</Button>
      </>}>
      <FormStack>
        <Input label="Name" autoFocus value={name} onChange={(e) => setName(e.target.value)} data-testid="create-name" />
        <Input label="Slug" mono value={shown} onChange={(e) => setSlug(e.target.value)} data-testid="create-slug"
          hint={`In every link: ${window.location.host}/#/o/${shown || "…"}. It can't be changed later.`} />
        <Input label="First admin's email" type="email" value={email} onChange={(e) => setEmail(e.target.value)} data-testid="create-email"
          hint="You get their invitation link to send them. Yours, if you'll be its first admin." />
      </FormStack>
    </Dialog>
  );
}

function AdminOrganizationPage({ client, org, onList, onChanged }: { client: ApiClient; org: AdminOrganization; onList: () => void; onChanged: () => void }) {
  const [people, setPeople] = useState<AdminPerson[] | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const [version, setVersion] = useState(0);
  const [inviting, setInviting] = useState<{ initial?: string } | null>(null);
  const [removing, setRemoving] = useState<AdminPerson | null>(null);
  const [settingEmail, setSettingEmail] = useState<AdminPerson | null>(null);
  const api: InvitationsApi = useMemo(() => ({
    list: (p) => client.adminInvitations(org.id, p),
    invite: (i, role) => client.adminInvite(org.id, i, role),
    renew: (id) => client.adminRenewInvitation(org.id, id),
    revoke: (id) => client.adminRevokeInvitation(org.id, id),
  }), [client, org.id]);
  useEffect(() => {
    client.adminOrganization(org.id).then((r) => setPeople(r.people), (err: unknown) => setProblem(errorText(err)));
  }, [client, org.id, version]);
  const changed = () => {
    setVersion((v) => v + 1);
    onChanged();
  };
  const setRole = (p: AdminPerson, role: PersonRole) => {
    setProblem(null);
    client.adminUpdatePerson(p.id, { role }).then(changed, (err: unknown) => setProblem(errorText(err)));
  };
  return (
    <div className="settingsPage" data-testid="admin-org-page">
      <SettingsHeader title={<span className="inlineFace"><OrgFace org={org} size={24} aria-hidden />{org.name}</span>}
        description={`${window.location.host}/#/o/${org.slug} · made in ${monthWords(org.createdAt)} · ${org.members} ${org.members === 1 ? "member" : "members"} · joined by invitation`}
        actions={<>
          <Button variant="quiet" onClick={onList}>All organisations</Button>
          <Button variant="primary" leadingIcon="plus" onClick={() => setInviting({})} data-testid="invite">Invite people</Button>
        </>} />
      <Callout tone="info">You can see who is in {org.name}, invite and remove people. Its projects, tasks and settings are its members'; to work in it, invite yourself.</Callout>
      {problem ? <Callout tone="danger" data-testid="admin-problem">{problem}</Callout> : null}
      <SettingsSection title={`Members · ${people?.length ?? org.members}`}>
        <Table density="comfortable" data-testid="admin-members">
          <THead><Tr><Th width="50%">Person</Th><Th>Role</Th><Th align="right"><span className="ds-sr-only">Actions</span></Th></Tr></THead>
          <TBody>
            {people === null ? null : people.length ? people.map((p) => (
              <Tr key={p.id} data-testid="admin-member" data-member={p.name}>
                <Td><EntityLine lead={<PersonAvatar person={p} size={32} aria-hidden title="" />} name={p.name}
                  detail={p.email ?? "no email: signs in only with a key"} /></Td>
                <Td><Select size="sm" aria-label={`Role of ${p.name}`} value={p.role} options={ROLES} onValueChange={(r) => setRole(p, r as PersonRole)} /></Td>
                <Td align="right" fit>
                  <span className="rowActions">
                    {!p.email ? <Button size="sm" variant="secondary" onClick={() => setSettingEmail(p)} data-testid="set-email">Set email…</Button> : null}
                    <Button size="sm" variant="quiet" onClick={() => setRemoving(p)} data-testid="member-remove">Remove…</Button>
                  </span>
                </Td>
              </Tr>
            )) : <TableEmpty colSpan={3}>Nobody yet: its first admin joins with their link.</TableEmpty>}
          </TBody>
        </Table>
      </SettingsSection>
      <SettingsSection title="Invitations">
        <InvitationsList api={api} orgName={org.name} version={version}
          personName={(id) => people?.find((p) => p.id === id)?.name}
          onInviteAgain={(row) => setInviting({ initial: row.name ? `${row.name} <${row.email}>` : row.email })} />
      </SettingsSection>
      {inviting ? (
        <InviteDialog api={api} orgName={org.name} memberEmails={(people ?? []).flatMap((p) => (p.email ? [p.email] : []))}
          initial={inviting.initial} initialRole={org.admins ? "member" : "admin"} onClose={() => setInviting(null)} onInvited={changed} />
      ) : null}
      {removing ? (
        <RemoveDialog orgName={org.name} person={removing} remove={() => client.adminRemovePerson(org.id, removing.id)}
          onClose={() => setRemoving(null)} onRemoved={changed} />
      ) : null}
      {settingEmail ? <SetEmail client={client} person={settingEmail} onClose={() => setSettingEmail(null)} onSet={changed} /> : null}
    </div>
  );
}

/** Link a key-only person to an account by email, so they can sign in with a browser as the same person. */
function SetEmail({ client, person, onClose, onSet }: { client: ApiClient; person: AdminPerson; onClose: () => void; onSet: () => void }) {
  const [email, setEmail] = useState("");
  const { busy, problem, save } = useSave();
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} size="sm" title={`Set ${person.name}'s email`}
      description="They keep their keys and history, and can sign in with a browser as the same person once Cloudflare Access proves this address."
      footerProblem={problem}
      footer={<>
        <Button variant="quiet" onClick={onClose}>Cancel</Button>
        <Button variant="primary" disabled={busy || !email.includes("@")} data-testid="set-email-submit"
          onClick={() => void save(() => client.adminUpdatePerson(person.id, { email: email.trim() }), () => { onClose(); onSet(); }, "Email set")}>Set email</Button>
      </>}>
      <Input label="Email" type="email" autoFocus value={email} onChange={(e) => setEmail(e.target.value)} data-testid="set-email-input" />
    </Dialog>
  );
}
