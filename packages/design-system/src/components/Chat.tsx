import { useState, type HTMLAttributes, type ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { Icon, type IconName } from "../icons/index.tsx";
import { Badge } from "../primitives/Badge.tsx";
import { Button } from "../primitives/Button.tsx";
import type { ToneName } from "../tokens/palette.ts";
import { PersonAvatar, initialsOf, type Person } from "./PersonAvatar.tsx";
import { EntityLine } from "./EntityLine.tsx";
import styles from "./Chat.module.css";

/*
 * Chat providers (Slack first) as shared surfaces draw them. A provider is
 * props — its name and its glyph — so nothing here names one in words of
 * its own: a second provider costs an entry, not a component.
 */

/** A chat provider: its name, its glyph, and what it calls a workspace. */
export interface ChatProvider {
  readonly id: string;
  readonly name: string;
  readonly glyph: IconName;
  readonly workspaceWord?: string | undefined;
}

/** A provider's glyph from an id the server sends; `integration` for one the set has no glyph for. */
export function providerGlyph(id: string, known: ReadonlyArray<IconName>): IconName {
  return (known as ReadonlyArray<string>).includes(id) ? (id as IconName) : "integration";
}

export interface ProviderGlyphProps {
  readonly provider: ChatProvider;
  readonly size?: number | undefined;
  /** Its own accessible name (the provider's), when it stands alone; beside its name it is decorative. */
  readonly label?: boolean | undefined;
  /** In a line of text: on the text's line, with the provider's name after it. */
  readonly inline?: boolean | undefined;
  readonly className?: string | undefined;
}

/** A provider's monochrome glyph, in the ink of the text beside it. */
export function ProviderGlyph({ provider, size = 14, label, inline, className }: ProviderGlyphProps) {
  const glyph = (
    <Icon name={provider.glyph} size={size} className={cx(styles["glyph"], className)} data-provider={provider.id}
      {...(label ? { title: provider.name } : {})} />
  );
  return inline ? <span className={styles["inlineGlyph"]}>{glyph}{provider.name}</span> : glyph;
}

/** Where a mention went. */
export type ChatRoute = "brainstorm" | "task" | "epic" | "resume" | "task_link" | "clarify" | "channel_projects" | "not_connected" | "not_member" | "failed";

/** Each route's tone, glyph and word: it reads without colour. Refusals and failures are subtle. */
export const ROUTE_SPEC: Record<ChatRoute, { readonly tone: ToneName; readonly icon: IconName; readonly label: string; readonly subtle: boolean }> = {
  brainstorm: { tone: "info", icon: "brainstorm", label: "Brainstorm", subtle: false },
  task: { tone: "success", icon: "inbox", label: "Task", subtle: false },
  epic: { tone: "success", icon: "layers", label: "Epic", subtle: false },
  resume: { tone: "neutral", icon: "retry", label: "Resume", subtle: false },
  task_link: { tone: "neutral", icon: "link", label: "Task link", subtle: false },
  clarify: { tone: "attention", icon: "question", label: "Clarify", subtle: false },
  channel_projects: { tone: "neutral", icon: "pin", label: "Channel projects", subtle: false },
  not_connected: { tone: "neutral", icon: "link", label: "Refused: not connected", subtle: true },
  not_member: { tone: "danger", icon: "lock", label: "Refused: not a member", subtle: true },
  failed: { tone: "danger", icon: "alert", label: "Failed", subtle: true },
};

export interface RouteBadgeProps {
  readonly route: ChatRoute;
  /** What the model answered when dude went another way: "was Task". */
  readonly fallback?: ChatRoute | null | undefined;
  /** A failed request's: what dude made instead, as "Failed → Brainstorm"; none when nothing was made. */
  readonly made?: ChatRoute | null | undefined;
  readonly size?: "sm" | "md" | undefined;
}

/** Where a mention went: a Badge with the route's glyph and word, and what it was asked as when dude overrode it. */
export function RouteBadge({ route, fallback, made, size = "sm" }: RouteBadgeProps) {
  const spec = ROUTE_SPEC[route];
  const label = route === "failed" && made && made !== route ? `${spec.label} → ${ROUTE_SPEC[made].label}` : spec.label;
  return (
    <span className={styles["route"]} data-route={route}>
      <Badge tone={spec.tone} emphasis={spec.subtle ? "subtle" : "tinted"} icon={spec.icon} size={size}>{label}</Badge>
      {fallback && fallback !== route && fallback !== made ? <span className={styles["was"]}>was {ROUTE_SPEC[fallback].label}</span> : null}
    </span>
  );
}

export interface OriginFactProps {
  readonly provider: ChatProvider;
  /** Where in the provider: "#support", "a direct message". */
  readonly place: string;
  /** Who started the thread. */
  readonly by?: string | null | undefined;
  /** The provider's permalink: the whole fact links there. */
  readonly href?: string | null | undefined;
  /** Drops "thread by" for list rows. */
  readonly compact?: boolean | undefined;
  readonly testId?: string | undefined;
}

/** Where a session, task or epic came from outside dude: "From <provider> · #support · thread by Márcia". */
export function OriginFact({ provider, place, by, href, compact, testId }: OriginFactProps) {
  const words = (
    <>
      <ProviderGlyph provider={provider} size={12} />
      <span>From {provider.name}</span>
      <span className={styles["sep"]} aria-hidden>·</span>
      <span>{place}</span>
      {by && !compact ? <><span className={styles["sep"]} aria-hidden>·</span><span>thread by {by}</span></> : null}
      {href ? <Icon name="external" size={10} className={styles["external"]} /> : null}
    </>
  );
  return href ? (
    <a className={styles["origin"]} href={href} target="_blank" rel="noreferrer noopener" data-testid={testId}>{words}</a>
  ) : (
    <span className={styles["origin"]} data-testid={testId}>{words}</span>
  );
}

/** Someone outside dude, on neutral initials: never an identity colour. Punctuation ("Priya (Support)") is no initial. */
export function ExternalFace({ name, size = 24 }: { readonly name: string; readonly size?: 20 | 24 | 32 | undefined }) {
  const letters = name.replace(/[^\p{L}\p{N}\s]+/gu, " ").trim() || name;
  return (
    <span className={styles["externalFace"]} style={{ ["--ext-size" as string]: `${size}px` }} role="img" aria-label={`${name}, not in dude`} title={`${name} · not in dude`}>
      {initialsOf(letters)}
    </span>
  );
}

/** One message of a thread from outside dude. */
export interface ExternalMessage {
  readonly by: { readonly display: string; readonly handle?: string | undefined; readonly person?: Person | null | undefined };
  readonly time?: string | undefined;
  readonly text: string;
}

export interface ExternalThreadProps {
  readonly provider: ChatProvider;
  readonly place: string;
  readonly messages: ReadonlyArray<ExternalMessage>;
  readonly href?: string | null | undefined;
  /** The list's accessible name. */
  readonly label?: string | undefined;
  /** Messages shown before "Show N more"; all when unset. */
  readonly clip?: number | undefined;
}

/**
 * A conversation from outside dude, quoted as external text: chrome shade
 * and a strong left rule. A linked author shows their face and name; anyone
 * else their name on neutral initials and "not in dude". Never Markdown.
 */
export function ExternalThread({ provider, place, messages, href, label, clip }: ExternalThreadProps) {
  const [all, setAll] = useState(false);
  const shown = clip && !all ? messages.slice(-clip) : messages;
  const hidden = messages.length - shown.length;
  return (
    <figure className={styles["thread"]} aria-label={label ?? `Thread in ${place} on ${provider.name}`}>
      <figcaption className={styles["threadHead"]}>
        <ProviderGlyph provider={provider} size={12} />
        <span>Quoted from {provider.name}</span>
        <span className={styles["sep"]} aria-hidden>·</span>
        <span>{place}</span>
        <span className={styles["sep"]} aria-hidden>·</span>
        <span>{messages.length} {messages.length === 1 ? "message" : "messages"}</span>
        {href ? <a className={styles["threadLink"]} href={href} target="_blank" rel="noreferrer noopener">Open in {provider.name}
          <Icon name="external" size={10} /></a> : null}
      </figcaption>
      {hidden > 0 ? (
        <button type="button" className={styles["more"]} onClick={() => setAll(true)}>Show {hidden} earlier</button>
      ) : null}
      <ol className={styles["threadList"]}>
        {shown.map((m, i) => (
          <li key={i} className={styles["threadItem"]}>
            {m.by.person ? <PersonAvatar person={m.by.person} size={20} ring={false} /> : <ExternalFace name={m.by.display} size={20} />}
            <span className={styles["threadBody"]}>
              <span className={styles["threadBy"]}>
                <b>{m.by.person?.name ?? m.by.display}</b>
                {m.by.person ? null : <span className={styles["notInDude"]}>not in dude</span>}
                {m.time ? <span className={styles["time"]}>{m.time}</span> : null}
              </span>
              <span className={styles["threadText"]}>{m.text}</span>
            </span>
          </li>
        ))}
      </ol>
    </figure>
  );
}

export interface RoutingLineProps {
  readonly route: ChatRoute;
  readonly confidence?: number | null | undefined;
  readonly reasoning: string;
  /** How the model was asked, as a line under the reasoning: "Asked judge-model once, on the Judgements tier." */
  readonly model?: ReactNode;
  readonly fallback?: ChatRoute | null | undefined;
  readonly defaultOpen?: boolean | undefined;
  /** A private channel's or group's: who was added, who was skipped (not connected). */
  readonly shared?: { readonly place: string; readonly added: ReadonlyArray<Person>; readonly skipped: ReadonlyArray<string> } | null | undefined;
}

const ROUTED_AS: Partial<Record<ChatRoute, string>> = {
  brainstorm: "a brainstorm", task: "a task", epic: "an epic", resume: "a resumed session", failed: "a brainstorm (routing failed)",
};

/** dude's margin line in a chat-born transcript: how it was routed and why, and who it was shared with. */
export function RoutingLine({ route, confidence, reasoning, model, fallback, defaultOpen, shared }: RoutingLineProps) {
  const sure = confidence !== null && confidence !== undefined ? ` · ${Math.round(confidence * 100)}% sure` : "";
  const skipped = shared?.skipped.length ?? 0;
  const notConnected = skipped ? `${skipped} ${skipped === 1 ? "is" : "are"} not connected` : "";
  // Nobody added: say where it was made and who was skipped, not "with 0 people".
  const sharedWords = !shared ? null : shared.added.length
    ? `Shared with ${shared.added.length} ${shared.added.length === 1 ? "person" : "people"} from ${shared.place}${notConnected ? `; ${notConnected}` : ""}`
    : `Made in ${shared.place}${notConnected ? `; ${notConnected}, so nobody else was added` : ""}`;
  return (
    <details className={styles["routing"]} open={defaultOpen} data-testid="routing-line">
      <summary className={styles["routingSummary"]}>
        <span className={styles["rule"]} aria-hidden />
        <span className={styles["routingText"]}>
          <Icon name="git-branch" size={12} />
          <span>Routed as {ROUTED_AS[route] ?? ROUTE_SPEC[route].label.toLowerCase()}{sure}</span>
          {fallback && fallback !== route ? <span className={styles["was"]}>was {ROUTE_SPEC[fallback].label}</span> : null}
          {sharedWords ? <span data-testid="routing-shared">· {sharedWords}</span> : null}
        </span>
        <span className={styles["rule"]} aria-hidden />
      </summary>
      <div className={styles["routingBody"]}>
        <p>{reasoning}</p>
        {model ? <p className={styles["muted"]}>{model}</p> : null}
        {shared && (shared.added.length || skipped) ? (
          <dl className={styles["sharedGroups"]} aria-label="Who it was shared with">
            {shared.added.length ? (
              <div className={styles["sharedGroup"]}>
                <dt>Added, can chat</dt>
                <dd>{shared.added.map((p) => (
                  <EntityLine key={p.id ?? p.name} size="sm" lead={<PersonAvatar person={p} size={20} ring={false} />} name={p.name} />
                ))}</dd>
              </div>
            ) : null}
            {skipped ? (
              <div className={styles["sharedGroup"]}>
                <dt>Skipped, not connected</dt>
                <dd>{shared.skipped.map((n) => (
                  <EntityLine key={n} size="sm" lead={<ExternalFace name={n} size={20} />} name={n} />
                ))}</dd>
              </div>
            ) : null}
          </dl>
        ) : null}
      </div>
    </details>
  );
}

export interface FactListProps {
  /** The list's accessible name. */
  readonly label: string;
  readonly facts: ReadonlyArray<{ readonly icon: IconName; readonly when: ReactNode; readonly then: ReactNode }>;
}

/** Rules that are not settings, read as cases: a glyph and the case, then what happens. */
export function FactList({ label, facts }: FactListProps) {
  return (
    <dl className={styles["facts"]} aria-label={label}>
      {facts.map((f, i) => (
        <div key={i} className={styles["fact"]}>
          <dt className={styles["factWhen"]}>
            <span className={styles["factGlyph"]}><Icon name={f.icon} size={14} /></span>
            {f.when}
          </dt>
          <dd className={styles["factThen"]}>{f.then}</dd>
        </div>
      ))}
    </dl>
  );
}

export interface ConnectionCardProps {
  readonly title: ReactNode;
  readonly glyph: ReactNode;
  readonly intro: ReactNode;
  /** Label and value rows: who you are connected as. */
  readonly facts?: ReadonlyArray<{ readonly label: string; readonly value: ReactNode }> | undefined;
  /** The callback's outcome, above everything else. */
  readonly outcome?: { readonly tone: "success" | "attention" | "danger"; readonly text: ReactNode } | null | undefined;
  readonly connected: boolean;
  readonly connectLabel: string;
  readonly disconnectLabel: string;
  readonly onConnect?: (() => void) | undefined;
  readonly onDisconnect?: (() => void) | undefined;
  /** Connected, but it must be done again: Connect stays primary. */
  readonly reconnect?: boolean | undefined;
  readonly busy?: boolean | undefined;
  readonly testId?: string | undefined;
}

/** One account dude acts with elsewhere: a forge's or a chat provider's. Connect is primary until it is connected. */
export function ConnectionCard({ title, glyph, intro, facts, outcome, connected, connectLabel, disconnectLabel, onConnect, onDisconnect, reconnect,
  busy, testId }: ConnectionCardProps) {
  const primary = !connected || reconnect;
  return (
    <section className={styles["connection"]} data-testid={testId} data-connected={connected || undefined} aria-label={typeof title === "string" ? title : undefined}>
      <header className={styles["connectionHead"]}>
        <span className={styles["connectionGlyph"]}>{glyph}</span>
        <span className={styles["connectionTitle"]}>{title}</span>
        <Badge tone={connected ? "success" : "neutral"} emphasis="subtle" icon={connected ? "check" : undefined} size="sm">
          {connected ? "Connected" : "Not connected"}
        </Badge>
      </header>
      {outcome ? <p className={styles["outcome"]} data-tone={outcome.tone} role={outcome.tone === "danger" ? "alert" : "status"}>{outcome.text}</p> : null}
      <p className={styles["muted"]}>{intro}</p>
      {facts?.length ? (
        <dl className={styles["connectionFacts"]}>
          {facts.map((f) => (
            <div key={f.label} className={styles["connectionFact"]}><dt>{f.label}</dt><dd>{f.value}</dd></div>
          ))}
        </dl>
      ) : null}
      <footer className={styles["connectionActions"]}>
        {onConnect ? (
          <Button variant={primary ? "primary" : "secondary"} disabled={busy} onClick={onConnect} data-variant={primary ? "primary" : "secondary"}
            data-testid={testId ? `${testId}-connect` : undefined}>{connectLabel}</Button>
        ) : null}
        {connected && onDisconnect ? (
          <Button variant="danger" disabled={busy} onClick={onDisconnect} data-testid={testId ? `${testId}-disconnect` : undefined}>{disconnectLabel}</Button>
        ) : null}
      </footer>
    </section>
  );
}

export interface IdentityPairProps {
  /** The left side's label: the provider's name. */
  readonly provider: ChatProvider;
  /** The provider account; null when it is not known until the person signs in there (`lg` only). */
  readonly external: { readonly handle: string; readonly display?: string | undefined; readonly workspace?: string | undefined } | null;
  readonly person: Person & { readonly email?: string | undefined; readonly organisation?: string | undefined };
  /** `lg`: two identity cards with the link between, for the page whose point is the link. */
  readonly size?: "md" | "lg" | undefined;
  /** `lg` with no account yet: where the sign-in happens, under the provider's glyph. */
  readonly workspace?: string | undefined;
}

/** A provider account and the dude person it is linked to. */
export function IdentityPair({ provider, external, person, size = "md", workspace }: IdentityPairProps) {
  const label = external ? `${provider.name} @${external.handle} is ${person.name} in dude`
    : `Your ${provider.name} account, once you sign in, is ${person.name} in dude`;
  if (size === "lg") {
    return (
      <div className={styles["pairLg"]} role="group" aria-label={label} data-testid="identity-pair">
        {external ? (
          <div className={styles["pairCard"]}>
            <span className={styles["pairCaps"]}>{provider.name}</span>
            <ExternalFace name={external.display || external.handle} size={32} />
            <b className={styles["pairName"]}>@{external.handle}</b>
            {external.display ? <span className={styles["muted"]}>{external.display}</span> : null}
            {external.workspace ? <span className={styles["pairMono"]}>{external.workspace}</span> : null}
          </div>
        ) : (
          <div className={styles["pairCard"]} data-unknown="true">
            <span className={styles["pairCaps"]}>{provider.name}</span>
            <span className={styles["pairUnknown"]}><ProviderGlyph provider={provider} size={18} /></span>
            <b className={styles["pairName"]}>Your {provider.name} account</b>
            <span className={styles["muted"]}>Signed in next</span>
            {workspace ? <span className={styles["pairMono"]}>{workspace}</span> : null}
          </div>
        )}
        <span className={styles["pairLink"]} aria-hidden><Icon name="link" size={14} /></span>
        <div className={styles["pairCard"]}>
          <span className={styles["pairCaps"]}>dude</span>
          <PersonAvatar person={person} size={32} />
          <b className={styles["pairName"]}>{person.name}</b>
          {person.organisation ? <span className={styles["muted"]}>{person.organisation}</span> : null}
          {person.email ? <span className={styles["muted"]}>{person.email}</span> : null}
        </div>
      </div>
    );
  }
  return (
    <span className={styles["pair"]} aria-label={label}>
      <span className={styles["pairSide"]}>
        <ProviderGlyph provider={provider} size={14} />
        <span><span className={styles["pairLabel"]}>{provider.name}</span> {external ? `@${external.handle}` : "not signed in yet"}</span>
      </span>
      <Icon name="link" size={12} className={styles["muted"]} />
      <span className={styles["pairSide"]}>
        <PersonAvatar person={person} size={20} ring={false} />
        <span><span className={styles["pairLabel"]}>dude</span> {person.name}</span>
      </span>
    </span>
  );
}

/** One layer of what a model is told. */
export interface InstructionLayer {
  readonly id: string;
  readonly title: ReactNode;
  /** Whose it is, and where: "dude's own", "every chat". */
  readonly owner: ReactNode;
  readonly text: string;
  /** Where it is changed: a link or a button. */
  readonly action?: ReactNode;
  /** The layer edited on this page. */
  readonly here?: boolean | undefined;
  /** Only the head (the built-in instructions). */
  readonly collapsed?: boolean | undefined;
}

export interface InstructionStackProps {
  readonly label: string;
  readonly layers: ReadonlyArray<InstructionLayer>;
}

const lines = (t: string) => (t.trim() ? t.trim().split("\n").length : 0);

export interface PromptTextProps extends Omit<HTMLAttributes<HTMLDivElement>, "children"> {
  /** What a model is told, as written: shown as it is, never as Markdown. */
  readonly text: string;
  /** A label for the well, read by a screen reader. */
  readonly label: string;
}

/** A read-only well of instructions a model is told: wrapped at the column's width, its own line breaks kept, scrolling down past 24 lines, never sideways. */
export function PromptText({ text, label, className, ...rest }: PromptTextProps) {
  return <div className={cx(styles["promptText"], className)} role="document" aria-label={label} tabIndex={0} {...rest}>{text.trim()}</div>;
}

/** What a model is told, in order: one row per layer, whose it is, its length and where it is changed. Read-only. */
export function InstructionStack({ label, layers }: InstructionStackProps) {
  return (
    <ol className={styles["stack"]} aria-label={label}>
      {layers.map((l, i) => {
        const n = lines(l.text);
        return (
          <li key={l.id} className={cx(styles["layer"], l.here && styles["layerHere"])} data-layer={l.id}>
            <span className={styles["layerNo"]} aria-hidden>{i + 1}</span>
            <span className={styles["layerBody"]}>
              <span className={styles["layerHead"]}>
                <b>{l.title}</b>
                <span className={styles["muted"]}>{l.owner} · {n ? `${n} ${n === 1 ? "line" : "lines"}` : "Nothing added"}</span>
                {l.here ? <Badge tone="info" size="sm">This page</Badge> : null}
                {l.action ? <span className={styles["layerAction"]}>{l.action}</span> : null}
              </span>
              {!l.collapsed && n ? <span className={styles["layerText"]}>{l.text.trim().split("\n").join(" · ")}</span> : null}
            </span>
          </li>
        );
      })}
    </ol>
  );
}

export interface ProviderListProps {
  readonly providers: ReadonlyArray<{ readonly provider: ChatProvider; readonly installed: boolean; readonly detail: ReactNode; readonly illustrative?: boolean | undefined }>;
  readonly onOpen: (provider: ChatProvider) => void;
}

/** The Chat page's providers: a row each, its glyph, name and detail, Installed or Available, to its child page. */
export function ProviderList({ providers, onOpen }: ProviderListProps) {
  return (
    <ul className={styles["providers"]} aria-label="Providers">
      {providers.map(({ provider, installed, detail, illustrative }) => (
        <li key={provider.id}>
          <button type="button" className={styles["providerRow"]} onClick={() => onOpen(provider)} data-provider-row={provider.id}>
            <EntityLine lead={<span className={styles["providerTile"]}><ProviderGlyph provider={provider} size={16} /></span>}
              name={<>{provider.name}{illustrative ? <span className={styles["muted"]}> · illustrative</span> : null}</>} detail={detail}
              trailing={<Badge tone={installed ? "success" : "neutral"} emphasis="subtle" icon={installed ? "check" : undefined} size="sm">
                {installed ? "Installed" : "Available"}</Badge>} />
            <Icon name="chevron-right" size={14} className={styles["muted"]} />
          </button>
        </li>
      ))}
    </ul>
  );
}
