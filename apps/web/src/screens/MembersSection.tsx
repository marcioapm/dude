/** The organization's members, with admin role and removal controls. */

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
import { errorText, useSave } from "../hooks/useSave.tsx";
import { whereWords } from "../people.tsx";

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
  const [removing, setRemoving] = useState<Member | null>(null);
  const admin = me?.role === "admin";


  const setRole = (person: Member, role: PersonRole) => {
    setProblem(null);
    void client.updatePerson(person.id, { role }).then(onChanged, (err: unknown) => setProblem(errorText(err)));
  };

  return (
    <Card data-testid="members">
      <CardHeader title="Members" />
      <CardBody>
        <p className="muted">
          Everyone here can create, deliver and steer tasks. Admins manage the organization: its members and its settings.
        </p>
        <p className="muted">
          People join this organisation by invitation. Cloudflare Access must also let them in; dude doesn't manage that.
          Inviting people from this screen is not available yet.
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
      {removing ? (
        <RemoveDialog client={client} person={removing} onClose={() => setRemoving(null)} onRemoved={onChanged} />
      ) : null}
    </Card>
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
