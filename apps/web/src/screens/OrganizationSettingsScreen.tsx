/**
 * The organization's settings, for its admins: its GitHub connection, and
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
import type { ApiClient, ForgeConnection } from "../api/client.ts";
import { errorText, FormDialog, useSave } from "../hooks/useSave.tsx";
import { settingsPage } from "../settings.ts";
import { agentsNav, deliveryNav, isRole, SettingsFrame, useSettings } from "./SettingsFrame.tsx";
import { DeliveryPage, RolePage } from "./settingsPages.tsx";

// The first is where the screen opens: GitHub, what a new organization sets up first.
const PAGES = ["github", "general", "implementer", "reviewer", "fixer", "simplifier", "qa_browser", "delivery"] as const;

export interface OrganizationSettingsScreenProps {
  client: ApiClient;
  page?: string | undefined;
  onPage: (page: string) => void;
  onOpenRun?: ((runId: string) => void) | undefined;
}

export function OrganizationSettingsScreen({ client, page: given, onPage, onOpenRun }: OrganizationSettingsScreenProps) {
  const page = settingsPage(given, PAGES);
  const { scope, problem } = useSettings(client, () => client.organizationSettings(), (p) => client.updateOrganizationSettings(p));
  const settings = scope?.settings;
  return (
    <SettingsFrame
      testId="org-settings"
      loading={!scope}
      problem={problem}
      page={page}
      onPage={onPage}
      scope={{ title: settings?.organization.name ?? "", subtitle: "Organisation settings", leading: <AgentAvatar role="orchestrator" size="lg" /> }}
      items={
        settings
          ? [
              { id: "general", label: "General", icon: "settings" },
              { id: "github", label: "GitHub", icon: "git-branch" },
              agentsNav(settings),
              deliveryNav(settings),
            ]
          : []
      }
    >
      {scope ? (
        <>
          <SettingsNote icon="info">
            Organisation admins only. Every project starts from these; a project’s admins can change them for their project.
          </SettingsNote>
          {page === "general" ? (
            <>
              <SettingsHeader title="General" />
              <SettingsSection title="Organisation">
                <SettingRow label="Name" help="How dude names this organisation, and what “From …” says in a project’s settings.">
                  <span data-testid="org-name">{scope.settings.organization.name}</span>
                </SettingRow>
              </SettingsSection>
            </>
          ) : page === "github" ? (
            <GitHubPage client={client} />
          ) : page === "delivery" ? (
            <DeliveryPage scope={scope} />
          ) : isRole(page) ? (
            <RolePage key={page} scope={scope} role={page} onOpenRun={onOpenRun} />
          ) : null}
        </>
      ) : null}
    </SettingsFrame>
  );
}

/** Where every project's pull requests are opened — the one thing most often wrong in a way nobody notices until an agent's work cannot land. */
function GitHubPage({ client }: { client: ApiClient }) {
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
      <SettingsHeader title="GitHub" description="Where every project’s pull requests are opened." />
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
          <Button variant={connection.connected ? "quiet" : "primary"} onClick={() => setReplacing(true)} data-testid="forge-connect">
            {connection.connected ? "Replace token" : "Connect GitHub"}
          </Button>
        </CardFooter>
      </Card>
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
