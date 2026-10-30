/**
 * The project's Servers page, against the fixture client (the v4 mockup's
 * world): every state it draws — loading, the list in its two sections,
 * search and its no match, nothing running, a failed read that still lists
 * this tab's servers — and the board's way in.
 */

import { afterEach, beforeEach, expect, mock, test } from "bun:test";
import { ApiError, type ApiClient } from "../src/api/client.ts";
import { FixtureClient } from "../src/fixtures/client.ts";
import { SERVERS_KEY } from "../src/fixtures/scenario.ts";
import { arrange, matches, ProjectServersScreen } from "../src/screens/ProjectServersScreen.tsx";
import { markServerLanding, resetTabWindows, serverLanding } from "../src/serverWindows.ts";
import { runningRuns } from "../src/fixtures/projectServers.ts";
import { act, click, mount, type, until } from "./dom.ts";

const unmounts: Array<() => Promise<void>> = [];
beforeEach(() => {
  localStorage.removeItem(SERVERS_KEY);
  sessionStorage.clear();
  resetTabWindows();
});
afterEach(async () => {
  for (const u of unmounts.splice(0)) await u();
  localStorage.removeItem(SERVERS_KEY);
  resetTabWindows();
});

async function page(client: ApiClient = new FixtureClient("a")) {
  const opened: string[] = [];
  const { container, unmount } = await mount(<ProjectServersScreen client={client} projectId="p_webconsole" projectName="web-console"
    onBoard={() => opened.push("board")} onTaskFeedback={(id) => opened.push(`feedback:${id}`)} />);
  unmounts.push(unmount);
  return { container, opened };
}

const tid = (root: ParentNode, id: string) => root.querySelector<HTMLElement>(`[data-testid="${id}"]`);
const count = (root: ParentNode, sel: string) => root.querySelectorAll(sel).length;
const keys = (root: ParentNode) => [...root.querySelectorAll<HTMLElement>('[data-testid="project-server"]')].map((r) => r.dataset.key);

test("loading says it is reading; the list shows every Run's servers, grouped, everyone's, with no Start or Stop", async () => {
  const { container } = await page();
  expect(tid(container, "servers-loading")?.textContent).toContain("Reading the project's servers");
  await until(() => tid(container, "servers-others"), "the list");
  expect(count(container, '[data-testid="servers-loading"]')).toBe(0);
  expect(count(container, '[data-testid="server-group"]')).toBe(4);
  expect(keys(container)).toEqual(["run_pv_checkout/web", "run_pv_checkout/admin", "run_web142/web", "run_pv_returns/web", "run_web151/web", "run_web151/storybook"]);
  // Nothing connected to this tab yet: one section, called Running.
  expect(count(container, '[data-testid="servers-connected"]')).toBe(0);
  expect(tid(container, "servers-others")!.querySelector("h2")!.textContent).toBe("Running");
  expect(container.textContent).toContain("6 running on 4 runs");
  expect(count(container, '[data-testid="server-start"], [data-testid="server-stop"], [data-testid="server-logs"]')).toBe(0);
  const heads = [...container.querySelectorAll('[data-testid="server-group"] header')].map((h) => h.textContent);
  expect(heads[0]).toContain("Branch preview");
  expect(heads[0]).toContain("feat/checkout-polish");
  expect(heads[1]).toContain("WEB-142");
  expect(heads[1]).toContain("Streaming Markdown");
  // Feedback counts on each row, and "N people commenting" only past one person.
  const web = container.querySelector('[data-key="run_pv_checkout/web"]')!;
  expect(tid(web, "server-fold-toggle")!.textContent).toBe("2 open · 1 resolved");
  expect(tid(web, "commenting-count")!.textContent).toContain("2 people commenting");
  expect(count(container.querySelector('[data-key="run_pv_checkout/admin"]')!, '[data-testid="commenting-count"]')).toBe(0);
});

test("search matches a server's name, branch, task key and title and its starter; no match says so and Clear search brings them back", async () => {
  const { container } = await page();
  await until(() => tid(container, "servers-others"), "the list");
  const field = tid(container, "servers-search")!;
  await type(field, "checkout");
  expect(keys(container)).toEqual(["run_pv_checkout/web", "run_pv_checkout/admin"]);
  await type(field, "storybook");
  expect(keys(container)).toEqual(["run_web151/storybook"]);
  await type(field, "web-142");
  expect(keys(container)).toEqual(["run_web142/web"]);
  await type(field, "Lin");
  expect(keys(container)).toEqual(["run_web142/web", "run_web151/web", "run_web151/storybook"]);
  await type(field, "billing");
  expect(tid(container, "servers-nomatch")!.textContent).toContain("No server matches “billing”");
  await click([...tid(container, "servers-nomatch")!.querySelectorAll("button")].find((b) => b.textContent === "Clear search")!);
  expect(keys(container).length).toBe(6);
});

