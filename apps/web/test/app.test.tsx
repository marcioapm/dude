/**
 * The whole shell, from its URL: `App` mounted at a hash, against the
 * fixture client, so the hash is parsed and routed the way a browser's is.
 */

import { afterEach, describe, expect, test } from "bun:test";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import { click, mount, until } from "./dom.ts";
import { FixtureClient } from "../src/fixtures/client.ts";
import { RUN_ID, TASK_ID } from "../src/fixtures/data.ts";
import { ApiError, type RunDetail } from "../src/api/client.ts";
import { App } from "../src/App.tsx";
import { PeopleProvider } from "../src/people.tsx";

let mounted: Array<() => Promise<void>> = [];
afterEach(async () => {
  for (const unmount of mounted) await unmount();
  mounted = [];
  window.history.replaceState(null, "", " ");
});

async function app(hash: string, client: FixtureClient) {
  window.history.replaceState(null, "", hash);
  const { container, unmount } = await mount(
    <TooltipProvider>
      <ToastProvider>
        <PeopleProvider client={client}>
          <App client={client} onSignOut={() => {}} onKeyRefused={() => {}} />
        </PeopleProvider>
      </ToastProvider>
    </TooltipProvider>,
  );
  mounted.push(unmount);
  return container;
}

const selectedTab = (page: HTMLElement) => page.querySelector("[data-testid=task-screen] [role=tab][aria-selected=true]")?.textContent ?? "";

async function onServers(page: HTMLElement) {
  await until(() => (selectedTab(page).startsWith("Servers") ? true : null), "the Servers tab selected");
  const panel = await until(() => page.querySelector<HTMLElement>("[role=tabpanel][data-state=active]"), "the selected panel");
  await until(() => panel.querySelector("[data-testid=servers-panel]"), "the servers in the selected panel");
}

describe("the shell's routes to a task's servers", () => {
  test("#/task/<id>/servers opens the task on its Servers tab", async () => {
    const page = await app(`#/task/${TASK_ID}/servers`, new FixtureClient("a"));
    await onServers(page);
  });

  test("#/task/<id> opens the task on its overview", async () => {
    const page = await app(`#/task/${TASK_ID}`, new FixtureClient("a"));
    await until(() => (selectedTab(page) === "Overview" ? true : null), "the Overview tab selected");
  });

  test("a standalone preview session's Servers → goes to #/task/<id>/servers", async () => {
    // A preview session whose task the shell could not learn: not in the
    // tree, and the shell's lookup of the Run found nothing. The session
    // itself still reads its Run, and through it its task.
    class Standalone extends FixtureClient {
      private lookups = 0;
      override navigation() {
        return super.navigation().then((n) => ({
          ...n,
          projects: n.projects.map((p) => ({
            ...p,
            tasks: p.tasks?.map((t) => ({ ...t, runs: [] })),
            epics: p.epics?.map((e) => ({ ...e, tasks: e.tasks.map((t) => ({ ...t, runs: [] })) })),
          })),
        }));
      }
      override async getRun(id: string): Promise<RunDetail> {
        if (this.lookups++ === 0) throw new ApiError(404, "not_found", "No such run.");
        return { ...(await super.getRun(id)), kind: "preview", phase: null, role: null };
      }
    }
    const page = await app(`#/session/${RUN_ID}`, new Standalone("e"));
    const button = await until(() => page.querySelector("[data-testid=preview-run-servers]"), "the standalone preview's way to its servers", 80);
    expect(page.querySelector("[data-testid=task-screen]") !== null).toBe(false);
    await click(button);
    expect(window.location.hash).toBe(`#/task/${TASK_ID}/servers`);
    await onServers(page);
  });
});
