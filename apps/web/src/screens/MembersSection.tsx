/**
 * The organisation's members: who is in it, how each joined, their role,
 * and for its admins Invite people, role changes and removal. The last
 * admin's rules are the API's; its refusal is shown as it says it.
 */

import { useEffect, useMemo, useState } from "react";
import { EntityLine, PersonAvatar, SettingsHeader, SettingsSection } from "@dude/design-system/components";
import { Badge, Button, Callout, Dialog, Select, Table, TBody, Td, Th, THead, Tr } from "@dude/design-system/primitives";
import type { PersonRole } from "@dude/domain";
import type { ApiClient, InvitationRow, Member } from "../api/client.ts";
import { errorText, useSave } from "../hooks/useSave.tsx";
import { whereWords } from "../people.tsx";
import { InviteDialog, type InvitationsApi } from "../accounts/InviteDialog.tsx";
import { useAccount } from "../accounts/context.tsx";
import { roleWord } from "../accounts/words.ts";

const ROLES = [
  { value: "member", label: "Member" },
  { value: "admin", label: "Admin" },
];

/** The organisation's own invitation routes, for the invite dialog and the Invitations page. */
export function organizationInvitations(client: ApiClient): InvitationsApi {
  return {
    list: (p) => client.listInvitations(p),
    counts: () => client.invitationCounts(),
    invite: (i, role) => client.invite(i, role),
    renew: (id) => client.renewInvitation(id),
    revoke: (id) => client.revokeInvitation(id),
  };
}

/** Every accepted invitation, following the cursor page by page: who joined by whose link. */
async function acceptedInvitations(api: InvitationsApi): Promise<InvitationRow[]> {
  const all: InvitationRow[] = [];
  let cursor: string | undefined;
  do {
    const page = await api.list({ status: "accepted", ...(cursor ? { cursor } : {}) });
    all.push(...page.invitations);
    cursor = page.cursor ?? undefined;
  } while (cursor);
  return all;
}

