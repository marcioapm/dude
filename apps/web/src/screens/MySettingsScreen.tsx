/**
 * Your settings — this browser's, not the organization's: how dude looks,
 * and whether it tells you when something waits on you. Appearance is kept
 * by the design system's ThemeProvider; reduced motion follows the system.
 */

import { useEffect, useState } from "react";
import { useTheme } from "@dude/design-system";
import { Breadcrumb, EntityLine, OrgFace } from "@dude/design-system/components";
import { Button, Callout, Card, CardBody, CardHeader, FormActions, FormStack, Page, PageHeader, Select } from "@dude/design-system/primitives";
import type { ApiClient, Me, Member } from "../api/client.ts";
import { KeysSection, ProfileSection, type KeyGroup } from "./YouSections.tsx";
import { useAccount } from "../accounts/context.tsx";
import { untilWords } from "../accounts/words.ts";
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
  const [read, setRead] = useState(0);

  useEffect(() => {
    void pushState().then(setPush);
  }, []);
  useEffect(() => {
    void client.me().then((r) => setGithub({ githubLogin: r.person.githubLogin ?? null, commitEmail: r.person.commitEmail ?? null }),
      () => setGithub({ githubLogin: null, commitEmail: null }));
  }, [client, read]);

  const change = (to: () => Promise<PushState>) => save(async () => setPush(await to()));
  const account = useAccount();
  // Keys across your organisations: read with the credential that names none.
  const keysClient = account?.base ?? client;
  const groups: KeyGroup[] = account
    ? account.memberships.map((m) => ({ id: m.id, name: m.name, slug: m.slug, role: m.role }))
    : me ? [{ id: "", name: "this organisation", slug: "", role: me.role }] : [];

  return (
    <Page data-testid="my-settings">
      <PageHeader breadcrumb={<Breadcrumb items={[{ id: "me", label: "You" }]} />} title="Your settings"
        description="Only you see these. Your profile and keys are yours in every organisation; how dude looks is this browser's." />
      {me && github ? (
        // Keyed by what is saved (your name from the shell, your GitHub fields
        // from /v1/me): after a save the form starts again as each read lands,
        // so it ends on the saved values whichever lands first.
        <ProfileSection key={`${me.id}:${me.name}:${github.githubLogin ?? ""}:${github.commitEmail ?? ""}`} client={client} me={{ ...me, ...github }}
          organizations={account?.memberships.map((m) => m.name)} accessEmail={account?.authMethod === "cloudflare_access"}
          onChanged={() => { setRead((n) => n + 1); onChanged(); account?.refresh(); }} />
      ) : null}
      {groups.length ? <KeysSection client={keysClient} groups={groups} you={me?.name ?? ""} /> : null}
      {account && account.memberships.length + account.invitations.length > 1 ? (
        <Card data-testid="my-organisations">
          <CardHeader title="Organisations" />
          <CardBody padding="flush">
            <ul className="plainRows" role="list">
              {account.memberships.map((m) => (
                <li key={m.id} className="plainRow">
                  <EntityLine lead={<OrgFace org={m} size={24} aria-hidden />} name={m.name} detail={`${m.role === "admin" ? "Admin" : "Member"} · ${window.location.host}/#/o/${m.slug}`} />
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
