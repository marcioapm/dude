/**
 * Where a session, task or epic came from outside dude, as the screens show
 * it: the origin fact (header meta, rail, board card) and, for a chat-born
 * session, the thread it was made from and how dude routed it. The
 * provider is named only by its entry (name and glyph), never here.
 */

import { useEffect, useState } from "react";
import { ExternalThread, OriginFact, ProviderGlyph, RoutingLine, providerGlyph, type ChatProvider, type ChatRoute } from "@dude/design-system/components";
import { ICON_NAMES } from "@dude/design-system";
import type { ChatOrigin, SessionChatOrigin } from "@dude/domain";
import type { ApiClient } from "./api/client.ts";

/** Providers dude knows by id, for the screens that have only an origin's provider id. */
const KNOWN_NAMES: Record<string, string> = { slack: "Slack" };

/** A provider from an origin's id: its known name, else the id itself; its glyph, or the neutral one. */
export function originProvider(id: string): ChatProvider {
  return { id, name: KNOWN_NAMES[id] ?? id.charAt(0).toUpperCase() + id.slice(1), glyph: providerGlyph(id, ICON_NAMES) };
}

/** "From Slack · #support · thread by Márcia", linked to the permalink. */
export function OriginLine({ origin, compact, testId }: { origin: ChatOrigin; compact?: boolean; testId?: string }) {
  return <OriginFact provider={originProvider(origin.provider)} place={origin.conversationName || "a conversation"} by={origin.requestedBy || null}
    href={origin.permalink || null} compact={compact} testId={testId} />;
}

/** An epic's origin in its header's meta, read from the project's epics; nothing for one made in dude. */
export function EpicOrigin({ client, projectId, epicId }: { client: ApiClient; projectId: string; epicId: string }) {
  const [origin, setOrigin] = useState<ChatOrigin | null>(null);
  useEffect(() => {
    let current = true;
    client.listEpics(projectId).then((r) => current && setOrigin(r.epics.find((e) => e.id === epicId)?.origin ?? null), () => undefined);
    return () => {
      current = false;
    };
  }, [client, projectId, epicId]);
  return origin ? <OriginLine origin={origin} testId="epic-origin" /> : null;
}

/** A board card's lead: the provider's glyph and name. */
export function OriginLead({ provider }: { provider: string }) {
  const p = originProvider(provider);
  return <span data-testid="card-origin"><ProviderGlyph provider={p} size={12} />{p.name}</span>;
}

/**
 * The top of a chat-born session's transcript: the thread quoted as
 * external text, then dude's routing line with who it was shared with.
 * `names` resolves the added people's ids to people.
 */
export function SessionOrigin({ origin, names }: { origin: SessionChatOrigin; names: (id: string) => { id: string; name: string } | null }) {
  const p = originProvider(origin.provider);
  const routing = origin.routing;
  const group = origin.conversationKind === "private" || origin.conversationKind === "group";
  return (
    <div className="sessionOrigin" data-testid="session-origin">
      {origin.thread?.length ? (
        <ExternalThread provider={p} place={origin.conversationName} href={origin.permalink || null} clip={6}
          messages={origin.thread.map((m) => ({ by: { display: m.by, person: m.inDude && m.person ? names(m.person) ?? { id: m.person, name: m.by } : null }, text: m.text }))} />
      ) : null}
      {routing ? (
        <RoutingLine route={routing.route as ChatRoute} confidence={routing.confidence} reasoning={routing.reasoning}
          fallback={(routing.askedRoute as ChatRoute | undefined) ?? null}
          shared={group ? { place: origin.conversationName, added: routing.added.flatMap((id) => names(id) ?? []), skipped: routing.skippedNames ?? routing.skipped } : null} />
      ) : null}
    </div>
  );
}
