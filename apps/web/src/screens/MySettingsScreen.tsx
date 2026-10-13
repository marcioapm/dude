/**
 * Your settings — this browser's, not the organization's: how dude looks,
 * and whether it tells you when something waits on you. Appearance is kept
 * by the design system's ThemeProvider; reduced motion follows the system.
 */

import { useEffect, useState } from "react";
import { useTheme } from "@dude/design-system";
import { Breadcrumb } from "@dude/design-system/components";
import { Button, Callout, Card, CardBody, CardHeader, FormActions, FormStack, Page, PageHeader, Select } from "@dude/design-system/primitives";
import type { ApiClient, Me, Member } from "../api/client.ts";
import { KeysSection, ProfileSection } from "./YouSections.tsx";
import { useSave } from "../hooks/useSave.tsx";
import { pushState, showTestNotification, turnPushOff, turnPushOn, type PushState } from "../push.ts";

const PUSH_TEXT: Record<PushState, string> = {
  on: "On in this browser: a question or a request from an agent shows as a notification, even with dude closed.",
  off: "Off in this browser. Waiting work still shows under Needs you.",
  blocked: "Blocked by this browser. Allow notifications for this site in the browser's settings, then come back.",
  unsupported: "This browser cannot show dude's notifications here (it needs a secure connection and push support).",
};

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

  useEffect(() => {
    void pushState().then(setPush);
  }, []);
  useEffect(() => {
    void client.me().then((r) => setGithub({ githubLogin: r.person.githubLogin ?? null, commitEmail: r.person.commitEmail ?? null }),
      () => setGithub({ githubLogin: null, commitEmail: null }));
  }, [client, read]);

  useEffect(() => {
    void client.githubAuthorization().then(setAuthorization, () => setAuthorization(null));
  }, [client, read]);

  const change = (to: () => Promise<PushState>) => save(async () => setPush(await to()));

  return (
    <Page data-testid="my-settings">
      <PageHeader breadcrumb={<Breadcrumb items={[{ id: "me", label: "You" }]} />} title="Settings"
        description="Only you see these: how teammates see you, your keys, and — in this browser — how dude looks and what it tells you." />
      {me && github ? (
        // Keyed by what is saved (your name from the shell, your GitHub fields
        // from /v1/me): after a save the form starts again as each read lands,
        // so it ends on the saved values whichever lands first.
        <ProfileSection key={`${me.id}:${me.name}:${github.githubLogin ?? ""}:${github.commitEmail ?? ""}`} client={client} me={{ ...me, ...github }}
          onChanged={() => { setRead((n) => n + 1); onChanged(); }} />
      ) : null}
      {authorization?.available ? <Card data-testid="github-authorization">
        <CardHeader title="GitHub authorization" />
        <CardBody>
          {new URLSearchParams(window.location.search).get("github") ? <Callout tone="neutral">{
            ({ connected: "GitHub connected.", authorization_denied: "GitHub authorization was denied. Connect again when ready.",
              invalid_or_expired: "This GitHub connection request expired or was already used. Connect again.",
              enable_expiring_tokens: "Ask an administrator to enable expiring user authorization tokens in the GitHub App.",
              identity_failed: "GitHub could not verify your account. Connect again.",
              app_changed: "The GitHub App changed. Connect again." } as Record<string, string>)[new URLSearchParams(window.location.search).get("github")!]
              ?? "GitHub authorization failed. Connect again."
          }</Callout> : null}
          <p data-testid="github-authorization-status">{authorization.connection ? `Connected as ${authorization.connection.login}` : "Not connected"}</p>
          {authorization.connection ? <p className="muted">Access expires {authorization.connection.expiresAt}.
            {authorization.connection.reconnectRequired || new Date(authorization.connection.refreshExpiresAt).getTime() <= Date.now()
              ? " Connect GitHub again to open pull requests." : " Refreshed automatically while authorized."}</p> : null}
          <p className="muted">Connect to open your tasks' pull requests as you. Git and monitoring still use the App.</p>
          {problem ? <Callout tone="danger">{problem}</Callout> : null}
          <FormActions>
            <Button data-testid="github-connect" disabled={busy} onClick={() => void save(async () => {
              const { url } = await client.connectGithub();
              window.location.assign(url);
            })}>Connect GitHub</Button>
            {authorization.connection ? <Button data-testid="github-disconnect" disabled={busy} variant="quiet" onClick={() => void save(async () => {
              await client.disconnectGithub(); setRead((n) => n + 1);
            })}>Disconnect GitHub</Button> : null}
          </FormActions>
        </CardBody>
      </Card> : null}
      <KeysSection client={client} />
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
              <p className="muted" data-testid="push-state" data-state={push}>{PUSH_TEXT[push]}</p>
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
                    <Button variant="quiet" disabled={busy} onClick={() => void change(() => turnPushOff(client))} data-testid="push-off">
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
