/**
 * The session screen: what its bar offers and what it says for a branch
 * preview. Mounted in happy-dom against the fixture client, so it reads
 * the Run and its events as it would from the API.
 */

import { afterEach, describe, expect, test } from "bun:test";
import { click, mount, until } from "./dom.ts";
import { FixtureClient } from "../src/fixtures/client.ts";
import { RUN_ID } from "../src/fixtures/data.ts";
import { RunScreen } from "../src/screens/RunScreen.tsx";
import type { RunDetail } from "../src/api/client.ts";

let mounted: Array<() => Promise<void>> = [];
afterEach(async () => {
  for (const unmount of mounted) await unmount();
  mounted = [];
  localStorage.clear();
});

/** The fixture world with the Run patched: its status, its kind. */
class RunClient extends FixtureClient {
  constructor(private readonly patch: Partial<RunDetail>, scenario: "a" | "e" = "a") {
    super(scenario);
  }
  override async getRun(id: string): Promise<RunDetail> {
    return { ...(await super.getRun(id)), ...this.patch };
  }
}

async function session(props: Partial<Parameters<typeof RunScreen>[0]> & { client: FixtureClient }) {
  const { container, unmount } = await mount(<RunScreen runId={RUN_ID} onBack={() => {}} {...props} />);
  mounted.push(unmount);
  await until(() => container.querySelector("[data-testid=run-screen]"), "the session");
  return container;
}

describe("the session's bar", () => {
  test("is Conversation, Changes and Events: no Servers toggle, no drawer", async () => {
    // A browser that had the drawer open keeps no way back to it.
    localStorage.setItem("dude.run.servers", "1");
    const page = await session({ client: new RunClient({}) });
    const bar = page.querySelector("[data-testid=session-view]")!;
    const options = [...bar.querySelectorAll("button, [role=radio]")].map((b) => b.textContent?.replace(/\d+/g, "").trim());
    expect(options).toEqual(["Conversation", "Changes", "Events"]);
    expect(page.textContent).not.toContain("Servers");
    expect(page.querySelector("[aria-label=Servers]") !== null).toBe(false);
  });
});

describe("the session's machine", () => {
  test("the header names the size the Run recorded, with its spec", async () => {
    const page = await session({ client: new RunClient({}) });
    const chip = await until(() => page.querySelector("[data-testid=run-machine]"), "the machine chip");
    expect(chip.textContent).toContain("Large");
    expect(chip.textContent).toContain("8 CPUs · 16 GiB · 80 GiB");
  });

  test("a Run from before sizes has no chip", async () => {
    const page = await session({ client: new RunClient({ machine: null }) });
    expect(page.querySelector("[data-testid=run-machine]")).toBeNull();
  });
});

describe("a branch preview's session", () => {
  test("on its task's page, points at the task's Servers tab", async () => {
    let opened = 0;
    const page = await session({ client: new RunClient({ kind: "preview", phase: null, role: null }, "e"), onOpenServers: () => void opened++ });
    const note = await until(() => page.querySelector("[data-testid=preview-run-note]"), "the preview note");
    expect(note.textContent).toContain("Servers tab");
    expect(page.querySelector("textarea") !== null).toBe(false);
    await click(note.querySelector("[data-testid=preview-run-servers]")!);
    expect(opened).toBe(1);
  });

  test("on its own, opens its task on the Servers tab once the task is known", async () => {
    const opened: Array<[string, string | undefined]> = [];
    const page = await session({ client: new RunClient({ kind: "preview", phase: null, role: null }, "e"), onOpenTask: (id, tab) => void opened.push([id, tab]) });
    const button = await until(() => page.querySelector("[data-testid=preview-run-servers]"), "the way to the servers");
    await click(button);
    expect(opened).toEqual([["tsk_01j9x4kqf8b2m7e3", "servers"]]);
  });

  test("with no task to reach, says where its servers are in words", async () => {
    class NoTask extends RunClient {
      override getTask(): Promise<never> {
        return Promise.reject(new Error("unreachable"));
      }
    }
    const page = await session({ client: new NoTask({ kind: "preview", phase: null, role: null }, "e"), onOpenTask: () => {} });
    const note = await until(() => page.querySelector("[data-testid=preview-run-note]"), "the preview note");
    expect(note.textContent).toContain("on its task’s page, under the Servers tab");
    expect(note.querySelector("button") !== null).toBe(false);
  });
});

