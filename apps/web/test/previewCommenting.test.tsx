/**
 * Commenting on the branch preview, on the task page: every link to a
 * commentable server opening it in the task's window, the reason when
 * commenting is off (and plain links then), Restart for a server serving
 * its pages without the overlay, the notice while a window is open, and
 * Show on preview and the Page link on a preview card.
 */

import { afterEach, expect, mock, test } from "bun:test";
import type { FeedbackItem, RunServer, TaskServers } from "@dude/domain";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import { ThemeProvider, useTheme } from "@dude/design-system";
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

/**
 * window.open stubbed; `dude` is what dude opened in the task's window. happy-dom
 * follows a link a click left alone with window.open(href, "_blank", …), which
 * `browser` records: a modified or plain-link click lands there.
 */
function stubOpen() {
  const dude: Array<[string, string]> = [];
  const browser: string[] = [];
  // One window for the task's name, as a browser gives back the same named window.
  const win = { postMessage() {}, closed: false, focused: 0, focus() { win.focused++; } };
  (window as unknown as { open: unknown }).open = (url: string, name?: string) => {
    if (name === `dude-preview-${TASK_ID}`) dude.push([url, name]);
    else browser.push(url);
    return win;
  };
  return { dude, browser, win };
}

/** The overlay in the task's window saying hello from `url`. */
async function hello(win: object, url: string) {
  await act(async () => {
    window.dispatchEvent(new window.MessageEvent("message", { data: { type: "hello", v: 1, url }, origin: WEB, source: win as unknown as MessageEventSource }));
  });
}

/** A click as a person's, with the modifiers given: whether the link's default (the browser opening it) went ahead. */
async function press(el: Element, init: MouseEventInit = {}): Promise<boolean> {
  let followed = false;
  await act(async () => {
    followed = el.dispatchEvent(new window.MouseEvent("click", { bubbles: true, cancelable: true, button: 0, ...init }));
  });
  return followed;
}

// A ready server's links: on the Servers tab its URL, its open icon and Preview; on the Overview its URL and Preview.
const LINKS = {
  servers: ['[data-testid="server-url"]', '[data-server="web"] a[aria-label^="Open"]', '[data-testid="server-preview"]'],
  overview: ['[data-testid="servers-summary"] [data-testid="server-url"]', '[data-testid="servers-summary"] [data-testid="server-preview"]'],
} as const;

test("every link to a commentable server opens it in the task's named window without noopener, and stays a real link", async () => {
  for (const tab of ["servers", "overview"] as const) {
    for (const sel of LINKS[tab]) {
      const open = stubOpen();
      const c = await page(new PreviewClient([web()]), tab);
      const link = await until(() => c.querySelector(sel), `${sel} on ${tab}`);
      // A real <a href>: middle-click and copy address still have the URL.
      expect([sel, link.localName, link.getAttribute("href")]).toEqual([sel, "a", WEB]);
      expect([sel, await press(link)]).toEqual([sel, false]);
      expect([sel, open.dude, open.browser]).toEqual([sel, [[WEB, `dude-preview-${TASK_ID}`]], []]);
      const notice = await until(() => c.querySelector('[data-testid="commenting-notice"]'), "the commenting notice");
      expect(notice.textContent).toContain("Commenting on web");
      for (const u of mounted.splice(0)) await u();
    }
  }
});

test("a modified click on a server's link is the browser's: dude opens nothing", async () => {
  const open = stubOpen();
  const c = await page(new PreviewClient([web()]), "servers");
  const link = await until(() => c.querySelector('[data-testid="server-preview"]'), "Preview");
  for (const init of [{ metaKey: true }, { ctrlKey: true }, { shiftKey: true }, { altKey: true }]) {
    expect([init, await press(link, init)]).toEqual([init, true]);
  }
  // A middle click (auxclick in a browser) never reaches onClick; one dispatched as a click with button 1 is the browser's too.
  expect(await press(link, { button: 1 })).toBe(true);
  expect(open.dude).toEqual([]);
});

test("a second click on a server's link brings the open window forward instead of reloading it", async () => {
  const open = stubOpen();
  const c = await page(new PreviewClient([web()]), "servers");
  const link = await until(() => c.querySelector('[data-testid="server-preview"]'), "Preview");
  expect(await press(link)).toBe(false);
  await hello(open.win, `${WEB}/checkout`);
  await until(() => c.querySelector('[data-testid="commenting-notice"][data-state="open"]'), "the open notice");
  const focused = open.win.focused;
  for (const sel of LINKS.servers) expect([sel, await press(c.querySelector(sel)!)]).toEqual([sel, false]);
  expect([open.dude, open.browser]).toEqual([[[WEB, `dude-preview-${TASK_ID}`]], []]);
  expect(open.win.focused).toBe(focused + LINKS.servers.length);
});

