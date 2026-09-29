/**
 * The organization's members: everyone, by face, with their role; for an
 * admin, inviting (a key shown once — the invitation, until there is
 * sign-in), changing roles, and removing (every key of theirs stops at
 * once). Anyone else sees the list.
 */

import { useState } from "react";
import { EntityLine, PersonAvatar } from "@dude/design-system/components";
import {
  Badge,
  Button,
  Callout,
  Card,
  CardBody,
  CardHeader,
  Dialog,
  Input,
  Select,
  Table,
  TBody,
  Td,
  Th,
  THead,
  Tr,
} from "@dude/design-system/primitives";
import type { PersonRole } from "@dude/domain";
import type { ApiClient, Member } from "../api/client.ts";
import { errorText, FormDialog, useSave } from "../hooks/useSave.tsx";
import { whereWords } from "../people.tsx";
import { KeyShownOnce } from "./YouSections.tsx";

const ROLES = [
  { value: "member", label: "Member" },
  { value: "admin", label: "Admin" },
];

/** `me`, `people` and `onChanged` are the shell's (`usePeople`), so this list is as live as the sidebar's. */
export function MembersSection({ client, me, people, onChanged }: {
  client: ApiClient;
  me: Member | null;
  people: readonly Member[];
  onChanged: () => void;
}) {
  const [problem, setProblem] = useState<string | null>(null);
  const [inviting, setInviting] = useState(false);
  const [invited, setInvited] = useState<{ name: string; key: string } | null>(null);
  const [removing, setRemoving] = useState<Member | null>(null);
  const admin = me?.role === "admin";


  const setRole = (person: Member, role: PersonRole) => {
    setProblem(null);
    void client.updatePerson(person.id, { role }).then(onChanged, (err: unknown) => setProblem(errorText(err)));
  };

  return (
    <Card data-testid="members">
      <CardHeader
        title="Members"
        actions={admin ? (
          <Button variant="primary" leadingIcon="plus" size="sm" onClick={() => setInviting(true)} data-testid="invite">
            Invite people
          </Button>
        ) : null}
      />
      <CardBody>
        <p className="muted">
          Everyone here can create, deliver and steer tasks. Admins manage the organization: its members and its settings.
        </p>
        {problem ? <Callout tone="danger">{problem}</Callout> : null}
        {people.length > 0 ? (
          <Table density="comfortable">
            <THead>
              <Tr>
                <Th>Person</Th>
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
                        detail={[you ? "you" : null, p.email, p.online ? "online" : whereWords(p) ? `active ${whereWords(p)}` : "not seen yet"]
                          .filter(Boolean).join(" · ")}
                      />
                    </Td>
                    <Td>
                      {admin && !you ? (
                        <Select size="sm" aria-label={`Role of ${p.name}`} value={p.role} options={ROLES}
                          onValueChange={(role) => setRole(p, role as PersonRole)} />
                      ) : p.role === "admin" ? <Badge>Admin</Badge> : <span className="muted">Member</span>}
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
        ) : null}
      </CardBody>
      {inviting ? (
        <InviteDialog client={client} onClose={() => setInviting(false)} onInvited={(made) => {
          setInviting(false);
          setInvited(made);
          onChanged();
        }} />
      ) : null}
      {invited ? (
        <KeyShownOnce title={`${invited.name} is invited`} secret={invited.key} onClose={() => setInvited(null)}
          description={`Give ${invited.name} this key to sign in with. It is not shown again.`} />
      ) : null}
      {removing ? (
        <RemoveDialog client={client} person={removing} onClose={() => setRemoving(null)} onRemoved={onChanged} />
      ) : null}
    </Card>
  );
}

function InviteDialog({ client, onClose, onInvited }: {
  client: ApiClient;
  onClose: () => void;
  onInvited: (made: { name: string; key: string }) => void;
}) {
  const [name, setName] = useState("");
  const [email, setEmail] = useState("");
  const [role, setRole] = useState<PersonRole>("member");
  const { busy, problem, save } = useSave();
  return (
    <FormDialog open onOpenChange={(o) => !o && onClose()} title="Invite someone"
      description="They get a key to sign in with, shown to you once."
      submitLabel="Invite" submitTestId="invite-submit" canSubmit={!busy && Boolean(name.trim()) && email.includes("@")}
      problem={problem}
      onSubmit={() => {
        let key = "";
        void save(async () => { key = (await client.invitePerson({ name: name.trim(), email: email.trim(), role })).key; },
          () => onInvited({ name: name.trim(), key }));
      }}>
      <Input label="Name" autoFocus value={name} onChange={(e) => setName(e.target.value)} data-testid="invite-name" />
      <Input label="Email" type="email" value={email} onChange={(e) => setEmail(e.target.value)} data-testid="invite-email" />
      <Select label="Role" value={role} options={ROLES} onValueChange={(v) => setRole(v as PersonRole)} />
    </FormDialog>
  );
}

function RemoveDialog({ client, person, onClose, onRemoved }: {
  client: ApiClient;
  person: Member;
  onClose: () => void;
  onRemoved: () => void;
}) {
  const { busy, problem, save } = useSave();
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} tone="danger" size="sm" title={`Remove ${person.name}?`}
      description="Every key of theirs stops working at once. Tasks they own become nobody's; what they did keeps their name."
      footer={
        <>
          <Button variant="quiet" onClick={onClose}>Cancel</Button>
          <Button variant="danger" solid disabled={busy} data-testid="member-remove-confirm"
            onClick={() => void save(() => client.removePerson(person.id), () => {
              onClose();
              onRemoved();
            }, `${person.name} removed`)}>
            Remove {person.name}
          </Button>
        </>
      }>
      {problem ? <Callout tone="danger">{problem}</Callout> : null}
    </Dialog>
  );
}
