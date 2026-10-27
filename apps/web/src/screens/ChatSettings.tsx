/**
 * Organisation settings → Chat, its provider pages and Requests: what every
 * incoming chat shares (routing instructions, the fixed facts, who gets a
 * session), each provider's installation and its own additional
 * instructions, and every routed mention (admins only). Provider-neutral:
 * a provider is the server's entry, drawn with its name and glyph.
 */

import { useCallback, useEffect, useMemo, useState } from "react";
import {
  Breadcrumb,
  ConnectionCard,
  EntityLine,
  FactList,
  FlowSteps,
  IdentityPair,
  InstructionStack,
  PersonAvatar,
  PromptText,
  ProviderGlyph,
  ProviderList,
  RouteBadge,
  SettingRow,
  SettingsHeader,
  SettingsSection,
  TextButton,
  providerGlyph,
  type ChatMadeRoute,
  type ChatProvider as ProviderView,
  type ChatRoute as RouteView,
  type SettingsNavItem,
} from "@dude/design-system/components";
import { ICON_NAMES, Icon, formatTimestamp } from "@dude/design-system";
import {
  Badge,
  Button,
  Callout,
  Card,
  CardBody,
  CardFooter,
  CardHeader,
  Dialog,
  EmptyState,
  FormStack,
  IconButton,
  KeyValueList,
  Select,
  Spinner,
  Table,
  TBody,
  Td,
  Th,
  THead,
  Textarea,
  Tooltip,
  Tr,
} from "@dude/design-system/primitives";
import {
  CHAT_CLARIFY_ROUNDS,
  CHAT_CONFIDENCE_BAR,
  CHAT_EPIC_TASKS_MAX,
  CHAT_INSTRUCTIONS_MAX,
  CHAT_ROUTES,
  type ChatInstallationView,
  type ChatOverview,
  type ChatProjectRef,
  type ChatProjectsView,
  type ChatProvider,
  type ChatRequestRow,
  type ChatRequestsSummary,
  type ProviderRoutingInstructions,
  type RoutingInstructions,
} from "@dude/domain";
import type { ApiClient } from "../api/client.ts";
import { errorText, useSave } from "../hooks/useSave.tsx";

/** The settings pages of Chat: "chat", "chat/<provider>", "chat/requests". */
export const CHAT_PAGE = "chat";
export const REQUESTS_PAGE = "chat/requests";
export const isChatPage = (page: string): boolean => page === CHAT_PAGE || page.startsWith("chat/");

/** A provider as the DS draws it: its glyph from the icon set, the neutral one when the set has none. */
export const providerView = (p: ChatProvider): ProviderView => ({ id: p.id, name: p.name, glyph: providerGlyph(p.glyph, ICON_NAMES),
  workspaceWord: p.workspaceWord });

/** What the outcome of an install's callback says. */
const INSTALL_OUTCOME: Record<string, { tone: "success" | "attention" | "danger"; text: (name: string) => string }> = {
  installed: { tone: "success", text: (n) => `${n} is installed. Mention @dude there to start.` },
  authorization_denied: { tone: "attention", text: (n) => `${n} did not install dude: the authorization was denied, or you may not install apps there.` },
  invalid_or_expired: { tone: "danger", text: () => "That install request expired or was already used. Install again." },
  workspace_taken: { tone: "danger", text: (n) => `That ${n} workspace is already connected to another organisation. A workspace belongs to one organisation.` },
};
const INSTALL_FAILED = { tone: "danger" as const, text: (n: string) => `Installing ${n} failed. Try again.` };

/** The providers and the caller's links, for the menu, the Chat page and Your settings. */
export function useChatOverview(client: ApiClient) {
  const [overview, setOverview] = useState<ChatOverview | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const reload = useCallback(() => {
    client.chatOverview().then((o) => {
      setOverview(o);
      setProblem(null);
    }, (err: unknown) => setProblem(errorText(err)));
  }, [client]);
  useEffect(reload, [reload]);
  return { overview, problem, reload };
}

/** Chat in the settings menu: a page with a child per provider, then Requests for admins. */
export function chatNav(overview: ChatOverview | null, admin: boolean, requests?: number): SettingsNavItem {
  return {
    id: CHAT_PAGE,
    label: "Chat",
    icon: "message",
    page: true,
    items: [
      ...(overview?.providers ?? []).map((p) => ({
        id: `chat/${p.provider.id}`, label: p.provider.name, icon: providerGlyph(p.provider.glyph, ICON_NAMES), note: p.installed ? undefined : "off",
      })),
      ...(admin ? [{ id: REQUESTS_PAGE, label: "Requests", note: requests || undefined }] : []),
    ],
  };
}

