import { afterEach, describe, expect, test } from "bun:test";
import { ApiClient, ApiError, type MeResponse } from "../src/api/client.ts";
import { ACCESS_LOGOUT, AuthSession, KEY_STORAGE } from "../src/auth.ts";

const originalFetch = globalThis.fetch;
afterEach(() => { globalThis.fetch = originalFetch; });
const me = { person: {}, organization: { id: "org", name: "Organization" } } as MeResponse;

function setup(key: string | null, responses: Array<MeResponse | Error | Promise<MeResponse>>) {
  const values = new Map(key === null ? [] : [[KEY_STORAGE, key]]);
  const keys: Array<string | undefined> = [];
  const clients: ApiClient[] = [];
  const storage = {
    getItem: (name: string) => values.get(name) ?? null,
    removeItem: (name: string) => { values.delete(name); },
  };
  const session = new AuthSession(storage, (apiKey) => {
    keys.push(apiKey);
    const client = new ApiClient({ apiKey });
    client.me = async () => {
      const response = responses.shift();
      if (response instanceof Error) throw response;
      if (!response) throw new Error("Unexpected probe");
      return response;
    };
    clients.push(client);
    return client;
  });
  return { session, values, keys, clients };
}

const refusal = (status: number) => new ApiError(status, "unauthorized", "Refused");

describe("session-first authentication", () => {
  test("a stored valid key is verified before authenticated state", async () => {
    let resolve!: (value: MeResponse) => void;
    const pending = new Promise<MeResponse>((done) => { resolve = done; });
    const { session, keys, values, clients } = setup("valid", [pending]);
    const check = session.check();
    expect(session.snapshot().kind).toBe("checking");
    expect(session.check()).toBe(check);
    resolve(me);
    await check;
    expect(session.snapshot()).toEqual({ kind: "authenticated", client: clients[0], authMethod: "api_key" });
    expect(keys).toEqual(["valid"]);
    expect(values.get(KEY_STORAGE)).toBe("valid");
  });

  for (const status of [401, 403]) {
    test(`${status} clears stored key and probes cookies once`, async () => {
      const { session, keys, values, clients } = setup("revoked", [refusal(status), { ...me, authMethod: "cloudflare_access" }]);
      await session.check();
      expect(keys).toEqual(["revoked", undefined]);
      expect(values.has(KEY_STORAGE)).toBe(false);
      expect(session.snapshot()).toEqual({ kind: "authenticated", client: clients[1], authMethod: "cloudflare_access" });
      await session.check();
      expect(keys).toHaveLength(2);
    });
  }

  test("no key probes cookies before showing a prompt", async () => {
    const { session, keys } = setup(null, [refusal(401)]);
    expect(session.snapshot().kind).toBe("checking");
    await session.check();
    expect(keys).toEqual([undefined]);
    expect(session.snapshot()).toEqual({ kind: "key-prompt", refused: false });
  });

  test("refused key and cookie show the refusal without repeated probes", async () => {
    const { session, keys } = setup("bad", [refusal(403), refusal(401)]);
    await session.check();
    expect(session.snapshot()).toEqual({ kind: "key-prompt", refused: true });
    await session.check();
    expect(keys).toEqual(["bad", undefined]);
  });

  for (const failure of [new TypeError("Network failed"), refusal(503)]) {
    test(`${failure.message} preserves the key and retries the same credential`, async () => {
      const { session, keys, values } = setup("valid", [failure, me]);
      await session.check();
      expect(session.snapshot().kind).toBe("network-error");
      expect(values.get(KEY_STORAGE)).toBe("valid");
      await session.check();
      expect(session.snapshot().kind).toBe("authenticated");
      expect(keys).toEqual(["valid", "valid"]);
    });
  }

  test("cookie network failure retries cookies after clearing a refused key", async () => {
    const { session, keys, values } = setup("bad", [refusal(401), refusal(502), { ...me, authMethod: "cloudflare_access" }]);
    await session.check();
    expect(session.snapshot().kind).toBe("network-error");
    expect(values.has(KEY_STORAGE)).toBe(false);
    await session.check();
    expect(session.snapshot().kind).toBe("authenticated");
    expect(keys).toEqual(["bad", undefined, undefined]);
  });

  test("a live refusal unmounts and checks cookies without duplicate probes", async () => {
    const { session, keys, values, clients } = setup("valid", [me, { ...me, authMethod: "cloudflare_access" }]);
    await session.check();
    session.refused(clients[0]!);
    session.refused(clients[0]!);
    expect(session.snapshot().kind).toBe("checking");
    await session.check();
    expect(session.snapshot().kind).toBe("authenticated");
    expect(keys).toEqual(["valid", undefined]);
    expect(values.has(KEY_STORAGE)).toBe(false);
    session.refused(clients[1]!);
    expect(session.snapshot()).toEqual({ kind: "key-prompt", refused: true });
    expect(keys).toEqual(["valid", undefined]);
  });

  test("a refusal from a replaced client leaves the recovered session", async () => {
    const { session, keys, clients } = setup("valid", [me, { ...me, authMethod: "cloudflare_access" }]);
    await session.check();
    session.refused(clients[0]!);
    await session.check();
    session.refused(clients[0]!);
    expect(session.snapshot()).toEqual({ kind: "authenticated", client: clients[1], authMethod: "cloudflare_access" });
    expect(keys).toEqual(["valid", undefined]);
  });

  test("manual signout ignores unsubscribe failure and does not reprobe cookies", async () => {
    const { session, keys, values, clients } = setup("valid", [me]);
    await session.check();
    const assigned: string[] = [];
    await session.signOut(async (client) => {
      expect(client).toBe(clients[0]!);
      expect(values.get(KEY_STORAGE)).toBe("valid");
      throw new Error("Push unavailable");
    }, (url) => assigned.push(url));
    expect(session.snapshot()).toEqual({ kind: "key-prompt", refused: false });
    expect(values.has(KEY_STORAGE)).toBe(false);
    await session.check();
    session.refused(clients[0]!);
    expect(keys).toEqual(["valid"]);
    expect(assigned).toEqual([]);
  });

  test("Access signout clears state before fixed navigation and cannot remount", async () => {
    const { session, keys, values, clients } = setup("valid", [{ ...me, authMethod: "cloudflare_access", logoutUrl: "https://untrusted.invalid" } as unknown as MeResponse]);
    await session.check();
    const order: string[] = [];
    await session.signOut(async () => { order.push("unsubscribe"); }, (url) => {
      expect(url).toBe(ACCESS_LOGOUT);
      expect(values.has(KEY_STORAGE)).toBe(false);
      expect(session.snapshot().kind).toBe("checking");
      order.push("assign");
    }, (update) => { update(); order.push("unmount"); });
    await session.check();
    session.refused(clients[0]!);
    expect(keys).toEqual(["valid"]);
    expect(order).toEqual(["unsubscribe", "unmount", "assign"]);
  });
});

