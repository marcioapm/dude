import { afterEach, expect, test, spyOn } from "bun:test";
import { ThemeProvider } from "@dude/design-system";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import { click, mount, until } from "./dom.ts";
import { FixtureClient } from "../src/fixtures/client.ts";
import { PEOPLE } from "../src/fixtures/data.ts";
import { MySettingsScreen } from "../src/screens/MySettingsScreen.tsx";

const HOUR = 3_600_000;
const DAY = 24 * HOUR;
const ISO = /\d{4}-\d{2}-\d{2}T\d{2}:\d{2}/;

let cleanup: (() => Promise<void>) | undefined;
afterEach(async () => {
  await cleanup?.();
  cleanup = undefined;
  document.body.innerHTML = "";
  window.history.replaceState(null, "", "/");
});

class AuthorizationClient extends FixtureClient {
  connected = true;
  reconnectRequired = false;
  disconnects = 0;
  // Half a day past the 150-day mark, so the coarse age reads 150d however long the test takes.
  refreshExpiresAt = new Date(Date.now() + 150 * DAY + 12 * HOUR).toISOString();
  expiresAt = new Date(Date.now() + 8 * HOUR - 60_000).toISOString();
  override githubAuthorization() {
    return Promise.resolve({ available: true, connection: this.connected ? {
      login: "CanonicalOwner", expiresAt: this.expiresAt, refreshExpiresAt: this.refreshExpiresAt, reconnectRequired: this.reconnectRequired,
    } : null });
  }
  connectUrl = "https://github.com/login/oauth/authorize?client_id=client&state=opaque";
  override connectGithub() { return Promise.resolve({ url: this.connectUrl }); }
  override disconnectGithub() {
    this.disconnects++; this.connected = false;
    return Promise.resolve();
  }
}

async function openSettings(client: AuthorizationClient, search = "") {
  window.history.replaceState(null, "", `/${search}#/me/settings`);
  const { container, unmount } = await mount(<ThemeProvider target={document.createElement("div")}>
    <TooltipProvider><ToastProvider><MySettingsScreen client={client} me={{ ...PEOPLE[0]!, lastSeenWhere: null }} onChanged={() => {}} /></ToastProvider></TooltipProvider>
  </ThemeProvider>);
  cleanup = unmount;
  return until(() => container.querySelector<HTMLElement>("[data-testid=github-authorization]"), "authorization card");
}

test("an unknown callback outcome is shown as the fixed failure text in danger, not echoed", async () => {
  const card = await openSettings(new AuthorizationClient("a"), "?github=%3Cb%3Ecode%3Dsecret%3C%2Fb%3E");
  const outcome = card.querySelector("[data-testid=github-outcome]")!;
  expect(outcome.textContent).toBe("GitHub authorization failed. Connect again.");
  expect(outcome.getAttribute("data-tone")).toBe("danger");
  expect(card.textContent).not.toContain("secret");
  expect(document.documentElement.getAttribute("data-theme")).toBeNull();
});

test("each callback outcome takes its own words and a tone by what it means", async () => {
  const cases: Array<[string, string, string]> = [
    ["connected", "GitHub connected.", "success"],
    ["authorization_denied", "GitHub authorization was denied. Connect again when ready.", "attention"],
    ["invalid_or_expired", "This GitHub connection request expired or was already used. Connect again.", "danger"],
    ["enable_expiring_tokens", "Ask an administrator to enable expiring user authorization tokens in the GitHub App.", "attention"],
    ["identity_failed", "GitHub could not verify your account. Connect again.", "danger"],
    ["app_changed", "The GitHub App changed. Connect again.", "danger"],
    ["superseded", "This connection request was replaced by a newer one or by Disconnect.", "attention"],
  ];
  for (const [outcome, text, tone] of cases) {
    const card = await openSettings(new AuthorizationClient("a"), `?github=${outcome}`);
    const shown = card.querySelector("[data-testid=github-outcome]")!;
    expect([outcome, shown.textContent, shown.getAttribute("data-tone")]).toEqual([outcome, text, tone]);
    await cleanup?.();
    cleanup = undefined;
  }
});

test("GitHub authorization shows its canonical status and when a reconnect would be needed, and disconnects only once confirmed", async () => {
  const client = new AuthorizationClient("a");
  const card = await openSettings(client);
  expect(card.querySelector("[data-testid=github-authorization-status]")?.textContent).toBe("Connected as CanonicalOwner");
  // The refresh token renews on use, so its end is how long it may sit unused; never an ISO stamp, and no access-token expiry while it refreshes itself.
  expect(card.querySelector("[data-testid=github-authorization-reconnect]")?.textContent)
    .toBe("Not needed while dude uses it; lapses after 150d unused");
  expect(card.textContent).not.toMatch(ISO);
  expect(card.textContent).not.toContain("Access");
  expect(card.textContent).not.toContain("state=opaque");

  expect(card.querySelector("[data-testid=github-connect]")?.textContent).toBe("Connect GitHub");
  expect(card.querySelector("[data-testid=github-connect]")?.getAttribute("data-variant")).toBe("secondary");
  const navigate = spyOn(window.location, "assign").mockImplementation(() => {});
  try {
    await click(card.querySelector<HTMLButtonElement>("[data-testid=github-connect]")!);
    await until(() => navigate.mock.calls.length ? true : null, "full-page OAuth navigation");
    expect(navigate.mock.calls).toEqual([[client.connectUrl]]);
  } finally { navigate.mockRestore(); }

  await click(card.querySelector<HTMLButtonElement>("[data-testid=github-disconnect]")!);
  const confirm = await until(() => document.querySelector<HTMLButtonElement>("[data-testid=github-disconnect-confirm]"), "the confirmation");
  expect(client.disconnects).toBe(0);
  await click(confirm);
  await until(() => client.disconnects === 1 && card.querySelector("[data-testid=github-authorization-status]")?.textContent === "Not connected" ? true : null,
    "disconnected status");
  expect(card.querySelectorAll("[data-testid=github-disconnect]").length).toBe(0);
  // The dialog is gone and the keyboard is on Connect, not <body>: the button that opened the dialog went with the connection.
  await until(() => document.querySelector("[role=dialog]") === null && document.activeElement === card.querySelector("[data-testid=github-connect]")
    ? true : null, "dialog closed, focus on Connect");
});

