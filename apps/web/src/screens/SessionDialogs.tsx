/**
 * A brainstorm session's dialogs: what it reads (Link projects), who is in
 * it (Share), and handing it to someone else (Make owner). Only its owner
 * opens them; the orchestrator checks again.
 */

import { useEffect, useState, type ReactNode } from "react";
import type { NavProject } from "@dude/design-system";
import { firstName } from "@dude/design-system";
import { PersonAvatar, SESSION_ROLE_WORD } from "@dude/design-system/components";
import { Button, Callout, Checkbox, ChoiceList, Dialog, Select } from "@dude/design-system/primitives";
import type { SessionDetail, SessionLink, SessionMemberView } from "@dude/domain";
import { ApiError, type ApiClient, type Repository } from "../api/client.ts";
import { errorText } from "../hooks/useSave.tsx";
import { usePeople } from "../people.tsx";

const MEMBER_ROLES = [
  { value: "chat" as const, label: SESSION_ROLE_WORD.chat },
  { value: "read" as const, label: SESSION_ROLE_WORD.read },
];

const HANDOVER_ROLES = [
  ...MEMBER_ROLES,
  { value: "leave" as const, label: "Leave", description: "You lose the conversation; what you filed stays yours." },
];

/** Which projects and repositories a session reads; a new session's too. */
export function LinkDialog({ client, projects, linked, open, onClose, onSave, title = "Link projects", saveLabel = "Link", lead }: {
  client: ApiClient;
  projects: readonly NavProject[];
  linked: SessionDetail["session"]["projects"];
  open: boolean;
  onClose: () => void;
  onSave: (links: SessionLink[]) => Promise<void>;
  title?: string;
  saveLabel?: string;
  /** Above the projects: a new session's title. */
  lead?: ReactNode;
}) {
  // Picked repositories by project; a project picked with none reads only its tasks.
  const [picked, setPicked] = useState<Map<string, Set<string>>>(new Map());
  const [repos, setRepos] = useState<Map<string, Repository[]>>(new Map());
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<string | null>(null);
  // A refusal of the picks themselves (two projects under one key): the
  // whole sentence, over the list it is about, not the footer's one line.
  const [refused, setRefused] = useState<string | null>(null);
  useEffect(() => {
    if (!open) return;
    setPicked(new Map(linked.map((p) => [p.id, new Set(p.repositories.map((r) => r.id))])));
    setProblem(null);
    setRefused(null);
  }, [open, linked]);
  const want = [...picked.keys()].filter((id) => !repos.has(id)).join(",");
  useEffect(() => {
    if (!want) return;
    for (const id of want.split(",")) {
      void client.getProject(id).then((p) => setRepos((m) => new Map(m).set(id, p.repositories)), () => undefined);
    }
  }, [client, want]);

  const toggleProject = (id: string) => setPicked((m) => {
    const next = new Map(m);
    if (next.has(id)) next.delete(id);
    else next.set(id, new Set());
    return next;
  });
  const toggleRepo = (projectId: string, repoId: string) => setPicked((m) => {
    const next = new Map(m);
    const set = new Set(next.get(projectId));
    if (set.has(repoId)) set.delete(repoId);
    else set.add(repoId);
    next.set(projectId, set);
    return next;
  });
  const save = async () => {
    setBusy(true);
    setProblem(null);
    setRefused(null);
    try {
      await onSave([...picked].map(([projectId, ids]) => ({ projectId, repositoryIds: [...ids] })));
      onClose();
    } catch (err) {
      if (err instanceof ApiError && err.status === 409) setRefused(err.message);
      else setProblem(errorText(err));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()} size="md" title={title}
      description="The agent reads the projects you link: their tasks, pull requests and findings, and the repositories you tick, checked out read-only. It changes nothing."
      footerProblem={problem}
      footer={<>
        <Button variant="quiet" onClick={onClose}>Cancel</Button>
        <Button variant="primary" disabled={busy} onClick={() => void save()} data-testid="link-save">{saveLabel}</Button>
      </>}>
      {lead}
      {refused ? <Callout tone="danger" data-testid="link-refused">{refused}</Callout> : null}
      <ul className="sessionLinkList" data-testid="link-projects">
        {projects.map((p) => (
          <li key={p.id}>
            <Checkbox label={p.name} checked={picked.has(p.id)} onCheckedChange={() => toggleProject(p.id)} data-testid="link-project" data-project={p.id} />
            {picked.has(p.id) ? (
              <ul className="sessionLinkRepos">
                {(repos.get(p.id) ?? []).map((r) => (
                  <li key={r.id}>
                    <Checkbox label={`${r.name} · ${r.defaultBranch}`} checked={picked.get(p.id)!.has(r.id)}
                      onCheckedChange={() => toggleRepo(p.id, r.id)} data-testid="link-repository" data-repository={r.id} />
                  </li>
                ))}
              </ul>
            ) : null}
          </li>
        ))}
      </ul>
    </Dialog>
  );
}

/**
 * Who is in a session and what each can do; adding people. Before the
 * first share the dialog says the new people see everything said so far.
 */
