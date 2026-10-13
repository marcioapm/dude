/**
 * The task page's tabs: which one a URL opens, and what the Servers tab
 * says of the servers. Mounted in happy-dom against the fixture client.
 */

import { afterEach, describe, expect, test } from "bun:test";
import { renderToStaticMarkup } from "react-dom/server";
import type { PersistedEvent, Run } from "@dude/domain";
import { act, click, mount, settle, until } from "./dom.ts";
import { FixtureClient } from "../src/fixtures/client.ts";
import { TASK_ID } from "../src/fixtures/data.ts";
import { PeopleProvider } from "../src/people.tsx";
import { TaskScreen, activityLines } from "../src/screens/TaskScreen.tsx";

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

describe("the task header's owner", () => {
  test("says once whose task it is and whose agent works, and the select beside it is named for changing it", async () => {
    const page = await taskPage(new FixtureClient("a"));
    const header = await until(() => page.querySelector<HTMLElement>("[data-testid=task-header]"), "the header");
    const owner = await until(() => header.querySelector<HTMLElement>("[data-testid=task-owner]"), "the owner field");
    // The attribution: the owner's name and that their implementer is working.
    expect(header.textContent).toContain("Márcio Martins");
    expect(header.textContent).toContain("Owner · Márcio's implementer is working");
    // "Owner" labels the attribution only; the select is the action.
    expect(header.textContent!.split("Owner")).toHaveLength(2);
    const select = owner.querySelector<HTMLElement>("[role=combobox]")!;
    expect(select.getAttribute("aria-label")).toBe("Change owner");
    expect(owner.textContent).toContain("Change owner");
    expect(owner.textContent).toContain("Márcio Martins");
  });
});

describe("Activity's started lines", () => {
  const run = (id: string, phase: string, role: string, category: string | null = null) => ({ id, phase, role, category, kind: "agent", attempt: 1 }) as unknown as Run;
  let cursor = 0;
  const created = (runId: string | null, payload: Record<string, unknown>): PersistedEvent => ({
    eventId: `evt_c${++cursor}`, cursor, eventType: "run.created", occurredAt: new Date(Date.UTC(2026, 9, 3, 10, 0, cursor)).toISOString(),
    organizationId: "org", projectId: "prj", taskId: TASK_ID, runId, sessionId: null, workflowRunId: null,
    actor: { type: "system", id: "workflow" }, source: "control-plane", correlationId: null, causationId: null, payload,
  }) as PersistedEvent;
  const said = (events: PersistedEvent[], runs: Run[]) => {
    const people = { names: new Map<string, string>(), byId: new Map(), you: null } as unknown as Parameters<typeof activityLines>[1];
    return activityLines(events, people, runs, [], 1).map((l) => renderToStaticMarkup(<>{l.text}</>).replace(/<[^>]+>/g, ""));
  };

  test("name who started, as the role does, never the stage or a bare agent", () => {
    const runs = [run("r_impl", "implement", "implementer"), run("r_rev", "review", "reviewer", "correctness"),
      run("r_fix", "fix", "implementer"), run("r_simp", "simplify", "simplifier")];
    const lines = said([
      created("r_impl", { phase: "implement", role: "implementer" }),
      created("r_rev", { phase: "review", role: "reviewer", category: "correctness" }),
      // The round's own event: no Run, its reviewers named in the payload.
      created(null, { phase: "review", reviewers: ["correctness", "security"], iteration: 0 }),
      created("r_fix", { phase: "fix", role: "implementer" }),
      created("r_simp", { phase: "simplify", role: "simplifier" }),
    ], runs);
    // Newest first, as Activity reads.
    expect(lines).toEqual([
      "The simplifier started",
      "The fixer started",
      "A review round started: correctness, security",
      "The correctness reviewer started",
      "The implementer started",
    ]);
    expect(lines.some((l) => l.includes("The agent started") || l.includes("The simplify"))).toBe(false);
  });

  test("a Run the page has not read yet is named from its event", () => {
    expect(said([created("r_later", { phase: "simplify", role: "simplifier" })], [])).toEqual(["The simplifier started"]);
  });

  test("the API reviewer keeps its capitals, in its own line and in the round's; other categories stay lower case", () => {
    const lines = said([
      created("r_api", { phase: "review", role: "reviewer", category: "api" }),
      created(null, { phase: "review", reviewers: ["correctness", "api"], iteration: 0 }),
      created("r_api_unread", { phase: "review", role: "reviewer", category: "api" }),
    ], [run("r_api", "review", "reviewer", "api")]);
    expect(lines).toEqual([
      "The API reviewer started",
      "A review round started: correctness, API",
      "The API reviewer started",
    ]);
  });
});

