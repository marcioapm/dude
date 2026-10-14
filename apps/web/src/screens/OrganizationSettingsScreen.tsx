/**
 * The organization's settings: who is in it (everyone sees; admins manage),
 * then, for its admins, its GitHub connection and
 * the defaults every project starts from — each agent role (a sub-page of
 * Agents in the menu) and delivery. A project changes any of them for
 * itself in its own settings.
 */

import { useCallback, useEffect, useMemo, useState } from "react";
import { AgentAvatar, OrgFace, Segmented, SettingRow, SettingsHeader, SettingsNote, SettingsSection } from "@dude/design-system/components";
import {
  Badge,
  Button,
  Callout,
  Card,
  CardBody,
  CardFooter,
  CardHeader,
  Checkbox,
  Input,
  KeyValueList,
  SecretField,
  Spinner,
} from "@dude/design-system/primitives";
import type { ApiClient, ForgeConnection, ForgeVerification, Member } from "../api/client.ts";
import { MembersSection, organizationInvitations } from "./MembersSection.tsx";
import { countOf, countWord, InvitationsList } from "../accounts/InvitationsList.tsx";
import { InviteDialog } from "../accounts/InviteDialog.tsx";
import { orgOf, useAccount } from "../accounts/context.tsx";
import { errorText, FormDialog, useSave } from "../hooks/useSave.tsx";
import { settingsPage } from "../settings.ts";
import { SETTINGS_ROLES } from "@dude/domain";
import { agentsNav, deliveryNav, isRole, SettingsFrame, useSettings } from "./SettingsFrame.tsx";
import { DeliveryPage, RolePage } from "./settingsPages.tsx";
import { ForgePermissionList, GithubBehaviour, verificationSummary, WebhookCard } from "./GithubSettings.tsx";
import { isMemoryPage, MEMORY_PAGES, MemoryPages, memoryNav, useIndexSummary, type ProjectChoice } from "./MemorySettings.tsx";
import { MachinesPage, useMachineSizes } from "./MachinesSettings.tsx";
import { ModelsPage, useModelTiers } from "./ModelsSettings.tsx";
import { ImagesPage, queueCount, useImages } from "./ImagesSettings.tsx";
import { useImageChoices } from "../images.tsx";
import { NetworkPage } from "./NetworkSettings.tsx";

// The first is where the screen opens: who is in the organization, then
// GitHub, what a new organization sets up first.
const PAGES = ["members", "invitations", "github", "general", ...SETTINGS_ROLES, "network", "models", "machines", "images", "delivery", ...MEMORY_PAGES] as const;

export interface OrganizationSettingsScreenProps {
  client: ApiClient;
  /** You: admins manage the members. */
  me: Member | null;
  people: readonly Member[];
  onPeopleChanged: () => void;
  /** For Memory's project filters. */
  projects: readonly ProjectChoice[];
  page?: string | undefined;
  /** Deeper than the page: an image, its tab, a build (Images). */
  sub?: string | undefined;
  onPage: (page: string, sub?: string) => void;
  onOpenRun?: ((runId: string) => void) | undefined;
}

