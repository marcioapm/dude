/**
 * Welcome to <organisation>: where Join lands, inside the organisation's
 * shell (docs/design/accounts.md, *First sign-in from a link*). You as
 * others will see you, editable here; who invited you; its projects; how
 * dude works; Start your first session. Shown once per membership: saving
 * here or leaving it sets `welcomedAt` (App).
 */

import { useEffect, useState } from "react";
import { EntityLine, FrontDoor, HowList, OrgFace, PersonAvatar, ProjectAvatar } from "@dude/design-system/components";
import { Button, Callout, Card, CardBody, CardFooter, CardHeader, Input, KeyValueList } from "@dude/design-system/primitives";
import { firstName, projectCounts, type NavProject } from "@dude/design-system";
import type { ApiClient, Member, MembershipInfo } from "../api/client.ts";
import { useSave } from "../hooks/useSave.tsx";
import { FacePicker } from "../screens/FacePicker.tsx";

const HOW = [
  { icon: "conductor", title: "Talk it through", body: "On a task: you and the conductor work it out in Chat, and it runs the agents." },
  { icon: "git-pr", title: "Deliver", body: "On a task: dude implements, reviews and opens a pull request on its own, and stops there." },
  { icon: "brainstorm", title: "Sessions", body: "Before there is a task: a brainstorm reads what you link and proposes work you file." },
] as const;

/** Who invited you, kept by the accept page for the welcome it opens: the welcome has no invitation to read. */
export const JOINED_FROM = "dude.joinedFrom";

export function JoinedScreen({ client, organization, projects, people, me, onChanged, onStart, onWelcomed }: {
  client: ApiClient;
  organization: MembershipInfo;
  projects: readonly NavProject[] | null;
  people: number;
  me: Member | null;
  onChanged: () => void;
  onStart: () => void;
  /** Saved here: the membership is welcomed, and this page is not shown again. */
  onWelcomed: () => void;
}) {
  const [name, setName] = useState(me?.name ?? "");
  useEffect(() => setName(me?.name ?? ""), [me?.name]);
  const { busy, problem, save } = useSave();
  const [inviter] = useState(() => {
    const kept = sessionStorage.getItem(`${JOINED_FROM}:${organization.id}`);
    return kept ? (JSON.parse(kept) as { inviter: string; role: string }) : null;
  });
  const you = me ? { ...me, name: name || me.name } : null;
  const list = projects ?? [];
  return (
    <FrontDoor variant="inside" data-testid="first-one"
      title={`Welcome to ${organization.name}${me ? `, ${firstName(me.name)}` : ""}`}
      line={inviter
        ? `You joined from ${inviter.inviter}'s invitation link, as ${inviter.role === "admin" ? "an admin" : "a member"}. Your account was made when you joined, with the name and photo Cloudflare Access gave.`
        : `You're ${organization.role === "admin" ? "an admin" : "a member"} of ${organization.name}.`}
      mark={<OrgFace org={organization} size={48} aria-hidden />}>
      {you ? (
        <Card>
          <CardHeader title="You" />
          <CardBody>
            <div className="profileStrip">
              <FacePicker testId="welcome-photo" face={<PersonAvatar person={you} size={56} ring={false} />} hasImage={Boolean(you.photoUrl)} busy={busy}
                onPick={(image) => void save(async () => client.uploadMyPhoto(await image), onChanged, "Photo updated")}
                onRemove={() => void save(() => client.updateMe({ photoUrl: null }), onChanged, "Photo removed")} />
              <div className="profileStripFields">
                <Input label="Your name" value={name} onChange={(e) => setName(e.target.value)} hint="How everyone sees you, in every organisation." data-testid="welcome-name" />
                <KeyValueList items={[{ label: "Email", value: <span>{you.email} <span className="muted">· from Cloudflare Access</span></span> }]} />
              </div>
            </div>
          </CardBody>
          {problem ? <CardBody><Callout tone="danger">{problem}</Callout></CardBody> : null}
          <CardFooter>
            <Button variant="secondary" disabled={busy || !name.trim() || name.trim() === me?.name} data-testid="welcome-save"
              onClick={() => void save(() => client.updateMe({ name: name.trim() }), () => { onChanged(); onWelcomed(); }, "Name saved")}>Save</Button>
          </CardFooter>
        </Card>
      ) : null}
      <Card>
        <CardHeader title={`${organization.name}'s projects`} actions={<span className="muted">{list.length} {list.length === 1 ? "project" : "projects"} · {people} {people === 1 ? "person" : "people"}</span>} />
        <CardBody padding="flush">
          {list.length ? (
            <ul className="plainRows" role="list">
              {list.map((p) => {
                const c = projectCounts(p, me?.id ?? null);
                return (
                  <li key={p.id} className="plainRow">
                    <EntityLine lead={<ProjectAvatar project={{ id: p.id, name: p.name, imageUrl: p.imageUrl }} size={24} />} name={p.name}
                      detail={[c.active ? `${c.active} running` : null, c.ready ? `${c.ready} ready` : null].filter(Boolean).join(" · ") || "Nothing running"} />
                  </li>
                );
              })}
            </ul>
          ) : <p className="plainRow muted">No projects yet.</p>}
        </CardBody>
      </Card>
      <section className="howBlock">
        <h2 className="ds-label">How dude works</h2>
        <HowList items={HOW} label="How dude works" />
      </section>
      <div className="nextStep">
        <Button variant="primary" size="lg" leadingIcon="brainstorm" onClick={onStart} data-testid="start">Start your first session</Button>
        <span className="muted">Or open a project in the sidebar.</span>
      </div>
    </FrontDoor>
  );
}
