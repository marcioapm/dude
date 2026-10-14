/**
 * Your settings — this browser's, not the organization's: how dude looks,
 * and whether it tells you when something waits on you. Appearance is kept
 * by the design system's ThemeProvider; reduced motion follows the system.
 */

import { useEffect, useRef, useState, type RefObject } from "react";
import { useTheme } from "@dude/design-system";
import { Duration, EntityLine, OrgFace } from "@dude/design-system/components";
import { Button, Callout, Card, CardBody, CardFooter, CardHeader, Dialog, FormActions, FormStack, KeyValueList, Page, PageHeader, Select,
  type CalloutTone } from "@dude/design-system/primitives";
import type { ApiClient, Me, Member } from "../api/client.ts";
import { KeysSection, ProfileSection, type KeyGroup } from "./YouSections.tsx";
import { useAccount } from "../accounts/context.tsx";
import { roleWord, untilWords } from "../accounts/words.ts";
import { useSave } from "../hooks/useSave.tsx";
import { pushState, showTestNotification, turnPushOff, turnPushOn, type PushState } from "../push.ts";

// Notifications are on or off per organisation: the card speaks of the one this tab is in.
const PUSH_TEXT: Record<PushState, string> = {
  on: "On in this browser, for this organisation: a question or a request from an agent here shows as a notification, even with dude closed.",
  off: "Off in this browser, for this organisation. Waiting work still shows under Needs you.",
  blocked: "Blocked by this browser. Allow notifications for this site in the browser's settings, then come back.",
  unsupported: "This browser cannot show dude's notifications here (it needs a secure connection and push support).",
};

// Attention where a person decided or must act (you declined, an admin must
// change the App, a newer request won); danger where the attempt failed.
const GITHUB_OUTCOME: Record<string, { text: string; tone: CalloutTone }> = {
  connected: { text: "GitHub connected.", tone: "success" },
  authorization_denied: { text: "GitHub authorization was denied. Connect again when ready.", tone: "attention" },
  invalid_or_expired: { text: "This GitHub connection request expired or was already used. Connect again.", tone: "danger" },
  enable_expiring_tokens: { text: "Ask an administrator to enable expiring user authorization tokens in the GitHub App.", tone: "attention" },
  identity_failed: { text: "GitHub could not verify your account. Connect again.", tone: "danger" },
  app_changed: { text: "The GitHub App changed. Connect again.", tone: "danger" },
  superseded: { text: "This connection request was replaced by a newer one or by Disconnect.", tone: "attention" },
};
const GITHUB_OUTCOME_FAILED = { text: "GitHub authorization failed. Connect again.", tone: "danger" } as const;

type GithubAuthorization = NonNullable<Awaited<ReturnType<ApiClient["githubAuthorization"]>>["connection"]>;

/**
 * Your own GitHub authorization, which opens your tasks' pull requests as
 * you. dude refreshes it only when it needs the token and the access token
 * has ended, and a refresh renews the refresh token too; so the refresh
 * token's end is shown as how long the authorization may sit unused. The
 * access token's end appears only once it has ended and a reconnect is needed.
 */
function GithubAuthorizationCard({ client, connection, outcome, onDisconnected }: {
  client: ApiClient;
  connection: GithubAuthorization | null;
  /** The callback's `?github=` value; anything not in the table reads as a failure. */
  outcome: string | null;
  onDisconnected: () => void;
}) {
  const { busy, problem, save } = useSave();
  const [disconnecting, setDisconnecting] = useState(false);
  const connect = useRef<HTMLButtonElement>(null);
  const now = Date.now();
  const refreshLeft = connection ? Date.parse(connection.refreshExpiresAt) - now : 0;
  const accessLeft = connection ? Date.parse(connection.expiresAt) - now : 0;
  const reconnect = connection !== null && (connection.reconnectRequired || !(refreshLeft > 0));
  const shown = outcome ? GITHUB_OUTCOME[outcome] ?? GITHUB_OUTCOME_FAILED : null;
  const connectVariant = connection && !reconnect ? "secondary" : "primary";
  return (
    <Card data-testid="github-authorization">
      <CardHeader title="GitHub authorization" />
      <CardBody>
        <FormStack>
          {shown ? <Callout tone={shown.tone} data-testid="github-outcome">{shown.text}</Callout> : null}
          <p className="muted">Connect to open your tasks' pull requests as you. Git and monitoring still use the App.</p>
          <KeyValueList items={[
            { label: "Status", value: <span data-testid="github-authorization-status">
              {connection ? `Connected as ${connection.login}` : "Not connected"}</span> },
            ...(connection ? [{ label: "Reconnect", value: <span data-testid="github-authorization-reconnect">
              {reconnect ? "Needed now: pull requests wait until you connect again"
                : <>Not needed while dude uses it; lapses after <Duration ms={refreshLeft} format="age" /> unused</>}
            </span> }] : []),
            ...(reconnect && accessLeft <= 0 ? [{ label: "Access", value: <span data-testid="github-authorization-access">
              Ended <Duration ms={-accessLeft} format="age" /> ago</span> }] : []),
          ]} />
          {problem ? <Callout tone="danger">{problem}</Callout> : null}
        </FormStack>
      </CardBody>
      <CardFooter>
        <Button ref={connect} data-testid="github-connect" variant={connectVariant} data-variant={connectVariant} disabled={busy}
          onClick={() => void save(async () => {
            const { url } = await client.connectGithub();
            window.location.assign(url);
          })}>Connect GitHub</Button>
        {connection ? (
          <Button data-testid="github-disconnect" variant="danger" disabled={busy} onClick={() => setDisconnecting(true)}>
            Disconnect GitHub…
          </Button>
        ) : null}
      </CardFooter>
      {disconnecting ? (
        <DisconnectGithubDialog client={client} onClose={() => setDisconnecting(false)} connect={connect} onDisconnected={onDisconnected} />
      ) : null}
    </Card>
  );
}