export function OrganizationSettingsScreen({ client, me, people, onPeopleChanged, projects, page: given, sub, onPage, onOpenRun }: OrganizationSettingsScreenProps) {
  const page = settingsPage(given, PAGES);
  const index = useIndexSummary(client);
  const { scope, problem } = useSettings(client, () => client.organizationSettings(), (p) => client.updateOrganizationSettings(p));
  const settings = scope?.settings;
  // The sizes, once for the screen: the menu's count, the Machines page and
  // each role's Machine field. A change on the Machines page lands here.
  const machines = useMachineSizes(client);
  // The tiers, likewise: the menu's count, the Models page and each role's tier.
  const models = useModelTiers(client);
  // The library and its queue, once for the screen: the menu's count of
  // builds running and waiting, the Images pages, and every role's image.
  const images = useImages(client);
  const imageChoices = useImageChoices(client);
  // Members and GitHub are the backend's own: they show at once, and still
  // work while the orchestrator (defaults, built-in prompts) is away. Only
  // the pages that need its settings wait for them.
  const needsSettings = page === "general" || page === "delivery" || page === "network" || isRole(page);
  const admin = me?.role === "admin";
  const account = useAccount();
  const orgName = account?.current.name ?? settings?.organization.name ?? "this organisation";
  const invitations = useMemo(() => organizationInvitations(client), [client]);
  // The menu's "3 pending", and what bumps the Invitations page after an invite from Members.
  const [pending, setPending] = useState<number | null>(null);
  const [invited, setInvited] = useState(0);
  useEffect(() => {
    if (!admin) return;
    let current = true;
    void invitations.list({ status: "pending" }).then((r) => current && setPending(countOf(r)), () => undefined);
    return () => {
      current = false;
    };
  }, [admin, invitations, invited]);
  const [inviting, setInviting] = useState<{ initial?: string } | null>(null);
  return (
    <SettingsFrame
      testId="org-settings"
      loading={false}
      problem={null}
      page={page}
      onPage={onPage}
      scope={{ title: orgName, subtitle: "Organisation settings",
        leading: account ? <OrgFace org={orgOf(account.current)} size={32} aria-hidden /> : <AgentAvatar role="conductor" size="lg" /> }}
      items={[
        { id: "members", label: "Members", icon: "human" },
        ...(admin ? [{ id: "invitations", label: "Invitations", icon: "link" as const, note: pending ? `${countWord(pending)} pending` : undefined }] : []),
        { id: "general", label: "General", icon: "settings" },
        { id: "github", label: "GitHub", icon: "git-branch" },
        agentsNav(settings),
        { id: "network", label: "Network", icon: "globe" },
        { id: "models", label: "Models", icon: "sparkle", note: models.tiers?.tiers.length || undefined },
        { id: "machines", label: "Machines", icon: "chip", note: machines.sizes?.sizes.length ?? undefined },
        { id: "images", label: "Images", icon: "cube", note: queueCount(images.data) ? <span className="imagesNavCount" data-testid="images-queue-count"><span className="ds-live-dot" aria-hidden />{queueCount(images.data)}</span> : undefined },
        deliveryNav(settings),
        memoryNav(index.status?.failed),
      ]}
    >
      {isMemoryPage(page) ? (
        // A page with another audience says so itself: Memory says who may do what there.
        <MemoryPages client={client} page={page} projects={projects} admin={me?.role === "admin"} index={index} onPage={onPage}
          scope={{ kind: "organization", name: settings?.organization.name ?? "the organisation" }} />
      ) : page === "models" ? (
        // Models says who may change tiers itself.
        <ModelsPage client={client} orgName={settings?.organization.name ?? "the organisation"}
          tiers={models.tiers} problem={models.problem} setTiers={models.setTiers} />
      ) : page === "images" ? (
        <ImagesPage client={client} orgName={settings?.organization.name ?? "the organisation"} images={images} sub={sub}
          onSub={(next) => {
            onPage("images", next);
            imageChoices.reload();
          }} />
      ) : page === "machines" ? (
        // Machines says who may change sizes itself.
        <MachinesPage client={client} orgName={settings?.organization.name ?? "the organisation"}
          sizes={machines.sizes} problem={machines.problem} setSizes={machines.setSizes} />
      ) : page === "members" || page === "invitations" ? (
        <>
          <SettingsNote icon="info">
            Everyone here can create, deliver and steer tasks. Admins manage the organisation: its members, invitations and settings.
          </SettingsNote>
          {page === "members" || !admin ? (
            <MembersSection client={client} me={me} people={people} onChanged={onPeopleChanged} orgName={orgName}
              onInvited={() => setInvited((n) => n + 1)} />
          ) : (
            <div className="settingsPage" data-testid="org-invitations">
              <SettingsHeader title="Invitations"
                description={`Every invitation to ${orgName} and what became of it. A link works only for its address, for 14 days; Renew gives an expired one 14 more.`}
                actions={<Button variant="primary" leadingIcon="plus" onClick={() => setInviting({})} data-testid="invite">Invite people</Button>} />
              <InvitationsList api={invitations} orgName={orgName} version={invited}
                personName={(id) => people.find((p) => p.id === id)?.name}
                onInviteAgain={(row) => setInviting({ initial: row.name ? `${row.name} <${row.email}>` : row.email })} />
            </div>
          )}
          {inviting ? (
            <InviteDialog api={invitations} orgName={orgName} memberEmails={people.flatMap((p) => (p.email ? [p.email] : []))}
              initial={inviting.initial} onClose={() => setInviting(null)} onInvited={() => setInvited((n) => n + 1)} />
          ) : null}
        </>
      ) : (
        <>
          <SettingsNote icon="info">
            Organisation admins only change these. Every project starts from them; an admin can change them for one project in its own settings.
          </SettingsNote>
          {page === "github" ? (
            <GitHubPage client={client} admin={me?.role === "admin"} />
          ) : needsSettings && !scope ? (
            <div className="centered">{problem ? <Callout tone="danger">{problem}</Callout> : <Spinner label="Loading…" />}</div>
          ) : scope && page === "general" ? (
            <>
              <SettingsHeader title="General" />
              <SettingsSection title="Organisation">
                <SettingRow label="Name" help="How dude names this organisation, and what “From …” says in a project’s settings.">
                  <span data-testid="org-name">{scope.settings.organization.name}</span>
                </SettingRow>
              </SettingsSection>
            </>
          ) : scope && page === "delivery" ? (
            <DeliveryPage scope={scope} />
          ) : scope && page === "network" ? (
            <NetworkPage scope={scope} />
          ) : scope && isRole(page) ? (
            <RolePage key={page} scope={scope} role={page} onOpenRun={onOpenRun} sizes={machines.sizes?.sizes ?? null}
              tiers={models.tiers?.tiers ?? null} tiersProblem={models.problem}
              onManageTiers={me?.role === "admin" ? () => onPage("models") : undefined}
              onManageSizes={me?.role === "admin" ? () => onPage("machines") : undefined} images={imageChoices}
              onManageImages={() => onPage("images")} />
          ) : null}
        </>
      )}
    </SettingsFrame>
  );
}

