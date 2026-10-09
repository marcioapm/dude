/**
 * The session screen: what its bar offers and what it says for a branch
 * preview. Mounted in happy-dom against the fixture client, so it reads
 * the Run and its events as it would from the API.
 */

import { afterEach, describe, expect, test } from "bun:test";
import { click, emitForTest, mount, until } from "./dom.ts";
import { FixtureClient } from "../src/fixtures/client.ts";
import { RUN_ID } from "../src/fixtures/data.ts";
import { PreparingImage, RunScreen } from "../src/screens/RunScreen.tsx";
import type { RunDetail } from "../src/api/client.ts";
import { BUILDER_GIVE_UP_MINUTES } from "@dude/domain";

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
    const chip = await until(() => machineChip(page), "the machine chip");
    expect(chip.textContent).toBe("Large8 CPUs · 16 GiB · 80 GiB");
  });

  test("a Run from before sizes has no chip", async () => {
    const page = await session({ client: new RunClient({ machine: null }) });
    await until(() => page.querySelector("[data-testid=session-rail]"), "the rail");
    expect(page.querySelector("[data-testid=run-machine]")).toBeNull();
  });

  const FIXED = "Set when the session started or last resumed — a change to its size reaches it when it next resumes.";
  for (const [from, phase, role, says] of [
    ["project", "implement", "implementer", "From its project’s settings for the Implementer."],
    ["organization", "review", "reviewer", "From the organisation’s settings for the Reviewer."],
    ["implementer", "fix", "implementer", "The implementer’s size: the fixer has none of its own."],
    ["default", "simplify", "simplifier", "The organisation’s default size: nothing names another for it."],
    ["organization", "fix", "implementer", "From the organisation’s settings for the Fixer."],
  ] as const) {
    test(`a ${phase} Run whose size is from ${from} says so`, async () => {
      const page = await session({ client: new RunClient({ phase, role, status: "completed", endedAt: new Date().toISOString(), machine: { ...LARGE, from } }) });
      expect(await machineTip(page)).toBe(`Machine: Large${says} ${FIXED}`);
    });
  }

  test("a resume that moved the Run to another size shows it, read again on run.resized", async () => {
    class Resized extends RunClient {
      machine: NonNullable<RunDetail["machine"]> = LARGE;
      override async getRun(id: string): Promise<RunDetail> {
        return { ...(await super.getRun(id)), machine: this.machine };
      }
    }
    const client = new Resized({});
    const page = await session({ client });
    await until(() => machineChip(page), "Large");
    client.machine = { ...LARGE, sizeId: "msz_tiny", name: "Tiny", cpus: 0.5, memoryMiB: 1024, diskGiB: 20, from: "project",
      diskKept: { requestedGiB: 5, reason: "its saved state used up to 8.0 GiB" } };
    await emitForTest("run.resized", { machine: client.machine });
    const chip = await until(() => machineChip(page, "Machine: Tiny, 0.5 CPUs · 1 GiB · 20 GiB"), "the chip saying Tiny");
    expect(chip.textContent).toBe("Tiny0.5 CPUs · 1 GiB · 20 GiB");
    expect(await machineTip(page, "Machine: Tiny, 0.5 CPUs · 1 GiB · 20 GiB")).toContain("lux kept its 20 GiB disk rather than 5: its saved state used up to 8.0 GiB.");
  });

  test("a size the resume could not move it to: the tooltip says why", async () => {
    const note = "Its settings now name Big, in another pool: a stopped Run cannot change pools, so it keeps Large. A new Run gets Big.";
    const page = await session({ client: new RunClient({ machine: { ...LARGE, note } }) });
    expect(await machineTip(page)).toContain(` ${note}`);
  });
});

/** The machine chip, by its role and its exact accessible name. */
const machineChip = (page: HTMLElement, name = "Machine: Large, 8 CPUs · 16 GiB · 80 GiB") =>
  [...page.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.getAttribute("aria-label") === name) ?? null;