describe("API authentication transport", () => {
  for (const apiKey of [undefined, "", "secret"]) {
    test(`fetch uses same-origin credentials and conditional bearer (${JSON.stringify(apiKey)})`, async () => {
      let request: Request | undefined;
      let credentials: RequestCredentials | undefined;
      globalThis.fetch = (async (input: string | URL | Request, init?: RequestInit) => {
        credentials = init?.credentials;
        request = new Request(new URL(String(input), "http://localhost"), init);
        return Response.json(me);
      }) as typeof fetch;
      const result = await new ApiClient({ apiKey }).me();
      expect(result).toEqual(me);
      expect(request!.url).toBe("http://localhost/v1/me");
      expect(credentials).toBe("same-origin");
      expect(request!.headers.get("authorization")).toBe(apiKey ? `Bearer ${apiKey}` : null);
    });

    test(`SSE retains scope but omits absent keys (${JSON.stringify(apiKey)})`, () => {
      const url = new URL(new ApiClient({ apiKey }).streamUrl({ after: 17, taskId: "task", live: true }), "http://localhost");
      expect(url.pathname).toBe("/v1/events/stream");
      expect(url.searchParams.get("key")).toBe(apiKey || null);
      expect(url.searchParams.get("after")).toBe("17");
      expect(url.searchParams.get("taskId")).toBe("task");
      expect(url.searchParams.get("live")).toBe("1");
    });
  }

  test("HTML auth refusals preserve status for fallback", async () => {
    globalThis.fetch = (async () => new Response("<h1>Forbidden</h1>", { status: 403 })) as typeof fetch;
    await expect(new ApiClient({}).me()).rejects.toMatchObject({ status: 403 });
  });
});