test("All, Branch previews and Tasks filter the Runs", async () => {
  const { container } = await page();
  await until(() => tid(container, "servers-others"), "the list");
  const by = (label: string) => [...container.querySelectorAll<HTMLButtonElement>('[aria-label="Which"] button')].find((b) => b.textContent === label)!;
  await click(by("Tasks"));
  expect(keys(container)).toEqual(["run_web142/web", "run_web151/web", "run_web151/storybook"]);
  await click(by("Branch previews"));
  expect(keys(container)).toEqual(["run_pv_checkout/web", "run_pv_checkout/admin", "run_pv_returns/web"]);
  await click(by("All"));
  expect(keys(container).length).toBe(6);
});

test("`/` outside a field focuses the search", async () => {
  const { container } = await page();
  await until(() => tid(container, "servers-others"), "the list");
  await act(async () => {
    window.dispatchEvent(new KeyboardEvent("keydown", { key: "/" }));
  });
  expect(document.activeElement).toBe(tid(container, "servers-search"));
});

test("nothing running says where servers are started, with no Start here", async () => {
  localStorage.setItem(SERVERS_KEY, "none");
  const { container } = await page();
  const none = await until(() => tid(container, "servers-none"), "nothing running");
  expect(none.textContent).toContain("Nothing is running in web-console");
  expect(none.textContent).toContain("Start them from a task's Servers tab");
  expect(container.textContent).toContain("nothing running");
  expect([...container.querySelectorAll("button")].some((b) => /^Start/.test(b.textContent ?? ""))).toBe(false);
});

test("a failed read says so with Try again, never as an empty project, and Try again reads again", async () => {
  localStorage.setItem(SERVERS_KEY, "failed");
  const client = new FixtureClient("a");
  const { container } = await page(client);
  const failed = await until(() => tid(container, "servers-failed"), "the failure");
  expect(failed.textContent).toContain("Couldn't read the project's servers");
  expect(count(container, '[data-testid="servers-none"]')).toBe(0);
  expect(count(container, '[data-testid="servers-nomatch"]')).toBe(0);
  localStorage.removeItem(SERVERS_KEY);
  await click([...failed.querySelectorAll("button")].find((b) => b.textContent?.includes("Try again"))!);
  await until(() => tid(container, "servers-others"), "the list again");
  expect(count(container, '[data-testid="servers-failed"]')).toBe(0);
});

test("a read failing after servers are connected to this tab keeps listing them, and only them", async () => {
  class Flaky extends FixtureClient {
    fail = false;
    override async runningServers(id: string) {
      if (this.fail) throw new ApiError(503, "unavailable", "down");
      return super.runningServers(id);
    }
  }
  const client = new Flaky("a");
  const opened: unknown[] = [];
  const own = window.open;
  (window as { open: unknown }).open = (url: string) => {
    const w = { postMessage() {}, closed: false, focus() {}, get location(): Location { if (url) throw new DOMException("x", "SecurityError"); return { href: "about:blank" } as Location; } };
    opened.push(w);
    return w;
  };
  try {
    const { container } = await page(client);
    await until(() => tid(container, "servers-others"), "the list");
    const row = container.querySelector('[data-key="run_pv_checkout/web"]')!;
    await click(tid(row, "server-preview")!);
    expect(opened.length).toBeGreaterThan(0);
    await until(() => tid(container, "servers-connected"), "connected section");
    expect(tid(container, "servers-connected")!.querySelector("h2")!.textContent).toBe("Connected to this tab");
    expect(tid(container, "servers-others")!.querySelector("h2")!.textContent).toBe("Everything else running");
    expect(tid(container.querySelector('[data-key="run_pv_checkout/web"]')!, "link-here")!.textContent).toBe("Connected · this tab");
    // The page is left and opened again: the read fails, the connected one still lists.
    for (const u of unmounts.splice(0)) await u();
    client.fail = true;
    const again = await page(client);
    await until(() => tid(again.container, "servers-failed"), "the failure");
    expect(keys(again.container)).toEqual(["run_pv_checkout/web"]);
    expect(count(again.container, '[data-testid="servers-others"]')).toBe(0);
  } finally {
    (window as { open: unknown }).open = own;
  }
});

