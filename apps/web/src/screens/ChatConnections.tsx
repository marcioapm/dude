/**
 * Your settings → Connections, for chat: one card per chat provider the
 * organisation has installed, in install order. Connect signs in with the
 * provider to link your account (never by email); the callback lands here
 * with `?chat=<provider>&outcome=<o>`, shown once on that provider's card.
 */

import { useEffect, useRef, useState } from "react";
import { ConnectionCard, IdentityPair, ProviderGlyph } from "@dude/design-system/components";
import { formatTimestamp } from "@dude/design-system";
import { Button, Callout, Dialog } from "@dude/design-system/primitives";
import type { ChatOverview, ChatProviderEntry } from "@dude/domain";
import type { ApiClient } from "../api/client.ts";
import { useSave } from "../hooks/useSave.tsx";
import { CONNECT_FAILED, CONNECT_OUTCOME } from "./ConnectScreen.tsx";
import { providerView } from "./ChatSettings.tsx";

export function ChatConnections({ client, me, onInstalled }: {
  client: ApiClient;
  me?: { id: string; name: string } | null;
  /** Whether any chat provider is installed, once read: the Connections heading shows for one. */
  onInstalled?: (any: boolean) => void;
}) {
  const [overview, setOverview] = useState<ChatOverview | null>(null);
  const [read, setRead] = useState(0);
  const [outcome, setOutcome] = useState(() => {
    const q = new URLSearchParams(window.location.search);
    return q.get("chat") ? { provider: q.get("chat")!, outcome: q.get("outcome") ?? "" } : null;
  });
  // Read once: off the address, so neither a reload nor a later Disconnect shows it again.
  useEffect(() => {
    const url = new URL(window.location.href);
    if (!url.searchParams.has("chat")) return;
    url.searchParams.delete("chat");
    url.searchParams.delete("outcome");
    window.history.replaceState(window.history.state, "", `${url.pathname}${url.search}${url.hash}`);
  }, []);
  useEffect(() => {
    void client.chatOverview().then(setOverview, () => setOverview(null));
  }, [client, read]);
  const installed = overview?.providers.filter((p) => p.installed) ?? [];
  const any = installed.length > 0;
  useEffect(() => onInstalled?.(any), [onInstalled, any]);
  if (!any) return null;
  return (
    <section className="chatConnections" aria-label="Connections" data-testid="chat-connections">
      {installed.map((p) => (
        <ChatConnection key={p.provider.id} client={client} entry={p} me={me ?? null}
          outcome={outcome?.provider === p.provider.id ? outcome.outcome : null}
          onChanged={() => { setOutcome(null); setRead((n) => n + 1); }} />
      ))}
    </section>
  );
}

function ChatConnection({ client, entry, me, outcome, onChanged }: {
  client: ApiClient;
  entry: ChatProviderEntry;
  me: { id: string; name: string } | null;
  outcome: string | null;
  onChanged: () => void;
}) {
  const { busy, problem, save } = useSave();
  const [disconnecting, setDisconnecting] = useState(false);
  const done = useRef(false);
  const pv = providerView(entry.provider);
  const name = entry.provider.name;
  const shown = outcome ? (CONNECT_OUTCOME[outcome] ?? CONNECT_FAILED) : null;
  const workspaceLabel = entry.provider.workspaceWord.charAt(0).toUpperCase() + entry.provider.workspaceWord.slice(1);
  const workspace = entry.workspaceDomain ?? entry.workspaceName;
  const c = entry.connected;
  return (
    <>
      <ConnectionCard testId={`chat-connection-${entry.provider.id}`} title={name} glyph={<ProviderGlyph provider={pv} size={16} />}
        intro={c ? `Mentions of @dude in ${entry.workspaceName ?? name} act as you.`
          : `Connect your ${name} account so dude can act for you when you mention @dude in ${entry.workspaceName ?? name}. dude never matches people by email.`}
        outcome={shown ? { tone: shown.tone, text: shown.text(name) } : null}
        connected={Boolean(c)} busy={busy}
        facts={c ? [{ label: "Connected as", value: me
          ? <IdentityPair provider={pv} external={{ handle: c.handle }} person={{ id: me.id, name: me.name }} />
          : `@${c.handle}` },
        ...(workspace ? [{ label: workspaceLabel, value: <span className="ds-mono" data-testid="chat-connection-workspace">{workspace}</span> }] : []),
        { label: "Since", value: <span data-testid="chat-connection-since">{formatTimestamp(c.linkedAt, "date")}
          {c.mentions !== undefined ? ` · ${c.mentions} ${c.mentions === 1 ? "mention" : "mentions"} in 30 days` : ""}</span> }]
          : undefined}
        connectLabel={`Connect ${name}`} disconnectLabel={`Disconnect ${name}…`}
        onConnect={c ? undefined : () => void save(async () => window.location.assign((await client.connectChat(entry.provider.id)).url))}
        onDisconnect={() => setDisconnecting(true)} />
      {problem ? <Callout tone="danger">{problem}</Callout> : null}
      {disconnecting ? (
        <Dialog open onOpenChange={(o) => !o && setDisconnecting(false)} tone="danger" size="sm" title={`Disconnect ${name}?`}
          description={`dude forgets which ${name} account is yours. Mentioning @dude there offers to connect again.`}
          footer={<>
            <Button variant="quiet" onClick={() => setDisconnecting(false)}>Cancel</Button>
            <Button variant="danger" solid disabled={busy} data-testid={`chat-disconnect-${entry.provider.id}-confirm`}
              onClick={() => void save(() => client.unlinkChat(entry.provider.id), () => {
                done.current = true;
                setDisconnecting(false);
                onChanged();
              }, `${name} disconnected`)}>Disconnect {name}</Button>
          </>} />
      ) : null}
    </>
  );
}
