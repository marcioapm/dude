/**
 * GitHub connected as an App in Settings: the Webhooks card says the App
 * delivers for every repository, shows the URL and secret to put in the
 * App's settings, and offers no per-repository registration. Mounted in
 * happy-dom against the fixture client.
 */

import { afterEach, expect, test } from "bun:test";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import { click, mount, type, until } from "./dom.ts";
import type { ForgeConnection, ForgeVerification, GithubAppInput, WebhookHealth } from "../src/api/client.ts";
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
  connectedApps: Array<[GithubAppInput, string | undefined]> = [];
  tokens: string[] = [];
  verdict: ForgeVerification = { ok: true, app: true, bot: "dude-acme[bot]", repository: "acme/api" };
  constructor(private connection: ForgeConnection) {
    super("a");
  }
  override forgeConnection(): Promise<ForgeConnection> {
    return Promise.resolve(this.connection);
  }
  override revealWebhookSecret(): Promise<{ secret: string }> {
    return Promise.resolve({ secret: "whsec_app" });
  }
  override connectGithubApp(app: GithubAppInput, apiBaseUrl?: string): Promise<unknown> {
    this.connectedApps.push([app, apiBaseUrl]);
    return Promise.resolve({});
  }
  override connectForge(token: string): Promise<unknown> {
    this.tokens.push(token);
    return Promise.resolve({});
  }
  override verifyForge(): Promise<ForgeVerification> {
    return Promise.resolve(this.verdict);
  }
}

const APP: ForgeConnection = { connected: true, auth: "github_app", secretHint: "", appId: "9001", installationId: null, apiBaseUrl: null,
  webhookPath: "/v1/webhooks/github/org_a", updatedAt: new Date().toISOString(), webhook: HEALTH };

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
  const client = new AppClient(APP);
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

/** A paste into the masked key field, as the browser sends one: every line of it. */
async function paste(el: HTMLElement, text: string) {
  const { act } = await import("./dom.ts");
  await act(async () => {
    const e = new Event("paste", { bubbles: true, cancelable: true }) as Event & { clipboardData: unknown };
    e.clipboardData = { getData: (type: string) => (type === "text/plain" ? text : "") };
    el.dispatchEvent(e);
  });
}

test("connecting a GitHub App sends its key, App ID and installation, and nothing until the App ID is a number", async () => {
  const client = new AppClient({ connected: false });
  const container = await openGithub(client);
  await click(await until(() => container.querySelector<HTMLButtonElement>("[data-testid=forge-connect]"), "Connect GitHub"));
  const choice = await until(() => document.querySelector("[data-testid=forge-auth]"), "the choice");
  const appButton = [...choice.querySelectorAll("button")].find((b) => b.textContent === "GitHub App")!;
  await click(appButton);
  expect(document.querySelector("[data-testid=forge-token]")).toBeNull();
  const save = document.querySelector<HTMLButtonElement>("[data-testid=forge-save]")!;
  // The design system's secret field: masked, a password input, until shown.
  const key = document.querySelector<HTMLInputElement>("[data-testid=forge-private-key]")!;
  expect(key.type).toBe("password");
  await paste(key, "-----BEGIN RSA PRIVATE KEY-----\nMIIE\n-----END RSA PRIVATE KEY-----\n");
  expect(document.body.textContent).toContain("3 lines");
  await type(document.querySelector("[data-testid=forge-app-id]")!, "app");
  expect(save.disabled).toBe(true);
  await type(document.querySelector("[data-testid=forge-app-id]")!, "9001");
  expect(save.disabled).toBe(false);
  await click(save);
  await until(() => client.connectedApps.length === 1 ? true : null, "the App sent");
  expect(client.connectedApps).toEqual([[{ privateKey: "-----BEGIN RSA PRIVATE KEY-----\nMIIE\n-----END RSA PRIVATE KEY-----",
    appId: "9001", installationId: "" }, undefined]]);
  expect(client.tokens).toEqual([]);
});

test("reopening App settings prefills canonical Client ID but no secrets", async () => {
  const client = new AppClient({ ...APP, connected: true, auth: "github_app", oauthClientId: "saved-client", oauthClientSecretConfigured: true });
  const container = await openGithub(client);
  for (let n=0;n<2;n++) {
    await click(await until(() => container.querySelector<HTMLButtonElement>("[data-testid=forge-connect]"), "Replace credentials"));
    const id = await until(() => document.querySelector<HTMLInputElement>("[data-testid=forge-oauth-client-id]"), "Client ID");
    expect(id.value).toBe("saved-client");
    expect(document.querySelector<HTMLInputElement>("[data-testid=forge-oauth-client-secret]")!.value).toBe("");
    expect(document.querySelector<HTMLInputElement>("[data-testid=forge-private-key]")!.value).toBe("");
    await click([...document.querySelectorAll("button")].find(b=>b.textContent==="Cancel")!);
  }
});

test("a connected App shows its App ID, and Verify reports the token it minted", async () => {
  const client = new AppClient(APP);
  const container = await openGithub(client);
  await until(() => container.querySelector("[data-testid=forge-verify]"), "Verify");
  const text = container.textContent ?? "";
  expect(text).toContain("9001");
  expect(text).toContain("found per repository owner");
  expect(container.querySelector("[data-testid=forge-connect]")?.textContent).toBe("Replace credentials");
  await click(container.querySelector("[data-testid=forge-verify]")!);
  const verdict = await until(() => container.querySelector("[data-testid=forge-verdict]"), "the verdict");
  expect(verdict.textContent).toBe("The App has a token for acme/api; it commits as dude-acme[bot]");

  client.verdict = { ok: false, reason: "GitHub would not give the App a token for acme/api: github 404: the GitHub App is not installed on acme/api" };
  await click(container.querySelector("[data-testid=forge-verify]")!);
  await until(() => container.querySelector("[data-testid=forge-verdict]")?.textContent?.includes("not installed") ? true : null, "the refusal");
});
