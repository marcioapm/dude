import { afterEach, expect, mock, test } from "bun:test";
import { ApiClient, type BranchPreviewServerAt } from "../src/api/client.ts";
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
  submissions = mock(async () => ({ id: "fbi_saved" }) as Awaited<ReturnType<ApiClient["submitPreviewFeedback"]>>);
  override inOrganization(org: string) { expect(org).toBe("org_acme"); return this; }
  override async previewServerAt() { return this.owner; }
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
  expect(opener.postMessage).toHaveBeenCalledWith(expect.objectContaining({ type: "refused", reason: "storage unavailable" }), ORIGIN);
});