export function ShareDialog({ client, detail, open, onClose, onChanged, onMakeOwner }: {
  client: ApiClient;
  detail: SessionDetail;
  open: boolean;
  onClose: () => void;
  onChanged: () => void;
  onMakeOwner: (member: SessionMemberView) => void;
}) {
  const people = usePeople();
  const [adding, setAdding] = useState<string[]>([]);
  const [role, setRole] = useState<"chat" | "read">("chat");
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<string | null>(null);
  useEffect(() => {
    if (open) {
      setAdding([]);
      setProblem(null);
    }
  }, [open]);
  const members = detail.session.people;
  const inIt = new Set(members.map((m) => m.person.id));
  const candidates = people.all.filter((p) => !inIt.has(p.id) && !adding.includes(p.id));
  const shared = members.some((m) => m.role !== "owner");
  const act = async (what: () => Promise<unknown>) => {
    setBusy(true);
    setProblem(null);
    try {
      await what();
      onChanged();
      return true;
    } catch (err) {
      setProblem(errorText(err));
      return false;
    } finally {
      setBusy(false);
    }
  };
  const names = adding.map((id) => firstName(people.names.get(id) ?? "someone"));
  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()} size="md" title={`Share “${detail.session.title}”`}
      description="People you add see this conversation and its linked projects' checkouts. They can't see your other sessions."
      footerProblem={problem}
      footerStart={<span className="muted">They're told in their inbox. Nothing is posted anywhere else.</span>}
      footer={<>
        <Button variant="quiet" onClick={onClose}>{adding.length > 0 ? "Cancel" : "Done"}</Button>
        {adding.length > 0 ? (
          <Button variant="primary" disabled={busy} data-testid="share-send" onClick={() => void act(() => client.inviteToSession(detail.session.id, adding, role))
            .then((ok) => ok && setAdding([]))}>
            Share with {adding.length}
          </Button>
        ) : null}
      </>}>
      <div className="sessionShareAdd">
        <Select<string> size="sm" aria-label="Add people" placeholder="Add people…" value={undefined}
          options={candidates.map((p) => ({ value: p.id, label: p.name }))}
          onValueChange={(id) => setAdding((a) => [...a, id])} data-testid="share-add" />
        <Select<"chat" | "read"> size="sm" aria-label="What they can do" value={role} onValueChange={setRole}
          options={[
            { value: "chat", label: SESSION_ROLE_WORD.chat, description: "Write to the agent and file work as themselves" },
            { value: "read", label: SESSION_ROLE_WORD.read, description: "See the conversation; write nothing" },
          ]} />
      </div>
      {adding.length > 0 ? (
        <ul className="sessionShareNew" data-testid="share-adding">
          {adding.map((id) => (
            <li key={id}>
              <PersonAvatar person={people.byId.get(id) ?? { id, name: people.names.get(id) ?? id }} size={24} />
              {people.names.get(id) ?? id}
              <Button size="sm" variant="quiet" onClick={() => setAdding((a) => a.filter((x) => x !== id))}>Remove</Button>
            </li>
          ))}
        </ul>
      ) : null}
      {adding.length > 0 && !shared ? (
        <Callout tone="attention" data-testid="share-history">
          This session has been private. {names.join(" and ")} will see all {detail.session.messages} message{detail.session.messages === 1 ? "" : "s"} so far, from the first.
        </Callout>
      ) : null}
      <ul className="sessionShareMembers" data-testid="share-members">
        {members.map((m) => {
          const you = m.person.id === detail.you.id;
          return (
            <li key={m.person.id} data-person={m.person.id}>
              <PersonAvatar person={m.person} size={24} />
              <span>{m.person.name}{you ? " (you)" : ""}{m.accepted ? "" : m.becomesOwner ? " · invited as owner" : " · invited"}</span>
              {m.role === "owner" ? <span className="muted">Owner</span> : (
                <>
                  <Select<"chat" | "read"> size="sm" aria-label={`What ${m.person.name} can do`} value={m.role as "chat" | "read"} disabled={busy}
                    onValueChange={(r) => void act(() => client.setSessionRole(detail.session.id, m.person.id, r))}
                    options={MEMBER_ROLES} />
                  {m.accepted ? (
                    <Button size="sm" variant="quiet" disabled={busy} onClick={() => onMakeOwner(m)} data-testid="make-owner">Make owner…</Button>
                  ) : null}
                  <Button size="sm" variant="quiet" disabled={busy} data-testid="remove-member"
                    onClick={() => void act(() => client.removeFromSession(detail.session.id, m.person.id))}>Remove</Button>
                </>
              )}
            </li>
          );
        })}
      </ul>
    </Dialog>
  );
}

/** Handing the session over: the new owner decides who is in it from then on. */
export function MakeOwnerDialog({ client, detail, member, onClose, onChanged }: {
  client: ApiClient;
  detail: SessionDetail;
  member: SessionMemberView | null;
  onClose: () => void;
  onChanged: () => void;
}) {
  const [keep, setKeep] = useState<"chat" | "read" | "leave">("chat");
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<string | null>(null);
  const name = member ? firstName(member.person.name) : "";
  const hand = async () => {
    if (!member) return;
    setBusy(true);
    setProblem(null);
    try {
      await client.handOverSession(detail.session.id, member.person.id, keep);
      onChanged();
      onClose();
    } catch (err) {
      setProblem(errorText(err));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog open={member !== null} onOpenChange={(o) => !o && onClose()} size="sm" tone="attention"
      title={`Make ${name} the owner of “${detail.session.title}”?`}
      description={`${name} decides who's in it from now on, and can hand it on.`}
      footerProblem={problem}
      footer={<>
        <Button variant="quiet" onClick={onClose}>Cancel</Button>
        <Button variant="primary" disabled={busy} onClick={() => void hand()} data-testid="make-owner-confirm">Make {name} owner</Button>
      </>}>
      <ChoiceList label="You, after" options={HANDOVER_ROLES} value={keep} onChange={setKeep} />
      <p className="muted">Nothing you filed changes hands. Hand a task over on the task itself.</p>
    </Dialog>
  );
}