/** A chip's tooltip, opened as a keyboard user does: focusing the chip. */
async function tipOf(chip: HTMLButtonElement): Promise<string> {
  const { act } = await import("react");
  await act(async () => chip.focus());
  const tip = await until(() => document.querySelector("[role=tooltip]"), "the chip's tooltip");
  return tip.textContent ?? "";
}

describe("the session's model", () => {
  const modelChip = (page: HTMLElement) => page.querySelector<HTMLButtonElement>("[data-testid=run-model]");

  test("the header says the tier and the model it requested, as the Run recorded them", async () => {
    const page = await session({ client: new RunClient({ model: "claude-opus-5-5", modelTier: "Coder", effort: null }) });
    const chip = await until(() => modelChip(page), "the model chip");
    expect(chip.getAttribute("aria-label")).toBe("Model: Coder, requests claude-opus-5-5");
    expect(chip.textContent).toBe("Coder ·claude-opus-5-5");
    expect(await tipOf(chip)).toBe("Coder" +
      "The Implementer’s tier. When this session started, Coder asked the proxy for claude-opus-5-5; changing Coder now changes the next session, not this one." +
      "That is what dude asked for; how the proxy served it is the proxy’s to say.");
  });

  test("and the effort the tier asked for, when it asked for one", async () => {
    const page = await session({ client: new RunClient({ model: "claude-sonnet-5", modelTier: "Coder", effort: "medium" }) });
    const chip = await until(() => modelChip(page), "the model chip");
    expect(chip.textContent).toBe("Coder ·claude-sonnet-5· medium");
    expect(await tipOf(chip)).toContain("Coder asked the proxy for claude-sonnet-5 at effort medium;");
  });

  test("a fix Run names the Fixer", async () => {
    const page = await session({ client: new RunClient({ phase: "fix", role: "implementer", modelTier: "Coder" }) });
    expect(await tipOf(await until(() => modelChip(page), "the model chip"))).toContain("The Fixer’s tier.");
  });

  test("a Run from before tiers shows its model alone", async () => {
    const page = await session({ client: new RunClient({ model: "llm-anthropic/claude-sonnet-5", modelTier: null, effort: null }) });
    const chip = await until(() => modelChip(page), "the model chip");
    expect(chip.getAttribute("aria-label")).toBe("Model: llm-anthropic/claude-sonnet-5");
    expect(chip.textContent).toBe("llm-anthropic/claude-sonnet-5");
    expect(await tipOf(chip)).toBe("llm-anthropic/claude-sonnet-5When this session started, dude asked the proxy for llm-anthropic/claude-sonnet-5." +
      "That is what dude asked for; how the proxy served it is the proxy’s to say.");
  });

  test("a Run that has asked for nothing yet has no chip", async () => {
    const page = await session({ client: new RunClient({ model: null, modelTier: null }) });
    await until(() => page.querySelector("[data-testid=session-rail]"), "the rail");
    expect(modelChip(page)).toBeNull();
  });
});

