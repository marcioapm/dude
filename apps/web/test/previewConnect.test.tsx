/**
 * The page a preview's Connect to dude opens (`#/preview-connect`): with a
 * stand-in for the preview as this tab's opener, its hello is answered by
 * a lookup of the origin the browser gave it, and the tab goes to the task
 * found, or says why not.
 */

import { afterEach, beforeEach, expect, test } from "bun:test";
import { ApiClient, ApiError, type PreviewServerAt } from "../src/api/client.ts";
import { arrivedFromPreview } from "../src/previewComment.ts";
import { PreviewConnectScreen } from "../src/screens/PreviewConnectScreen.tsx";
import { act, mount, until } from "./dom.ts";

const WEB = "https://web-shop-1.preview.example.test";

class LookupClient extends ApiClient {
  asked: string[] = [];
  constructor(readonly answer: (origin: string) => Promise<PreviewServerAt>) {
    super({ apiKey: "test" });
  }
  override previewServerAt(origin: string): Promise<PreviewServerAt> {
    this.asked.push(origin);
    return this.answer(origin);
  }
}

const own = Object.getOwnPropertyDescriptor(window, "opener");
let opener: object | null;
const unmounts: Array<() => Promise<void>> = [];
beforeEach(() => {
  opener = { postMessage() {}, closed: false };
  Object.defineProperty(window, "opener", { configurable: true, get: () => opener });
  sessionStorage.clear();
});
afterEach(async () => {
  for (const u of unmounts.splice(0)) await u();
  if (own) Object.defineProperty(window, "opener", own);
  else delete (window as { opener?: unknown }).opener;
});

async function screen(client: ApiClient, waitMs = 2_000) {
  const found: PreviewServerAt[] = [];
  const { container, unmount } = await mount(<PreviewConnectScreen client={client} onFound={(f) => found.push(f)} waitMs={waitMs} />);
  unmounts.push(unmount);
  return { container, found, step: () => container.querySelector('[data-testid="preview-connect"]')?.getAttribute("data-step") };
}

async function hello(origin: string, url: string, source: unknown = opener) {
  await act(async () => {
    window.dispatchEvent(new window.MessageEvent("message", { data: { type: "hello", v: 1, url }, origin, source: source as MessageEventSource }));
  });
}

const FOUND = { organizationSlug: "acme", taskId: "wi_7", taskKey: "SHOP-7", server: "web" } as const;

test("the opener's hello is looked up by the origin the browser gave it, and the tab goes to the task found, marked as arrived", async () => {
  const client = new LookupClient(async () => FOUND);
  const s = await screen(client);
  expect(s.step()).toBe("waiting");
  // Another window's hello, and one whose page names another site than it came from, are not taken.
  await hello(WEB, `${WEB}/cart`, { postMessage() {} });
  await hello(WEB, "https://elsewhere.example.test/cart");
  await hello("http://web-shop-1.preview.example.test", "http://web-shop-1.preview.example.test/cart");
  expect(client.asked).toEqual([]);
  await hello(WEB, `${WEB}/cart`);
  await until(() => s.found.length > 0 || null, "the task found");
  expect(client.asked).toEqual([WEB]);
  expect(s.found).toEqual([FOUND]);
  expect(arrivedFromPreview(window, "wi_7")).toBe(true);
  expect(arrivedFromPreview(window, "wi_other")).toBe(false);
});

test("no task of yours at that origin says so, naming the origin", async () => {
  const client = new LookupClient(() => Promise.reject(new ApiError(404, "not_found", "no task")));
  const s = await screen(client);
  await hello(WEB, `${WEB}/`);
  const said = await until(() => (s.step() === "none" ? s.container.textContent : null), "the not-found words");
  expect(said).toContain(`No task of yours has a preview at ${WEB}`);
  expect(s.found).toEqual([]);
  expect(arrivedFromPreview(window, "wi_7")).toBe(false);
});

test("with no opener it says to open it from a preview's Connect to dude, and listens to nothing", async () => {
  opener = null;
  const client = new LookupClient(async () => FOUND);
  const s = await screen(client);
  expect(s.step()).toBe("no-opener");
  expect(s.container.textContent).toContain("Open this from a preview's Connect to dude button");
  // A message with no source (as from a closed window) equals the absent opener, and a stranger's: neither is heard.
  await hello(WEB, `${WEB}/`, null);
  await hello(WEB, `${WEB}/`, { postMessage() {} });
  expect(client.asked).toEqual([]);
  expect(s.step()).toBe("no-opener");
});

test("a lookup that fails for another reason than not found says dude couldn't look the preview up", async () => {
  const client = new LookupClient(() => Promise.reject(new ApiError(500, "internal", "database unavailable")));
  const s = await screen(client);
  await hello(WEB, `${WEB}/`);
  await until(() => (s.step() === "failed" ? true : null), "the failed step");
  expect(s.container.textContent).toContain("dude couldn't look the preview up");
  expect(s.found).toEqual([]);
});

test("a hello in another protocol version is not looked up", async () => {
  const client = new LookupClient(async () => FOUND);
  await screen(client);
  await act(async () => {
    window.dispatchEvent(new window.MessageEvent("message", { data: { type: "hello", v: 2, url: `${WEB}/` }, origin: WEB, source: opener as MessageEventSource }));
  });
  expect(client.asked).toEqual([]);
});

test("hellos said again while the lookup is pending look it up once", async () => {
  const client = new LookupClient(() => new Promise(() => {}));
  const s = await screen(client);
  await hello(WEB, `${WEB}/`);
  await hello(WEB, `${WEB}/`);
  await hello(WEB, `${WEB}/cart`);
  expect(client.asked).toEqual([WEB]);
  expect(s.step()).toBe("looking");
});

test("an opener that never says hello is said to have not answered", async () => {
  const client = new LookupClient(async () => FOUND);
  const s = await screen(client, 60);
  await until(() => (s.step() === "silent" ? true : null), "the silent step");
  expect(s.container.textContent).toContain("The preview didn't answer");
  // A hello after the wait still connects: the page may have been slow.
  await hello(WEB, `${WEB}/`);
  await until(() => s.found.length > 0 || null, "the task found late");
});

test("a lookup that lands after the page was left does nothing: no move to the task, no arrival marked", async () => {
  let land!: (found: PreviewServerAt) => void;
  const client = new LookupClient(() => new Promise((r) => { land = r; }));
  const s = await screen(client);
  await hello(WEB, `${WEB}/`);
  expect(client.asked).toEqual([WEB]);
  await unmounts.pop()!();
  await act(async () => {
    land(FOUND);
    await new Promise((r) => setTimeout(r, 5));
  });
  expect(s.found).toEqual([]);
  expect(arrivedFromPreview(window, FOUND.taskId)).toBe(false);
});

test("a branch preview stays on the submission relay, never navigates to a task", async () => {
  const client = new LookupClient(async () => ({ kind: "branch", organizationId: "org_acme", organizationSlug: "acme",
    projectId: "prj_acme", previewRunId: "run_branch", branch: "feature/cart", server: "web" }));
  // Keep all calls in the test client, rather than creating a network client.
  client.inOrganization = () => client;
  const s = await screen(client);
  await hello(WEB, `${WEB}/cart`);
  await until(() => s.step() === "connected" || null, "branch relay connected");
  expect(s.found).toEqual([]);
  expect(s.container.textContent).toContain("Saved feedback is available only through the CLI and API");
  expect(arrivedFromPreview(window, "run_branch")).toBe(false);
});
