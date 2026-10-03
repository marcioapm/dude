/**
 * Talk it through and Deliver send an empty body: the images a task is
 * started with are those its text shows, never ids sent with the start.
 */

import { afterEach, expect, test } from "bun:test";
import { ApiClient } from "../src/api/client.ts";

const originalFetch = globalThis.fetch;
afterEach(() => { globalThis.fetch = originalFetch; });

test("starting a task, either way, sends no attachment ids", async () => {
  const sent: Array<{ path: string; body: unknown }> = [];
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    sent.push({ path: new URL(String(input), "http://localhost").pathname, body: JSON.parse(String(init?.body)) });
    return Response.json({}, { status: 201 });
  }) as typeof fetch;
  const client = new ApiClient({ apiKey: "k" });
  await client.talk("wi_1");
  await client.deliver("wi_1");
  expect(sent).toEqual([
    { path: "/v1/tasks/wi_1/talk", body: {} },
    { path: "/v1/tasks/wi_1/deliver", body: {} },
  ]);
});
