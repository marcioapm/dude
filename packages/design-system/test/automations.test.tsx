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
  AgentTools,
  ChipsInput,
  cronOf,
  DayToggles,
  describeCron,
  KindMark,
  ListSwitch,
  NextRuns,
  OwnerMark,
  RepoFilterField,
  repoFilterCount,
  repoGlob,
  SchedulePicker,
  scheduleOf,
  ScopeChip,
  TriggerSummary,
} from "../src/components/Automations.tsx";
import { TooltipProvider } from "../src/primitives/Tooltip.tsx";
import { PaneDensityContext } from "../src/gallery/Frame.tsx";
import { AutomationsSection } from "../src/gallery/sections/Automations.tsx";

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

  test("the outbound kind reads Send a webhook", () => {
    const html = renderToStaticMarkup(<KindMark kind="webhook" />);
    expect(html).toContain(">Send a webhook<");
    expect(html).not.toContain("Webhook only");
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

describe("SchedulePicker and NextRuns", () => {
  test("a scope chip says Organisation for the org's, and a project's name beside its avatar", () => {
    const org = renderToStaticMarkup(<ScopeChip org="Acme" />);
    expect(org).toContain('data-scope="organization"');
    expect(org).toContain(">Organisation<");
    const project = renderToStaticMarkup(<ScopeChip org="Acme" project="web" projectId="prj_web" />);
    expect(project).toContain('data-scope="project"');
    expect(project).toContain(">web<");
    expect(project).toContain('role="img"');
  });
  async function typeTime(input: HTMLInputElement, text: string) {
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, text);
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
  }

  test("a daily schedule's time is 24-hour text; a full time changes the cron; the cron line and Advanced are under it", async () => {
    const changes: Array<{ cron: string; tz: string }> = [];
    const el = await mount(<SchedulePicker cron="0 3 * * *" tz="Europe/Lisbon" zones={["UTC", "Europe/Lisbon"]} zoneSource="Acme's"
      onChange={(c) => changes.push(c)} />);
    const time = el.querySelector<HTMLInputElement>("input[aria-label=Time]")!;
    expect(time.value).toBe("03:00");
    expect(time.type).not.toBe("time");
    await typeTime(time, "1");
    expect(changes).toEqual([]);
    await typeTime(time, "15:30");
    expect(changes).toEqual([{ cron: "30 15 * * *", tz: "Europe/Lisbon" }]);
    const asCron = el.querySelector("[data-testid=schedule-as-cron]")!;
    expect(asCron.textContent).toBe("As cron 0 3 * * * · Advanced: write a cron expression");
    expect(el.querySelectorAll("select").length).toBe(0);
    await act(async () => [...asCron.querySelectorAll("button")].find((b) => b.textContent?.startsWith("Advanced"))!.click());
    expect(el.querySelector<HTMLInputElement>("[data-testid=schedule-cron]")?.value).toBe("0 3 * * *");
  });

  test("minutes and hourly modes use the DS controls and say the change", async () => {
    const changes: string[] = [];
    const el = await mount(<SchedulePicker cron="7 * * * *" tz="UTC" zones={["UTC"]} onChange={(c) => changes.push(c.cron)} />);
    expect(el.querySelector<HTMLInputElement>("input[aria-label='At minute']")?.value).toBe("7");
    const minutes = [...el.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === "Minutes")!;
    await act(async () => minutes.click());
    expect(changes.at(-1)).toBe("*/15 * * * *");
  });

  test("next runs read as a day and a time in the zone, or say that none comes", async () => {
    const el = await mount(<NextRuns tz="Europe/Lisbon" times={[new Date("2026-10-24T02:00:00Z"), new Date("2026-10-25T03:00:00Z")]} />);
    expect([...el.querySelectorAll("li")].map((li) => li.textContent)).toEqual(["Sat 24 Oct, 03:00", "Sun 25 Oct, 03:00"]);
    const none = await mount(<NextRuns tz="UTC" times={[]} />);
    expect(none.textContent).toBe("Never runs: no date matches.");
  });
});

describe("an automation agent's tools", () => {
  const tools = [
    { name: "post_to_chat", what: "Posts a message to a channel.", fresh: true },
    { name: "create_task", what: "Files a task." },
  ];

  test("each tool is its mono name and one line; New only on the fresh one", async () => {
    const el = await mount(<AgentTools tools={tools} />);
    const list = el.querySelector("ul[aria-label=\"The agent's tools\"]")!;
    const rows = [...list.querySelectorAll("li")];
    expect(rows.map((r) => r.getAttribute("data-tool"))).toEqual(["post_to_chat", "create_task"]);
    expect(rows.map((r) => r.querySelector("code")?.textContent)).toEqual(["post_to_chat", "create_task"]);
    expect(rows[0]!.textContent).toContain("New");
    expect(rows[1]!.textContent).not.toContain("New");
    expect(rows[1]!.textContent).toContain("Files a task.");
    expect(el.querySelector("[data-testid=tool-unavailable]")).toBeNull();
  });

  test("an unavailable tool keeps its row and says why, that runs fail, and links the fix", async () => {
    const el = await mount(<AgentTools tools={tools}
      unavailable={{ post_to_chat: { why: "Slack is not connected for Acme", fix: { label: "Connect Slack in Organisation settings → Chat", href: "#chat" } } }} />);
    const row = el.querySelector("li[data-tool=post_to_chat]")!;
    expect(row.getAttribute("data-unavailable")).toBe("true");
    const callout = row.querySelector("[data-testid=tool-unavailable]")!;
    expect(callout.getAttribute("data-tone")).toBe("attention");
    expect(callout.textContent).toBe("Slack is not connected for Acme. Runs that call it fail on the call. Connect Slack in Organisation settings → Chat");
    expect(callout.querySelector("a")?.getAttribute("href")).toBe("#chat");
    expect(el.querySelector("li[data-tool=create_task]")?.getAttribute("data-unavailable")).toBeNull();
  });

  test("the gallery's tools block mounts in all four panes", async () => {
    const el = await mount(<PaneDensityContext.Provider value="both"><AutomationsSection mode="both" /></PaneDensityContext.Provider>);
    const panes = [...el.querySelectorAll("#au-tools [data-theme]")].map((p) => `${p.getAttribute("data-theme")}·${p.getAttribute("data-density")}`);
    expect(panes.sort()).toEqual(["dark·comfortable", "dark·compact", "light·comfortable", "light·compact"]);
    expect(el.querySelectorAll("#au-tools [data-testid=tool-unavailable]").length).toBe(8);
  });
});
