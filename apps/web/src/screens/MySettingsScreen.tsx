/**
 * Your settings — this browser's, not the organization's: how dude looks,
 * and whether it tells you when something waits on you. Appearance is kept
 * by the design system's ThemeProvider; reduced motion follows the system.
 */

import { useEffect, useState } from "react";
import { useTheme } from "@dude/design-system";
import { Breadcrumb } from "@dude/design-system/components";
import { Button, Callout, Card, CardBody, CardHeader, FormActions, FormStack, Page, PageHeader, Select } from "@dude/design-system/primitives";
import type { ApiClient } from "../api/client.ts";
import { useSave } from "../hooks/useSave.tsx";
import { pushState, showTestNotification, turnPushOff, turnPushOn, type PushState } from "../push.ts";

const PUSH_TEXT: Record<PushState, string> = {
  on: "On in this browser: a question or a request from an agent shows as a notification, even with dude closed.",
  off: "Off in this browser. Waiting work still shows under Needs you.",
  blocked: "Blocked by this browser. Allow notifications for this site in the browser's settings, then come back.",
  unsupported: "This browser cannot show dude's notifications here (it needs a secure connection and push support).",
};

export function MySettingsScreen({ client }: { client: ApiClient }) {
  const theme = useTheme();
  const [push, setPush] = useState<PushState | null>(null);
  const { busy, problem, save } = useSave();

  useEffect(() => {
    void pushState().then(setPush);
  }, []);

  const change = (to: () => Promise<PushState>) => save(async () => setPush(await to()));

  return (
    <Page data-testid="my-settings">
      <PageHeader breadcrumb={<Breadcrumb items={[{ id: "me", label: "You" }]} />} title="Settings"
        description="Yours, in this browser: how dude looks, and what it tells you." />
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
