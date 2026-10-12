/**
 * GitHub connected as an App in Settings: the Webhooks card says the App
 * delivers for every repository, shows the URL and secret to put in the
 * App's settings, and offers no per-repository registration. Mounted in
 * happy-dom against the fixture client.
 */

import { afterEach, expect, test } from "bun:test";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import { click, mount, until } from "./dom.ts";
import type { ForgeConnection, WebhookHealth } from "../src/api/client.ts";
import { FixtureClient } from "../src/fixtures/client.ts";
import { PEOPLE } from "../src/fixtures/data.ts";
import { OrganizationSettingsScreen } from "../src/screens/OrganizationSettingsScreen.tsx";

let mounted: Array<() => Promise<void>> = [];
afterEach(async () => {
  for (const unmount of mounted) await unmount();
  mounted = [];
  document.body.innerHTML = "";
});

const HEALTH: WebhookHealth = { lastDeliveryAt: new Date().toISOString(), lastFailureAt: null, lastFailure: null, failedToday: 0,
  rotatedAt: null, publicUrl: "https://dude.example.com", retrying: 0, lastError: null,
  repositories: [{ id: "r1", name: "api", url: "https://github.com/acme/api", projectName: "API", hookId: null, registeredAt: null, error: null }] };

class AppClient extends FixtureClient {
  constructor(private connection: ForgeConnection) {
    super("a");
  }
  override forgeConnection(): Promise<ForgeConnection> {
    return Promise.resolve(this.connection);
  }
  override revealWebhookSecret(): Promise<{ secret: string }> {
    return Promise.resolve({ secret: "whsec_app" });
  }
}

async function openGithub(client: FixtureClient) {
  const { container, unmount } = await mount(
    <TooltipProvider>
      <ToastProvider>
        <OrganizationSettingsScreen client={client} me={PEOPLE.find((p) => p.role === "admin")!} people={PEOPLE}
          onPeopleChanged={() => {}} projects={[]} page="github" onPage={() => {}} />
      </ToastProvider>
    </TooltipProvider>,
  );
  mounted.push(unmount);
  return container;
}

test("an App's webhooks card says the App delivers, and shows the URL and secret for its settings", async () => {
  const client = new AppClient({ connected: true, auth: "github_app", secretHint: "KEY-", apiBaseUrl: null, webhookPath: "/v1/webhooks/github/org_a",
    updatedAt: new Date().toISOString(), webhook: HEALTH });
  const container = await openGithub(client);
  const card = await until(() => container.querySelector("[data-testid=webhooks]"), "the webhooks card");
  expect(card.querySelector("[data-testid=webhook-app]")?.textContent).toContain("Deliveries come from the GitHub App");
  expect(card.querySelector<HTMLInputElement>("[data-testid=webhook-url]")?.value).toBe(`${window.location.origin}/v1/webhooks/github/org_a`);
  expect(card.querySelector("[data-testid=webhook-register]")).toBeNull();
  expect(card.querySelector("[data-testid=webhook-repo]")).toBeNull();
  // Not "0 of 1 registered": an App's repositories have no hooks to count.
  expect(card.querySelector("[data-testid=webhook-summary]")?.textContent).toContain("the GitHub App's webhook");
  await click(card.querySelector("[data-testid=webhook-show-secret]")!);
  expect((await until(() => card.querySelector<HTMLInputElement>("[data-testid=webhook-secret]"), "the secret")).value).toBe("whsec_app");
});

test("a token's webhooks card still registers on every repository", async () => {
  const client = new AppClient({ connected: true, auth: "pat", secretHint: "abcd", apiBaseUrl: null, webhookPath: "/v1/webhooks/github/org_a",
    updatedAt: new Date().toISOString(), webhook: HEALTH });
  const container = await openGithub(client);
  const card = await until(() => container.querySelector("[data-testid=webhooks]"), "the webhooks card");
  expect(card.querySelector("[data-testid=webhook-register]")).not.toBeNull();
  expect(card.querySelector("[data-testid=webhook-app]")).toBeNull();
  expect(card.querySelectorAll("[data-testid=webhook-repo]").length).toBe(1);
});