/** The route's word set, as the DS draws it. */
const routeView = (r: string) => r as RouteView;

export function ChatPages({ client, page, orgName, admin, overview, onPage, onChanged, judgementTier }: {
  client: ApiClient;
  page: string;
  orgName: string;
  admin: boolean;
  overview: ReturnType<typeof useChatOverview>;
  onPage: (page: string) => void;
  onChanged: () => void;
  /** The Judgements tier as the Chat page's fixed fact names it; null while off. */
  judgementTier: { name: string; model: string | null } | null;
}) {
  if (overview.problem) return <div className="centered"><Callout tone="danger">{overview.problem}</Callout></div>;
  if (!overview.overview) return <div className="centered"><Spinner label="Loading…" /></div>;
  if (page === REQUESTS_PAGE) {
    return admin ? <RequestsPage client={client} providers={overview.overview.providers.map((p) => p.provider)} onPage={onPage} />
      : <Callout tone="info">Only {orgName}’s admins see Requests.</Callout>;
  }
  const provider = overview.overview.providers.find((p) => `chat/${p.provider.id}` === page);
  if (provider) {
    return <ProviderPage key={provider.provider.id} client={client} provider={provider.provider} orgName={orgName} onPage={onPage}
      onChanged={() => { overview.reload(); onChanged(); }} />;
  }
  return <ChatPage client={client} orgName={orgName} admin={admin} overview={overview.overview} onPage={onPage} judgementTier={judgementTier} />;
}

/** Everything every incoming chat shares. */
function ChatPage({ client, orgName, admin, overview, onPage, judgementTier }: {
  client: ApiClient;
  orgName: string;
  admin: boolean;
  overview: ChatOverview;
  onPage: (page: string) => void;
  judgementTier: { name: string; model: string | null } | null;
}) {
  const [instructions, setInstructions] = useState<RoutingInstructions | null>(null);
  const [draft, setDraft] = useState("");
  const [builtInOpen, setBuiltInOpen] = useState(false);
  const { busy, problem, save } = useSave();
  useEffect(() => {
    void client.routingInstructions().then((r) => {
      setInstructions(r);
      setDraft(r.additional);
    }, () => undefined);
  }, [client]);
  const lines = instructions ? instructions.builtIn.split("\n").length : 0;
  return (
    <div className="settingsPage" data-testid="chat-page">
      <SettingsHeader title="Chat"
        description={`Mention @dude in a chat ${orgName} has added and dude starts a session, files a task or an epic, or asks first, then replies with the link. These settings are the same for every chat; each provider's own page has its connection and what it adds.`} />
      <FlowSteps steps={[
        { title: "Who is asking", children: <>A connected account and an active member of {orgName}. Otherwise a private reply, and nothing else.</> },
        { title: "A thread already bound", children: <>To a session: the message goes in. To a task or an epic: its link. No model.</> },
        { title: "One routing call", children: <>Brainstorm, task, epic, resume or a question; checked by dude, then the link in the thread.{admin ? <> Every call is in <TextButton onClick={() => onPage(REQUESTS_PAGE)}>Requests</TextButton>.</> : null}</> },
      ]} />
      <SettingsSection title="Routing">
        <SettingRow label="Built-in instructions" help="dude's own, kept up to date with dude. Read-only." fill>
          <span data-testid="chat-builtin-summary">{lines} lines · routes, confidence, reasoning{" "}
            <TextButton onClick={() => setBuiltInOpen((o) => !o)} data-testid="chat-builtin-view">{builtInOpen ? "Hide" : "View"}</TextButton></span>
          {builtInOpen && instructions ? <PromptText label="Built-in instructions" text={instructions.builtIn} data-testid="chat-builtin-text" /> : null}
        </SettingRow>
        <SettingRow label="Additional instructions" htmlFor="chat-instructions" fill
          help={`Appended to the built-in ones, for every chat. What your organisation knows that the model can't: which channel means which project, where bugs go. Each provider can add its own after these.`}>
          <Textarea id="chat-instructions" aria-label="Additional instructions" rows={4} value={draft} readOnly={!instructions?.canEdit}
            maxLength={CHAT_INSTRUCTIONS_MAX} onChange={(e) => setDraft(e.target.value)} data-testid="chat-instructions"
            placeholder={instructions?.canEdit ? "#support is almost always about Billing (BIL)." : "Nothing added."} />
          {problem ? <Callout tone="danger">{problem}</Callout> : null}
          {instructions?.canEdit ? (
            <span><Button variant="primary" size="sm" disabled={busy || draft === instructions.additional} data-testid="chat-instructions-save"
              onClick={() => void save(async () => {
                const next = await client.setRoutingInstructions(draft);
                setInstructions(next);
                setDraft(next.additional);
              }, undefined, "Routing instructions saved")}>Save</Button></span>
          ) : null}
        </SettingRow>
        <SettingRow label="Fixed" help="The same for every organisation and every chat.">
          <KeyValueList data-testid="chat-facts" items={[
            { label: "Model", value: <span data-testid="chat-fact-model">The Judgements tier{judgementTier ? <> (<b>{judgementTier.name}</b>{judgementTier.model ? <> · <code>{judgementTier.model}</code></> : null})</> : " (off: every mention becomes a brainstorm)"} — <TextButton onClick={() => onPage("judgements")}>change it in Judgements</TextButton></span> },
            { label: "Confidence bar", value: <><b>{Math.round(CHAT_CONFIDENCE_BAR * 100)}%</b> — under it, dude asks a question, or falls back</> },
            { label: "Clarifying rounds", value: <>At most {CHAT_CLARIFY_ROUNDS}, then a brainstorm</> },
            { label: "Fallback", value: "A brainstorm session with the thread, on any failure or no answer" },
            { label: "Epic size", value: <>At most <b>{CHAT_EPIC_TASKS_MAX}</b> tasks per epic; more, and dude asks first</> },
          ]} />
        </SettingRow>
      </SettingsSection>
      <SettingsSection title="Who gets the session">
        <p className="muted">Where the mention was decides it, in every chat. There is no setting: a session never reaches people the conversation did not.</p>
        <FactList label="Who gets the session" facts={[
          { icon: "globe", when: "Public channel", then: `Everyone in ${orgName} · can chat. The session is the organisation's.` },
          { icon: "lock", when: "Private channel or group message", then: `Shared, as can chat, with every person in that conversation who has connected their account and is a member of ${orgName}. The person who mentioned dude owns it; the others are listed as skipped.` },
          { icon: "human", when: "Direct message with @dude", then: "Private to the person." },
        ]} />
      </SettingsSection>
      <SettingsSection title="Providers" actions={admin ? <TextButton onClick={() => onPage(REQUESTS_PAGE)}>Requests from every provider</TextButton> : undefined}>
        <ProviderList onOpen={(p) => onPage(`chat/${p.id}`)} providers={overview.providers.map((p) => ({
          provider: providerView(p.provider), installed: p.installed,
          detail: p.installed ? `${p.workspaceName ?? p.provider.name} ${p.provider.workspaceWord}` : "Not added yet",
        }))} />
      </SettingsSection>
    </div>
  );
}

