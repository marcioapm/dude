/**
 * The task page's tabs: which one a URL opens, and what the Servers tab
 * says of the servers. Mounted in happy-dom against the fixture client.
 */

import { afterEach, describe, expect, test } from "bun:test";
import { mount, until } from "./dom.ts";
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