describe("whether the session can start containers", () => {
  const badge = (page: HTMLElement) => page.querySelector<HTMLButtonElement>("[data-testid=can-run-containers]");
  const IMAGE: NonNullable<RunDetail["image"]> = { imageId: "img_1", name: "agents-podman", versionId: "imv_1", version: 3,
    ref: "registry.test/dude/custom@sha256:" + "1".repeat(64), layer: "registry.test/dude/layer@sha256:" + "2".repeat(64) };

  test("the header says so, after the image, when the Run recorded it can", async () => {
    const page = await session({ client: new RunClient({ image: IMAGE, canRunContainers: true }) });
    const shown = await until(() => badge(page), "the badge");
    expect(shown.textContent).toBe("Can run containers");
    const image = page.querySelector("[data-testid=run-image]")!;
    expect(image.compareDocumentPosition(shown) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    // A keyboard user reaches it, and a screen reader hears it, as the chips before it.
    expect(shown.getAttribute("aria-label")).toBe("Can run containers");
    expect(shown.hasAttribute("title")).toBe(false);
    expect(await tipOf(shown)).toBe("This Run can start containers inside it. Set when the session started; resuming keeps it.");
    expect(document.activeElement).toBe(shown);
  });

  test("on a typed image or dude's fallback (no image chip), the header still says so", async () => {
    const page = await session({ client: new RunClient({ image: null, canRunContainers: true }) });
    expect((await until(() => badge(page), "the badge")).textContent).toBe("Can run containers");
  });

  for (const [what, patch] of [["it recorded it cannot", { canRunContainers: false }], ["it recorded nothing", { canRunContainers: null }]] as const) {
    test(`nothing when ${what}`, async () => {
      const page = await session({ client: new RunClient({ image: IMAGE, ...patch }) });
      await until(() => page.querySelector("[data-testid=run-image]"), "the image chip");
      expect(badge(page)).toBeNull();
    });
  }
});

/** The machine chip's tooltip, opened as a keyboard user does: focusing the chip. */
async function machineTip(page: HTMLElement, name?: string): Promise<string> {
  const chip = await until(() => machineChip(page, name), "the machine chip");
  const { act } = await import("react");
  await act(async () => chip.focus());
  const tip = await until(() => document.querySelector("[role=tooltip]"), "the chip's tooltip");
  return tip.textContent ?? "";
}

const LARGE: NonNullable<RunDetail["machine"]> = { sizeId: "msz_large", name: "Large", cpus: 8, memoryMiB: 16384, diskGiB: 80, poolId: null, pool: null, from: "organization" };

describe("the memory the Run's container got", () => {
  test("while it runs, from lux's answer", async () => {
    class Limited extends RunClient {
      override async runServers() {
        const s = await super.runServers();
        return { ...s, run: s.run ? { ...s.run, memoryLimit: 15.2 * 1024 ** 3 } : null };
      }
    }
    const page = await session({ client: new Limited({ machine: LARGE }) });
    // Read once the terminal is known: the limit comes in the same answer.
    await until(() => page.querySelector("[data-testid=terminal-link]"), "lux's answer");
    expect(await machineTip(page)).toContain("It asked for 16 GiB and got 15.2:");
  });

  test("on a finished Run, from what the Run recorded", async () => {
    const page = await session({ client: new RunClient({ status: "completed", endedAt: new Date().toISOString(), machine: { ...LARGE, memoryLimit: 15.2 * 1024 ** 3 } }) });
    expect(await machineTip(page)).toContain("It asked for 16 GiB and got 15.2:");
  });

  test("not reported: the tooltip does not say", async () => {
    const page = await session({ client: new RunClient({ status: "completed", endedAt: new Date().toISOString(), machine: LARGE }) });
    expect(await machineTip(page)).not.toContain("It asked for");
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

describe("a Run preparing its image", () => {
  const callout = async (preparing: NonNullable<RunDetail["preparingImage"]>) => {
    const { container, unmount } = await mount(<PreparingImage preparing={preparing} />);
    mounted.push(unmount);
    return container.querySelector("[data-testid=preparing-image]")!.textContent;
  };
  const waiting = { buildId: "imb_1", state: "running", imageName: "acme-base", version: 1, builderOfflineSince: null };

  test("its image's first version building says so, without a finish's minute or two", async () => {
    expect(await callout({ ...waiting, kind: "build" })).toBe(
      "Preparing image: building acme-base v1 (its first version). The builder is on it now; the session starts once it is built and published. Nothing is spent until then.",
    );
  });

  test("the dude layer being added to it", async () => {
    expect(await callout({ ...waiting, kind: "finish", state: "queued" })).toBe(
      "Preparing image: adding the dude layer to acme-base v1. It is next in the builder’s line; the session starts once it is done, usually within a minute or two. Nothing is spent until then.",
    );
  });

  test("an offline builder names the give-up limit", async () => {
    expect(await callout({ ...waiting, kind: "build", builderOfflineSince: "2026-10-01T08:00:00Z" })).toContain(
      `if it stays offline for ${BUILDER_GIVE_UP_MINUTES} minutes of the wait, this Run fails before it starts`,
    );
  });
});
