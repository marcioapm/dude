import { afterEach, expect, mock, test } from "bun:test";
import { ApiClient, ApiError, type BranchPreviewServerAt } from "../src/api/client.ts";
import { branchPreviewRelay } from "../src/branchPreviewRelay.ts";

const ORIGIN = "https://branch.preview.example.test";
const owner: BranchPreviewServerAt = { kind: "branch", organizationId: "org_acme", organizationSlug: "acme",
  projectId: "prj_acme", previewRunId: "run_first", branch: "feature/cart", server: "web" };
let stop: (() => void) | undefined;
afterEach(() => stop?.());

class Client extends ApiClient {
  constructor() { super({}); }
  owner = owner;
  uploads = mock(async () => "att_shot");
  submissions = mock(async (..._args: Parameters<ApiClient["submitPreviewFeedback"]>) => ({ id: "fbi_saved" }) as Awaited<ReturnType<ApiClient["submitPreviewFeedback"]>>);
  discoveries = mock(async () => this.owner);
  checks = mock(async () => this.owner);
  reads = mock(async () => ({ feedback: [{ id: "fbi_private", body: "private feedback", anchor: { attachmentId: "att_private" } }] }));
  override inOrganization(org: string) { expect(org).toBe("org_acme"); return this; }
  override previewServerAt = this.discoveries;
  override async validatePreviewServer() { return this.checks(); }
  override async feedback() { return await this.reads() as unknown as Awaited<ReturnType<ApiClient["feedback"]>>; }
  override uploadPreviewAttachment = this.uploads;
  override submitPreviewFeedback = this.submissions;
}
function setup() {
  const client = new Client();
  const opener = { closed: false, postMessage: mock(() => {}) };
  const statuses: string[] = [];
  stop = branchPreviewRelay(window, opener as unknown as Window, ORIGIN, owner, client, (s) => statuses.push(s));
  const send = (data: unknown, origin = ORIGIN, source: unknown = opener) => window.dispatchEvent(
    new window.MessageEvent("message", { data, origin, source: source as MessageEventSource }));
  return { client, opener, statuses, send };
}
const draft = { type: "draft", v: 1, nonce: "retry", body: "Button overlaps", anchor: { kind: "preview", url: `${ORIGIN}/cart`, selector: "#pay" } };
const tick = () => new Promise((r) => setTimeout(r, 10));

test("branch relay rejects spoofed origin/source and sends no saved feedback or task navigation", async () => {
  const { client, opener, send } = setup();
  send(draft, "https://evil.test"); send(draft, ORIGIN, {});
  send({ ...draft, anchor: { ...draft.anchor, url: "https://elsewhere.test" } });
  send({ type: "open", v: 1, itemId: "wi_not_a_task" });
  expect(client.submissions).not.toHaveBeenCalled();
  send({ type: "hello", v: 1, url: `${ORIGIN}/cart` }); await tick();
  expect(opener.postMessage).toHaveBeenCalledWith(expect.objectContaining({ type: "session", mode: "branch", pins: [], drafts: 0, showPins: false }), ORIGIN);
  expect(client.uploads).not.toHaveBeenCalled();
});

test("branch relay submits once on doubled messages and acknowledges only the originating window", async () => {
  const { client, opener, statuses, send } = setup();
  send(draft); send(draft); await tick(); send(draft); await tick();
  expect(client.submissions).toHaveBeenCalledTimes(1);
  expect(client.submissions).toHaveBeenCalledWith("run_first", "web", { body: draft.body, anchor: draft.anchor, submissionKey: "retry" });
  expect(opener.postMessage).toHaveBeenCalledWith({ type: "saved", v: 1, nonce: "retry", id: "fbi_saved", drafts: 0, picture: "none" }, ORIGIN);
  expect(statuses).toContain("saved");
});

test("a reused URL never retargets the relay across preview incarnations", async () => {
  const { client, opener, send } = setup(); client.owner = { ...owner, previewRunId: "run_second" };
  send({ type: "hello", v: 1, url: ORIGIN }); await tick();
  expect(opener.postMessage).toHaveBeenCalledWith(expect.objectContaining({ type: "refused", reason: expect.stringContaining("ended") }), ORIGIN);
  expect(client.submissions).not.toHaveBeenCalled();
});

test("requested screenshot upload failure refuses submission instead of silently dropping it", async () => {
  const { client, opener, send } = setup(); client.uploads.mockImplementation(async () => { throw new Error("storage unavailable"); });
  send({ ...draft, picture: "data:image/png;base64,iVBORw0KGgo=" }); await tick();
  expect(client.uploads).toHaveBeenCalledTimes(1); expect(client.submissions).not.toHaveBeenCalled();
  expect(opener.postMessage).toHaveBeenCalledWith(expect.objectContaining({ type: "retryable", reason: "storage unavailable" }), ORIGIN);
});