test("a card's Page link brings the window forward when it is on that page, and navigates it to another", async () => {
  const client = new PreviewClient([web()]);
  client.feedbackItems.push(previewDraft("fbk_a", `${WEB}/cart?step=2`), previewDraft("fbk_b", `${WEB}/checkout`));
  const open = stubOpen();
  const c = await page(client, "feedback");
  const pageLink = (id: string) => c.querySelector(`[data-id="${id}"] [data-testid="fb-preview-page"]`)!;
  await until(() => c.querySelector('[data-id="fbk_b"] [data-testid="fb-preview-page"]'), "the Page links");
  expect(await press(pageLink("fbk_a"))).toBe(false);
  await hello(open.win, `${WEB}/cart?step=2`);
  const focused = open.win.focused;
  expect(await press(pageLink("fbk_a"))).toBe(false);
  expect([open.dude.length, open.win.focused]).toEqual([1, focused + 1]);
  expect(await press(pageLink("fbk_b"))).toBe(false);
  expect([open.dude, open.browser]).toEqual([[[`${WEB}/cart?step=2`, `dude-preview-${TASK_ID}`], [`${WEB}/checkout`, `dude-preview-${TASK_ID}`]], []]);
});

test("there is no Comment button: the Servers tab says its links open the comment tool", async () => {
  const c = await page(new PreviewClient([web()]), "servers");
  await until(() => c.querySelector('[data-testid="server-preview"]'), "Preview");
  expect(c.querySelectorAll('[data-testid="comment-on-server"]').length).toBe(0);
  expect(c.querySelector('[data-testid="commenting-links"]')!.textContent).toContain("opens the preview with dude's comment tool");
  for (const u of mounted.splice(0)) await u();
  const o = await page(new PreviewClient([web()]), "overview");
  await until(() => o.querySelector('[data-testid="servers-summary"] [data-testid="server-preview"]'), "the Overview's Preview");
  expect(o.querySelectorAll('[data-testid="comment-on-preview"]').length).toBe(0);
});

test("with commenting off the links are plain links to a new tab, and the reason is said on the Servers tab, the Overview and the Feedback tab", async () => {
  for (const [reason, words] of [["public_url", "Commenting on previews needs auth.public_url"], ["lux", "Commenting on previews needs lux v0.1.32 or later"]] as const) {
    for (const tab of ["servers", "overview", "feedback"] as const) {
      const open = stubOpen();
      const t = await page(new PreviewClient([web({ commenting: { available: false, reason } })]), tab);
      const where = tab === "feedback" ? '[data-testid="preview-way-in"] [data-testid="commenting-unavailable"]'
        : `${tab === "overview" ? '[data-testid="servers-summary"]' : '[data-testid="servers-panel"]'} [data-testid="commenting-unavailable"]`;
      const said = await until(() => t.querySelector(where), `the reason on ${tab}`);
      expect([tab, said.textContent]).toEqual([tab, words]);
      expect([tab, t.querySelectorAll('button[data-testid="preview-way-in"]').length]).toEqual([tab, 0]);
      if (tab !== "feedback") {
        for (const sel of LINKS[tab]) {
          const link = t.querySelector(sel)!;
          expect([tab, sel, link.getAttribute("target"), link.getAttribute("rel")]).toEqual([tab, sel, "_blank", "noopener noreferrer"]);
          expect([tab, sel, await press(link)]).toEqual([tab, sel, true]);
        }
        expect([tab, open.dude, open.browser.length]).toEqual([tab, [], LINKS[tab].length]);
      }
      for (const u of mounted.splice(0)) await u();
    }
  }
});

