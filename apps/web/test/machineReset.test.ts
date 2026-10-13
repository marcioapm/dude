/**
 * Reset's requests as the control plane routes them: a task role's and a
 * session's, the note sent only when there is one, and a refusal's exact
 * words and code carried to the dialog.
 */

import { afterEach, expect, test } from "bun:test";
import { ApiClient, ApiError } from "../src/api/client.ts";

const originalFetch = globalThis.fetch;
afterEach(() => { globalThis.fetch = originalFetch; });

type Sent = { method: string | undefined; path: string; body: unknown };

function answering(status: number, body: unknown, sent: Sent[]) {
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    sent.push({ method: init?.method, path: new URL(String(input), "http://localhost").pathname, body: JSON.parse(String(init?.body)) });
    return Response.json(body, { status });
  }) as typeof fetch;
}

test("a task role's and a session's Reset are DELETEs on their routes, with the note only when given", async () => {
  const sent: Sent[] = [];
  answering(200, { ok: true, moved: true }, sent);
  const client = new ApiClient({ apiKey: "k" });
  expect(await client.resetTaskMachine("task_1", "qa_browser", "use 2 workers")).toEqual({ ok: true, moved: true });
  await client.resetTaskMachine("task_1", "implementer");
  await client.resetSessionMachine("ssn_1", "fits now");
  await client.resetSessionMachine("ssn_1");
  expect(sent).toEqual([
    { method: "DELETE", path: "/v1/tasks/task_1/agent-machines/qa_browser", body: { note: "use 2 workers" } },
    { method: "DELETE", path: "/v1/tasks/task_1/agent-machines/implementer", body: {} },
    { method: "DELETE", path: "/v1/sessions/ssn_1/agent-machine", body: { note: "fits now" } },
    { method: "DELETE", path: "/v1/sessions/ssn_1/agent-machine", body: {} },
  ]);
});

for (const [status, code, message] of [
  [409, "pending", "a machine move is under way"],
  [404, "not_found", "nothing is raised"],
  [422, "unknown_pool", "Standard's pool has no host it fits"],
  [503, "pools_unavailable", "lux's pools could not be read; try again shortly"],
] as const) {
  test(`a ${status} ${code} refusal reaches the caller in the server's words`, async () => {
    answering(status, { error: { code, message } }, []);
    const err = await new ApiClient({ apiKey: "k" }).resetTaskMachine("task_1", "implementer", "x").catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect((err as ApiError).status).toBe(status);
    expect((err as ApiError).code).toBe(code);
    expect((err as ApiError).message).toBe(message);
  });
}
