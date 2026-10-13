import { afterEach, expect, test, spyOn } from "bun:test";
import { ThemeProvider } from "@dude/design-system";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import { click, mount, until } from "./dom.ts";
import { FixtureClient } from "../src/fixtures/client.ts";
import { PEOPLE } from "../src/fixtures/data.ts";
import { MySettingsScreen } from "../src/screens/MySettingsScreen.tsx";

let cleanup: (() => Promise<void>) | undefined;
afterEach(async () => { await cleanup?.(); document.body.innerHTML = ""; });
class AuthorizationClient extends FixtureClient {
  connected = true;
  disconnects = 0;
  override githubAuthorization() {
    return Promise.resolve({ available: true, connection: this.connected ? {
      login: "CanonicalOwner", expiresAt: "2026-10-13T12:00:00Z", refreshExpiresAt: "2027-01-01T00:00:00Z", reconnectRequired: false,
    } : null });
  }
  connectUrl = "https://github.com/login/oauth/authorize?client_id=client&state=opaque";
  override connectGithub() { return Promise.resolve({ url: this.connectUrl }); }
  override disconnectGithub() {
    this.disconnects++; this.connected = false;
    return Promise.resolve();
  }
}
test("an unknown callback outcome is shown as the fixed failure text, not echoed", async () => {
  window.history.replaceState(null, "", "/?github=%3Cb%3Ecode%3Dsecret%3C%2Fb%3E#/me/settings");
  const target = document.createElement("div");
  try {
    const { container, unmount } = await mount(<ThemeProvider target={target}>
      <TooltipProvider><ToastProvider><MySettingsScreen client={new AuthorizationClient("a")} me={{ ...PEOPLE[0]!, lastSeenWhere: null }} onChanged={() => {}} /></ToastProvider></TooltipProvider>
    </ThemeProvider>);
    cleanup = unmount;
    const card = await until(() => container.querySelector("[data-testid=github-authorization]"), "authorization card");
    expect(card.textContent).toContain("GitHub authorization failed. Connect again.");
    expect(card.textContent).not.toContain("secret");
    expect(document.documentElement.getAttribute("data-theme")).toBeNull();
  } finally { window.history.replaceState(null, "", "/"); }
});

test("GitHub authorization shows its canonical status and disconnects without exposing credentials", async () => {
  const client = new AuthorizationClient("a");
  const { container, unmount } = await mount(<ThemeProvider target={document.createElement("div")}>
    <TooltipProvider><ToastProvider><MySettingsScreen client={client} me={{ ...PEOPLE[0]!, lastSeenWhere: null }} onChanged={() => {}} /></ToastProvider></TooltipProvider>
  </ThemeProvider>);
  cleanup = unmount;
  const status = await until(() => container.querySelector("[data-testid=github-authorization-status]"), "authorization status");
  expect(status.textContent).toBe("Connected as CanonicalOwner");

  expect(container.querySelector("[data-testid=github-connect]")?.textContent).toBe("Connect GitHub");
  const navigate = spyOn(window.location, "assign").mockImplementation(() => {});
  try {
    await click(container.querySelector<HTMLButtonElement>("[data-testid=github-connect]")!);
    await until(() => navigate.mock.calls.length ? true : null, "full-page OAuth navigation");
    expect(navigate.mock.calls).toEqual([[client.connectUrl]]);
  } finally { navigate.mockRestore(); }
  await click(container.querySelector<HTMLButtonElement>("[data-testid=github-disconnect]")!);
  await until(() => client.disconnects === 1 && container.querySelector("[data-testid=github-authorization-status]")?.textContent === "Not connected" ? true : null,
    "disconnected status");
});
