/**
 * Talk it through and Deliver send an empty body: the images a task is
 * started with are those its text shows, never ids sent with the start.
 * A session made by its first message sends the message and its links.
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

test("a session made by its first message posts the message and its links", async () => {
  const sent: Array<{ method: string | undefined; path: string; body: unknown }> = [];
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    sent.push({ method: init?.method, path: new URL(String(input), "http://localhost").pathname, body: JSON.parse(String(init?.body)) });
    return Response.json({ id: "ssn_1", title: null, runId: "run_1" }, { status: 201 });
  }) as typeof fetch;
  const client = new ApiClient({ apiKey: "k" });
  const made = await client.createSession({ message: "hi", projects: [{ projectId: "p", repositoryIds: ["r"] }] });
  expect(made).toEqual({ id: "ssn_1", title: null, runId: "run_1" });
  expect(sent).toEqual([{ method: "POST", path: "/v1/brainstorms",
    body: { message: "hi", projects: [{ projectId: "p", repositoryIds: ["r"] }] } }]);
});