function DisconnectGithubDialog({ client, connect, onClose, onDisconnected }: {
  client: ApiClient;
  /** Where focus goes after a disconnect: the button that opened the dialog is about to go. */
  connect: RefObject<HTMLButtonElement | null>;
  onClose: () => void;
  onDisconnected: () => void;
}) {
  const { busy, problem, save } = useSave();
  const done = useRef(false);
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} tone="danger" size="sm" title="Disconnect GitHub?"
      description="dude forgets your GitHub authorization. Your tasks' pull requests wait until you connect again."
      onCloseAutoFocus={(e) => {
        if (!done.current || !connect.current) return;
        e.preventDefault();
        connect.current.focus();
      }}
      footer={
        <>
          <Button variant="quiet" onClick={onClose}>Cancel</Button>
          <Button variant="danger" solid disabled={busy} data-testid="github-disconnect-confirm"
            onClick={() => void save(() => client.disconnectGithub(), () => {
              done.current = true;
              onClose();
              onDisconnected();
            }, "GitHub disconnected")}>
            Disconnect GitHub
          </Button>
        </>
      }>
      {problem ? <Callout tone="danger">{problem}</Callout> : null}
    </Dialog>
  );
}

export function MySettingsScreen({ client, me, onChanged }: {
  client: ApiClient;
  /** You, as the shell has you (`usePeople`); `onChanged` reads you again. */
  me: Member | null;
  onChanged: () => void;
}) {
  const theme = useTheme();
  const [push, setPush] = useState<PushState | null>(null);
  const { busy, problem, save } = useSave();
  // Who you are on GitHub is only on your own /v1/me, not the people list.
  const [github, setGithub] = useState<Pick<Me, "githubLogin" | "commitEmail"> | null>(null);
  const [authorization, setAuthorization] = useState<Awaited<ReturnType<ApiClient["githubAuthorization"]>> | null>(null);
  const [read, setRead] = useState(0);
  const account = useAccount();

  useEffect(() => {
    void pushState(client).then(setPush);
  }, [client]);
  useEffect(() => {
    void client.me().then((r) => setGithub({ githubLogin: r.person.githubLogin ?? null, commitEmail: r.person.commitEmail ?? null }),
      () => setGithub({ githubLogin: null, commitEmail: null }));
  }, [client, read]);

  useEffect(() => {
    void client.githubAuthorization().then(setAuthorization, () => setAuthorization(null));
  }, [client, read]);

  const change = (to: () => Promise<PushState>) => save(async () => setPush(await to()));
  // Your other organisations, asked before the browser's subscription is dropped.
  const elsewhere = () => (account?.memberships ?? []).filter((m) => m.id !== account?.current.id)
    .map((m) => account!.base.inOrganization(m.id));
  const pushText = push ? PUSH_TEXT[push] : "";
  // Keys across your organisations: read with the credential that names none.
  const keysClient = account?.base ?? client;
  const groups: KeyGroup[] = account
    ? account.memberships.map((m) => ({ id: m.id, name: m.name, slug: m.slug, role: m.role }))
    : me ? [{ id: "", name: "this organisation", slug: "", role: me.role }] : [];
  // The callback lands here with a fixed outcome; anything else reads as a failure.
  const [githubOutcome, setGithubOutcome] = useState(() => new URLSearchParams(window.location.search).get("github"));
  // Read once: off the address, so neither a reload nor a later Disconnect shows it again.
  useEffect(() => {
    const url = new URL(window.location.href);
    if (!url.searchParams.has("github")) return;
    url.searchParams.delete("github");
    window.history.replaceState(window.history.state, "", `${url.pathname}${url.search}${url.hash}`);
  }, []);

  return (
    <Page data-testid="my-settings">
      <PageHeader title="Your settings"
        description="Only you see these. Your profile and keys are yours in every organisation; how dude looks is this browser's." />
      {me && github ? (
        // Keyed by what is saved (your name from the shell, your GitHub fields
        // from /v1/me): after a save the form starts again as each read lands,
        // so it ends on the saved values whichever lands first.
        <ProfileSection key={`${me.id}:${me.name}:${github.githubLogin ?? ""}:${github.commitEmail ?? ""}`} client={client} me={{ ...me, ...github }}
          organizations={account?.memberships.map((m) => m.name)} accessEmail={account?.authMethod === "cloudflare_access"}
          onChanged={() => { setRead((n) => n + 1); onChanged(); account?.refresh(); }} />
      ) : null}
      {authorization?.available ? (
        <GithubAuthorizationCard client={client} connection={authorization.connection} outcome={githubOutcome}
          onDisconnected={() => { setGithubOutcome(null); setRead((n) => n + 1); }} />
      ) : null}
      {groups.length ? <KeysSection client={keysClient} groups={groups} /> : null}
      {account && account.memberships.length + account.invitations.length > 1 ? (
        <Card data-testid="my-organisations">
          <CardHeader title="Organisations" />
          <CardBody padding="flush">
            <ul className="plainRows" role="list">
              {account.memberships.map((m) => (
                <li key={m.id} className="plainRow">
                  <EntityLine lead={<OrgFace org={m} size={24} aria-hidden />} name={m.name} detail={`${roleWord(m.role)} · ${window.location.host}/#/o/${m.slug}`} />
                  {m.id === account.current.id ? <span className="muted">This tab</span>
                    : <Button size="sm" variant="quiet" onClick={() => account.switchTo(m.id)}>Open</Button>}
                </li>
              ))}
              {account.invitations.map((i) => (
                <li key={i.code} className="plainRow">
                  <EntityLine lead={<OrgFace org={i.organization} size={24} aria-hidden />} name={i.organization.name}
                    detail={`Invited by ${i.inviter} · expires ${untilWords(i.expiresAt)}`} />
                  <Button size="sm" variant="secondary" onClick={() => account.join(i.code)} data-testid={`settings-join-${i.organization.slug}`}>Join…</Button>
                </li>
              ))}
            </ul>
          </CardBody>
        </Card>
      ) : null}
      <Card>
        <CardHeader title="Appearance" />
        <CardBody>
          <FormStack>
            <Select
              label="Theme"
              value={theme.preference}
              onValueChange={theme.setPreference}
              options={[
                { value: "system", label: "Follow the system" },
                { value: "dark", label: "Dark" },
                { value: "light", label: "Light" },
              ]}
            />
            <Select
              label="Density"
              value={theme.density}
              onValueChange={theme.setDensity}
              options={[
                { value: "comfortable", label: "Comfortable" },
                { value: "compact", label: "Compact — more on screen" },
              ]}
            />
          </FormStack>
        </CardBody>
      </Card>
      <Card data-testid="notifications">
        <CardHeader title="Notifications" />
        <CardBody>
          {push === null ? null : (
            <FormStack>
              <p className="muted" data-testid="push-state" data-state={push}>{pushText}</p>
              {problem ? <Callout tone="danger">{problem}</Callout> : null}
              <FormActions>
                {push === "off" ? (
                  <Button variant="primary" disabled={busy} onClick={() => void change(() => turnPushOn(client))} data-testid="push-on">
                    Notify me in this browser
                  </Button>
                ) : null}
                {push === "on" ? (
                  <>
                    <Button variant="secondary" disabled={busy} onClick={() => void showTestNotification()}>
                      Send a test notification
                    </Button>
                    <Button variant="quiet" disabled={busy} onClick={() => void change(() => turnPushOff(client, elsewhere()))} data-testid="push-off">
                      Turn off
                    </Button>
                  </>
                ) : null}
              </FormActions>
            </FormStack>
          )}
        </CardBody>
      </Card>
    </Page>
  );
}
