/**
 * Commenting on the branch preview, on the task page: Comment where a
 * ready preview server is, the reason in its place when commenting is
 * off, Restart for a server serving its pages without the overlay, the
 * notice while a window is open, and Show on preview on a preview card.
 */

import { afterEach, expect, mock, test } from "bun:test";
import type { FeedbackItem, RunServer, TaskServers } from "@dude/domain";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import { act, click, mount, settle, until } from "./dom.ts";
import { FixtureClient } from "../src/fixtures/client.ts";
import { TASK_ID, YOU, PEOPLE } from "../src/fixtures/data.ts";
import { PeopleProvider } from "../src/people.tsx";
import { TaskScreen } from "../src/screens/TaskScreen.tsx";

const WEB = "https://web-shop-1.preview.example.test";

const mounted: Array<() => Promise<void>> = [];
afterEach(async () => {
  for (const u of mounted.splice(0)) await u();
  document.body.innerHTML = "";
});

const web = (over: Partial<RunServer> = {}): RunServer => ({
  name: "web", port: 3000, command: ["npm", "start"], workdir: "", env: {}, fromSpec: true, state: "ready", since: new Date().toISOString(),
  readySince: new Date().toISOString(), stopReason: null, stoppedEpoch: null, epoch: 1, url: WEB, serverState: "ready",
  commenting: { available: true }, needsRestart: false, ...over,
});

class PreviewClient extends FixtureClient {
  servers: RunServer[];
  restarts: string[] = [];
  constructor(servers: RunServer[]) {
    super("a");
    this.servers = servers;
  }
  override async taskServers(): Promise<TaskServers> {
    const base = await super.taskServers();
    return { ...base, run: { ...base.run!, kind: "preview", label: "Branch preview", wakeable: true, previewStage: "ready" }, servers: this.servers.map((s) => ({ ...s })), preview: null };
  }
  override async serverAction(_runId: string, name: string, action: "start" | "stop" | "restart"): Promise<RunServer> {
    this.restarts.push(`${action}:${name}`);
    this.servers = this.servers.map((s) => (s.name === name ? { ...s, needsRestart: false } : s));
    return this.servers.find((s) => s.name === name)!;
  }
}

async function page(client: FixtureClient, tab: "servers" | "feedback" | "overview") {
  const { container, unmount } = await mount(
    <ToastProvider><TooltipProvider><PeopleProvider client={client}>
      <TaskScreen client={client} taskId={TASK_ID} onOpenRun={() => {}} onBack={() => {}} {...(tab === "overview" ? {} : { tab })} />
    </PeopleProvider></TooltipProvider></ToastProvider>,
  );
  mounted.push(unmount);
  await until(() => container.querySelector("[data-testid=task-screen]"), "the task page");
  await settle(100);
  return container;
}

test("Comment sits beside Preview on a ready preview server and opens the task's named window without noopener", async () => {
  const client = new PreviewClient([web()]);
  const open = mock((_url: string, _name?: string, _features?: string) => ({ postMessage() {}, closed: false, focus() {} }));
  (window as unknown as { open: unknown }).open = open;
  const c = await page(client, "servers");
  const button = await until(() => c.querySelector('[data-testid="comment-on-server"][data-server="web"]'), "Comment on web");
  await click(button);
  expect(open).toHaveBeenCalledWith(WEB, `dude-preview-${TASK_ID}`);
  expect(open.mock.calls[0]!.length).toBe(2);
  // The notice says a window is open.
  const notice = await until(() => c.querySelector('[data-testid="commenting-notice"]'), "the commenting notice");
  expect(notice.textContent).toContain("Commenting on web");
  // The existing Preview link is unchanged.
  expect(c.querySelector('[data-testid="server-preview"]')!.getAttribute("rel")).toBe("noopener noreferrer");
});