describe("the terminal", () => {
  const rail = (page: HTMLElement) => page.querySelector("[data-testid=session-rail]")!;

  test("while the run is running, is in the session's rail, opening lux in a new tab", async () => {
    const page = await session({ client: new RunClient({}) });
    const link = await until(() => rail(page).querySelector<HTMLAnchorElement>("[data-testid=terminal-link]"), "the rail's terminal");
    expect(link.textContent).toContain("Open terminal in lux");
    expect(link.getAttribute("href")).toBe("https://lux.example.com/runs/run_k3jq7x2mfa9vbn4z/terminal");
    expect(link.getAttribute("target")).toBe("_blank");
    // The header keeps its icon as the fallback for where the rail is not.
    // Which of the two shows is a container query on the session's width
    // (app.css), which happy-dom does not lay out: browser_terminal.py covers it.
    expect(page.querySelector("[data-testid=terminal-icon]")?.getAttribute("href")).toBe(link.getAttribute("href"));
  });

  for (const status of ["paused", "completed", "aborted"] as const) {
    test(`a run that is ${status} has none, in the rail or the header`, async () => {
      let reads = 0;
      class Counting extends RunClient {
        override runServers() {
          reads++;
          return super.runServers();
        }
      }
      const page = await session({ client: new Counting({ status, endedAt: status === "paused" ? null : new Date().toISOString() }) });
      await until(() => page.querySelector("[data-testid=session-rail]"), "the rail");
      expect(page.querySelector("[data-testid=terminal-link]") !== null).toBe(false);
      expect(page.querySelector("[data-testid=terminal-icon]") !== null).toBe(false);
      // Nothing is read for a link that would not show.
      expect(reads).toBe(0);
    });
  }

  test("a run lux has not taken yet gets it once lux has", async () => {
    let luxHasIt = false;
    class Early extends RunClient {
      override async runServers() {
        const s = await super.runServers();
        return luxHasIt ? s : { ...s, run: s.run ? { ...s.run, terminalUrl: null } : null };
      }
    }
    const page = await session({ client: new Early({}) });
    await until(() => page.querySelector("[data-testid=session-rail]"), "the rail");
    expect(page.querySelector("[data-testid=terminal-link]") !== null).toBe(false);
    luxHasIt = true;
    const { emitForTest } = await import("./dom.ts");
    await emitForTest("servers.changed");
    await until(() => page.querySelector("[data-testid=terminal-link]"), "the terminal once lux has the run");
  });

  /**
   * `runServers` held open until the test lets each answer go, counting the
   * reads started and the most ever in flight at once.
   */
  class Held extends RunClient {
    started = 0;
    active = 0;
    peak = 0;
    private readonly waiting: Array<(withUrl: boolean) => void> = [];
    override async runServers() {
      this.started++;
      this.peak = Math.max(this.peak, ++this.active);
      const withUrl = await new Promise<boolean>((r) => this.waiting.push(r));
      this.active--;
      const s = await super.runServers();
      return withUrl ? s : { ...s, run: s.run ? { ...s.run, terminalUrl: null } : null };
    }
    answer(withUrl: boolean) {
      this.waiting.shift()!(withUrl);
    }
  }
  const burst = async (n: number) => {
    const { emitForTest } = await import("./dom.ts");
    for (let i = 0; i < n; i++) await emitForTest("servers.changed");
  };

  test("a burst of events while the read is out keeps the read's answer, and asks nothing more once it is known", async () => {
    const client = new Held({});
    const page = await session({ client });
    await until(() => (client.started === 1 ? true : null), "the first read");
    await burst(10);
    expect(client.started).toBe(1);
    expect(client.active).toBe(1);
    client.answer(true);
    const link = await until(() => page.querySelector("[data-testid=terminal-link]"), "the terminal from the read that was out");
    expect(link.getAttribute("href")).toBe("https://lux.example.com/runs/run_k3jq7x2mfa9vbn4z/terminal");
    await burst(5);
    expect(client.started).toBe(1);
    expect(client.peak).toBe(1);
  });

  test("a read that finds no terminal yet is followed by one more read for the whole burst", async () => {
    const client = new Held({});
    const page = await session({ client });
    await until(() => (client.started === 1 ? true : null), "the first read");
    await burst(10);
    client.answer(false);
    await until(() => (client.started === 2 ? true : null), "the one follow-up");
    expect(client.active).toBe(1);
    client.answer(true);
    await until(() => page.querySelector("[data-testid=terminal-link]"), "the terminal from the follow-up");
    await burst(3);
    expect(client.started).toBe(2);
    expect(client.peak).toBe(1);
  });

  test("a run that pauses loses it", async () => {
    let status: RunDetail["status"] = "running";
    class Pausing extends FixtureClient {
      override async getRun(id: string): Promise<RunDetail> {
        return { ...(await super.getRun(id)), status };
      }
    }
    const client = new Pausing("a");
    const page = await session({ client });
    await until(() => page.querySelector("[data-testid=terminal-link]"), "the terminal while running");
    status = "paused";
    // A status event makes the page read the Run again.
    const { emitForTest } = await import("./dom.ts");
    await emitForTest("run.paused");
    await until(() => (page.querySelector("[data-testid=terminal-link]") ? null : true), "the terminal gone once paused");
    expect(page.querySelector("[data-testid=terminal-icon]") !== null).toBe(false);
  });
});
