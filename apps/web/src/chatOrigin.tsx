/**
 * Where a session, task or epic came from outside dude, as the screens show
 * it: the origin fact (header meta, rail, board card) and, for a chat-born
 * session, the thread it was made from and how dude routed it. The
 * provider is named only by its entry (name and glyph), never here.
 */

import { ExternalThread, OriginFact, ProviderGlyph, RoutingLine, providerGlyph, type ChatProvider, type ChatRoute } from "@dude/design-system/components";
import { ICON_NAMES } from "@dude/design-system";
import { Callout } from "@dude/design-system/primitives";
import type { ChatOrigin, SessionChatOrigin } from "@dude/domain";

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

/** An epic's origin in its header's meta, from navigation; nothing for one made in dude. */
export function EpicOrigin({ origin }: { origin: Pick<ChatOrigin, "provider" | "conversationName" | "permalink" | "requestedBy"> | null | undefined }) {
  return origin ? <OriginFact provider={originProvider(origin.provider)} place={origin.conversationName || "a conversation"}
    by={origin.requestedBy || null} href={origin.permalink || null} testId="epic-origin" /> : null;
}

/** A board card's lead: the provider's glyph and name, on the key's line. */
export function OriginLead({ provider }: { provider: string }) {
  return <span data-testid="card-origin"><ProviderGlyph provider={originProvider(provider)} size={12} inline /></span>;
}

/**
 * What an origin quotes, as external text: the channel before a mention
 * made outside a thread, else the thread; nothing when it carries neither.
 */
function OriginThread({ origin, names, clip }: { origin: ChatOrigin; names: (id: string) => { id: string; name: string } | null; clip?: number }) {
  const channel = origin.channel?.length ? origin.channel : null;
  const messages = channel ?? origin.thread;
  if (!messages?.length) return null;
  const p = originProvider(origin.provider);
  const place = origin.conversationName || "a conversation";
  return (
    <div data-testid={channel ? "origin-channel" : "origin-thread"}>
      <ExternalThread provider={p} place={channel ? `${place}, before the mention` : place} href={origin.permalink || null} clip={clip}
        label={channel ? `The channel ${place} before the mention, on ${p.name}` : undefined}
        messages={messages.map((m) => ({ by: { display: m.by, person: m.inDude && m.person ? names(m.person) ?? { id: m.person, name: m.by } : null }, text: m.text }))} />
    </div>
  );
}

/**
 * A chat-filed task's overview, under its goal: where it was filed from
 * and the thread (or the channel before the mention) as it was filed.
 */
export function TaskOrigin({ origin, names }: { origin: ChatOrigin; names: (id: string) => { id: string; name: string } | null }) {
  const p = originProvider(origin.provider);
  const what = origin.channel?.length ? "channel before the mention" : "thread";
  return (
    <section className="taskBlock" aria-label={`The ${p.name} ${what}, as filed`} data-testid="task-origin-thread">
      <Callout tone="info" data-testid="task-origin-callout">
        Filed from {p.name}{origin.requestedBy ? ` by ${origin.requestedBy}'s mention` : ""} in {origin.conversationName || "a conversation"}, in the Backlog. Nothing runs until someone starts it.
      </Callout>
      {origin.thread?.length || origin.channel?.length ? (
        <>
          <h2 className="ds-label">The {p.name} {what}, as filed</h2>
          <OriginThread origin={origin} names={names} />
        </>
      ) : null}
    </section>
  );
}

/**
 * The top of a chat-born session's transcript: the thread (or the channel
 * before the mention) quoted as external text, then dude's routing line
 * with who it was shared with. `names` resolves the added people's ids to
 * people.
 */
export function SessionOrigin({ origin, names, nested }: {
  origin: SessionChatOrigin;
  names: (id: string) => { id: string; name: string } | null;
  /** Under the asker's opening message, at its text column. */
  nested?: boolean;
}) {
  const routing = origin.routing;
  const group = origin.conversationKind === "private" || origin.conversationKind === "group";
  const asked = routing && routing.confidence !== null && routing.route !== "failed";
  return (
    <div className={nested ? "sessionOrigin sessionOriginNested" : "sessionOrigin"} data-testid="session-origin" data-nested={nested || undefined}>
      <OriginThread origin={origin} names={names} clip={6} />
      {routing ? (
        <RoutingLine route={routing.route as ChatRoute} confidence={routing.confidence} reasoning={routing.reasoning}
          model={asked ? "Asked the model once, on the Judgements tier. Recorded in Organisation settings → Chat → Requests." : null}
          fallback={(routing.askedRoute as ChatRoute | undefined) ?? null}
          shared={group ? { place: origin.conversationName, added: routing.added.flatMap((id) => names(id) ?? []), skipped: routing.skippedNames ?? routing.skipped } : null} />
      ) : null}
    </div>
  );
}
