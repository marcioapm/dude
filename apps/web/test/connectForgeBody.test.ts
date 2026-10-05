/**
 * Connecting GitHub sends where GitHub reaches dude with the token, so the
 * backend registers the organization's webhooks; and the API's own base
 * URL when one is given.
 */

import { afterEach, expect, test } from "bun:test";
import { ApiClient } from "../src/api/client.ts";

const originalFetch = globalThis.fetch;
afterEach(() => { globalThis.fetch = originalFetch; });

test("connecting GitHub sends the token with the public URL", async () => {
  const sent: Array<{ method: string; path: string; body: unknown }> = [];
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    sent.push({ method: String(init?.method), path: new URL(String(input), "http://localhost").pathname,
      body: JSON.parse(String(init?.body)) });
    return Response.json({});
  }) as typeof fetch;
  const client = new ApiClient({ apiKey: "k" });
  await client.connectForge("ghp_new", undefined, "https://dude.example.com");
  await client.connectForge("ghp_ghe", "https://ghe.example.com/api/v3", "https://dude.example.com");
  await client.connectForge("ghp_bare");
  expect(sent).toEqual([
    { method: "POST", path: "/v1/forge/credential", body: { auth: "pat", secret: "ghp_new", publicUrl: "https://dude.example.com" } },
    { method: "POST", path: "/v1/forge/credential",
      body: { auth: "pat", secret: "ghp_ghe", apiBaseUrl: "https://ghe.example.com/api/v3", publicUrl: "https://dude.example.com" } },
    { method: "POST", path: "/v1/forge/credential", body: { auth: "pat", secret: "ghp_bare" } },
  ]);
});