/** `me`, `people` and `onChanged` are the shell's (`usePeople`), so this list is as live as the sidebar's. */
export function MembersSection({ client, me, people, onChanged, orgName, onInvited }: {
  client: ApiClient;
  me: Member | null;
  people: readonly Member[];
  onChanged: () => void;
  orgName: string;
  /** Someone was invited: the menu's pending count reads again. */
  onInvited?: () => void;
}) {
  const [problem, setProblem] = useState<string | null>(null);
  const [removing, setRemoving] = useState<Member | null>(null);
  const [inviting, setInviting] = useState(false);
  const [joined, setJoined] = useState<ReadonlyMap<string, string> | null>(null);
  const admin = me?.role === "admin";
  const api = useMemo(() => organizationInvitations(client), [client]);
  // Invitations are how people join over Cloudflare Access; with keys (api_key), people are added through the API.
  const account = useAccount();
  const byInvitation = account?.authMethod === "cloudflare_access";

  // How each joined, read from the invitation accepted into their row: an admin's to read.
  useEffect(() => {
    if (!admin || !byInvitation) return;
    let current = true;
    void acceptedInvitations(api).then((rows) => current && setJoined(new Map(rows
      .filter((i) => i.acceptedPersonId).map((i) => [i.acceptedPersonId!, `invited by ${i.inviter === "the operator" ? "the operator" : i.inviter.split(" ")[0]}`]))),
    () => undefined);
    return () => {
      current = false;
    };
  }, [admin, byInvitation, api, people.length]);

  const setRole = (person: Member, role: PersonRole) => {
    setProblem(null);
    void client.updatePerson(person.id, { role }).then(onChanged, (err: unknown) => setProblem(errorText(err)));
  };

  return (
    <div data-testid="members" className="settingsPage">
      <SettingsHeader title="Members"
        description={byInvitation
          ? `Who is in ${orgName}. People join ${orgName} by invitation. Cloudflare Access must also let them in; dude doesn't manage that.`
          : `Who is in ${orgName}.`}
        actions={admin && byInvitation ? <Button variant="primary" leadingIcon="plus" onClick={() => setInviting(true)} data-testid="invite">Invite people</Button> : undefined} />
      {problem ? <Callout tone="danger" data-testid="members-problem">{problem}</Callout> : null}
      <SettingsSection title={`Members · ${people.length}`}>
        <Table density="comfortable">
          <THead>
            <Tr>
              <Th width="45%">Person</Th>
              <Th hideWhenNarrow>Joined</Th>
              <Th>Role</Th>
              <Th align="right"><span className="ds-sr-only">Actions</span></Th>
            </Tr>
          </THead>
          <TBody>
            {people.map((p) => {
              const you = p.id === me?.id;
              return (
                <Tr key={p.id} data-testid="member" data-member={p.name}>
                  <Td>
                    <EntityLine
                      lead={<PersonAvatar person={p} size={32} aria-hidden title="" />}
                      name={p.name}
                      detail={[you ? "you" : null, p.email ?? "no email: signs in only with a key",
                        p.online ? "online" : whereWords(p) ? `active ${whereWords(p)}` : "not seen yet"].filter(Boolean).join(" · ")}
                    />
                  </Td>
                  <Td wrap muted hideWhenNarrow>{joined?.get(p.id) ?? (joined ? "joined before invitations" : "—")}</Td>
                  <Td fit>
                    {admin && !you ? (
                      <Select size="sm" aria-label={`Role of ${p.name}`} value={p.role} options={ROLES}
                        onValueChange={(role) => setRole(p, role as PersonRole)} />
                    ) : p.role === "admin" ? <Badge>{roleWord(p.role)}</Badge> : <span className="muted">{roleWord(p.role)}</span>}
                  </Td>
                  <Td align="right" fit>
                    {admin && !you ? (
                      <Button size="sm" variant="quiet" onClick={() => setRemoving(p)} data-testid="member-remove">Remove…</Button>
                    ) : null}
                  </Td>
                </Tr>
              );
            })}
          </TBody>
        </Table>
      </SettingsSection>
      {removing ? (
        <RemoveDialog orgName={orgName} person={removing} remove={() => client.removePerson(removing.id)} onClose={() => setRemoving(null)} onRemoved={onChanged} />
      ) : null}
      {inviting ? (
        <InviteDialog api={api} orgName={orgName} memberEmails={people.flatMap((p) => (p.email ? [p.email] : []))}
          onClose={() => setInviting(false)} onInvited={() => onInvited?.()} />
      ) : null}
    </div>
  );
}

/** Removing someone, in the design's words; the API's refusal (the last admin) is shown in it. */
export function RemoveDialog({ orgName, person, remove, onClose, onRemoved }: {
  orgName: string;
  person: { name: string };
  remove: () => Promise<unknown>;
  onClose: () => void;
  onRemoved: () => void;
}) {
  const { busy, problem, save } = useSave();
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} tone="danger" size="sm" title={`Remove ${person.name} from ${orgName}?`}
      description={<>
        Their keys for {orgName} stop working at once, and so does their next page in {orgName}. Tasks they own pass to the next person on them; what they did keeps their name.
        {" "}To bring them back, invite them again; their old keys stay revoked. If {orgName} is their only organisation, dude stops letting them in.
      </>}
      footer={
        <>
          <Button variant="quiet" onClick={onClose}>Cancel</Button>
          <Button variant="danger" solid disabled={busy} data-testid="member-remove-confirm"
            onClick={() => void save(remove, () => {
              onClose();
              onRemoved();
            }, `${person.name} removed`)}>
            Remove {person.name.split(" ")[0]}
          </Button>
        </>
      }>
      {problem ? <Callout tone="danger" data-testid="remove-problem">{problem}</Callout> : null}
    </Dialog>
  );
}
