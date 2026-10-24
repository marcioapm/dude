/**
 * Where a chat provider's private Connect button lands
 * (`#/o/<org>/connect/<provider>?state=…`): the person, signed in to dude,
 * signs in with the provider to link the two accounts. dude never matches
 * people by email: this sign-in is the link. Once linked, the mention that
 * offered the button is routed. The callback comes back here with
 * `?chat=<provider>&outcome=<o>`.
 */

import { useEffect, useState } from "react";
import { FrontDoorMessage, ProviderGlyph } from "@dude/design-system/components";
import { Button, Callout, Card, CardBody, CardFooter, Spinner } from "@dude/design-system/primitives";
import type { ChatConnectStateView } from "@dude/domain";
import type { ApiClient } from "../api/client.ts";
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

export function ConnectScreen({ client, provider, state, onSettings }: {
  client: ApiClient;
  provider: string;
  state: string;
  onSettings: () => void;
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
    client.chatConnectState(provider, state).then(setView, (err: unknown) => setProblem(errorText(err)));
  }, [client, provider, state]);
  if (problem) return <div className="connectPage"><Callout tone="danger">{problem}</Callout></div>;
  if (!view) return <div className="centered"><Spinner label="Loading…" /></div>;
  const pv = providerView(view.provider);
  const name = view.provider.name;
  const shown = outcome ? (CONNECT_OUTCOME[outcome] ?? CONNECT_FAILED) : null;
  const words: Record<ChatConnectStateView["status"], { title: string; line: string }> = {
    ready: { title: `Connect your ${name} account`,
      line: `When you mention @dude in ${view.workspaceName ?? name}, dude will act as you here. dude never matches people by email: signing in with ${name} is the link. You can disconnect in Your settings.` },
    connected: { title: `Your ${name} account is connected`, line: view.connected ? `As @${view.connected.handle}. Mentions of @dude act as you.` : "" },
    expired: { title: "This connect link has expired", line: `It lasts 15 minutes and works once. Mention @dude in ${name} again for a new one, or connect from Your settings.` },
    wrong_organization: { title: "This link is for another organisation", line: "You are signed in to dude in a different organisation from the one this link was made for. Switch organisation, then open the link again." },
  };
  const w = words[view.status];
  return (
    <div className="connectPage" data-testid="chat-connect" data-status={view.status}>
      {shown ? <Callout tone={shown.tone} data-testid="chat-connect-outcome">{shown.text(name)}</Callout> : null}
      <Card>
        <CardBody>
          <FrontDoorMessage lead={<span aria-hidden><ProviderGlyph provider={pv} size={20} /></span>} title={w.title} line={w.line} />
          {saveProblem ? <Callout tone="danger">{saveProblem}</Callout> : null}
        </CardBody>
        <CardFooter>
          {view.status === "ready" ? (
            <Button variant="primary" disabled={busy} data-testid="chat-connect-go"
              onClick={() => void save(async () => window.location.assign((await client.connectChat(provider, state)).url))}>Connect {name}</Button>
          ) : (
            <Button variant="secondary" onClick={onSettings}>Your settings</Button>
          )}
        </CardFooter>
      </Card>
    </div>
  );
}