/** One provider: its installation, your own account, and what it adds to routing. */
function ProviderPage({ client, provider, orgName, onPage, onChanged }: {
  client: ApiClient;
  provider: ChatProvider;
  orgName: string;
  onPage: (page: string) => void;
  onChanged: () => void;
}) {
  const [view, setView] = useState<ChatInstallationView | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const [instructions, setInstructions] = useState<(ProviderRoutingInstructions & { installed: boolean }) | null>(null);
  const [draft, setDraft] = useState("");
  const [disconnecting, setDisconnecting] = useState(false);
  const [showPeople, setShowPeople] = useState(false);
  const { busy, problem: saveProblem, save } = useSave();
  const [outcome, setOutcome] = useState(() => {
    const q = new URLSearchParams(window.location.search);
    return q.get("chatInstall") === provider.id ? q.get("outcome") : null;
  });
  useEffect(() => {
    const url = new URL(window.location.href);
    if (!url.searchParams.has("chatInstall")) return;
    url.searchParams.delete("chatInstall");
    url.searchParams.delete("outcome");
    window.history.replaceState(window.history.state, "", `${url.pathname}${url.search}${url.hash}`);
  }, []);
  const load = useCallback(() => {
    client.chatInstallation(provider.id).then(setView, (err: unknown) => setProblem(errorText(err)));
    client.providerRoutingInstructions(provider.id).then((r) => {
      setInstructions(r);
      setDraft(r.additional);
    }, () => undefined);
  }, [client, provider.id]);
  useEffect(load, [load]);
  const pv = providerView(provider);
  if (problem) return <div className="centered"><Callout tone="danger">{problem}</Callout></div>;
  if (!view) return <div className="centered"><Spinner label="Loading…" /></div>;
  const shownOutcome = outcome ? (INSTALL_OUTCOME[outcome] ?? INSTALL_FAILED) : null;
  const eventsUrl = `${window.location.origin}/v1/webhooks/chat/${provider.id}/events`;
  const interactionsUrl = `${window.location.origin}/v1/webhooks/chat/${provider.id}/interactions`;
  const health = view.health;
  return (
    <div className="settingsPage" data-testid={`chat-provider-${provider.id}`}>
      <Breadcrumb size="sm" items={[{ id: "chat", label: "Chat", icon: "message", onSelect: () => onPage(CHAT_PAGE) }, { id: provider.id, label: provider.name }]} />
      <SettingsHeader title={provider.name}
        description={`Mention @dude in ${orgName}'s ${provider.name} to start a session or file a task. dude replies with a link; the conversation continues here.`} />
      {shownOutcome ? <Callout tone={shownOutcome.tone} data-testid="chat-install-outcome">{shownOutcome.text(provider.name)}</Callout> : null}
      {view.installed && !view.canEdit ? (
        <ConnectionCard testId="chat-member-connection" title={`Your ${provider.name} account`} glyph={<ProviderGlyph provider={pv} size={16} />}
          intro={view.connected ? `Mentions of @dude in ${provider.name} act as you.` : `Connect your ${provider.name} account so dude can act for you when you mention it. Your settings → Connections.`}
          connected={Boolean(view.connected)} facts={view.connected ? [{ label: "Connected as", value: `@${view.connected.handle}` }] : undefined}
          connectLabel={`Connect your ${provider.name} account`} disconnectLabel={`Disconnect ${provider.name}…`}
          onConnect={view.connected ? undefined : () => void client.connectChat(provider.id).then(({ url }) => window.location.assign(url), () => undefined)} />
      ) : null}
      {!view.installed && !view.canEdit ? (
        <Callout tone="info" data-testid="chat-ask-admin">
          <FormStack>
            <span>Ask an admin to add dude to {provider.name}.</span>
            <span className="chatAdmins">{view.admins.map((a) => <EntityLine key={a.id} size="sm" lead={<PersonAvatar person={a} size={20} />} name={a.name} />)}</span>
          </FormStack>
        </Callout>
      ) : null}
      <Card data-testid="chat-installation" aria-label={`${provider.name} installation`}>
        <CardHeader title={<span className="chatCardTitle"><ProviderGlyph provider={pv} size={16} />{provider.name}</span>}
          actions={<Badge tone={view.installed ? "success" : "neutral"} emphasis="subtle" icon={view.installed ? "check" : undefined} size="sm">
            {view.installed ? "Installed" : "Not installed"}</Badge>} />
        <CardBody>
          <FormStack>
            {view.installed ? (
              <KeyValueList items={[
                { label: provider.workspaceWord[0]!.toUpperCase() + provider.workspaceWord.slice(1), value: <span data-testid="chat-workspace">{view.workspaceName}{view.workspaceDomain ? ` · ${view.workspaceDomain}` : ""}</span> },
                { label: "Installed by", value: view.installedBy ? <EntityLine size="sm" lead={<PersonAvatar person={view.installedBy} size={20} />} name={view.installedBy.name}
                  detail={view.installedAt ? formatTimestamp(view.installedAt, "date") : undefined} /> : "—" },
                ...(view.botUser ? [{ label: "Bot user", value: <span data-testid="chat-bot-user">@dude · {view.botUser}</span> }] : []),
                ...(view.canEdit ? [
                  { label: "Events URL", value: eventsUrl, mono: true },
                  { label: "Interactions URL", value: interactionsUrl, mono: true },
                  { label: "Bot token", value: "stored" },
                ] : []),
              ]} />
            ) : (
              <p className="muted">{view.canEdit
                ? `Install dude's app in your ${provider.name} ${provider.workspaceWord}: one ${provider.workspaceWord} per organisation. Its people then connect their own accounts.`
                : `${provider.name} is not set up for ${orgName}.`}</p>
            )}
            {health ? (
              <Callout tone={health.failedToday ? "attention" : "success"} data-testid="chat-health">
                {health.lastEventAt ? `Events reaching dude · last event ${formatTimestamp(health.lastEventAt, "relative")}` : "No event has reached dude yet"}
                {` · ${health.failedToday} failed today`}{health.lastFailure ? ` · last: ${health.lastFailure}` : ""}
              </Callout>
            ) : null}
            {saveProblem ? <Callout tone="danger">{saveProblem}</Callout> : null}
            {view.canEdit && !view.configured ? <span className="muted">The operator has not configured {provider.name}'s app.</span> : null}
          </FormStack>
        </CardBody>
        {view.canEdit ? (
          <CardFooter>
            {!view.installed ? (
              <Button variant="primary" disabled={busy || !view.configured} data-testid="chat-install"
                onClick={() => void save(async () => window.location.assign((await client.installChat(provider.id)).url))}>
                Add dude to {provider.name}
              </Button>
            ) : (
              <Button variant="danger" disabled={busy} onClick={() => setDisconnecting(true)} data-testid="chat-disconnect">Disconnect…</Button>
            )}
          </CardFooter>
        ) : null}
      </Card>
      {view.installed && instructions ? (
        <SettingsSection title={`Routing for ${provider.name}`}>
          <p className="muted">Routing is the same for every chat: its model, instructions and limits are on the <TextButton onClick={() => onPage(CHAT_PAGE)}>Chat page</TextButton>. Here, only what {provider.name} adds.</p>
          <SettingRow label={`Additional instructions for ${provider.name}`} htmlFor="chat-provider-instructions" fill
            help={`Appended after ${orgName}'s, for mentions in ${provider.name} only: its channel names, its conventions.`}>
            <Textarea id="chat-provider-instructions" aria-label={`Additional instructions for ${provider.name}`} rows={3} value={draft}
              readOnly={!instructions.canEdit} maxLength={CHAT_INSTRUCTIONS_MAX} onChange={(e) => setDraft(e.target.value)}
              data-testid="chat-provider-instructions" placeholder={instructions.canEdit ? "" : "Nothing added."} />
            {instructions.canEdit ? (
              <span><Button variant="primary" size="sm" disabled={busy || draft === instructions.additional} data-testid="chat-provider-instructions-save"
                onClick={() => void save(async () => {
                  const next = await client.setProviderRoutingInstructions(provider.id, draft);
                  setInstructions(next);
                  setDraft(next.additional);
                }, undefined, `${provider.name} instructions saved`)}>Save</Button></span>
            ) : null}
          </SettingRow>
          <SettingRow label="What the model is told" help={`In this order, for every mention in ${provider.name}. Read-only.`} fill>
            <InstructionStack label="What the model is told" layers={[
              { id: "builtin", title: "Built-in", owner: "dude's own", text: instructions.builtIn, collapsed: true,
                action: <TextButton onClick={() => onPage(CHAT_PAGE)}>View on Chat</TextButton> },
              { id: "organisation", title: `${orgName}'s`, owner: "every chat", text: instructions.organisation,
                action: <TextButton onClick={() => onPage(CHAT_PAGE)}>Edit on Chat</TextButton> },
              { id: "provider", title: `${provider.name}'s`, owner: `${provider.name} only`, text: draft, here: true },
            ]} />
          </SettingRow>
        </SettingsSection>
      ) : null}
      {view.installed ? <ProjectsSection client={client} provider={provider} orgName={orgName} /> : null}
      {view.installed ? (
        <SettingsSection title="People">
          <SettingRow label="Connected people" fill
            help={`Each person connects their own ${provider.name} account in Your settings. Others are asked to when they mention @dude.`}>
            <span data-testid="chat-connected-count"><b>{view.connectedCount}</b> {view.connectedCount === 1 ? "person" : "people"} connected
              {view.people && view.people.length ? <>{" "}<TextButton onClick={() => setShowPeople((s) => !s)} data-testid="chat-people-show">
                {showPeople ? "Hide" : "Show who"}</TextButton></> : null}</span>
            {showPeople && view.people ? (
              <ul className="plainRows" role="list" data-testid="chat-people">
                {view.people.map((p) => (
                  <li key={p.person.id} className="plainRow">
                    <IdentityPair provider={pv} external={{ handle: p.handle }} person={p.person} />
                  </li>
                ))}
              </ul>
            ) : null}
          </SettingRow>
        </SettingsSection>
      ) : null}
      {view.installed && view.canEdit ? (
        <SettingsSection title={`Recent requests from ${provider.name}`}
          actions={<TextButton onClick={() => onPage(REQUESTS_PAGE)}>All requests</TextButton>}>
          <RequestsTable client={client} providers={[provider]} provider={provider.id} limit={5} />
        </SettingsSection>
      ) : null}
      {disconnecting ? (
        <Dialog open onOpenChange={(o) => !o && setDisconnecting(false)} tone="danger" size="sm" title={`Disconnect ${provider.name}?`}
          description={`dude stops answering in ${view.workspaceName ?? provider.name} and its token is revoked. People's connections and bound threads go with it, so another organisation may add this ${provider.workspaceWord}; what was made keeps where it came from.`}
          footer={<>
            <Button variant="quiet" onClick={() => setDisconnecting(false)}>Cancel</Button>
            <Button variant="danger" solid disabled={busy} data-testid="chat-disconnect-confirm" onClick={() => void save(() => client.disconnectChat(provider.id), () => {
              setDisconnecting(false);
              setOutcome(null);
              load();
              onChanged();
            }, `${provider.name} disconnected`)}>Disconnect {provider.name}</Button>
          </>} />
      ) : null}
    </div>
  );
}

/**
 * A provider's projects: what each channel is usually about (a hint the
 * routing model weighs; a message pointing elsewhere still goes elsewhere),
 * and the projects hidden from everything this provider starts. Admins edit;
 * members read.
 */
function ProjectsSection({ client, provider, orgName }: { client: ApiClient; provider: ChatProvider; orgName: string }) {
  const [view, setView] = useState<ChatProjectsView | null>(null);
  const { busy, problem, save } = useSave();
  useEffect(() => {
    client.chatProjects(provider.id).then(setView, () => undefined);
  }, [client, provider.id]);
  if (!view) return null;
  const hiddenIds = view.hidden.map((p) => p.id);
  return (
    <SettingsSection title="Projects">
      {problem ? <Callout tone="danger">{problem}</Callout> : null}
      <SettingRow label="Usually about" fill
        help={`What a channel is usually about. dude takes it as a hint when it routes a mention there; what the message and the conversation say still decide. People can set it from ${provider.name} too: “@dude this channel is usually about ABS”. Only admins can change it.`}>
        {view.channels.length === 0 ? <span className="muted">No channel has mentioned @dude yet.</span> : (
          <ul className="plainRows" role="list" data-testid="chat-channel-projects">
            {view.channels.map((c) => (
              <li key={c.conversationId} className="plainRow" data-conversation={c.conversationId}>
                <ProjectChips label={c.conversationName || c.conversationId} chosen={c.projects} all={view.projects.filter((p) => !hiddenIds.includes(p.id))}
                  canEdit={view.canEdit} busy={busy} testId="chat-channel"
                  onChange={(ids) => void save(async () => setView(await client.setChannelProjects(provider.id, c.conversationId, c.conversationName, ids)),
                    undefined, `${c.conversationName || "Channel"} updated`)} />
              </li>
            ))}
          </ul>
        )}
      </SettingRow>
      <SettingRow label={`Hidden from ${provider.name}`} fill
        help={`Nothing new from ${provider.name} can see these: they are left out of routing, never linked, and every tool of its sessions refuses them, their repositories, tasks and memory. A task already filed in one keeps working on its own project, and reaches no other hidden one. Set here only, never from ${provider.name}. A member of ${orgName} can still use them outside ${provider.name}.`}>
        <ProjectChips label="Hidden" chosen={view.hidden} all={view.projects} canEdit={view.canEdit} busy={busy} testId="chat-hidden"
          onChange={(ids) => void save(async () => setView(await client.setHiddenProjects(provider.id, ids)), undefined, "Hidden projects saved")} />
      </SettingRow>
    </SettingsSection>
  );
}

/** A list of projects as chips: removable, and one more added from the rest, for an admin. */
function ProjectChips({ label, chosen, all, canEdit, busy, testId, onChange }: {
  label: string;
  chosen: ChatProjectRef[];
  all: ChatProjectRef[];
  canEdit: boolean;
  busy: boolean;
  testId: string;
  onChange: (ids: string[]) => void;
}) {
  const ids = chosen.map((p) => p.id);
  const rest = all.filter((p) => !ids.includes(p.id));
  return (
    <span className="chatProjectChips" data-testid={testId}>
      <b>{label}</b>
      {chosen.length === 0 ? <span className="muted">{canEdit ? "None" : "None set"}</span> : null}
      {chosen.map((p) => (
        <Badge key={p.id} size="sm" emphasis="subtle" data-testid={`${testId}-project`} data-project={p.key}>
          {p.key}
          {canEdit ? <IconButton size="sm" icon="close" label={`Remove ${p.key} from ${label}`} disabled={busy}
            data-testid={`${testId}-remove`} onClick={() => onChange(ids.filter((id) => id !== p.id))} /> : null}
        </Badge>
      ))}
      {canEdit && rest.length ? (
        <Select size="sm" aria-label={`Add a project to ${label}`} placeholder="Add a project…" value="" disabled={busy}
          data-testid={`${testId}-add`} options={rest.map((p) => ({ value: p.id, label: `${p.key} · ${p.name}` }))}
          onValueChange={(id) => id && onChange([...ids, id])} />
      ) : null}
    </span>
  );
}

const ROUTE_WORD: Record<string, string> = {
  brainstorm: "Brainstorm", task: "Task", epic: "Epic", resume: "Resume", task_link: "Task link", clarify: "Clarify",
  channel_projects: "Channel projects",
  not_connected: "Refused: not connected", not_member: "Refused: not a member", failed: "Failed",
};
const EVERY = "__every__";

const MADE_ROUTES: ReadonlySet<string> = new Set<ChatMadeRoute>(["brainstorm", "task", "epic", "resume", "task_link"]);

/**
 * What a failed request still made: its made_route, recorded when it
 * failed. A row older than that is read from its result: a task or epic
 * bound by a task link is the link, a session the brainstorm made instead
 * unless resume was asked. Null when nothing was made.
 */
export function failedMade(r: Pick<ChatRequestRow, "route" | "askedRoute" | "result" | "madeRoute">): ChatMadeRoute | null {
  if (r.route !== "failed" || !r.result) return null;
  if (r.madeRoute && MADE_ROUTES.has(r.madeRoute)) return r.madeRoute as ChatMadeRoute;
  if (r.askedRoute === "task_link") return "task_link";
  switch (r.result.kind) {
    case "session": return r.askedRoute === "resume" ? "resume" : "brainstorm";
    case "task": return "task";
    case "epic": return "epic";
  }
}

/** A failure's text past which the line is clipped, with the rest behind "Show all". */
export const FAILURE_CLIP = 140;

/** Why a failed request failed: one muted danger line under its route, the whole text on hover or on Show all. */
function FailureLine({ failure }: { failure: string }) {
  const [open, setOpen] = useState(false);
  const long = failure.length > FAILURE_CLIP;
  const shown = open || !long ? failure : `${failure.slice(0, FAILURE_CLIP - 1).trimEnd()}…`;
  return (
    <span className="chatRequestFailure" data-testid="chat-request-failure" data-open={open || !long}>
      <Tooltip content={failure} disabled={!long || open}>
        <span data-testid="chat-request-failure-text" tabIndex={long && !open ? 0 : undefined}>{shown}</span>
      </Tooltip>
      {long ? (
        <TextButton onClick={() => setOpen((o) => !o)} aria-expanded={open} data-testid="chat-request-failure-more">
          {open ? "Show less" : "Show all"}</TextButton>
      ) : null}
    </span>
  );
}

/** Every routed mention, from every provider. */
function RequestsPage({ client, providers, onPage }: { client: ApiClient; providers: ChatProvider[]; onPage: (page: string) => void }) {
  const [provider, setProvider] = useState(EVERY);
  const [route, setRoute] = useState(EVERY);
  return (
    <div className="settingsPage" data-testid="chat-requests">
      <Breadcrumb size="sm" items={[{ id: "chat", label: "Chat", icon: "message", onSelect: () => onPage(CHAT_PAGE) }, { id: "requests", label: "Requests" }]} />
      <SettingsHeader title="Requests"
        description="Every mention of @dude in chat and where it went: who asked, what the router decided and why, and what dude made. Kept for 30 days. Only admins see this page." />
      <div className="chatRequestFilters">
        <Select aria-label="Provider" size="sm" value={provider} onValueChange={setProvider} data-testid="chat-requests-provider"
          options={[{ value: EVERY, label: "Every provider" }, ...providers.map((p) => ({ value: p.id, label: p.name }))]} />
        <Select aria-label="Route" size="sm" value={route} onValueChange={setRoute} data-testid="chat-requests-route"
          options={[{ value: EVERY, label: "Every route" }, ...CHAT_ROUTES.map((r) => ({ value: r, label: ROUTE_WORD[r]! }))]} />
      </div>
      <RequestsTable client={client} providers={providers} provider={provider === EVERY ? undefined : provider} route={route === EVERY ? undefined : route}
        summary />
    </div>
  );
}

const RESULT_ICON = { session: "brainstorm", task: "inbox", epic: "layers" } as const;

/** Requests as a table: the person and where, the route, how sure, what it made, when; the reasoning behind ⓘ. */
function RequestsTable({ client, providers, provider, route, limit, summary }: {
  client: ApiClient;
  providers: ChatProvider[];
  provider?: string | undefined;
  route?: string | undefined;
  /** Only the newest so many. */
  limit?: number | undefined;
  /** The last 7 days' count over the table. */
  summary?: boolean | undefined;
}) {
  const [rows, setRows] = useState<ChatRequestRow[] | null>(null);
  const [week, setWeek] = useState<ChatRequestsSummary | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  useEffect(() => {
    setRows(null);
    client.chatRequests({ provider, route, limit, summary }).then((r) => {
      setRows(r.requests);
      setWeek(r.week ?? null);
    }, (err: unknown) => setProblem(errorText(err)));
  }, [client, provider, route, limit, summary]);
  const byId = useMemo(() => new Map(providers.map((p) => [p.id, providerView(p)])), [providers]);
  if (problem) return <Callout tone="danger">{problem}</Callout>;
  if (rows === null) return <Spinner label="Loading…" />;
  if (rows.length === 0) {
    return <EmptyState title="No requests" description="Nobody has mentioned @dude in an installed chat in the last 30 days, or none matches." />;
  }
  const shown = limit ? rows.slice(0, limit) : rows;
  return (
    <>
      {summary && week ? (
        <p className="muted" data-testid="chat-requests-summary">Last 7 days · {week.mentions} {week.mentions === 1 ? "mention" : "mentions"} · {week.asked} asked the model</p>
      ) : null}
      <Table>
        <THead><Tr><Th>Request</Th><Th width="200px">Route</Th><Th align="right" width="64px">Sure</Th><Th>Result</Th><Th align="right">When</Th></Tr></THead>
        <TBody>
          {shown.map((r) => {
            const pv = byId.get(r.provider) ?? { id: r.provider, name: r.provider, glyph: "integration" as const };
            return (
              <Tr key={r.id} data-testid="chat-request" data-route={r.route}>
                <Td wrap>
                  <span className="chatRequestWho">
                    {r.person ? <PersonAvatar person={r.person} size={24} /> : <span className="chatRequestNoFace" aria-hidden />}
                    <span className="chatRequestText">
                      <span className="chatRequestQuote">“{r.text}”</span>
                      <span className="muted chatRequestWhere" data-testid="chat-request-where">{r.person?.name ?? r.externalUserName} · <ProviderGlyph provider={pv} size={11} inline />
                        {r.conversationName ? ` · ${r.conversationName}` : ""}</span>
                    </span>
                  </span>
                </Td>
                <Td wrap>
                  <span className="chatRequestRoute">
                    <RouteBadge route={routeView(r.route)} fallback={r.askedRoute ? routeView(r.askedRoute) : null} made={failedMade(r)} />
                    {r.route === "failed" && r.failure ? <FailureLine failure={r.failure} /> : null}
                    {r.route === "task_link" || (r.route === "resume" && r.confidence === null) ? <span className="muted">Bound thread · no model</span> : null}
                    {r.sharedAdded.length || r.sharedSkipped.length ? <span className="muted">Shared with {r.sharedAdded.length}{r.sharedSkipped.length ? ` · ${r.sharedSkipped.length} not connected` : ""}</span> : null}
                    {r.route === "clarify" ? <span className="muted">{r.rounds} {r.rounds === 1 ? "round" : "rounds"} asked</span> : null}
                  </span>
                </Td>
                <Td align="right" mono>{r.confidence === null ? "—" : `${Math.round(r.confidence * 100)}%`}</Td>
                <Td wrap>{r.result ? (
                  <a href={resultHref(r.result)} data-testid="chat-request-result" className="chatRequestResult">
                    <span className="chatRequestKind"><Icon name={RESULT_ICON[r.result.kind]} size={12} /></span>{r.result.label ?? r.result.id}</a>
                ) : <span className="muted">{r.route === "not_connected" ? "Asked to connect" : r.route.startsWith("not_") ? "Refused privately" : "—"}</span>}
                  {r.linked.map((l) => (
                    <span key={l.key} className="muted chatRequestLinked" data-testid="chat-request-linked" title={`${l.key} · ${l.name}`}>
                      {l.by} linked {l.key} from {pv.name}</span>
                  ))}</Td>
                <Td align="right" muted fit>
                  <span className="chatRequestWhen">{formatTimestamp(r.createdAt, "relative")}
                    {r.reasoning ? (
                      <Tooltip content={r.reasoning}>
                        <IconButton size="sm" icon="info" label={`Why: ${r.reasoning}`} data-testid="chat-request-why" />
                      </Tooltip>
                    ) : null}</span>
                </Td>
              </Tr>
            );
          })}
        </TBody>
      </Table>
    </>
  );
}

function resultHref(result: { kind: "session" | "task" | "epic"; id: string }): string {
  const base = window.location.hash.match(/^#\/o\/[^/]+\//)?.[0] ?? "#/";
  return `${base}${result.kind === "session" ? "sessions" : result.kind}/${encodeURIComponent(result.id)}`;
}
