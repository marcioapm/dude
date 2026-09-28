/**
 * Application entry point.
 *
 * The API key is read from localStorage rather than baked in. Real auth is a
 * later concern (plan §53 has the organization model); what matters now is
 * that the frontend holds a credential it was given, and never a database
 * connection or a host-specific capability — the app must keep working
 * unchanged inside the desktop shell (plan §117).
 */

import { StrictMode, useCallback, useMemo, useState } from "react";
import { createRoot } from "react-dom/client";
import { Button, Card, CardBody, CardHeader, FormStack, Input, ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import { ThemeProvider } from "@dude/design-system";

import "@dude/design-system/tokens.css";
import "@dude/design-system/base.css";
import "./app.css";

import { ApiClient } from "./api/client.ts";
import { App } from "./App.tsx";
import { turnPushOff } from "./push.ts";
import { PeopleProvider } from "./people.tsx";
import { DudeMark } from "./DudeMark.tsx";

const KEY_STORAGE = "dude.apiKey";

function Root() {
  const [apiKey, setApiKey] = useState(() => localStorage.getItem(KEY_STORAGE) ?? "");
  // The server refused the key it was given: the prompt says so.
  const [refused, setRefused] = useState(false);

  // Same-origin in the browser (Vite proxies /v1), loopback in the desktop
  // shell — so no base URL is needed in either.
  //
  // Memoized because the client is an effect dependency downstream: a new
  // instance per render would tear down and re-establish the event stream.
  // Declared before the early return: hooks must run in the same order on
  // every render, and signing out changes which branch is taken.
  const client = useMemo(() => new ApiClient({ apiKey }), [apiKey]);
  const keyRefused = useCallback(() => {
    localStorage.removeItem(KEY_STORAGE);
    setRefused(true);
    setApiKey("");
  }, []);

  if (!apiKey) {
    return <KeyPrompt refused={refused} onSubmit={(key) => {
      localStorage.setItem(KEY_STORAGE, key);
      setRefused(false);
      setApiKey(key);
    }} />;
  }

  return (
    <TooltipProvider>
      <ToastProvider>
        <PeopleProvider client={client}>
          <App
            client={client}
            onKeyRefused={keyRefused}
            onSignOut={() => {
              // This browser stops hearing about the organization it leaves.
              void turnPushOff(client).finally(() => {
                localStorage.removeItem(KEY_STORAGE);
                setApiKey("");
              });
            }}
          />
        </PeopleProvider>
      </ToastProvider>
    </TooltipProvider>
  );
}

/** Minimal credential entry until real auth exists. */
function KeyPrompt({ refused, onSubmit }: { refused: boolean; onSubmit: (key: string) => void }) {
  const [value, setValue] = useState("");

  return (
    <div className="keyPrompt">
      <Card variant="flat">
        <DudeMark size={160} className="keyPromptMark" />
        <CardHeader title={<h1>dude</h1>} />
        <CardBody>
          <form
            onSubmit={(event) => {
              event.preventDefault();
              if (value.trim()) onSubmit(value.trim());
            }}
          >
            <FormStack>
              <Input
                autoFocus
                type="password"
                label="API key"
                hint="Paste an API key to continue."
                error={refused && !value ? "That key was not accepted. It may be mistyped, or revoked." : undefined}
                value={value}
                placeholder="dude_sk_…"
                onChange={(event) => setValue(event.target.value)}
              />
              <Button type="submit" variant="primary" disabled={!value.trim()}>
                Continue
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
