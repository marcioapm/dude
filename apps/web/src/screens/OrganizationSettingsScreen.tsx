/**
 * The organization's settings: who is in it (everyone sees; admins manage),
 * then, for its admins, its GitHub connection and
 * the defaults every project starts from — each agent role (a sub-page of
 * Agents in the menu) and delivery. A project changes any of them for
 * itself in its own settings.
 */

import { useCallback, useEffect, useState } from "react";
import { AgentAvatar, SettingRow, SettingsHeader, SettingsNote, SettingsSection } from "@dude/design-system/components";
import {
  Badge,
  Button,
  Callout,
  Card,
  CardBody,
  CardFooter,
  CardHeader,
  Input,
  KeyValueList,
  Spinner,
} from "@dude/design-system/primitives";
import type { ApiClient, ForgeConnection, Member } from "../api/client.ts";
import { MembersSection } from "./MembersSection.tsx";
import { errorText, FormDialog, useSave } from "../hooks/useSave.tsx";
import { settingsPage } from "../settings.ts";
import { SETTINGS_ROLES } from "@dude/domain";
import { agentsNav, deliveryNav, isRole, SettingsFrame, useSettings } from "./SettingsFrame.tsx";
import { DeliveryPage, RolePage } from "./settingsPages.tsx";
import { GithubBehaviour, WebhookCard } from "./GithubSettings.tsx";
import { isMemoryPage, MEMORY_PAGES, MemoryPages, memoryNav, useIndexSummary, type ProjectChoice } from "./MemorySettings.tsx";
import { MachinesPage, useMachineSizes } from "./MachinesSettings.tsx";
import { ModelsPage, useModelTiers } from "./ModelsSettings.tsx";
import { ImagesPage, queueCount, useImages } from "./ImagesSettings.tsx";
import { useImageChoices } from "../images.tsx";

// The first is where the screen opens: who is in the organization, then
// GitHub, what a new organization sets up first.
const PAGES = ["members", "github", "general", ...SETTINGS_ROLES, "models", "machines", "images", "delivery", ...MEMORY_PAGES] as const;

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
  const needsSettings = page === "general" || page === "delivery" || isRole(page);
  return (
    <SettingsFrame
      testId="org-settings"
      loading={false}
      problem={null}
      page={page}
      onPage={onPage}
      scope={{ title: settings?.organization.name ?? "Organisation", subtitle: "Organisation settings", leading: <AgentAvatar role="orchestrator" size="lg" /> }}
      items={[
        { id: "members", label: "Members", icon: "human" },
        { id: "general", label: "General", icon: "settings" },
        { id: "github", label: "GitHub", icon: "git-branch" },
        agentsNav(settings),
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
      ) : (
        <>
          <SettingsNote icon="info">
            Organisation admins only change these. Every project starts from them; an admin can change them for one project in its own settings.
          </SettingsNote>
          {page === "members" ? (
            <MembersSection client={client} me={me} people={people} onChanged={onPeopleChanged} />
          ) : page === "github" ? (
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
  const [verdict, setVerdict] = useState<{ ok: boolean; text: string } | null>(null);
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
    setVerdict(null);
    try {
      const v = await client.verifyForge();
      setVerdict(
        v.ok
          ? { ok: true, text: `Connected as ${v.login ?? "an unnamed account"}${v.scopes ? ` · scopes: ${v.scopes}` : ""}` }
          : { ok: false, text: v.reason },
      );
    } catch (err) {
      setVerdict({ ok: false, text: errorText(err) });
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
            <KeyValueList items={[
              { label: "Authentication", value: connection.auth === "pat" ? "Personal access token" : "GitHub App" },
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
        </CardBody>
        <CardFooter>
          {connection.connected ? (
            <Button variant="secondary" onClick={() => void verify()} disabled={verifying} data-testid="forge-verify">
              {verifying ? "Checking…" : "Verify"}
            </Button>
          ) : null}
          {admin ? (
            <Button variant={connection.connected ? "quiet" : "primary"} onClick={() => setReplacing(true)} data-testid="forge-connect">
              {connection.connected ? "Replace token" : "Connect GitHub"}
            </Button>
          ) : null}
        </CardFooter>
      </Card>
      {connection.connected ? (
        <>
          <WebhookCard client={client} health={connection.webhook} onChanged={() => void load()} admin={admin} />
          <GithubBehaviour client={client} admin={admin} />
        </>
      ) : null}
      {replacing ? (
      <TokenDialog
        client={client}
        open={replacing}
        onOpenChange={setReplacing}
        onSaved={() => {
          setVerdict(null);
          void load();
        }}
      />
      ) : null}
    </>
  );
}

/** Mounted only while open, so each opening starts empty. */
function TokenDialog(props: { client: ApiClient; open: boolean; onOpenChange: (open: boolean) => void; onSaved: () => void }) {
  const [token, setToken] = useState("");
  const [apiBase, setApiBase] = useState("");
  const { busy, problem, save } = useSave();
  return (
    <FormDialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title="Connect GitHub"
      description="A personal access token with repository access. It is stored for this organization and never shown again."
      submitLabel="Save"
      submitTestId="forge-save"
      canSubmit={!busy && Boolean(token.trim())}
      problem={problem}
      onSubmit={() =>
        void save(
          () => props.client.connectForge(token.trim(), apiBase.trim() || undefined),
          () => {
            props.onOpenChange(false);
            props.onSaved();
          },
          "GitHub connection saved",
        )
      }
    >
      <Input label="Token" type="password" mono autoComplete="off" autoFocus value={token}
        onChange={(e) => setToken(e.target.value)} data-testid="forge-token" />
      <Input label="API base URL" mono value={apiBase} placeholder="https://api.github.com"
        hint="Only for GitHub Enterprise." onChange={(e) => setApiBase(e.target.value)} />
    </FormDialog>
  );
}