/** Where every project's pull requests are opened — the one thing most often wrong in a way nobody notices until an agent's work cannot land. */
function GitHubPage({ client, admin }: { client: ApiClient; admin: boolean }) {
  const [connection, setConnection] = useState<ForgeConnection | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const [verifying, setVerifying] = useState(false);
  const [verification, setVerification] = useState<ForgeVerification | null>(null);
  const verdict = verification ? verificationSummary(verification) : null;
  const [replacing, setReplacing] = useState(false);

  const load = useCallback(async () => {
    try {
      setConnection(await client.forgeConnection());
    } catch (err) {
      setProblem(errorText(err));
    }
  }, [client]);

  useEffect(() => {
    void load();
  }, [load]);

  async function verify() {
    setVerifying(true);
    setVerification(null);
    try {
      setVerification(await client.verifyForge());
    } catch (err) {
      setVerification({ ok: false, reason: errorText(err) });
    } finally {
      setVerifying(false);
    }
  }

  if (!connection) return <div className="centered">{problem ?? <Spinner label="Loading…" />}</div>;

  const webhookUrl = connection.connected ? `${window.location.origin}${connection.webhookPath}` : null;

  return (
    <>
      <SettingsHeader title="GitHub" description="Where every project’s pull requests are opened, and how dude behaves there." />
      <Card>
        <CardHeader
          title="GitHub"
          actions={connection.connected ? <Badge tone="success">Connected</Badge> : <Badge>Not connected</Badge>}
        />
        <CardBody>
          {connection.connected ? (
            <KeyValueList items={connection.auth === "github_app" ? [
              { label: "Authentication", value: "GitHub App" },
              { label: "App ID", value: connection.appId ?? "—", mono: true },
              { label: "Installation", value: connection.installationId ?? "found per repository owner", mono: Boolean(connection.installationId) },
              { label: "Private key", value: "stored" },
              { label: "API", value: connection.apiBaseUrl ?? "https://api.github.com", mono: true },
              { label: "Webhook", value: webhookUrl, mono: true },
            ] : [
              { label: "Authentication", value: "Personal access token" },
              { label: "Token", value: `…${connection.secretHint}`, mono: true },
              { label: "API", value: connection.apiBaseUrl ?? "https://api.github.com", mono: true },
              { label: "Webhook", value: webhookUrl, mono: true },
            ]} />
          ) : (
            <p className="muted">Agents can implement work, but cannot open pull requests until GitHub is connected.</p>
          )}
          {verdict ? (
            <Callout tone={verdict.ok ? "success" : "danger"} data-testid="forge-verdict">
              {verdict.text}
            </Callout>
          ) : null}
          {verification ? <ForgePermissionList verification={verification} /> : null}
        </CardBody>
        <CardFooter>
          {connection.connected ? (
            <Button variant="secondary" onClick={() => void verify()} disabled={verifying} data-testid="forge-verify">
              {verifying ? "Checking…" : "Verify"}
            </Button>
          ) : null}
          {admin ? (
            <Button variant={connection.connected ? "quiet" : "primary"} onClick={() => setReplacing(true)} data-testid="forge-connect">
              {connection.connected ? (connection.auth === "github_app" ? "Replace credentials" : "Replace token") : "Connect GitHub"}
            </Button>
          ) : null}
        </CardFooter>
      </Card>
      {connection.connected ? (
        <>
          <WebhookCard client={client} health={connection.webhook} onChanged={() => void load()} admin={admin}
            app={connection.auth === "github_app"} webhookUrl={webhookUrl} />
          <GithubBehaviour client={client} admin={admin} />
        </>
      ) : null}
      {replacing ? (
      <TokenDialog
        client={client}
        open={replacing}
        auth={connection.connected ? connection.auth : "pat"}
        connection={connection}
        onOpenChange={setReplacing}
        publicUrl={(connection.connected ? connection.webhook.publicUrl : null) ?? window.location.origin}
        onSaved={() => {
          setVerification(null);
          void load();
        }}
      />
      ) : null}
    </>
  );
}

