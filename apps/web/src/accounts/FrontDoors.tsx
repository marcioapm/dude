/**
 * The pages outside any organisation (docs/design/accounts.md, *Flows*):
 * No organisation, You're invited (every invitation waiting for your
 * email), and an invitation link's accept page in each of its states.
 * They show only what the gate let this email see: its own identity, and
 * the organisation and inviter of an invitation Access proved is its own.
 */

import { useEffect, useState, type ReactNode } from "react";
import { FrontDoor, FrontDoorLink, FrontDoorMessage, OrgFace, PersonAvatar } from "@dude/design-system/components";
import { Button, Callout, Card, CardBody, CardFooter, CardHeader, FormStack, Input, KeyValueList, Spinner } from "@dude/design-system/primitives";
import { firstName } from "@dude/design-system";
import { ApiError, type ApiClient, type InvitationView, type MembershipInfo, type MyInvitation } from "../api/client.ts";
import { errorText } from "../hooks/useSave.tsx";
import { DudeMark } from "../DudeMark.tsx";
import { dayWords, roleWord, untilWords } from "./words.ts";
import { inOrganization } from "../place.ts";

/** "Signed in as … with Cloudflare Access. Sign out" at a front door's foot. */
export function SignedInAs({ email, onSignOut }: { email: string; onSignOut: () => void }) {
  return (
    <p data-testid="signed-in-as">
      Signed in as <b>{email}</b> with Cloudflare Access.{" "}
      <FrontDoorLink onClick={onSignOut} data-testid="front-sign-out">Sign out</FrontDoorLink>
    </p>
  );
}

/**
 * Access proved the email; it is in no organisation, holds no pending
 * invitation and is no super-admin. Nothing about this deployment is
 * shown: its own address, generic guidance, Check again and Sign out.
 */
export function NoOrganisation({ email, onCheck, onSignOut }: { email: string; onCheck: () => Promise<void>; onSignOut: () => void }) {
  const [checking, setChecking] = useState(false);
  return (
    <FrontDoor data-testid="no-org" mark={<DudeMark size={72} />}
      title="Your email isn't in any organisation on this dude"
      line={<>You signed in as <b>{email}</b>. dude lets people in by invitation.</>}
      footer="dude keeps no record of this visit. Cloudflare Access decides who reaches this page; dude decides who gets past it.">
      <Card>
        <CardBody>
          <FormStack>
            <p className="muted">If someone invited you, open the link they sent you. Links expire after 14 days; ask them for a new one.</p>
            <p className="muted">Signed in with the wrong address? Sign out and sign in with the one you were invited with.</p>
          </FormStack>
        </CardBody>
        <CardFooter>
          <Button variant="secondary" leadingIcon="retry" disabled={checking} data-testid="check-again"
            onClick={() => {
              setChecking(true);
              void onCheck().finally(() => setChecking(false));
            }}>
            {checking ? "Checking…" : "Check again"}
          </Button>
          <Button variant="quiet" leadingIcon="arrow-right" onClick={onSignOut} data-testid="no-org-sign-out">Sign out</Button>
        </CardFooter>
      </Card>
    </FrontDoor>
  );
}

/** Your face and name as others will see them; the name is sent with Join. */
function ProfileStrip({ name, email, photoUrl, onName }: { name: string; email: string; photoUrl: string | null; onName: (name: string) => void }) {
  return (
    <div className="profileStrip" data-testid="profile-strip">
      <PersonAvatar person={{ id: email, name: name || email, photoUrl }} size={56} ring={false} />
      <div className="profileStripFields">
        <Input label="Your name" value={name} onChange={(e) => onName(e.target.value)} hint="How everyone sees you, in every organisation." data-testid="invited-name" />
        <KeyValueList items={[{ label: "Email", value: <span>{email} <span className="muted">· from Cloudflare Access</span></span> }]} />
      </div>
    </div>
  );
}

export interface InvitedProps {
  email: string;
  name: string | null;
  photoUrl: string | null;
  invitations: readonly MyInvitation[];
  superAdmin: boolean;
  onJoin: (invitation: MyInvitation, name: string) => Promise<void>;
  onAdmin: () => void;
  onSignOut: () => void;
}

/**
 * First sign-in at the bare address with invitations waiting: dude lets
 * them in only to answer them. One card per invitation, Join or Not now;
 * the name above travels with Join.
 */
