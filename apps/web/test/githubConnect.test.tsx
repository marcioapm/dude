/**
 * Connecting GitHub in Settings registers dude's webhook: the token is
 * sent with where GitHub reaches this dude (this browser's origin, or
 * where the organization's hooks already deliver), which the backend
 * registers every repository at and keeps for repositories added later.
 * Mounted in happy-dom against the fixture client.
 */

import { afterEach, expect, test } from "bun:test";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import { act, click, mount, until } from "./dom.ts";
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

const HEALTH: WebhookHealth = { lastDeliveryAt: null, lastFailureAt: null, lastFailure: null, failedToday: 0, rotatedAt: null,
  publicUrl: "https://dude.example.com", retrying: 0, lastError: null, repositories: [] };

class ConnectingClient extends FixtureClient {
  connected: Array<[string, string | undefined, string | undefined]> = [];
  constructor(private connection: ForgeConnection) {
    super("a");
  }
  override forgeConnection(): Promise<ForgeConnection> {
    return Promise.resolve(this.connection);
  }
  override connectForge(token: string, apiBaseUrl?: string, publicUrl?: string): Promise<unknown> {
    this.connected.push([token, apiBaseUrl, publicUrl]);
    return Promise.resolve({});
  }
}

async function connect(client: ConnectingClient) {
  const { container, unmount } = await mount(
    <TooltipProvider>
      <ToastProvider>
        <OrganizationSettingsScreen client={client} me={PEOPLE.find((p) => p.role === "admin")!} people={PEOPLE}
          onPeopleChanged={() => {}} projects={[]} page="github" onPage={() => {}} />
      </ToastProvider>
    </TooltipProvider>,
  );
  mounted.push(unmount);
  await click(await until(() => container.querySelector<HTMLButtonElement>("[data-testid=forge-connect]"), "Connect GitHub"));
  const token = await until(() => document.querySelector<HTMLInputElement>("[data-testid=forge-token]"), "the token field");
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(token, "ghp_new");
    token.dispatchEvent(new Event("input", { bubbles: true }));
  });
  await click(await until(() => document.querySelector<HTMLButtonElement>("[data-testid=forge-save]"), "Save"));
  await until(() => client.connected.length === 1 ? true : null, "the connection sent");
}

test("a first connection registers the webhook where this browser reaches dude", async () => {
  const client = new ConnectingClient({ connected: false });
  await connect(client);
  expect(client.connected).toEqual([["ghp_new", undefined, window.location.origin]]);
});

test("a replaced token registers where the organization's hooks already deliver", async () => {
  const client = new ConnectingClient({ connected: true, auth: "pat", secretHint: "abcd", apiBaseUrl: null, webhookPath: "/v1/webhooks/github/org",
    updatedAt: new Date().toISOString(), webhook: HEALTH });
  await connect(client);
  expect(client.connected).toEqual([["ghp_new", undefined, "https://dude.example.com"]]);
});
