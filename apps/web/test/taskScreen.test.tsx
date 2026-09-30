/**
 * The task page's tabs: which one a URL opens, and what the Servers tab
 * says of the servers. Mounted in happy-dom against the fixture client.
 */

import { afterEach, describe, expect, test } from "bun:test";
import { act, click, mount, settle, until } from "./dom.ts";
import { FixtureClient } from "../src/fixtures/client.ts";
import { TASK_ID } from "../src/fixtures/data.ts";
import { PeopleProvider } from "../src/people.tsx";
import { TaskScreen } from "../src/screens/TaskScreen.tsx";

let mounted: Array<() => Promise<void>> = [];
afterEach(async () => {
  for (const unmount of mounted) await unmount();
  mounted = [];
});

export async function taskPage(client: FixtureClient, props: Partial<Parameters<typeof TaskScreen>[0]> = {}) {
  const { container, unmount } = await mount(
    <PeopleProvider client={client}>
      <TaskScreen client={client} taskId={TASK_ID} onOpenRun={() => {}} onBack={() => {}} {...props} />
    </PeopleProvider>,
  );
  mounted.push(unmount);
  await until(() => container.querySelector("[data-testid=task-screen]"), "the task page");
  return container;
}

const selectedTab = (page: HTMLElement) => page.querySelector("[role=tab][aria-selected=true]")?.textContent ?? "";

describe("the task page's tab from its URL", () => {
  test("a URL naming Servers opens the page on its servers", async () => {
    const page = await taskPage(new FixtureClient("a"), { tab: "servers" });
    expect(selectedTab(page)).toStartWith("Servers");
    await until(() => page.querySelector("[data-testid=servers-panel]"), "the servers panel");
  });

  test("a URL naming no tab opens the overview", async () => {
    const page = await taskPage(new FixtureClient("a"));
    expect(selectedTab(page)).toBe("Overview");
  });
});

/** The Servers tab: its count, its mark's state, and what its tooltip says on focus. */
async function serversTab(scenario: "a" | "b" | "c" | "d" | "e") {
  const page = await taskPage(new FixtureClient(scenario));
  const tab = await until(() => {
    const t = [...page.querySelectorAll<HTMLElement>("[role=tab]")].find((x) => x.textContent?.startsWith("Servers"));
    // The servers are read apart from the task: wait for them to say something.
    return t && (t.children.length > 0 || t.getAttribute("aria-describedby") !== null || scenario === "d") ? t : null;
  }, "the Servers tab");
  await settle(50);
  await act(async () => tab.focus());
  const tip = await until(() => document.querySelector("[role=tooltip]"), "the tab's tooltip");
  return {
    label: tab.textContent,
    mark: tab.querySelector("[data-server-state]")?.getAttribute("data-server-state") ?? null,
    tip: tip.textContent,
  };
}

describe("the task's Servers tab", () => {
  test("two on (web ready, api starting): the count, the starting dot, both named", async () => {
    const t = await serversTab("a");
    expect(t.label).toStartWith("Servers2");
    expect(t.mark).toBe("starting");
    expect(t.tip).toContain("2 servers on");
    expect(t.tip).toContain("web");
    expect(t.tip).toContain(":3000");
    expect(t.tip).toContain("api");
    expect(t.tip).toContain("Off: storybook");
  });

  test("one on and one exited: the count of what is on, and the danger dot", async () => {
    const t = await serversTab("c");
    expect(t.label).toStartWith("Servers1");
    expect(t.mark).toBe("exited");
    expect(t.tip).toContain("1 server on");
    expect(t.tip).toContain("Off: api (exited), storybook");
  });

  test("none on after a move: no count, the move's mark", async () => {
    const t = await serversTab("b");
    expect(t.label).not.toMatch(/\d/);
    expect(t.mark).toBe("unreachable");
    expect(t.tip).toContain("No servers on");
    expect(t.tip).toContain("Off: web, api, storybook");
  });

  test("a branch preview coming up: no count yet, the breathing dot", async () => {
    const t = await serversTab("e");
    expect(t.label).not.toMatch(/\d/);
    expect(t.mark).toBe("starting");
    expect(t.tip).toContain("No servers on");
  });

  test("no run: no count, no mark, and the project's servers named as off", async () => {
    const t = await serversTab("d");
    expect(t.label).toBe("Servers");
    expect(t.mark).toBeNull();
    expect(t.tip).toContain("No servers on");
    expect(t.tip).toContain("Off: web, api, storybook");
  });

  test("no run and no recipes: nothing to say, so no tooltip", async () => {
    class Bare extends FixtureClient {
      override taskServers() {
        return super.taskServers().then((d) => ({ ...d, run: null, servers: [], recipes: [] }));
      }
    }
    const page = await taskPage(new Bare("d"));
    const tab = await until(() => [...page.querySelectorAll<HTMLElement>("[role=tab]")].find((x) => x.textContent === "Servers"), "the Servers tab");
    await settle(100);
    await act(async () => tab.focus());
    await settle(50);
    expect(document.querySelector("[role=tooltip]") !== null).toBe(false);
  });
});

describe("a branch preview on the Servers tab", () => {
  /** Scenario e's preview, past its stages: ready, lux running it. */
  class ReadyPreview extends FixtureClient {
    override taskServers() {
      return super.taskServers().then((d) => ({ ...d, run: d.run ? { ...d.run, previewStage: "ready" as const, luxState: "running" } : null }));
    }
  }
  const states = (page: HTMLElement) =>
    Object.fromEntries([...page.querySelectorAll("[data-testid=servers-panel] [data-server][data-state]")].map((el) => [el.getAttribute("data-server"), el.getAttribute("data-state")]));
  // Every server of the three in one of `want`: an empty panel is not a pass.
  const all = (page: HTMLElement, ...want: string[]) => {
    const s = Object.values(states(page));
    return s.length === 3 && s.every((x) => want.includes(x!)) ? true : null;
  };
  const button = (page: HTMLElement, id: string) => page.querySelector<HTMLButtonElement>(`[data-testid=${id}]`);

  test("offers Start all and Stop all beside Stop preview, and they leave the preview live", async () => {
    const page = await taskPage(new ReadyPreview("e"), { tab: "servers" });
    await until(() => page.querySelector("[data-testid=servers-panel][data-run=preview]"), "the preview's panel");
    expect(button(page, "stop-preview") !== null).toBe(true);
    const startAll = await until(() => button(page, "start-all"), "Start all on the preview");
    expect(button(page, "stop-all") !== null).toBe(true);

    await click(startAll);
    await until(() => all(page, "starting", "ready"), "every server starting");
    await until(() => (button(page, "stop-all")?.disabled === false ? true : null), "Stop all ready to press");
    await click(button(page, "stop-all")!);
    await until(() => all(page, "stopped"), "every server stopped");
    // Stop all is not Stop preview: the preview run is still there, still live.
    expect(page.querySelector("[data-testid=servers-panel][data-run=preview]") !== null).toBe(true);
    expect(button(page, "stop-preview") !== null).toBe(true);
    expect(button(page, "add-server")?.disabled).toBe(false);

    await until(() => (button(page, "start-all")?.disabled === false ? true : null), "Start all ready to press");
    await click(button(page, "start-all")!);
    await until(() => all(page, "starting", "ready"), "every server started again");
  });
});