test("ambiguous committed POST retries the same key and screenshot without duplicating the item", async () => {
  const { client, opener, send } = setup();
  const items = new Map<string, string>(); const keys: string[] = [];
  client.submissions.mockImplementation(async (_run: string, _server: string, input: { submissionKey: string }) => {
    keys.push(input.submissionKey); items.set(input.submissionKey, "fbi_committed");
    if (keys.length === 1) throw new TypeError("response body aborted after commit");
    return { id: items.get(input.submissionKey)! } as Awaited<ReturnType<ApiClient["submitPreviewFeedback"]>>;
  });
  const picture = { ...draft, picture: "data:image/png;base64,iVBORw0KGgo=" };
  send(picture); await tick();
  expect(opener.postMessage).toHaveBeenCalledWith(expect.objectContaining({ type: "retryable", nonce: draft.nonce }), ORIGIN);
  send(picture); await tick();
  expect(keys).toEqual([draft.nonce, draft.nonce]); expect(items.size).toBe(1);
  expect(client.uploads).toHaveBeenCalledTimes(1);
  expect(opener.postMessage).toHaveBeenCalledWith(expect.objectContaining({ type: "saved", id: "fbi_committed" }), ORIGIN);
});

test("only definite 4xx submission refusals discard the retry identity", async () => {
  const { client, opener, send } = setup();
  client.submissions.mockImplementation(async () => { throw new ApiError(404, "not_found", "Preview ended"); });
  send(draft); await tick();
  expect(opener.postMessage).toHaveBeenCalledWith(expect.objectContaining({ type: "refused", nonce: draft.nonce }), ORIGIN);
});

test("5xx submission outcomes remain retryable", async () => {
  const { client, opener, send } = setup();
  client.submissions.mockImplementation(async () => { throw new ApiError(503, "unavailable", "Response unavailable"); });
  send(draft); await tick();
  expect(opener.postMessage).toHaveBeenCalledWith(expect.objectContaining({ type: "retryable", nonce: draft.nonce }), ORIGIN);
});

test("heartbeats coalesce a slow known-owner check and never discover again or read saved feedback", async () => {
  const { client, opener, send } = setup();
  let release!: () => void;
  const gate = new Promise<void>((resolve) => { release = resolve; });
  client.checks.mockImplementation(async () => { await gate; return owner; });
  for (let i = 0; i < 20; i++) send({ type: "hello", v: 1, url: `${ORIGIN}/cart` });
  await tick();
  expect(client.discoveries).not.toHaveBeenCalled(); expect(client.checks).toHaveBeenCalledTimes(1);
  release(); await tick();
  for (let i = 0; i < 20; i++) send({ type: "navigated", v: 1, url: `${ORIGIN}/next` });
  await tick();
  expect(client.checks).toHaveBeenCalledTimes(1); expect(client.reads).not.toHaveBeenCalled();
  expect(JSON.stringify(opener.postMessage.mock.calls)).not.toMatch(/fbi_private|private feedback|att_private/);
});

test("known-owner checks are bounded even when transport fails and do not refuse pending saves", async () => {
  const { client, opener, send } = setup();
  const clock = Date.now; let now = clock(); Date.now = () => now;
  try {
    client.checks.mockImplementation(async () => { throw new TypeError("network down"); });
    send({ type: "hello", v: 1, url: ORIGIN }); await tick();
    for (let i = 0; i < 10; i++) send({ type: "hello", v: 1, url: ORIGIN });
    await tick(); expect(client.checks).toHaveBeenCalledTimes(1);
    expect(opener.postMessage).not.toHaveBeenCalled();
    now += 30_001;
    send({ type: "hello", v: 1, url: ORIGIN }); await tick();
    expect(client.checks).toHaveBeenCalledTimes(2);
  } finally { Date.now = clock; }
});

test("even successful feedback read responses are forbidden on the submission-only relay", async () => {
  const original = globalThis.fetch;
  const requests: string[] = [];
  globalThis.fetch = mock(async (url: string | URL | Request) => {
    requests.push(String(url));
    return Response.json(String(url).includes("/owner?") ? owner : {
      feedback: [{ id: "fbi_private", body: "private feedback", anchor: { selector: "#private", attachmentId: "att_private" } }],
    });
  }) as unknown as typeof fetch;
  const opener = { closed: false, postMessage: mock(() => {}) };
  stop = branchPreviewRelay(window, opener as unknown as Window, ORIGIN, owner, new ApiClient({}), () => {});
  try {
    for (const type of ["hello", "hello", "navigated"]) window.dispatchEvent(new window.MessageEvent("message", {
      data: { type, v: 1, url: `${ORIGIN}/cart` }, origin: ORIGIN, source: opener as unknown as MessageEventSource,
    }));
    await tick();
    expect(requests).toHaveLength(1);
    expect(requests[0]).toContain("/servers/web/owner?");
    expect(JSON.stringify(opener.postMessage.mock.calls)).not.toMatch(/fbi_private|private feedback|#private|att_private/);
  } finally { globalThis.fetch = original; }
});