test("with commenting off there is no Comment: the reason is said in its place", async () => {
  for (const [reason, words] of [["public_url", "Commenting on previews needs auth.public_url"], ["lux", "Commenting on previews needs lux v0.1.32 or later"]] as const) {
    const client = new PreviewClient([web({ commenting: { available: false, reason } })]);
    const c = await page(client, "servers");
    const off = await until(() => c.querySelector('[data-testid="commenting-unavailable"]'), "the reason");
    expect(off.textContent).toBe(words);
    expect(c.querySelectorAll('[data-testid="comment-on-server"]').length).toBe(0);
    for (const u of mounted.splice(0)) await u();
  }
});

test("a server that needs a restart says so, and Restart web restarts it through the existing action", async () => {
  const client = new PreviewClient([web({ needsRestart: true })]);
  const c = await page(client, "overview");
  const notice = await until(() => c.querySelector('[data-testid="commenting-restart"]'), "the restart notice");
  expect(notice.textContent).toContain("without the comment tool");
  const restart = notice.querySelector('[data-testid="commenting-restart-server"][data-server="web"]')!;
  expect(restart.textContent).toBe("Restart web");
  await click(restart);
  await settle(80);
  expect(client.restarts).toEqual(["restart:web"]);
  expect(c.querySelectorAll('[data-testid="commenting-restart"]').length).toBe(0);
});

test("Overview's servers carry Comment on preview; the Feedback tab its way in", async () => {
  const client = new PreviewClient([web()]);
  const c = await page(client, "overview");
  await until(() => c.querySelector('[data-testid="servers-summary"] [data-testid="comment-on-preview"]'), "Comment on preview");
  for (const u of mounted.splice(0)) await u();
  const f = await page(new PreviewClient([web()]), "feedback");
  const wayIn = await until(() => f.querySelector('[data-testid="preview-way-in"]'), "the Feedback tab's way in");
  expect(wayIn.textContent).toContain("Comment on the branch preview");
});

test("a preview card's Show on preview opens its page in the task's window", async () => {
  const client = new PreviewClient([web()]);
  client.feedbackItems.push(previewDraft("fbk_p", `${WEB}/cart`));
  const open = mock((_url: string, _name?: string) => ({ postMessage() {}, closed: false, focus() {} }));
  (window as unknown as { open: unknown }).open = open;
  const c = await page(client, "feedback");
  const show = await until(() => c.querySelector('[data-id="fbk_p"] [data-testid="fb-show-on-preview"]'), "Show on preview");
  await act(async () => (show as HTMLElement).click());
  expect(open).toHaveBeenCalledWith(`${WEB}/cart`, `dude-preview-${TASK_ID}`);
});

test("Show on preview is offered only where it can work: not with commenting off, nor for a page on another server", async () => {
  const cases: Array<[string, RunServer[], string]> = [
    ["commenting off", [web({ commenting: { available: false, reason: "public_url" } })], `${WEB}/cart`],
    ["an earlier preview's hostname", [web()], "https://web-old-1.preview.example.test/cart"],
  ];
  for (const [what, servers, url] of cases) {
    const client = new PreviewClient(servers);
    client.feedbackItems.push(previewDraft("fbk_p", url));
    const c = await page(client, "feedback");
    const card = await until(() => c.querySelector('[data-id="fbk_p"]'), `the card (${what})`);
    expect([what, card.querySelectorAll('[data-testid="fb-show-on-preview"]').length]).toEqual([what, 0]);
    // The anchor line is plain text, no link.
    expect([what, card.querySelectorAll('button[aria-label^="Show it on the preview"]').length]).toEqual([what, 0]);
    expect(card.textContent).toContain("Preview");
    for (const u of mounted.splice(0)) await u();
  }
});

function previewDraft(id: string, url: string): FeedbackItem {
  const now = new Date().toISOString();
  return { id, taskId: TASK_ID, attempt: 1, batchId: null, author: { id: YOU, name: PEOPLE[0]!.name }, body: "Green",
    anchor: { kind: "preview", url, selector: "button.pay", viewport: { width: 1280, height: 720 } }, status: "draft", deliveredRunId: null,
    resolvedCommit: null, resolutionPending: false, outdated: false, routedTo: null, submittedAt: null, createdAt: now, updatedAt: now, history: [] };
}
