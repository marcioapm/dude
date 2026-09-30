/** Application entry point: verify the browser's credential before mounting product data. */

import { StrictMode, useEffect, useMemo, useRef, useState, useSyncExternalStore } from "react";
import { flushSync } from "react-dom";
import { createRoot } from "react-dom/client";
import { Button, Card, CardBody, CardHeader, FormStack, Input, ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import { ThemeProvider } from "@dude/design-system";

import "@dude/design-system/tokens.css";
import "@dude/design-system/base.css";
import "./app.css";

import { ApiClient, ApiError } from "./api/client.ts";
import { App } from "./App.tsx";
import { fixtureScenario, type FixtureScenario } from "./fixtures/scenario.ts";
import { turnPushOff } from "./push.ts";
import { PeopleProvider } from "./people.tsx";
import { DudeMark } from "./DudeMark.tsx";

import { AuthSession, KEY_STORAGE } from "./auth.ts";
import { AuthRefusal } from "./authRefusal.ts";

/**
 * The mockups' world in place of the API, outside production: loaded only
 * when asked for, so a production build carries none of it.
 */
async function fixtureClient(scenario: FixtureScenario): Promise<ApiClient> {
  const { FixtureClient, installFixtureStream } = await import("./fixtures/client.ts");
  installFixtureStream();
  return new FixtureClient(scenario);
}

function Root() {
  // Outside production, `?fixtures=a` … `e` answers the API from the mockups' world.
  const scenario = useMemo(() => (import.meta.env.DEV || import.meta.env.MODE === "fixtures" ? fixtureScenario() : null), []);
  const [fixtures, setFixtures] = useState<ApiClient | null>(null);
  const [session] = useState(() => new AuthSession(localStorage));
  const auth = useSyncExternalStore(session.subscribe, session.snapshot);
  useEffect(() => {
    if (scenario) void fixtureClient(scenario).then(setFixtures);
    else void session.check();
  }, [scenario, session]);

  if (scenario && !fixtures) return null;
  if (!scenario && auth.kind === "key-prompt") return <KeyPrompt refused={auth.refused} />;
  if (!scenario && auth.kind === "network-error") {
    return (
      <div className="keyPrompt" role="alert">
        <Card variant="flat">
          <CardHeader title={<h1>Could not check your session</h1>} />
          <CardBody>
            <p>The server could not be reached. Please try again.</p>
            <Button variant="primary" onClick={() => void session.check()}>Retry</Button>
          </CardBody>
        </Card>
      </div>
    );
  }
  const client = fixtures ?? (auth.kind === "authenticated" ? auth.client : null);
  if (!client) return <div className="keyPrompt" role="status">Checking session…</div>;

  return (
    <TooltipProvider>
      <ToastProvider>
        <AuthRefusal.Provider value={session.refused}>
          <PeopleProvider client={client}>
            <App
              client={client}
              onKeyRefused={session.refused}
              onSignOut={() => {
                void session.signOut(turnPushOff, (url) => location.assign(url), flushSync);
              }}
            />
          </PeopleProvider>
        </AuthRefusal.Provider>
      </ToastProvider>
    </TooltipProvider>
  );
}

/** Minimal credential entry until real auth exists. */
function KeyPrompt({ refused }: { refused: boolean }) {
  const [value, setValue] = useState("");
  const [error, setError] = useState<string>();
  const [pending, setPending] = useState(false);
  const submitting = useRef(false);

  async function submit() {
    const key = value.trim();
    if (!key || submitting.current) return;
    submitting.current = true;
    setPending(true);
    setError(undefined);
    try {
      await new ApiClient({ apiKey: key }).me();
      localStorage.setItem(KEY_STORAGE, key);
      // A new document replaces password-manager injected DOM, not just the form.
      location.replace(`${location.origin}${location.pathname}${location.hash}`);
    } catch (cause) {
      setError(cause instanceof ApiError && (cause.status === 401 || cause.status === 403)
        ? "That key was not accepted. It may be mistyped, or revoked."
        : "Could not check that key. Please try again.");
      submitting.current = false;
      setPending(false);
    }
  }

  return (
    <div className="keyPrompt">
      <Card variant="flat">
        <DudeMark size={160} className="keyPromptMark" />
        <CardHeader title={<h1>dude</h1>} />
        <CardBody>
          <form
            onSubmit={(event) => {
              event.preventDefault();
              void submit();
            }}
          >
            <FormStack>
              <Input
                autoFocus
                type="password"
                label="API key"
                hint="Paste an API key to continue."
                error={error ?? (refused && !value ? "That key was not accepted. It may be mistyped, or revoked." : undefined)}
                disabled={pending}
                value={value}
                placeholder="dude_sk_…"
                onChange={(event) => setValue(event.target.value)}
              />
              <Button type="submit" variant="primary" disabled={pending || !value.trim()}>
                {pending ? "Checking…" : "Continue"}
              </Button>
            </FormStack>
          </form>
        </CardBody>
      </Card>
    </div>
  );
}

const container = document.getElementById("root");
if (!container) throw new Error("#root is missing from index.html");

// Around everything, the key prompt too: theme and density are this
// browser's, remembered across sign-ins.
createRoot(container).render(
  <StrictMode>
    <ThemeProvider>
      <Root />
    </ThemeProvider>
  </StrictMode>,
);
