/**
 * Automations' pieces: the words a schedule reads as, the picker's reading
 * of a cron line and back, the repository filter's count, and the marks'
 * accessible names.
 */

import { afterEach, describe, expect, test } from "bun:test";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { renderToStaticMarkup } from "react-dom/server";
import {
  ChipsInput,
  cronOf,
  DayToggles,
  describeCron,
  KindMark,
  ListSwitch,
  OwnerMark,
  RepoFilterField,
  repoFilterCount,
  repoGlob,
  scheduleOf,
  TriggerSummary,
} from "../src/components/Automations.tsx";
import { TooltipProvider } from "../src/primitives/Tooltip.tsx";

let root: Root | null = null;
let host: HTMLElement | null = null;
afterEach(async () => {
  await act(async () => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

async function mount(node: React.ReactNode): Promise<HTMLElement> {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => root!.render(<TooltipProvider>{node}</TooltipProvider>));
  return host;
}

describe("schedules in words", () => {
  test("each preset reads as a person says it; anything else is its cron line", () => {
    expect(describeCron("*/15 * * * *")).toBe("Every 15 minutes");
    expect(describeCron("5 * * * *")).toBe("Hourly at :05");
    expect(describeCron("0 3 * * *")).toBe("Daily at 03:00");
    expect(describeCron("30 9 * * 1-5")).toBe("Weekdays at 09:30");
    expect(describeCron("0 8 * * 1,4")).toBe("Mon, Thu at 08:00");
    expect(describeCron("0 12 * * 7")).toBe("Sun at 12:00");
    expect(describeCron("0 */6 1 * *")).toBe("0 */6 1 * *");
  });

  test("the picker reads a preset back into its mode and makes the same line", () => {
    for (const cron of ["*/10 * * * *", "7 * * * *", "0 3 * * *", "30 9 * * 1,3,5"]) {
      const s = scheduleOf(cron);
      expect(s.mode).not.toBe("cron");
      expect(cronOf(s.mode as Exclude<typeof s.mode, "cron">, s)).toBe(cron);
    }
    expect(scheduleOf("30 9 * * 1-5")).toMatchObject({ mode: "weekly", days: [1, 2, 3, 4, 5] });
    expect(scheduleOf("0 */6 1 * *").mode).toBe("cron");
    expect(scheduleOf("nonsense").mode).toBe("cron");
  });

  test("an event trigger names its actions, or any action", () => {
    expect(renderToStaticMarkup(<TriggerSummary trigger={{ kind: "github", event: "push" }} />)).toContain("any action");
    expect(renderToStaticMarkup(<TriggerSummary trigger={{ kind: "schedule", cron: "0 3 * * *", tz: "Europe/Lisbon" }} />)).toContain("Lisbon");
  });
});

describe("the repository filter", () => {
  const all = ["acme/web", "acme/svc-billing", "acme/svc-auth", "acme/sandbox-1"];
  test("globs match within a segment, any case", () => {
    expect(repoGlob("acme/svc-*", "Acme/SVC-billing")).toBe(true);
    expect(repoGlob("acme/svc-*", "acme/web")).toBe(false);
    expect(repoGlob("*/web", "other/web")).toBe(true);
  });

  test("counts what each mode covers", () => {
    expect(repoFilterCount({ mode: "all", repos: [] }, all)).toBe(4);
    expect(repoFilterCount({ mode: "only", repos: ["acme/svc-*"] }, all)).toBe(2);
    expect(repoFilterCount({ mode: "except", repos: ["acme/svc-*", "acme/sandbox-1"] }, all)).toBe(1);
  });

  test("says how many it matches, and on All that later ones count", async () => {
    const groups = [{ project: "web", repos: ["acme/web"] }, { project: "billing", repos: ["acme/svc-billing", "acme/svc-auth"] }];
    const el = await mount(<RepoFilterField value={{ mode: "only", repos: ["acme/svc-*"] }} onChange={() => undefined} org="Acme" groups={groups} />);
    expect(el.querySelector('[data-testid="repo-count"]')!.textContent).toBe("Matches 2 of 3 repositories");
    expect(el.querySelector('[data-glob="true"]')!.textContent).toContain("2");
    await act(async () => root!.render(<TooltipProvider><RepoFilterField value={{ mode: "all", repos: [] }} onChange={() => undefined} org="Acme" groups={groups} /></TooltipProvider>));
    expect(el.querySelector('[data-testid="repo-count"]')!.textContent).toBe("Matches 3 of 3 repositories, and any added later");
    expect(el.querySelectorAll("input").length).toBe(0);
  });
});

describe("ChipsInput", () => {
  test("Enter and comma add, Backspace takes the last back, each chip's Remove is named", async () => {
    let values: string[] = ["main"];
    const render = () => root!.render(<TooltipProvider><ChipsInput label="Branch" values={values} onChange={(v) => { values = v; render(); }} /></TooltipProvider>);
    const el = await mount(<ChipsInput label="Branch" values={values} onChange={(v) => { values = v; render(); }} />);
    const input = el.querySelector("input")!;
    expect(input.getAttribute("aria-label")).toBe("Branch");
    const type = async (text: string, key: string) => {
      await act(async () => {
        const setter = Object.getOwnPropertyDescriptor(Object.getPrototypeOf(input), "value")!.set!;
        setter.call(input, text);
        input.dispatchEvent(new Event("input", { bubbles: true }));
      });
      await act(async () => input.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true })));
    };
    await type("feat/*", "Enter");
    expect(values).toEqual(["main", "feat/*"]);
    await type("", "Backspace");
    expect(values).toEqual(["main"]);
    expect(el.querySelector('button[aria-label="Remove main"]')).not.toBeNull();
  });
});

describe("marks", () => {
  test("an agent that is one of dude's roles says which", () => {
    const html = renderToStaticMarkup(<KindMark kind="agent" role="Tester" />);
    expect(html).toContain("Agent");
    expect(html).toContain("· Tester");
  });

  test("an owner's avatar alone is named; one who left is the admins'", () => {
    expect(renderToStaticMarkup(<OwnerMark owner={{ name: "Ana Ribeiro" }} org="Acme" avatarOnly />)).toContain('aria-label="Ana Ribeiro"');
    const held = renderToStaticMarkup(<OwnerMark owner={null} org="Acme" left avatarOnly />);
    expect(held).toContain('aria-label="Acme&#x27;s admins"');
    expect(held).toContain('data-held="true"');
  });

  test("the list switch says which automation and whether it is on, and does not open the row", async () => {
    let on = true;
    let opened = 0;
    const el = await mount(<div onClick={() => opened++}><ListSwitch checked={on} onCheckedChange={(v) => { on = v; }} label="PR triage" /></div>);
    const sw = el.querySelector('[role="switch"]')!;
    expect(sw.getAttribute("aria-label")).toBe("PR triage: on");
    await act(async () => (sw as HTMLElement).click());
    expect(on).toBe(false);
    expect(opened).toBe(0);
  });

  test("the last day on cannot be turned off", async () => {
    const el = await mount(<DayToggles days={[3]} onChange={() => undefined} />);
    const pressed = [...el.querySelectorAll('button[aria-pressed="true"]')] as HTMLButtonElement[];
    expect(pressed.map((b) => b.textContent)).toEqual(["Wed"]);
    expect(pressed[0]!.disabled).toBe(true);
  });
});