describe("a finding's category", () => {
  test("is its reviewer's flavour, with the reviewer's own word beside it when it has one", async () => {
    const page = await taskPage(new FixtureClient("d"), { tab: "findings" });
    const rows = await until(() => {
      const r = [...page.querySelectorAll<HTMLElement>("[data-testid=finding]")];
      return r.length === 4 ? r : null;
    }, "the findings");
    const text = rows.map((r) => r.textContent ?? "");
    expect(text.filter((t) => t.includes("correctness · css"))).toHaveLength(1);
    expect(text.filter((t) => t.includes("correctness") && !t.includes("·"))).toHaveLength(3);
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
  for (const since of ["present", "null", "absent"] as const) {
    test(`stage timer uses previewStageSince ${since}`, async () => {
      const timestamp = { present: new Date(Date.now() - 24000).toISOString(), null: null, absent: undefined }[since];
      class TimedPreview extends FixtureClient {
        override taskServers() {
          return super.taskServers().then((d) => ({ ...d, run: d.run ? {
            ...d.run, startedAt: new Date(Date.now() - 34 * 60000).toISOString(),
            previewStage: "volumes" as const, ...(timestamp === undefined ? {} : { previewStageSince: timestamp }),
          } : null }));
        }
      }
      const page = await taskPage(new TimedPreview("e"), { tab: "servers" });
      const progress = await until(() => page.querySelector<HTMLElement>('[aria-label="Preview progress"]'), "progress");
      expect(progress.textContent).toContain("Restoring its workspace");
      expect(progress.textContent).not.toContain("Cloning");
      const active = progress.querySelector('[aria-current="step"]')!;
      expect(active.textContent).not.toContain("34m");
      expect(active.textContent?.includes("·")).toBe(Boolean(timestamp));
      // The timer counts from previewStageSince (24s ago), not from now or startedAt.
      if (timestamp) expect(active.textContent).toMatch(/\b2[4-6]s\b/);
    });
  }
  test("a stopping preview says Stopping, not starting, on its run and its tab", async () => {
    class StoppingPreview extends FixtureClient {
      override taskServers() {
        return super.taskServers().then((d) => ({ ...d, run: d.run ? {
          ...d.run, previewStage: "stopping" as const, previewStageSince: new Date().toISOString(), luxState: "stopping",
        } : null }));
      }
    }
    const page = await taskPage(new StoppingPreview("e"), { tab: "servers" });
    const progress = await until(() => page.querySelector<HTMLElement>('[aria-label="Preview progress"]'), "progress");
    expect(progress.getAttribute("data-stage")).toBe("stopping");
    const panel = page.querySelector<HTMLElement>("[data-testid=servers-panel]")!;
    const mark = await until(() => panel.querySelector<HTMLElement>("[data-status]"), "the run's mark");
    expect(mark.getAttribute("data-status")).not.toBe("starting");
    expect(mark.textContent).toBe("Stopping");
    // The tab reads the same servers: nothing on, and no "Preview starting" dot.
    const tab = [...page.querySelectorAll<HTMLElement>("[role=tab]")].find((x) => x.textContent?.startsWith("Servers"))!;
    await act(async () => tab.focus());
    const tip = await until(() => document.querySelector("[role=tooltip]"), "the tab's tooltip");
    expect(tip.textContent).toContain("No servers on");
    // toBeNull passes on a happy-dom element: compare instead.
    expect(tab.querySelector("[data-server-state]") === null).toBe(true);
  });
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

  test("a wakeable preview asleep: says so, how long until it sleeps, and offers no Add server", async () => {
    class AsleepPreview extends FixtureClient {
      override taskServers() {
        return super.taskServers().then((d) => ({
          ...d,
          run: d.run ? { ...d.run, state: "paused" as const, wakeable: true, asleep: true, previewStage: null, parksAfterMinutes: 30 } : null,
        }));
      }
    }
    const page = await taskPage(new AsleepPreview("e"), { tab: "servers" });
    const panel = await until(() => page.querySelector<HTMLElement>("[data-testid=servers-panel][data-run=preview]"), "the preview's panel");
    await until(() => (panel.textContent?.includes("· asleep") ? true : null), "the asleep detail");
    expect(panel.textContent).toContain("sleeps after 30m without a request");
    expect(panel.textContent).not.toContain("parks after");
    expect(panel.textContent).toContain("opening one wakes it on the branch’s latest commit");
    expect(button(page, "add-server")?.disabled).toBe(true);
    expect(page.querySelector("[data-testid=preview-error]")).toBe(null);
  });

  test("a wakeable preview whose Run failed to start says why", async () => {
    const why = "the preview's Run failed to start (start-failed) 3 times in a row; starting a server, or opening a URL once lux stops waiting, tries a new Run";
    class FailedPreview extends FixtureClient {
      override taskServers() {
        return super.taskServers().then((d) => ({
          ...d,
          run: d.run ? { ...d.run, state: "paused" as const, wakeable: true, asleep: true, previewStage: null, error: why } : null,
        }));
      }
    }
    const page = await taskPage(new FailedPreview("e"), { tab: "servers" });
    const callout = await until(() => page.querySelector<HTMLElement>("[data-testid=preview-error]"), "the preview's error");
    expect(callout.textContent).toBe(why);
  });

  test("an old-style preview's error is not shown as a wakeable preview's callout", async () => {
    class OldStyle extends FixtureClient {
      override taskServers() {
        return super.taskServers().then((d) => ({
          ...d,
          run: d.run ? { ...d.run, wakeable: false, asleep: false, error: "the preview stopped: failed" } : null,
        }));
      }
    }
    const page = await taskPage(new OldStyle("e"), { tab: "servers" });
    await until(() => page.querySelector("[data-testid=servers-panel][data-run=preview]"), "the preview's panel");
    expect(page.querySelector("[data-testid=preview-error]")).toBe(null);
  });

  test("a wakeable preview's error goes once it recovers", async () => {
    let error: string | null = "the preview's Run failed to start (start-failed); trying a new Run (start 2 of 3)";
    class Recovering extends FixtureClient {
      override taskServers() {
        return super.taskServers().then((d) => ({
          ...d,
          run: d.run ? { ...d.run, wakeable: true, asleep: false, error } : null,
        }));
      }
    }
    const page = await taskPage(new Recovering("e"), { tab: "servers" });
    await until(() => page.querySelector("[data-testid=preview-error]"), "the preview's error");
    error = null;
    const { emitForTest } = await import("./dom.ts");
    await emitForTest("servers.changed");
    await until(() => (page.querySelector("[data-testid=preview-error]") === null ? true : null), "the error gone after recovery");
  });
});