/**
 * Mounted only while open, so each opening starts empty. A token: saving
 * registers dude's webhook on every repository, delivering to `publicUrl`
 * (where this browser reaches dude, or where the organization's hooks
 * already deliver), which dude keeps: a project or repository added later
 * gets its webhook there too. A GitHub App: its private key, App ID and,
 * optionally, a default installation; its webhook is set in the App.
 * Neither secret is ever shown again.
 */
function TokenDialog(props: { client: ApiClient; open: boolean; publicUrl: string; auth: "pat" | "github_app"; connection: ForgeConnection;
  onOpenChange: (open: boolean) => void; onSaved: () => void }) {
  const [auth, setAuth] = useState<"pat" | "github_app">(props.auth);
  const [token, setToken] = useState("");
  const [privateKey, setPrivateKey] = useState("");
  const saved = props.connection.connected ? props.connection : null;
  const [appId, setAppId] = useState(saved?.appId ?? "");
  const [oauthClientId, setOauthClientId] = useState(saved?.oauthClientId ?? "");
  const [oauthClientSecret, setOauthClientSecret] = useState("");
  const [clearOauthClientSecret, setClearOauthClientSecret] = useState(false);
  const [installationId, setInstallationId] = useState(saved?.installationId ?? "");
  const [apiBase, setApiBase] = useState(saved?.apiBaseUrl ?? "");
  const { busy, problem, save } = useSave();
  const app = auth === "github_app";
  const ready = app ? Boolean(privateKey.trim()) && /^\d+$/.test(appId.trim()) && /^\d*$/.test(installationId.trim())
    : Boolean(token.trim());
  return (
    <FormDialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title="Connect GitHub"
      description={app
        ? "A GitHub App: commits are the App's; pull requests require the task owner's authorization. Secrets are never shown again."
        : "A personal access token with repository access. It is stored for this organisation and never shown again."}
      submitLabel="Save"
      submitTestId="forge-save"
      canSubmit={!busy && ready}
      problem={problem}
      onSubmit={() =>
        void save(
          () => app
            ? props.client.connectGithubApp({ privateKey: privateKey.trim(), appId: appId.trim(), installationId: installationId.trim(),
                ...(oauthClientId.trim() ? { oauthClientId: oauthClientId.trim() } : {}),
                ...(oauthClientSecret.trim() ? { oauthClientSecret: oauthClientSecret.trim() } : {}), ...(clearOauthClientSecret ? { clearOauthClientSecret } : {}) },
              apiBase.trim() || undefined)
            : props.client.connectForge(token.trim(), apiBase.trim() || undefined, props.publicUrl),
          () => {
            props.onOpenChange(false);
            props.onSaved();
          },
          "GitHub connection saved",
        )
      }
    >
      <Segmented label="Authenticate with" value={auth} onChange={setAuth} data-testid="forge-auth"
        options={[{ value: "pat", label: "Personal access token" }, { value: "github_app", label: "GitHub App" }]} />
      {app ? (
        <>
          <Input label="App ID" mono autoComplete="off" autoFocus value={appId} inputMode="numeric"
            onChange={(e) => setAppId(e.target.value)} data-testid="forge-app-id" />
          <Input label="Installation ID (optional)" mono autoComplete="off" value={installationId} inputMode="numeric"
            hint="Leave empty: dude finds the installation for each repository's owner."
            onChange={(e) => setInstallationId(e.target.value)} data-testid="forge-installation-id" />
          <SecretField label="Private key" value={privateKey} onChange={setPrivateKey}
            placeholder="Paste the whole .pem file" hint="The whole .pem file GitHub generated for the App."
            data-testid="forge-private-key" />
          <Input label="OAuth Client ID" mono data-testid="forge-oauth-client-id" value={oauthClientId} autoComplete="off"
            onChange={(e) => setOauthClientId(e.target.value)} />
          <SecretField label="OAuth Client secret" value={oauthClientSecret} onChange={setOauthClientSecret}
            hint="Blank keeps the saved secret. This is not the App's private key."
            data-testid="forge-oauth-client-secret" />
          <Checkbox checked={clearOauthClientSecret} onCheckedChange={(c) => setClearOauthClientSecret(c === true)}
            label="Clear saved OAuth Client secret" />
        </>
      ) : (
        <Input label="Token" type="password" mono autoComplete="off" autoFocus value={token}
          onChange={(e) => setToken(e.target.value)} data-testid="forge-token" />
      )}
      <Input label="API base URL" mono value={apiBase} placeholder="https://api.github.com"
        hint="Only for GitHub Enterprise." onChange={(e) => setApiBase(e.target.value)} />
    </FormDialog>
  );
}