export function YoureInvited({ email, name: given, photoUrl, invitations, superAdmin, onJoin, onAdmin, onSignOut }: InvitedProps) {
  const [name, setName] = useState(given ?? "");
  const [later, setLater] = useState<ReadonlySet<string>>(new Set());
  const [joining, setJoining] = useState<string | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const who = firstName(name.trim() || given || email.split("@")[0]!);
  const many = invitations.length > 1;
  return (
    <FrontDoor data-testid="invited" mark={<DudeMark size={72} />}
      title={`You're invited, ${who}`}
      line={many
        ? `${invitations.length === 2 ? "Two" : invitations.length} organisations on this dude sent you an invitation link. dude lets people in by invitation: join one to start; the other${invitations.length > 2 ? "s wait" : " waits"} in your menu.`
        : "An organisation on this dude sent you an invitation link. dude lets people in by invitation: join it to start."}
      footer={<>
        <SignedInAs email={email} onSignOut={onSignOut} />
        {superAdmin ? <FrontDoorLink onClick={onAdmin} data-testid="invited-admin">All organisations</FrontDoorLink> : null}
      </>}>
      <Card>
        <CardHeader title="You" />
        <CardBody><ProfileStrip name={name} email={email} photoUrl={photoUrl} onName={setName} /></CardBody>
      </Card>
      {problem ? <Callout tone="danger">{problem}</Callout> : null}
      <ul className="inviteCards" role="list" aria-label="Your invitations">
        {invitations.map((i) => (
          <li key={i.code} data-testid="invitation-card" data-org={i.organization.slug}>
            <Card variant="raised">
              <div className="inviteCard">
                <OrgFace org={i.organization} size={40} aria-hidden />
                <div className="inviteCardText">
                  <b>{i.organization.name}</b>
                  <span className="muted">{roleWord(i.role)} · {i.inviter === "the operator" ? "The operator" : i.inviter} sent you this link on {dayWords(i.createdAt)} · expires {untilWords(i.expiresAt)}</span>
                </div>
                <div className="inviteCardActions">
                  {later.has(i.code) ? <span className="muted">In your menu for later</span> : (
                    <>
                      <Button variant="quiet" onClick={() => setLater((s) => new Set(s).add(i.code))}>Not now</Button>
                      <Button variant="primary" disabled={joining !== null} data-testid={`join-${i.organization.slug}`}
                        onClick={() => {
                          setJoining(i.code);
                          setProblem(null);
                          onJoin(i, name.trim()).catch((err: unknown) => {
                            setProblem(errorText(err));
                            setJoining(null);
                          });
                        }}>
                        {joining === i.code ? "Joining…" : `Join ${i.organization.name}`}
                      </Button>
                    </>
                  )}
                </div>
              </div>
            </Card>
          </li>
        ))}
      </ul>
      <p className="fineCentre" data-testid="invited-note">Until you join one, this is all dude shows you. Joining makes your account, with the name and photo above.</p>
    </FrontDoor>
  );
}

/** What an invitation link shows the signed-in email. */
export type AcceptState =
  | { kind: "loading" }
  | { kind: "join"; invitation: InvitationView; first: boolean }
  | { kind: "member"; invitation: InvitationView }
  | { kind: "wrong" }
  | { kind: "invalid" }
  | { kind: "keys" };

export function acceptStateOf(result: InvitationView | ApiError, hasMembership: boolean): AcceptState {
  if (result instanceof ApiError) {
    if (result.code === "invitation_for_another_email") return { kind: "wrong" };
    if (result.code === "access_required") return { kind: "keys" };
    return { kind: "invalid" };
  }
  if (result.state === "member") return { kind: "member", invitation: result };
  return { kind: "join", invitation: result, first: !hasMembership };
}

export interface AcceptProps {
  client: ApiClient;
  code: string;
  email: string;
  /** Your memberships, when you have any: the role "already in" keeps. */
  memberships: readonly MembershipInfo[];
  /** Join or Open went through: open the organisation's page there. */
  onJoined: (organization: { id: string; name: string; slug: string }, hash: string, invitation: InvitationView) => void;
  onNotNow: () => void;
  onHome: () => void;
  onSignOut: () => void;
  /** For the different-email state: Access's logout, so they can sign in with the invited address. */
  onSwitchAccount: () => void;
}

/**
 * `#/invite/<code>`, every state, outside any organisation. A different
 * email learns nothing about the invitation, and its one action signs out
 * of Access: accepting is never attempted as the wrong person.
 */
