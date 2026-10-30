/**
 * A branch preview's window adopted by the dude tab its Connect to dude
 * opened (the project Servers page's ServerWindows, branch mode): the
 * strict checks the submission relay had, kept — origin and source,
 * schema, the server's identity checked by the dude tab, one submit per
 * nonce, a screenshot never silently dropped, 4xx refused and the rest
 * retryable, checks bounded in time — and now the server's saved feedback
 * sent as pins, a bounded projection with nothing private in it.
 */

import { afterEach, beforeEach, expect, mock, test } from "bun:test";
import type { PreviewFeedbackItem, RunningRun } from "@dude/domain";
import { overlaySessionSchema } from "@dude/domain";
import { ApiClient, ApiError, type BranchPreviewServerAt } from "../src/api/client.ts";
import { ServerWindows, markServerLanding } from "../src/serverWindows.ts";

const ORIGIN = "https://branch.preview.example.test";
const owner: BranchPreviewServerAt = { kind: "branch", organizationId: "org_acme", organizationSlug: "acme",
  projectId: "prj_acme", previewRunId: "run_first", branch: "feature/cart", server: "web" };
const run: RunningRun = { id: "run_first", kind: "preview", task: null, branch: "feature/cart", state: "running", startedAt: null, startedBy: null,
  servers: [{ name: "web", port: 3000, url: ORIGIN, state: "ready", since: null, readySince: null, feedback: { open: 0, resolved: 0, authors: 0 } }] };
const saved: PreviewFeedbackItem = { id: "fbi_old", projectId: "prj_acme", previewRunId: "run_first", serverName: "web",
  author: { id: "u_ana", name: "Ana Ribeiro" }, body: "older feedback",
  anchor: { kind: "preview", url: `${ORIGIN}/cart`, selector: "#pay", attachmentId: "att_private" }, captureSha: null, status: "open",
  createdAt: new Date().toISOString(), resolvedAt: null, resolvedBy: null, resolutionNote: "", history: [] };

let stop: (() => void) | undefined;
const own = Object.getOwnPropertyDescriptor(window, "opener");
let opener: { closed: boolean; postMessage: ReturnType<typeof mock> };
beforeEach(() => {
  sessionStorage.clear();
  opener = { closed: false, postMessage: mock(() => {}) };
  Object.defineProperty(window, "opener", { configurable: true, get: () => opener });
});
afterEach(() => {
  stop?.();
  if (own) Object.defineProperty(window, "opener", own);
  else delete (window as { opener?: unknown }).opener;
});

class Client extends ApiClient {
  constructor() { super({}); }
  owner = owner;
  uploads = mock(async () => "att_shot");
  submissions = mock(async (..._args: Parameters<ApiClient["submitPreviewFeedback"]>) => ({ id: "fbi_saved" }) as Awaited<ReturnType<ApiClient["submitPreviewFeedback"]>>);
  discoveries = mock(async () => this.owner);
  checks = mock(async () => this.owner);
  reads = mock(async () => ({ feedback: [saved], nextCursor: null }));
  override previewServerAt = this.discoveries;
  override async validatePreviewServer() { return this.checks(); }
  override async previewFeedback() { return this.reads(); }
  override uploadPreviewAttachment = this.uploads;
  override submitPreviewFeedback = this.submissions;
}

/** The dude tab a branch preview's Connect to dude opened, on its project's Servers page. */
function setup(client = new Client()) {
  markServerLanding(window, { projectId: "prj_acme", runId: "run_first", server: "web", branch: "feature/cart", origin: ORIGIN });
  const windows = new ServerWindows(window as unknown as Window, client, { you: "u_me", name: () => "Ana" }, () => {});
  windows.expect(run, "web");
  stop = () => windows.dispose();
  const send = (data: unknown, origin = ORIGIN, source: unknown = opener) => window.dispatchEvent(
    new window.MessageEvent("message", { data, origin, source: source as MessageEventSource }));
  return { client, windows, send };
}
const draft = { type: "draft", v: 1, nonce: "retry", body: "Button overlaps", anchor: { kind: "preview", url: `${ORIGIN}/cart`, selector: "#pay" } };
const tick = () => new Promise((r) => setTimeout(r, 10));
const told = () => opener.postMessage.mock.calls.map((c) => c[0] as { type: string });
async function connected(send: ReturnType<typeof setup>["send"]) {
  send({ type: "hello", v: 1, url: `${ORIGIN}/cart` });
  await tick();
}

