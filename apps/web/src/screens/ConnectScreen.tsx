/**
 * Where a chat provider's private Connect button lands
 * (`#/connect/<provider>?state=…`, placed in its organisation once signed
 * in): the person, signed in to dude, signs in with the provider to link
 * the two accounts. dude never matches people by email: this sign-in is
 * the link. Once linked, the mention that offered the button is routed. The
 * callback comes back here with `?chat=<provider>&outcome=<o>`.
 */

import { useEffect, useState } from "react";
import { FrontDoor, FrontDoorLink, FrontDoorMessage, IdentityPair, ProviderGlyph } from "@dude/design-system/components";
import { Button, Callout, Card, CardBody, CardFooter, LinkButton, Spinner } from "@dude/design-system/primitives";
import type { ChatConnectStateView } from "@dude/domain";
import type { ApiClient } from "../api/client.ts";
import { DudeMark } from "../DudeMark.tsx";
import { errorText, useSave } from "../hooks/useSave.tsx";
import { providerView } from "./ChatSettings.tsx";

/** What the connect callback's outcome says; anything else reads as a failure. */
export const CONNECT_OUTCOME: Record<string, { tone: "success" | "attention" | "danger"; text: (name: string) => string }> = {
  connected: { tone: "success", text: (n) => `${n} connected. dude is picking up your message now.` },
  authorization_denied: { tone: "attention", text: (n) => `${n} sign-in was cancelled. Connect again when ready.` },
  invalid_or_expired: { tone: "danger", text: () => "This connect request expired or was already used. Mention @dude again for a new one." },
  workspace_not_installed: { tone: "danger", text: (n) => `That ${n} workspace is not the one this organisation installed.` },
  already_linked: { tone: "danger", text: (n) => `That ${n} account is already connected to someone else, or yours to another ${n} account.` },
};
export const CONNECT_FAILED = { tone: "danger" as const, text: (n: string) => `Connecting ${n} failed. Try again.` };

/** Who is signed in to dude here: the "Not you?" line names them. */
export interface ConnectViewer {
  readonly id: string;
  readonly name: string;
  readonly email?: string | null | undefined;
  readonly photoUrl?: string | null | undefined;
  readonly organisation?: string | undefined;
}

export function ConnectScreen({ client, provider, state, onSettings, me, onSignOut }: {
  client: ApiClient;
  provider: string;
  state: string;
  onSettings: () => void;
  me?: ConnectViewer | null;
  /** "Not you?": sign out, to sign in as whoever the link is for. */
  onSignOut?: () => void;
}) {
  const [view, setView] = useState<ChatConnectStateView | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const { busy, problem: saveProblem, save } = useSave();
  const [outcome] = useState(() => {
    const q = new URLSearchParams(window.location.search);
    return q.get("chat") === provider ? q.get("outcome") : null;
  });
  useEffect(() => {
    const url = new URL(window.location.href);
    if (!url.searchParams.has("chat")) return;
    url.searchParams.delete("chat");
    url.searchParams.delete("outcome");
    window.history.replaceState(window.history.state, "", `${url.pathname}${url.search}${url.hash}`);
  }, []);
  useEffect(() => {
    client.chatConnectState(provider, state).then((v) => {
      // Kept for after the sign-in, when the state is spent and the server no longer has it.
      if (v.backUrl) sessionStorage.setItem(`dude.chatBack.${state}`, v.backUrl);
      setView(v);
    }, (err: unknown) => setProblem(errorText(err)));
  }, [client, provider, state]);
  if (problem) return <FrontDoor variant="inside" mark={<DudeMark size={56} />}><Callout tone="danger">{problem}</Callout></FrontDoor>;
  if (!view) return <div className="centered"><Spinner label="Loading…" /></div>;
  const pv = providerView(view.provider);
  const name = view.provider.name;
  const shown = outcome ? (CONNECT_OUTCOME[outcome] ?? CONNECT_FAILED) : null;
  const where = view.workspaceName ?? name;
  const words: Record<ChatConnectStateView["status"], { title: string; line: string }> = {
    ready: { title: `Connect your ${name} account`,
      line: `When you mention @dude in ${where}, dude will act as you${me?.organisation ? ` in ${me.organisation}` : ""}. You can disconnect in Your settings.` },
    connected: { title: `Your ${name} account is connected`, line: `Mentions of @dude in ${where} act as you. dude is picking up the message that sent you here.` },
    expired: { title: "This connect link has expired", line: `It lasts 15 minutes and works once. Mention @dude in ${name} again for a new one, or connect from Your settings.` },
    wrong_organization: { title: "This link is for another organisation", line: "You are signed in to dude in a different organisation from the one this link was made for. Switch organisation, then open the link again." },
  };
  const w = words[view.status];
  const back = view.backUrl ?? sessionStorage.getItem(`dude.chatBack.${state}`);
  const pair = me && view.status !== "wrong_organization" && view.status !== "expired" ? (
    <IdentityPair size="lg" provider={pv}
      external={view.connected ? { handle: view.connected.handle, display: view.connected.displayName, workspace: view.workspaceName ?? undefined }
        : { handle: "you", display: `Your ${name} account`, workspace: view.workspaceName ?? undefined }}
      person={{ id: me.id, name: me.name, photoUrl: me.photoUrl ?? null, email: me.email ?? undefined, organisation: me.organisation }} />
  ) : null;
  return (
    <FrontDoor variant="inside" mark={<DudeMark size={56} />} data-testid="chat-connect" data-status={view.status}
      footer={me ? (
        <span data-testid="chat-connect-who">Signed in to dude as {me.email || me.name}
          {onSignOut ? <> · <FrontDoorLink onClick={onSignOut} data-testid="chat-connect-not-you">Not you?</FrontDoorLink></> : null}</span>
      ) : undefined}>
      {shown ? <Callout tone={shown.tone} data-testid="chat-connect-outcome">{shown.text(name)}</Callout> : null}
      <Card>
        <CardBody>
          <FrontDoorMessage lead={<span aria-hidden><ProviderGlyph provider={pv} size={20} /></span>} title={w.title} line={w.line}
            facts={pair} />
          {saveProblem ? <Callout tone="danger">{saveProblem}</Callout> : null}
        </CardBody>
        <CardFooter>
          {view.status === "ready" ? (
            <>
              <Button variant="quiet" onClick={onSettings}>Cancel</Button>
              <Button variant="primary" disabled={busy} data-testid="chat-connect-go"
                onClick={() => void save(async () => window.location.assign((await client.connectChat(provider, state)).url))}>Connect {name}</Button>
            </>
          ) : view.status === "connected" ? (
            <>
              <Button variant="quiet" onClick={onSettings}>Your settings</Button>
              {back ? <LinkButton variant="primary" href={back} data-testid="chat-connect-back">Back to {name}</LinkButton> : null}
            </>
          ) : (
            <Button variant="secondary" onClick={onSettings}>Your settings</Button>
          )}
        </CardFooter>
      </Card>
    </FrontDoor>
  );
}