export function AcceptInvitation({ client, code, email, memberships, onJoined, onNotNow, onHome, onSignOut, onSwitchAccount }: AcceptProps) {
  const [state, setState] = useState<AcceptState>({ kind: "loading" });
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<string | null>(null);
  useEffect(() => {
    let live = true;
    client.invitation(code).then(
      (v) => live && setState(acceptStateOf(v, memberships.length > 0)),
      (err: unknown) => live && setState(err instanceof ApiError ? acceptStateOf(err, memberships.length > 0) : { kind: "invalid" }),
    );
    return () => {
      live = false;
    };
  }, [client, code, memberships.length]);

  const accept = (invitation: InvitationView) => {
    setBusy(true);
    setProblem(null);
    client.acceptInvitation(code).then(
      ({ organization }) => onJoined(organization, inOrganization(organization.slug, state.kind === "member" ? "#/" : "#/welcome"), invitation),
      (err: unknown) => {
        setBusy(false);
        if (err instanceof ApiError && (err.status === 403 || err.status === 404)) setState(acceptStateOf(err, memberships.length > 0));
        else setProblem(errorText(err));
      },
    );
  };

  const page = (testId: string, body: ReactNode, footer: ReactNode, centred = false) => (
    <FrontDoor data-testid={testId} mark={<DudeMark size={72} />} footer={<SignedInAs email={email} onSignOut={onSignOut} />}>
      <Card>
        <CardBody>{body}</CardBody>
        {problem ? <CardBody><Callout tone="danger">{problem}</Callout></CardBody> : null}
        <CardFooter className={centred ? "cardFooterCentred" : undefined}>{footer}</CardFooter>
      </Card>
    </FrontDoor>
  );

  switch (state.kind) {
    case "loading":
      return <FrontDoor data-testid="accept-loading" mark={<DudeMark size={72} />}><div className="centered"><Spinner label="Opening the invitation…" /></div></FrontDoor>;
    case "invalid":
      return page("accept-invalid",
        <FrontDoorMessage icon="link" title="This invitation can't be used" line="Ask whoever sent it for a new link." />,
        <Button variant="secondary" leadingIcon="arrow-right" onClick={onHome} data-testid="open-dude">Open dude</Button>);
    case "keys":
      return page("accept-keys",
        <FrontDoorMessage icon="link" title="Invitation links need Cloudflare Access" line="This dude signs browsers in with API keys, so it has nobody to bind an invitation to." />,
        <Button variant="secondary" leadingIcon="arrow-right" onClick={onHome} data-testid="open-dude">Open dude</Button>);
    case "wrong":
      return page("accept-wrong",
        <FrontDoorMessage icon="link" title="This invitation is for a different email address." line="or ask whoever sent it for a new link." />,
        <Button variant="primary" onClick={onSwitchAccount} data-testid="switch-account">Accept invitation</Button>, true);
    case "member": {
      const i = state.invitation;
      const role = memberships.find((m) => m.id === i.organization.id)?.role;
      return page("accept-member",
        <FrontDoorMessage lead={<OrgFace org={i.organization} size={48} aria-hidden />} title={`You're already in ${i.organization.name}`}
          line={`You joined from this link.${role ? ` Your role stays ${roleWord(role)}.` : ""}`} />,
        <Button variant="primary" disabled={busy} onClick={() => accept(i)} data-testid="open-org">Open {i.organization.name}</Button>);
    }
    case "join": {
      const i = state.invitation;
      return (
        <FrontDoor data-testid={state.first ? "accept-first" : "accept-join"} mark={<DudeMark size={72} />} footer={<SignedInAs email={email} onSignOut={onSignOut} />}>
          <Card>
            <CardBody>
              <FrontDoorMessage lead={<OrgFace org={i.organization} size={48} aria-hidden />}
                title={`Join ${i.organization.name} as ${roleWord(i.role).toLowerCase()}`}
                line={`Invited by ${i.inviter} on ${dayWords(i.createdAt)}.`}
                facts={<KeyValueList items={[
                  { label: "For", value: <span>{i.email} <span className="muted">· you</span></span> },
                  { label: "Role", value: roleWord(i.role) },
                  { label: "Expires", value: untilWords(i.expiresAt) },
                ]} />} />
            </CardBody>
            {state.first ? (
              <CardBody>
                <Callout tone="info">You aren't in any organisation on this dude yet. Join makes your account, with the name and photo Cloudflare Access gave; you can change both after. Not now keeps this invitation waiting for you until it expires.</Callout>
              </CardBody>
            ) : null}
            {problem ? <CardBody><Callout tone="danger">{problem}</Callout></CardBody> : null}
            <CardFooter>
              <Button variant="quiet" onClick={onNotNow} data-testid="not-now">Not now</Button>
              <Button variant="primary" disabled={busy} onClick={() => accept(i)} data-testid="accept-join-button">
                {busy ? "Joining…" : `Join ${i.organization.name}`}
              </Button>
            </CardFooter>
          </Card>
        </FrontDoor>
      );
    }
  }
}