test("spoofed origin or source, another site's anchor and task navigation are ignored; the session is branch mode with its pins", async () => {
  const { client, send, windows } = setup();
  send(draft, "https://evil.test"); send(draft, ORIGIN, {});
  send({ ...draft, anchor: { ...draft.anchor, url: "https://elsewhere.test" } });
  send({ type: "open", v: 1, itemId: "wi_not_a_task" });
  expect(client.submissions).not.toHaveBeenCalled();
  await connected(send);
  const session = told().find((m) => m.type === "session")!;
  expect(overlaySessionSchema.safeParse(session).success).toBe(true);
  expect(session).toMatchObject({ type: "session", mode: "branch", task: "feature/cart", drafts: 0, pins: [expect.objectContaining({ id: "fbi_old", status: "open" })] });
  expect(opener.postMessage.mock.calls[0]![1]).toBe(ORIGIN);
  expect(client.uploads).not.toHaveBeenCalled();
  expect([...windows.connected()]).toEqual(["run_first/web"]);
});

test("doubled messages submit once, and only the originating window is answered", async () => {
  const { client, send } = setup();
  await connected(send);
  send(draft); send(draft); await tick(); send(draft); await tick();
  expect(client.submissions).toHaveBeenCalledTimes(1);
  expect(client.submissions).toHaveBeenCalledWith("run_first", "web", { body: draft.body, anchor: draft.anchor, submissionKey: "retry" });
  expect(opener.postMessage).toHaveBeenCalledWith({ type: "saved", v: 1, nonce: "retry", id: "fbi_saved", drafts: 0, picture: "none" }, ORIGIN);
});

test("a reused URL never retargets the window across preview incarnations", async () => {
  const client = new Client(); client.owner = { ...owner, previewRunId: "run_second" };
  const { send } = setup(client);
  send({ type: "hello", v: 1, url: ORIGIN }); await tick();
  expect(opener.postMessage).toHaveBeenCalledWith(expect.objectContaining({ type: "refused", reason: expect.stringContaining("ended") }), ORIGIN);
  expect(told().some((m) => m.type === "session")).toBe(false);
  send(draft); await tick();
  expect(client.submissions).not.toHaveBeenCalled();
});

test("a requested screenshot's upload failure refuses the submission instead of silently dropping it", async () => {
  const { client, send } = setup(); client.uploads.mockImplementation(async () => { throw new Error("storage unavailable"); });
  await connected(send);
  send({ ...draft, picture: "data:image/png;base64,iVBORw0KGgo=" }); await tick();
  expect(client.uploads).toHaveBeenCalledTimes(1); expect(client.submissions).not.toHaveBeenCalled();
  expect(opener.postMessage).toHaveBeenCalledWith(expect.objectContaining({ type: "retryable", reason: "storage unavailable" }), ORIGIN);
});

test("an ambiguous committed POST retries the same key and screenshot without duplicating the item", async () => {
  const { client, send } = setup();
  const items = new Map<string, string>(); const keys: string[] = [];
  client.submissions.mockImplementation(async (_run: string, _server: string, input: { submissionKey: string }) => {
    keys.push(input.submissionKey); items.set(input.submissionKey, "fbi_committed");
    if (keys.length === 1) throw new TypeError("response body aborted after commit");
    return { id: items.get(input.submissionKey)! } as Awaited<ReturnType<ApiClient["submitPreviewFeedback"]>>;
  });
  await connected(send);
  const picture = { ...draft, picture: "data:image/png;base64,iVBORw0KGgo=" };
  send(picture); await tick();
  expect(opener.postMessage).toHaveBeenCalledWith(expect.objectContaining({ type: "retryable", nonce: draft.nonce }), ORIGIN);
  send(picture); await tick();
  expect(keys).toEqual([draft.nonce, draft.nonce]); expect(items.size).toBe(1);
  expect(client.uploads).toHaveBeenCalledTimes(1);
  expect(opener.postMessage).toHaveBeenCalledWith(expect.objectContaining({ type: "saved", id: "fbi_committed" }), ORIGIN);
});

