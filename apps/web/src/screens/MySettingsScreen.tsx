/**
 * Your settings — this browser's, not the organization's: how dude looks,
 * and whether it tells you when something waits on you. Appearance is kept
 * by the design system's ThemeProvider; reduced motion follows the system.
 */

import { useEffect, useState } from "react";
import { useTheme } from "@dude/design-system";
import { Button, Card, CardBody, CardHeader, Select } from "@dude/design-system/primitives";
import type { ApiClient } from "../api/client.ts";
import { errorText } from "../hooks/useSave.tsx";
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
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<string | null>(null);

  useEffect(() => {
    void pushState().then(setPush);
  }, []);

  const change = async (to: () => Promise<PushState>) => {
    setBusy(true);
    setProblem(null);
    try {
      setPush(await to());
    } catch (err) {
      setProblem(errorText(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="settingsScreen" data-testid="my-settings">
      <header className="settingsHeader">
        <h1 className="wiTitle">You</h1>
        <span className="muted">This browser</span>
      </header>
      <Card>
        <CardHeader title="Appearance" />
        <CardBody>
          <div className="settingsForm">
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
          </div>
        </CardBody>
      </Card>
      <Card data-testid="notifications">
        <CardHeader title="Notifications" />
        <CardBody>
          {push === null ? null : (
            <div className="settingsForm">
              <p className="muted" data-testid="push-state" data-state={push}>{PUSH_TEXT[push]}</p>
              {problem ? <p className="problem" role="alert">{problem}</p> : null}
              <div className="settingsActions">
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
                    <Button variant="ghost" disabled={busy} onClick={() => void change(() => turnPushOff(client))} data-testid="push-off">
                      Turn off
                    </Button>
                  </>
                ) : null}
              </div>
            </div>
          )}
        </CardBody>
      </Card>
    </div>
  );
}
