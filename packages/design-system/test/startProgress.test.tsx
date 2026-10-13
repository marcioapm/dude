/**
 * StartProgress and StartChip: the steps built from the shared words and a
 * stage list, the verb, a failure naming its stage, an older lux's line,
 * the live region (once per stage, never per tick), and the gallery's
 * Starting section in both themes and densities.
 */

import { afterEach, describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { renderToStaticMarkup } from "react-dom/server";
import { FRESH_START_STAGES, MOVE_STAGES, RESUME_STAGES, START_STAGE_WORDS } from "@dude/domain";
import { StartChip, StartProgress, startSteps } from "../src/components/StartProgress.tsx";
import { Duration } from "../src/components/Numbers.tsx";
import { PreviewStages } from "../src/components/PreviewStages.tsx";
import { PaneDensityContext } from "../src/gallery/Frame.tsx";
import { StartingSection } from "../src/gallery/sections/Starting.tsx";
import { TooltipProvider } from "../src/primitives/Tooltip.tsx";

let root: Root | null = null;
let host: HTMLElement | null = null;
afterEach(async () => {
  await act(async () => root?.unmount());
  host?.remove();
  root = host = null;
});

async function mount(node: React.ReactNode): Promise<HTMLElement> {
  host ??= document.body.appendChild(document.createElement("div"));
  root ??= createRoot(host);
  await act(async () => root!.render(<TooltipProvider>{node}</TooltipProvider>));
  return host;
}

const T0 = Date.now() - 31_000;

describe("startSteps", () => {
  test("the fresh list by default: done with times, the current with its since, the rest pending, words from the map", () => {
    const steps = startSteps({ stage: "repositories", since: T0 + 20_000, done: [{ stage: "waiting", tookMs: 9_400 }, { stage: "image", tookMs: 1_200 }] });
    expect(steps.map((s) => s.id)).toEqual([...FRESH_START_STAGES]);
    expect(steps.map((s) => s.state)).toEqual(["done", "done", "done", "current", "pending", "pending"]);
    expect(steps[0]).toEqual({ id: "waiting", label: "Waiting for a machine", doneLabel: "Machine found", state: "done", tookMs: 9_400 });
    expect(steps[3]!.since).toBe(T0 + 20_000);
    expect(steps[2]!.tookMs).toBeUndefined();
  });

  test("a resume's list draws the image and repositories, which lux announces on every resume", () => {
    expect(startSteps({ stage: "volumes", stages: RESUME_STAGES }).map((s) => s.id)).toEqual(["waiting", "image", "volumes", "repositories", "container", "running"]);
  });

  test("a stage lux reports that the list leaves out is drawn in its place", () => {
    expect(startSteps({ stage: "image", stages: ["waiting", "container"] }).map((s) => s.id)).toEqual(["waiting", "image", "container"]);
  });

  test("a move leads with park, in its own words, then a resume's stages", () => {
    const steps = startSteps({ stage: "park", stages: MOVE_STAGES });
    expect(steps.map((s) => s.id)).toEqual(["park", "waiting", "image", "volumes", "repositories", "container", "running"]);
    expect(steps[0]).toMatchObject({ label: "Stopping it, keeping its work", doneLabel: "Stopped, work kept", state: "current" });
    expect(START_STAGE_WORDS.park).toEqual({ now: "Stopping it, keeping its work", done: "Stopped, work kept" });
  });

  test("a pool names where it waits while waiting is current; once found it reads Machine found", () => {
    const waiting = startSteps({ stage: "waiting", stages: MOVE_STAGES, pool: "highmem" });
    expect(waiting.find((s) => s.id === "waiting")).toMatchObject({ label: "Waiting for a machine in highmem", state: "current" });
    const found = startSteps({ stage: "volumes", stages: MOVE_STAGES, pool: "highmem" }).find((s) => s.id === "waiting")!;
    expect(found.state).toBe("done");
    expect(found.doneLabel).toBe("Machine found");
    expect(startSteps({ stage: "waiting" })[0]!.label).toBe("Waiting for a machine");
  });

  test("failed marks the stage it failed in; no stage leaves every step pending", () => {
    expect(startSteps({ stage: "image", failed: true }).map((s) => s.state)).toEqual(["done", "failed", "pending", "pending", "pending", "pending"]);
    expect(startSteps({ stage: null }).every((s) => s.state === "pending")).toBe(true);
  });
});

describe("StartProgress", () => {
  const steps = (stage: "waiting" | "image" | "container") => startSteps({ stage, since: Date.now() - 3_000,
    done: stage === "waiting" ? [] : [{ stage: "waiting", tookMs: 5_000 }] });

  test("the verb defaults by kind and can be given", () => {
    expect(renderToStaticMarkup(<StartProgress steps={steps("image")} />)).toContain('aria-label="Starting the agent"');
    expect(renderToStaticMarkup(<StartProgress kind="resume" steps={steps("image")} />)).toContain('aria-label="Resuming the agent"');
    const moving = renderToStaticMarkup(<StartProgress kind="move" verb="Moving to another machine" steps={steps("image")} />);
    expect(moving).toContain('aria-label="Moving to another machine the agent"');
    expect(moving).toContain("Moving to another machine");
  });

  test("the line: the stage's words and clock; the total only once a stage has passed", () => {
    const first = renderToStaticMarkup(<StartProgress steps={steps("waiting")} startedAt={T0} />);
    expect(first).toContain("Waiting for a machine");
    expect(first).not.toContain("in all");
    const later = renderToStaticMarkup(<StartProgress steps={steps("image")} startedAt={T0} />);
    expect(later).toContain("in all");
    expect(later).toMatch(/00:3\d/);
  });

  test("an older lux names no stage: the verb and the total clock, nothing to open", () => {
    const html = renderToStaticMarkup(<StartProgress steps={startSteps({ stage: null })} startedAt={T0} />);
    expect(html).toContain("Starting");
    expect(html).toMatch(/00:3\d/);
    expect(html).not.toContain("aria-expanded");
    expect(html).not.toContain('aria-label="Stages"');
  });

  test("the line opens the stepper: aria-expanded, aria-current on the current stage, hidden words for each state", async () => {
    const el = await mount(<StartProgress steps={steps("image")} startedAt={T0} />);
    const toggle = el.querySelector<HTMLButtonElement>("[data-testid=start-toggle]")!;
    const list = el.querySelector("ol")!;
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(toggle.getAttribute("aria-controls")).toBe(list.id);
    expect(list.hidden).toBe(true);
    await act(async () => toggle.click());
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    expect(list.hidden).toBe(false);
    expect(list.querySelector("[aria-current=step]")!.textContent).toContain("Getting the image ready, now");
    expect(list.textContent).toContain("Machine found");
    expect(list.textContent).toContain("Starting the container, to come");
  });

  test("failed: the danger Callout names the stage and lux's reason, with Try again; the stages fold under it", async () => {
    let retried = 0;
    const el = await mount(<StartProgress steps={startSteps({ stage: "image", failed: true, done: [{ stage: "waiting", tookMs: 5_800 }] })}
      failure={{ reason: "pull failed: manifest unknown", onRetry: () => retried++ }} />);
    const alert = el.querySelector("[role=alert]")!;
    expect(alert.textContent).toBe("Did not start: failed while getting the image ready: pull failed: manifest unknown. Nothing was spent on the model.Try again");
    await act(async () => el.querySelector<HTMLButtonElement>("[data-testid=start-retry]")!.click());
    expect(retried).toBe(1);
    expect(el.querySelector("ol")!.hidden).toBe(true);
    const crossed = el.querySelector("li[data-state=failed]")!;
    expect(crossed.getAttribute("aria-current")).toBe("step");
    expect(crossed.textContent).toBe("Getting the image ready, failedfailed");
    expect(el.querySelector("[role=status]")).toBeNull();
  });

  test("a resume that failed says it did not resume; no reason ends the sentence there", () => {
    const html = renderToStaticMarkup(<StartProgress kind="resume" steps={startSteps({ stage: "volumes", failed: true, stages: RESUME_STAGES })} failure={{ reason: null }} />);
    expect(html).toContain("<b>Did not resume</b>: failed while restoring its workspace. Nothing was spent");
    expect(html).not.toContain("Try again");
  });

  test("a reason that ends its own sentence gets no second full stop; with no stage there is no 'failed:'", () => {
    const why = "The Brainstorm runs on Fast, which names no model yet. An admin sets it in Models.";
    const el = document.createElement("div");
    el.innerHTML = renderToStaticMarkup(<StartProgress steps={startSteps({ stage: null, failed: true })} failure={{ reason: why, plain: true }} />);
    expect(el.querySelector("[data-testid=start-failed]")!.textContent).toBe(`Did not start: ${why} Nothing was spent on the model.`);
    for (const end of ["!", "?"]) {
      el.innerHTML = renderToStaticMarkup(<StartProgress steps={startSteps({ stage: "image", failed: true })} failure={{ reason: `pull failed${end}` }} />);
      expect(el.querySelector("[data-testid=start-failed]")!.textContent)
        .toBe(`Did not start: failed while getting the image ready: pull failed${end} Nothing was spent on the model.`);
    }
  });

  test("lux's raw reason is set in mono; the app's own sentence (plain) in body text", () => {
    const el = document.createElement("div");
    el.innerHTML = renderToStaticMarkup(<StartProgress steps={startSteps({ stage: "image", failed: true })} failure={{ reason: "pull failed: manifest unknown" }} />);
    expect(el.querySelector("[data-testid=start-failed] code")!.textContent).toBe("pull failed: manifest unknown");
    el.innerHTML = renderToStaticMarkup(<StartProgress steps={startSteps({ stage: null, failed: true })} failure={{ reason: "No model yet.", plain: true }} />);
    expect(el.querySelector("[data-testid=start-failed] code")).toBeNull();
    expect(el.querySelector("[data-testid=start-failed]")!.textContent).toBe("Did not start: No model yet. Nothing was spent on the model.");
  });

  test("the slow line is calm, with lux's reason under it", () => {
    const html = renderToStaticMarkup(<StartProgress steps={steps("waiting")} slowNote="No machine free yet." slowReason="waiting for capacity" />);
    expect(html).toContain('data-testid="start-slow"');
    expect(html).toContain("lux: waiting for capacity");
    expect(html).not.toContain("data-tone");
  });

  test("the live region says each stage once, never the clocks, which tick as timers outside it", async () => {
    const since = Date.now() - 2_000;
    const at = (stage: "waiting" | "image") => startSteps({ stage, since, done: stage === "image" ? [{ stage: "waiting", tookMs: 1_000 }] : [] });
    const el = await mount(<StartProgress steps={at("waiting")} startedAt={since} />);
    const region = el.querySelector("[role=status][aria-live=polite]")!;
    expect(region.getAttribute("aria-atomic")).toBe("true");
    expect(region.textContent).toBe("Starting: Waiting for a machine.");
    const timers = [...el.querySelectorAll("[role=timer]")];
    expect(timers.length).toBeGreaterThan(0);
    for (const t of timers) expect(region.contains(t)).toBe(false);
    // The start's own mark is the line's one live thing: its clocks carry no live dot.
    for (const t of timers) expect(t.hasAttribute("data-live-dot")).toBe(false);
    const before = timers[0]!.textContent;
    const changes: string[] = [];
    const watch = new MutationObserver(() => changes.push(region.textContent ?? ""));
    watch.observe(region, { childList: true, characterData: true, subtree: true });
    await act(async () => new Promise((r) => setTimeout(r, 1_100)));
    expect(el.querySelector("[role=timer]")!.textContent).not.toBe(before);
    expect(changes).toEqual([]);
    await act(async () => root!.render(<TooltipProvider><StartProgress steps={at("image")} startedAt={since} /></TooltipProvider>));
    expect(region.textContent).toBe("Starting: Getting the image ready.");
  });

  test("its one loop freezes under reduced motion: divided by --ds-motion-live, on the breathing cadence", () => {
    // happy-dom runs no animations: the stylesheet is what reduced motion reads, as ChatProgress's guard does.
    const css = readFileSync(`${import.meta.dir}/../src/components/StartProgress.module.css`, "utf8");
    const loops = [...css.matchAll(/animation:[^;]+/g)].map((m) => m[0]);
    expect(loops.length).toBe(1);
    expect(loops[0]).toContain("/ max(var(--ds-motion-live)");
    expect(loops[0]).toContain("--ds-cadence-breathe");
  });
});

describe("StartChip", () => {
  test("session meta: the mark, the words, the clock; bare drops the mark", () => {
    const step = startSteps({ stage: "repositories", since: Date.now() - 12_000 })[3]!;
    const chip = renderToStaticMarkup(<StartChip kind="resume" step={step} />);
    expect(chip).toContain('data-status="starting"');
    expect(chip).toContain("Resuming");
    expect(chip).toContain("Checking out repositories");
    expect(chip).toMatch(/00:1[23]/);
    const bare = renderToStaticMarkup(<StartChip bare step={step} />);
    expect(bare).not.toContain("data-status");
    expect(bare).toContain("Checking out repositories");
  });

  test("failed: did not start, failed while…, no clock; an older lux: the verb and the total", () => {
    const failed = renderToStaticMarkup(<StartChip step={startSteps({ stage: "image", failed: true })[1]!} />);
    expect(failed).toContain('data-status="failed"');
    expect(failed).toContain("Did not start");
    expect(failed).toContain("Failed while getting the image ready");
    expect(failed).not.toContain('role="timer"');
    const legacy = renderToStaticMarkup(<StartChip startedAt={Date.now() - 8_000} />);
    expect(legacy).toContain("Starting");
    expect(legacy).toMatch(/00:0[89]/);
  });
});

test("Duration dot={false} ticks without the live dot", () => {
  const withDot = renderToStaticMarkup(<Duration since={Date.now() - 5_000} format="clock" />);
  const without = renderToStaticMarkup(<Duration since={Date.now() - 5_000} format="clock" dot={false} />);
  const classes = (h: string) => h.includes("data-live-dot");
  expect(classes(withDot)).toBe(true);
  expect(classes(without)).toBe(false);
  expect(without).toContain("00:05");
});

test("PreviewStages reads lux's stages in the agents' words", () => {
  expect(renderToStaticMarkup(<PreviewStages stage="image" />)).toContain(START_STAGE_WORDS.image.now);
  expect(renderToStaticMarkup(<PreviewStages stage="container" />)).toContain(START_STAGE_WORDS.container.now);
});

const FRAMES = ["comfortable dark", "comfortable light", "compact dark", "compact light"];

test("the gallery's Starting blocks render in dark and light, comfortable and compact, with a move", async () => {
  const el = await mount(<PaneDensityContext.Provider value="both"><StartingSection mode="both" /></PaneDensityContext.Provider>);
  const blocks = [...el.querySelectorAll("section#starting [id^='st-']")];
  expect(blocks.map((b) => b.id)).toEqual(["st-play", "st-states", "st-failed", "st-move", "st-chip"]);
  for (const block of blocks) {
    const panes = [...block.querySelectorAll(":scope [data-theme][data-density]")].filter((p) => !p.parentElement!.closest("[data-theme]"));
    expect(panes.map((p) => `${p.getAttribute("data-density")} ${p.getAttribute("data-theme")}`), block.id).toEqual(FRAMES);
  }
  for (const move of el.querySelectorAll("[data-testid=start-move]")) {
    expect(move.querySelector("[role=status]")!.textContent).toBe("Moving to Large: Waiting for a machine in highmem.");
    expect([...move.querySelectorAll("li")].map((li) => li.getAttribute("data-state"))).toEqual(["done", "current", "pending", "pending", "pending", "pending", "pending"]);
    expect(move.querySelector("li")!.textContent).toContain("Stopped, work kept");
  }
  expect(el.querySelectorAll("[data-testid=start-failed]").length).toBe(8);
  for (const plain of el.querySelectorAll("[data-testid=start-failure-plain] [data-testid=start-failed]")) {
    expect(plain.textContent).toBe("Did not start: The Brainstorm runs on Fast, which names no model yet. An admin sets it in Models. Nothing was spent on the model.");
  }
});