test("a server commenting is not about keeps its plain link beside one it is", async () => {
  const open = stubOpen();
  const api: RunServer = { ...web(), name: "api", port: 8080, url: "https://api-shop-1.preview.example.test" };
  delete (api as { commenting?: unknown }).commenting;
  const c = await page(new PreviewClient([web(), api]), "servers");
  const link = await until(() => c.querySelector('[data-server="api"] [data-testid="server-preview"]'), "api's Preview");
  expect(await press(link)).toBe(true);
  expect([open.dude, open.browser]).toEqual([[], ["https://api-shop-1.preview.example.test/"]]);
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

test("the Feedback tab keeps its way in", async () => {
  const f = await page(new PreviewClient([web()]), "feedback");
  const wayIn = await until(() => f.querySelector('[data-testid="preview-way-in"]'), "the Feedback tab's way in");
  expect(wayIn.textContent).toContain("Comment on the branch preview");
});

test("a preview card's Page link opens that page, not the server's root, in the task's window", async () => {
  const client = new PreviewClient([web()]);
  client.feedbackItems.push(previewDraft("fbk_p", `${WEB}/cart?step=2`));
  const open = stubOpen();
  const c = await page(client, "feedback");
  const link = await until(() => c.querySelector('[data-id="fbk_p"] [data-testid="fb-preview-page"]'), "the Page link");
  expect(link.getAttribute("href")).toBe(`${WEB}/cart?step=2`);
  expect(await press(link)).toBe(false);
  expect([open.dude, open.browser]).toEqual([[[`${WEB}/cart?step=2`, `dude-preview-${TASK_ID}`]], []]);
  // A modified click is the browser's.
  expect(await press(link, { metaKey: true })).toBe(true);
  expect([open.dude.length, open.browser]).toEqual([1, [`${WEB}/cart?step=2`]]);
});

test("a card's Page link on a page commenting can't reach stays a plain link", async () => {
  const cases: Array<[string, RunServer[], string]> = [
    ["commenting off", [web({ commenting: { available: false, reason: "public_url" } })], `${WEB}/cart`],
    ["an earlier preview's hostname", [web()], "https://web-old-1.preview.example.test/cart"],
    ["a server not ready yet", [web({ state: "starting", serverState: "starting" })], `${WEB}/cart`],
  ];
  for (const [what, servers, url] of cases) {
    const open = stubOpen();
    const client = new PreviewClient(servers);
    client.feedbackItems.push(previewDraft("fbk_p", url));
    const c = await page(client, "feedback");
    const link = await until(() => c.querySelector('[data-id="fbk_p"] [data-testid="fb-preview-page"]'), `the Page link (${what})`);
    expect([what, await press(link)]).toEqual([what, true]);
    expect([what, open.dude, open.browser]).toEqual([what, [], [url]]);
    for (const u of mounted.splice(0)) await u();
  }
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

test("the open preview hears dude's resolved theme in its session, and again when the person switches it", async () => {
  localStorage.setItem("dude.theme", "dark");
  const client = new PreviewClient([web()]);
  const popup = { postMessage: mock((_m: unknown, _o: string) => {}), closed: false, focus() {} };
  (window as unknown as { open: unknown }).open = () => popup;
  let setPreference: ((p: "light" | "dark") => void) | null = null;
  function Switch() {
    setPreference = useTheme().setPreference;
    return null;
  }
  const { container: c, unmount } = await mount(
    <ThemeProvider target={document.createElement("div")}><Switch /><ToastProvider><TooltipProvider><PeopleProvider client={client}>
      <TaskScreen client={client} taskId={TASK_ID} onOpenRun={() => {}} onBack={() => {}} tab="servers" />
    </PeopleProvider></TooltipProvider></ToastProvider></ThemeProvider>,
  );
  mounted.push(async () => { await unmount(); localStorage.removeItem("dude.theme"); });
  await press(await until(() => c.querySelector('[data-server="web"] [data-testid="server-preview"]'), "Preview web"));
  await act(async () => {
    window.dispatchEvent(new window.MessageEvent("message", { data: { type: "hello", v: 1, url: `${WEB}/` }, origin: WEB, source: popup as unknown as MessageEventSource }));
  });
  const themes = () => popup.postMessage.mock.calls.map((x) => x[0] as { type: string; theme?: string }).filter((m) => m.type === "session").map((m) => m.theme);
  await until(() => themes().length > 0 || null, "the first session");
  expect(themes().at(-1)).toBe("dark");
  await act(async () => setPreference!("light"));
  await until(() => themes().at(-1) === "light" || null, "a session in the light theme");
});

function previewDraft(id: string, url: string): FeedbackItem {
  const now = new Date().toISOString();
  return { id, taskId: TASK_ID, attempt: 1, batchId: null, author: { id: YOU, name: PEOPLE[0]!.name }, body: "Green",
    anchor: { kind: "preview", url, selector: "button.pay", viewport: { width: 1280, height: 720 } }, status: "draft", deliveredRunId: null,
    resolvedCommit: null, resolutionPending: false, outdated: false, routedTo: null, submittedAt: null, createdAt: now, updatedAt: now, history: [] };
}
