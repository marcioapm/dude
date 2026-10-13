/**
 * The task page's tabs: which one a URL opens, and what the Servers tab
 * says of the servers. Mounted in happy-dom against the fixture client.
 */

import { afterEach, describe, expect, test } from "bun:test";
import { act, click, mount, settle, until } from "./dom.ts";
import { FixtureClient } from "../src/fixtures/client.ts";
import { TASK_ID } from "../src/fixtures/data.ts";
import { RUN_KEY } from "../src/fixtures/scenario.ts";
import { PeopleProvider } from "../src/people.tsx";
import { ResetMachineConfirm } from "../src/screens/actions.tsx";
import { ApiError } from "../src/api/client.ts";
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

describe("machine reset confirmation", () => {
  test("keeps the note on exact refusal, prevents duplicates and closes only after success", async () => {
    let closed = 0;
    const notes: string[] = [];
    let resolve: (() => void) | undefined;
    const m = await mount(<ResetMachineConfirm label="implementer" from="Large" to="Standard" by="Márcio"
      onClose={() => closed++} onConfirm={async (note) => {
        notes.push(note);
        if (notes.length === 1) throw new ApiError(409, "moving", "The implementer is being moved to Large now.");
        await new Promise<void>((r) => { resolve = r; });
      }} />);
    mounted.push(m.unmount);
    const field = document.querySelector<HTMLTextAreaElement>("[data-testid=reset-note]")!;
    expect(field.maxLength).toBe(2000);
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(field, "Keep two workers.");
      field.dispatchEvent(new Event("input", { bubbles: true }));
    });
    const button = document.querySelector<HTMLButtonElement>("[data-testid=confirm-reset]")!;
    await click(button);
    expect(document.querySelector("[data-testid=reset-error]")?.textContent).toBe("The implementer is being moved to Large now.");
    expect(field.value).toBe("Keep two workers.");
    expect(closed).toBe(0);
    await click(button);
    expect(button.disabled).toBe(true);
    await click(button);
    expect(notes).toEqual(["Keep two workers.", "Keep two workers."]);
    await act(async () => resolve!());
    expect(closed).toBe(1);
  });
});

describe("machines raised in the task, on its Overview", () => {
  afterEach(() => localStorage.removeItem(RUN_KEY));

  test("the implementer's raise with its provenance and Reset, the fixer following it without one", async () => {
    localStorage.setItem(RUN_KEY, "raised");
    const page = await taskPage(new FixtureClient("a"));
    const panel = await until(() => page.querySelector<HTMLElement>("[data-testid=machine-raises]"), "the Machines panel");
    const rows = [...panel.querySelectorAll("li")].map((li) => li.textContent ?? "");
    expect(rows).toHaveLength(2);
    expect(rows[0]).toContain("Implementer runs on XL");
    expect(rows[0]).toContain("Asked for by run_01j9x5m2q7k8e4t1 (the implementer)");
    expect(rows[0]).toContain("jest OOM-killed with --maxWorkers 4 on 8 GiB");
    expect(rows[1]).toContain("Fixer runs on XL");
    expect(rows[1]).toContain("Follows the implementer: it names no size of its own");
    const resets = [...panel.querySelectorAll("button")].map((b) => b.getAttribute("aria-label"));
    expect(resets).toEqual(["Reset the implementer to Standard"]);
  });

  test("Reset posts the role and note, and the panel goes once the detail has no raise; a refusal keeps the dialog", async () => {
    localStorage.setItem(RUN_KEY, "raised");
    const client = new FixtureClient("a");
    const page = await taskPage(client);
    const reset = async () => click(await until(() => page.querySelector<HTMLElement>("[aria-label='Reset the implementer to Standard']"), "Reset"));
    const note = async (text: string) => {
      const field = await until(() => document.querySelector<HTMLTextAreaElement>("[data-testid=reset-note]"), "the note");
      await act(async () => {
        Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(field, text);
        field.dispatchEvent(new Event("input", { bubbles: true }));
      });
    };
    await reset();
    expect(document.body.textContent).toContain("Move the implementer back to Standard now?");
    // The fixture refuses a note of "fail" as the API refuses a Reset during a move.
    await note("fail");
    await click(document.querySelector<HTMLElement>("[data-testid=confirm-reset]")!);
    await until(() => document.querySelector("[data-testid=reset-error]"), "the refusal");
    expect(document.querySelector("[data-testid=reset-error]")!.textContent).toBe("a machine move is under way");
    await note("keep two workers");
    await click(document.querySelector<HTMLElement>("[data-testid=confirm-reset]")!);
    await until(() => (page.querySelector("[data-testid=machine-raises]") ? null : true), "the panel gone");
  });
});

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