test("a connection that needs reconnecting says so, with the access token's end in words", async () => {
  const client = new AuthorizationClient("a");
  client.reconnectRequired = true;
  client.expiresAt = new Date(Date.now() - 3 * HOUR - 60_000).toISOString();
  const card = await openSettings(client);
  expect(card.querySelector("[data-testid=github-authorization-reconnect]")?.textContent).toBe("Needed now: pull requests wait until you connect again");
  expect(card.querySelector("[data-testid=github-authorization-access]")?.textContent).toBe("Ended 3h ago");
  expect(card.textContent).not.toMatch(ISO);
});

test("a reconnect-needed connection whose access token has hours left shows no Access row", async () => {
  const client = new AuthorizationClient("a");
  client.reconnectRequired = true;
  const card = await openSettings(client);
  expect(card.querySelector("[data-testid=github-authorization-reconnect]")?.textContent).toBe("Needed now: pull requests wait until you connect again");
  expect(card.textContent).not.toContain("Access");
  expect(card.textContent).not.toContain("Ends in");
});

test("a refresh token that has ended needs a reconnect even when the server has not flagged one", async () => {
  const client = new AuthorizationClient("a");
  client.reconnectRequired = false;
  client.refreshExpiresAt = new Date(Date.now() - DAY).toISOString();
  client.expiresAt = new Date(Date.now() - 2 * DAY - HOUR).toISOString();
  const card = await openSettings(client);
  expect(card.querySelector("[data-testid=github-authorization-reconnect]")?.textContent).toBe("Needed now: pull requests wait until you connect again");
  expect(card.querySelector("[data-testid=github-authorization-access]")?.textContent).toBe("Ended 2d ago");
  expect(card.querySelector("[data-testid=github-connect]")?.getAttribute("data-variant")).toBe("primary");
  expect(card.textContent).not.toMatch(ISO);
});

test("a failed disconnect keeps the dialog open with the reason, and the connection as it was", async () => {
  const client = new AuthorizationClient("a");
  client.disconnectGithub = () => { client.disconnects++; return Promise.reject(new Error("GitHub did not answer")); };
  const card = await openSettings(client);
  await click(card.querySelector<HTMLButtonElement>("[data-testid=github-disconnect]")!);
  await click(await until(() => document.querySelector<HTMLButtonElement>("[data-testid=github-disconnect-confirm]"), "the confirmation"));
  const alert = await until(() => document.querySelector("[role=dialog] [role=alert]"), "the dialog's alert");
  expect(alert.textContent).toContain("GitHub did not answer");
  expect(client.disconnects).toBe(1);
  expect(document.querySelector("[role=dialog]")).not.toBeNull();
  expect(card.querySelector("[data-testid=github-authorization-status]")?.textContent).toBe("Connected as CanonicalOwner");
  expect(card.querySelector("[data-testid=github-disconnect]")).not.toBeNull();
});

test("Cancel closes the danger confirmation without disconnecting, and gives focus back to Disconnect", async () => {
  const client = new AuthorizationClient("a");
  const card = await openSettings(client);
  const trigger = card.querySelector<HTMLButtonElement>("[data-testid=github-disconnect]")!;
  // happy-dom's click does not focus, so the keyboard is put on the trigger as a real press would.
  trigger.focus();
  await click(trigger);
  const dialog = await until(() => document.querySelector("[role=dialog]"), "the confirmation");
  // The danger Dialog's only exposed mark is its tone icon.
  expect(dialog.querySelector("[data-icon=alert]")).not.toBeNull();
  await click([...dialog.querySelectorAll("button")].find((b) => b.textContent === "Cancel")!);
  await until(() => document.querySelector("[role=dialog]") === null ? true : null, "dialog closed");
  expect(client.disconnects).toBe(0);
  expect(card.querySelector("[data-testid=github-authorization-status]")?.textContent).toBe("Connected as CanonicalOwner");
  await until(() => document.activeElement === trigger ? true : null, "focus back on Disconnect");
});

test("the callback's outcome leaves the address once read, and a later Disconnect does not keep it showing", async () => {
  const client = new AuthorizationClient("a");
  const card = await openSettings(client, "?keep=1&github=connected");
  expect(card.querySelector("[data-testid=github-outcome]")?.textContent).toBe("GitHub connected.");
  expect([window.location.pathname, window.location.search, window.location.hash]).toEqual(["/", "?keep=1", "#/me/settings"]);
  await click(card.querySelector<HTMLButtonElement>("[data-testid=github-disconnect]")!);
  await click(await until(() => document.querySelector<HTMLButtonElement>("[data-testid=github-disconnect-confirm]"), "the confirmation"));
  await until(() => card.querySelector("[data-testid=github-authorization-status]")?.textContent === "Not connected" ? true : null, "disconnected status");
  expect(card.querySelectorAll("[data-testid=github-outcome]").length).toBe(0);
  expect(card.querySelectorAll("[data-tone=success]").length).toBe(0);
  expect(window.location.search).toBe("?keep=1");
});
