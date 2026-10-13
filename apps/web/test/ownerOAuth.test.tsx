import { afterEach, expect, test } from "bun:test";
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
  override disconnectGithub() {
    this.disconnects++; this.connected = false;
    return Promise.resolve();
  }
}
test("GitHub authorization shows its canonical status and disconnects without exposing credentials", async () => {
  const client = new AuthorizationClient("a");
  const { container, unmount } = await mount(<ThemeProvider target={document.createElement("div")}>
    <TooltipProvider><ToastProvider><MySettingsScreen client={client} me={{ ...PEOPLE[0]!, lastSeenWhere: null }} onChanged={() => {}} /></ToastProvider></TooltipProvider>
  </ThemeProvider>);
  cleanup = unmount;
  const status = await until(() => container.querySelector("[data-testid=github-authorization-status]"), "authorization status");
  expect(status.textContent).toBe("Connected as CanonicalOwner");
  expect(container.textContent).not.toContain("hidden-access");
  expect(container.textContent).not.toContain("hidden-refresh");
  expect(container.querySelector("[data-testid=github-connect]")?.textContent).toBe("Connect GitHub");
  await click(container.querySelector<HTMLButtonElement>("[data-testid=github-disconnect]")!);
  await until(() => client.disconnects === 1 && container.querySelector("[data-testid=github-authorization-status]")?.textContent === "Not connected" ? true : null,
    "disconnected status");
});