test("arrange puts this tab's servers first, the landed one first of all, and search looks at every word it names", () => {
  const runs = runningRuns();
  const out = arrange(runs, new Set(["run_pv_returns/web", "run_web151/storybook"]), "", "all", "run_pv_returns/web");
  expect(arrange(runs, new Set(["run_pv_checkout/web", "run_web151/storybook"]), "", "all", "run_web151/storybook")
    .connected.map((g) => g.run.id)).toEqual(["run_web151", "run_pv_checkout"]);
  expect(out.connected.map((g) => g.run.id)).toEqual(["run_pv_returns", "run_web151"]);
  expect(out.connected[1]!.servers.map((s) => s.name)).toEqual(["storybook", "web"]);
  expect(out.others.map((g) => g.run.id)).toEqual(["run_pv_checkout", "run_web142"]);
  const r = runs[1]!;
  for (const q of ["web", "WEB-142", "fences", "lin", "streaming-markdown"]) expect(matches(r, r.servers[0]!, q)).toBe(true);
  expect(matches(r, r.servers[0]!, "nope")).toBe(false);
});

test("arriving from a branch preview's Connect to dude: its server is connected to this tab, first, highlighted, with the success callout", async () => {
  const RETURNS = "https://web-pv-3m8q.preview.acme.dev";
  const own = Object.getOwnPropertyDescriptor(window, "opener");
  const preview = { postMessage: mock((_msg: unknown, _origin?: string) => {}), closed: false, focus() {} };
  Object.defineProperty(window, "opener", { configurable: true, get: () => preview });
  try {
    markServerLanding(window, { projectId: "p_webconsole", runId: "run_pv_returns", server: "web", branch: "docs/returns-page", origin: RETURNS });
    const landing = serverLanding("p_webconsole")!;
    const { container, unmount } = await mount(<ProjectServersScreen client={new FixtureClient("a")} projectId="p_webconsole" projectName="web-console"
      landing={landing} onBoard={() => {}} onTaskFeedback={() => {}} />);
    unmounts.push(unmount);
    await until(() => tid(container, "servers-others"), "the list");
    // Its opener says hello, as the overlay does every two seconds; the dude tab checks the server and answers.
    await act(async () => {
      window.dispatchEvent(new window.MessageEvent("message", { data: { type: "hello", v: 1, url: `${RETURNS}/returns` }, origin: RETURNS,
        source: preview as unknown as MessageEventSource }));
    });
    const landed = await until(() => tid(container, "servers-landed"), "the success callout");
    expect(landed.textContent).toBe("docs/returns-page / web is connected to this tab. Keep it open: its comments go through here. Other servers you open from this page join it.");
    expect(keys(tid(container, "servers-connected")!)[0]).toBe("run_pv_returns/web");
    const row = container.querySelector('[data-key="run_pv_returns/web"]')!;
    expect(row.className).toContain("serverLanded");
    expect(tid(row, "link-here")!.textContent).toBe("Connected just now");
    expect(preview.postMessage.mock.calls.map((c) => (c[0] as { type: string; mode?: string }))).toContainEqual(expect.objectContaining({ type: "session", mode: "branch" }));
  } finally {
    if (own) Object.defineProperty(window, "opener", own);
    else delete (window as { opener?: unknown }).opener;
  }
});

test("Preview on a server whose window another of my dude tabs holds moves it here and says so", async () => {
  const ADMIN = "https://admin-pv-7h2k.preview.acme.dev";
  const held = { postMessage: mock((_msg: unknown, _origin?: string) => {}), closed: false, focus() {}, get location(): Location { throw new DOMException("x", "SecurityError"); } };
  const own = window.open;
  (window as { open: unknown }).open = () => held;
  try {
    const { container } = await page();
    await until(() => tid(container, "servers-others"), "the list");
    await click(tid(container.querySelector('[data-key="run_pv_checkout/admin"]')!, "server-preview")!);
    // The window was found by name, already showing the preview: it is claimed, not navigated.
    expect(held.postMessage.mock.calls[0]![0]).toEqual({ type: "claim", v: 1 });
    expect(held.postMessage.mock.calls[0]![1]).toBe(ADMIN);
    // The overlay told the other tab it left, and says hello here as moved.
    await act(async () => {
      window.dispatchEvent(new window.MessageEvent("message", { data: { type: "hello", v: 1, url: `${ADMIN}/orders`, moved: true }, origin: ADMIN,
        source: held as unknown as MessageEventSource }));
    });
    const moved = await until(() => tid(container, "servers-moved"), "the moved callout");
    expect(moved.textContent).toBe("Your feat/checkout-polish / admin window was connected to another of your dude tabs. It's connected to this one now.");
    expect(keys(tid(container, "servers-connected")!)[0]).toBe("run_pv_checkout/admin");
  } finally {
    (window as { open: unknown }).open = own;
  }
});