test("only definite 4xx submission refusals discard the retry identity", async () => {
  const { client, send } = setup();
  client.submissions.mockImplementation(async () => { throw new ApiError(404, "not_found", "Preview ended"); });
  await connected(send);
  send(draft); await tick();
  expect(opener.postMessage).toHaveBeenCalledWith(expect.objectContaining({ type: "refused", nonce: draft.nonce }), ORIGIN);
});

test("5xx submission outcomes remain retryable", async () => {
  const { client, send } = setup();
  client.submissions.mockImplementation(async () => { throw new ApiError(503, "unavailable", "Response unavailable"); });
  await connected(send);
  send(draft); await tick();
  expect(opener.postMessage).toHaveBeenCalledWith(expect.objectContaining({ type: "retryable", nonce: draft.nonce }), ORIGIN);
});

test("heartbeats coalesce a slow server check and never look the preview up again", async () => {
  const { client, send } = setup();
  let release!: () => void;
  const gate = new Promise<void>((resolve) => { release = resolve; });
  client.checks.mockImplementation(async () => { await gate; return owner; });
  for (let i = 0; i < 20; i++) send({ type: "hello", v: 1, url: `${ORIGIN}/cart` });
  await tick();
  expect(client.discoveries).not.toHaveBeenCalled(); expect(client.checks).toHaveBeenCalledTimes(1);
  release(); await tick();
  for (let i = 0; i < 20; i++) send({ type: "navigated", v: 1, url: `${ORIGIN}/next` });
  await tick();
  expect(client.checks).toHaveBeenCalledTimes(1);
});

test("server checks are bounded even when transport fails, and a transport failure says nothing to the page", async () => {
  const { client, send } = setup();
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

test("pins are a bounded projection: no attachment id, capture or person id reaches the page", async () => {
  const { send } = setup();
  await connected(send);
  const all = JSON.stringify(opener.postMessage.mock.calls);
  expect(all).toContain("older feedback");
  expect(all).not.toMatch(/att_private|u_ana|captureSha|prj_acme/);
});

test("without the landing this tab arrived with, its opener is not adopted", async () => {
  const client = new Client();
  const windows = new ServerWindows(window as unknown as Window, client, { you: null, name: () => "x" }, () => {});
  windows.expect(run, "web");
  stop = () => windows.dispose();
  window.dispatchEvent(new window.MessageEvent("message", { data: { type: "hello", v: 1, url: `${ORIGIN}/cart` }, origin: ORIGIN,
    source: opener as unknown as MessageEventSource }));
  await tick();
  expect(opener.postMessage).not.toHaveBeenCalled();
  expect(client.checks).not.toHaveBeenCalled();
});

test("a preview that stopped drops its pins: its Run's end re-checks at once, and a refusal sends a session with no pins", async () => {
  const { client, send, windows } = setup();
  await connected(send);
  const sessions = () => told().filter((m): m is { type: "session"; pins: unknown[] } & { type: string } => m.type === "session") as Array<{ type: string; pins: unknown[] }>;
  expect(sessions().at(-1)!.pins.length).toBe(1);
  client.checks.mockImplementation(async () => { throw new ApiError(404, "not_found", "This preview ended."); });
  // Within the 30 s the check is cached for: the Run's end on the stream re-checks now, not at the next heartbeat after it.
  windows.recheck("run_first");
  await tick();
  expect(client.checks).toHaveBeenCalledTimes(2);
  expect(sessions().at(-1)!.pins).toEqual([]);
  expect(told().at(-1)).toMatchObject({ type: "refused", reason: "This preview ended." });
  // Nothing brings them back while the server is refused: a feedback event's reload sends no pins.
  const before = sessions().length;
  await windows.reload({ runId: "run_first", server: "web" });
  expect(sessions().length).toBe(before);
  // Another Run's end re-checks nothing here.
  windows.recheck("run_other");
  await tick();
  expect(client.checks).toHaveBeenCalledTimes(2);
});
