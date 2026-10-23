/**
 * The Automations pages against the fixture client: the organisation's list
 * (on/off from the row without opening it, owner avatars named, filters),
 * a project's list with "Also running here", an automation's page (owner
 * left, read-only for a member), Run now on an event (validity and filter
 * lines, Run disabled while it would be skipped), and Transfer.
 */

import { afterEach, describe, expect, test } from "bun:test";
import type { ReactNode } from "react";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import { act, click, mount, until } from "./dom.ts";
import { FixtureClient } from "../src/fixtures/client.ts";
import { PEOPLE, PROJECT } from "../src/fixtures/data.ts";
import { AutomationPage, AutomationsList, automationsPlace, automationsSub, checkJson, testTemplate, type AutomationsPlace } from "../src/screens/AutomationsSettings.tsx";

let mounted: Array<() => Promise<void>> = [];
afterEach(async () => {
  for (const unmount of mounted) await unmount();
  mounted = [];
  document.body.innerHTML = "";
});

async function show(node: ReactNode) {
  const { container, unmount } = await mount(<TooltipProvider><ToastProvider>{node}</ToastProvider></TooltipProvider>);
  mounted.push(unmount);
  return container;
}

/** aut_triage's prompt as the fixture backend holds it now. */
const promptNow = (client: FixtureClient) => (client.fixtureAutomation("aut_triage").agent as { own: { prompt: string } }).own.prompt;

const rows = (c: HTMLElement) => [...c.querySelectorAll<HTMLElement>("[data-testid=automation-row]")];
const names = (c: HTMLElement) => rows(c).map((r) => r.querySelector(".automationName button")?.textContent);

async function typeInto(el: HTMLTextAreaElement | HTMLInputElement, text: string) {
  await act(async () => {
    const proto = Object.getPrototypeOf(el);
    Object.getOwnPropertyDescriptor(proto, "value")!.set!.call(el, text);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

describe("places", () => {
  test("an automation, its runs and one run round-trip through the settings sub-path", () => {
    for (const p of [{ id: "aut_1" }, { id: "aut_1", tab: "runs" }, { id: "aut_1", tab: "runs", runId: "arn_2" }] as AutomationsPlace[]) {
      const back = automationsPlace(automationsSub(p));
      expect(back.id).toBe(p.id);
      expect(back.tab).toBe(p.tab ?? "settings");
      expect(back.runId).toBe(p.runId);
    }
    expect(automationsPlace(undefined)).toEqual({ id: undefined, tab: undefined, runId: undefined });
  });
});

describe("the organisation's list", () => {
  test("lists every automation with its owner's name for screen readers; the switch turns one off without opening it", async () => {
    const client = new FixtureClient("a");
    const opened: string[] = [];
    const c = await show(<AutomationsList client={client} orgName="Example" projectId={null} canCreate onOpen={(id) => opened.push(id)} />);
    await until(() => (rows(c).length ? rows(c) : null), "the rows");
    expect(names(c)).toEqual(["PR triage", "Nightly dependency audit", "Weekly cost report", "Release notes to Slack", "Nightly QA pass", "Changelog builder"]);
    const cost = rows(c).find((r) => r.getAttribute("data-id") === "aut_cost")!;
    expect(cost.querySelector('[data-held="true"]')?.getAttribute("aria-label")).toBe("Example's admins");
    const sw = rows(c)[0]!.querySelector<HTMLElement>("[data-testid=automation-enabled]")!;
    expect(sw.getAttribute("aria-label")).toBe("PR triage: on");
    await click(sw);
    expect(opened).toEqual([]);
    expect((await client.automation("aut_triage")).enabled).toBe(false);
    await click(rows(c)[1]!);
    expect(opened).toEqual(["aut_audit"]);
  });
});

describe("a project's list", () => {
  test("shows its own and, read-only, the organisation's that reach its repositories", async () => {
    const c = await show(<AutomationsList client={new FixtureClient("a")} orgName="Example" projectId={PROJECT.id} canCreate onOpen={() => undefined} />);
    const here = await until(() => c.querySelector("[data-testid=also-running]"), "Also running here");
    expect(here.textContent).toContain("Also running here, from Example");
    const own = rows(c).filter((r) => !here.contains(r)).map((r) => r.querySelector(".automationName button")?.textContent);
    expect(own).toEqual(["Nightly QA pass", "Changelog builder"]);
    const theirs = rows(c).filter((r) => here.contains(r)).map((r) => r.querySelector(".automationName button")?.textContent);
    expect(theirs).toContain("PR triage");
    expect(theirs).toContain("Release notes to Slack");
    expect(theirs).not.toContain("Nightly dependency audit");
    for (const r of rows(c).filter((x) => here.contains(x))) {
      expect((r.querySelector<HTMLButtonElement>("[data-testid=automation-enabled]")!).disabled).toBe(true);
    }
  });
});

describe("an automation's page", () => {
  test("an owner who left: the attention callout, Transfer, and the admins as owner", async () => {
    const c = await show(<AutomationPage client={new FixtureClient("a")} orgName="Example" people={PEOPLE} place={{ id: "aut_cost" }}
      onPlace={() => undefined} onBack={() => undefined} />);
    const left = await until(() => c.querySelector("[data-testid=owner-left]"), "the callout");
    expect(left.textContent).toContain("Owner left — transfer it.");
    expect(c.querySelector("[data-testid=owner-row]")?.textContent).toContain("Example's admins");
    await click(c.querySelector("[data-testid=owner-left-transfer]")!);
    const dialog = await until(() => document.querySelector("[role=dialog]"), "the transfer dialog");
    expect(dialog.textContent).toContain("Transfer Weekly cost report");
  });

  test("read-only for someone who cannot edit it: fields disabled, no Save, the owner row says who transfers", async () => {
    const client = new FixtureClient("a");
    const a = await client.automation("aut_qa");
    a.canEdit = false;
    a.canTransfer = false;
    const c = await show(<AutomationPage client={client} orgName="Example" people={PEOPLE} place={{ id: "aut_qa" }}
      onPlace={() => undefined} onBack={() => undefined} />);
    await until(() => c.querySelector("[data-testid=owner-row]"), "the owner row");
    expect(c.querySelector("[data-testid=owner-row]")?.textContent).toContain("Only Example's admins and the owner transfer it.");
    expect(c.querySelectorAll("button[type=submit]").length).toBe(0);
    expect(c.querySelectorAll("[data-testid=run-now]").length).toBe(0);
    // The brief reads, with no Edit; its history opens, with no Restore.
    const doc = c.querySelector<HTMLElement>("[data-testid=role-prompt] [data-testid=automation-prompt-document]")!;
    expect(doc.textContent).toContain("Walk the sign-in and checkout flows.");
    expect(doc.querySelector("[data-testid=markdown-edit]")).toBeNull();
  });

  test("a preset agent shows its role's instructions brief and where its values come from", async () => {
    const c = await show(<AutomationPage client={new FixtureClient("a")} orgName="Example" people={PEOPLE} place={{ id: "aut_qa" }}
      onPlace={() => undefined} onBack={() => undefined} />);
    const brief = await until(() => c.querySelector("[data-testid=role-prompt]"), "the brief");
    expect(brief.textContent).toContain("Tester gets its own prompt, as every task gives it, then these instructions as the task brief.");
    // The fixture overrides only the time limit.
    const field = (k: string) => c.querySelector<HTMLElement>(`[data-testid=preset-${k}]`)!;
    await until(() => (field("harness").textContent?.includes("From Tester ·") ? true : null), "the role's values");
    for (const k of ["harness", "tierId", "machineSizeId", "imageId"]) {
      expect(field(k).querySelector("[data-source=organization]")).not.toBeNull();
    }
    // The fixtures' image library is empty: there is nothing to override the image with.
    for (const k of ["harness", "tierId", "machineSizeId"]) {
      expect(c.querySelector(`[data-testid=preset-${k}-override]`)).not.toBeNull();
    }
    expect(c.querySelector("[data-testid=preset-imageId-override]")).toBeNull();
    expect(field("timeLimitMinutes").textContent).toContain("Overridden");
    expect(field("timeLimitMinutes").textContent).toContain("Tester:");
  });

  test("each preset field is overridden and reset on its own, and Save sends exactly those overrides", async () => {
    const client = new FixtureClient("a");
    const sent: unknown[] = [];
    const update = client.updateAutomation.bind(client);
    client.updateAutomation = (id, input) => { sent.push(input.agent); return update(id, input); };
    const c = await show(<AutomationPage client={client} orgName="Example" people={PEOPLE} place={{ id: "aut_qa" }}
      onPlace={() => undefined} onBack={() => undefined} />);
    await until(() => c.querySelector("[data-testid=preset-harness-override]"), "the override buttons");
    for (const k of ["harness", "tierId", "machineSizeId", "imageId"]) {
      const button = c.querySelector<HTMLElement>(`[data-testid=preset-${k}-override]`);
      if (button) await click(button);
    }
    const timeLimit = c.querySelector<HTMLElement>("[data-testid=preset-timeLimitMinutes]")!;
    await click([...timeLimit.querySelectorAll("button")].find((b) => b.textContent === "Reset")!);
    expect(timeLimit.querySelector("[data-source=organization]")).not.toBeNull();
    for (const k of ["harness", "tierId", "machineSizeId"]) {
      expect(c.querySelector(`[data-testid=preset-${k}]`)!.textContent).toContain("Overridden");
    }
    await click(c.querySelector<HTMLButtonElement>("button[type=submit]")!);
    await until(() => (sent.length ? sent : null), "the save");
    const overrides = (sent[0] as { preset: { overrides: Record<string, unknown> } }).preset.overrides;
    expect(overrides["timeLimitMinutes"]).toBeUndefined();
    expect(overrides["harness"]).toBeDefined();
    expect(overrides["tierId"]).toBeDefined();
    expect(overrides["machineSizeId"]).toBeDefined();
  });
  test("the prompt is the Markdown editor with the automation's variables; a save is a version, and an old one restores", async () => {
    const client = new FixtureClient("a");
    const c = await show(<AutomationPage client={client} orgName="Example" people={PEOPLE} place={{ id: "aut_triage" }}
      onPlace={() => undefined} onBack={() => undefined} />);
    const doc = await until(() => c.querySelector<HTMLElement>("[data-testid=automation-prompt] [data-testid=automation-prompt-document]"), "the prompt");
    expect(doc.textContent).toContain("Read {{pr.title}} in {{repository}} and label it.");
    await click(doc.querySelector<HTMLElement>("[data-testid=markdown-edit]")!);
    // {{ }} offers the automation's variables.
    const trigger = doc.querySelector<HTMLElement>("[data-testid=markdown-variable]")!;
    await act(async () => void trigger.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true, button: 0, pointerType: "mouse" })));
    const items = await until(() => {
      const found = [...document.querySelectorAll<HTMLElement>("[role=menuitem]")];
      return found.length ? found : null;
    }, "the variables");
    expect(items.map((i) => i.textContent)).toEqual(expect.arrayContaining([expect.stringContaining("{{event.name}}"), expect.stringContaining("{{pr.title}}")]));
    await act(async () => { document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })); });
    await typeInto(doc.querySelector<HTMLTextAreaElement>("[data-testid=markdown-source]")!, "Label {{pr.title}} from {{sender}}.");
    await click(doc.querySelector<HTMLElement>("[data-testid=markdown-save]")!);
    await until(() => (promptNow(client) === "Label {{pr.title}} from {{sender}}." ? true : null), "the save");

    let versions = (await client.automationPromptHistory("aut_triage")).versions;
    expect(versions.map((v) => [v.number, v.body])).toEqual([[2, "Label {{pr.title}} from {{sender}}."], [1, "Read {{pr.title}} in {{repository}} and label it."]]);

    await click(await until(() => c.querySelector<HTMLElement>("[data-testid=automation-prompt-history]"), "History"));
    const dialog = await until(() => document.querySelector<HTMLElement>("[data-testid=automation-prompt-history-dialog]"), "the history");
    // Who saved each, and when; the older version, then Restore.
    expect(dialog.querySelector("[data-version='2']")!.textContent).toContain("Márcio");
    await click(dialog.querySelector<HTMLElement>("[data-version='1']")!);
    await click(await until(() => dialog.querySelector<HTMLElement>("[data-testid=prompt-restore]"), "Restore"));
    await until(() => (promptNow(client) === "Read {{pr.title}} in {{repository}} and label it." ? true : null), "the restore");
    versions = (await client.automationPromptHistory("aut_triage")).versions;
    expect(versions[0]).toMatchObject({ number: 3, restoredFrom: 1, current: true });
  });
});

describe("the header's ⋯ menu", () => {
  const openMenu = async (c: HTMLElement) => {
    const more = await until(() => c.querySelector<HTMLElement>("[data-testid=automation-more]"), "the ⋯ button");
    await act(async () => void more.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true, button: 0, pointerType: "mouse" })));
    return until(() => {
      const items = [...document.querySelectorAll<HTMLElement>("[role=menuitem]")];
      return items.length ? items : null;
    }, "the menu");
  };

  test("Delete names the runs that go and the kept session it ends, then goes back to the list", async () => {
    const client = new FixtureClient("a");
    let back = 0;
    const c = await show(<AutomationPage client={client} orgName="Example" people={PEOPLE} place={{ id: "aut_audit" }}
      onPlace={() => undefined} onBack={() => { back++; }} />);
    const items = await openMenu(c);
    expect(items.map((i) => i.textContent)).toEqual(["Duplicate", "Delete…"]);
    await click(items.find((i) => i.textContent === "Delete…")!);
    const says = await until(() => {
      const el = document.querySelector<HTMLElement>("[data-testid=delete-consequences]");
      return el && !el.textContent?.includes("Reading") ? el : null;
    }, "what goes");
    expect(says.textContent).toContain("Its one run goes with it: history, log and payload.");
    expect(says.textContent).toContain("Its kept session is ended");
    await click(document.querySelector<HTMLElement>("[data-testid=delete-confirm]")!);
    await until(() => (back === 1 ? true : null), "back to the list");
    expect((await client.automations()).automations.some((a) => a.id === "aut_audit")).toBe(false);
  });

  test("Duplicate opens the copy: off, '<name> (copy)', owned by the caller", async () => {
    const client = new FixtureClient("a");
    const places: AutomationsPlace[] = [];
    const c = await show(<AutomationPage client={client} orgName="Example" people={PEOPLE} place={{ id: "aut_qa" }}
      onPlace={(p) => places.push(p)} onBack={() => undefined} />);
    const items = await openMenu(c);
    await click(items.find((i) => i.textContent === "Duplicate")!);
    await until(() => (places.length ? places : null), "the copy opened");
    const copy = await client.automation(places[0]!.id!);
    expect(copy).toMatchObject({ name: "Nightly QA pass (copy)", enabled: false, owner: { id: "u_marcio" }, projectId: PROJECT.id,
      agent: { preset: { role: "qa_browser", overrides: { timeLimitMinutes: 120 } }, brief: "Walk the sign-in and checkout flows." } });
  });

  test("someone who cannot edit it sees both disabled", async () => {
    const client = new FixtureClient("a");
    (await client.automation("aut_qa")).canEdit = false;
    const c = await show(<AutomationPage client={client} orgName="Example" people={PEOPLE} place={{ id: "aut_qa" }}
      onPlace={() => undefined} onBack={() => undefined} />);
    const items = await openMenu(c);
    for (const i of items) expect(i.getAttribute("aria-disabled") ?? i.getAttribute("data-disabled")).not.toBeNull();
  });
});

describe("Run now on an event", () => {
  test("a written event: valid JSON and a matching one run; the excluded author disables Run; broken JSON says where", async () => {
    const client = new FixtureClient("a");
    const c = await show(<AutomationPage client={client} orgName="Example" people={PEOPLE} place={{ id: "aut_triage" }}
      onPlace={() => undefined} onBack={() => undefined} />);
    await click(await until(() => c.querySelector("[data-testid=run-now]"), "Run now"));
    const dialog = await until(() => document.querySelector<HTMLElement>("[role=dialog]"), "the dialog");
    const write = [...dialog.querySelectorAll<HTMLButtonElement>("[data-testid=test-mode] button")].find((b) => b.textContent === "Write a test event")!;
    await click(write);
    const editor = await until(() => dialog.querySelector<HTMLTextAreaElement>("[data-testid=test-editor]"), "the editor");
    const run = () => dialog.querySelector<HTMLButtonElement>("[data-testid=test-run]")!;
    const checks = () => dialog.querySelector("[data-testid=test-checks]")!.textContent ?? "";
    await until(() => (checks().includes("it matches.") ? true : null), "it matches");
    expect(checks()).toContain("Valid JSON.");
    expect(run().disabled).toBe(false);

    const skipped = { ...JSON.parse(editor.value), sender: { login: "dependabot[bot]" } };
    await typeInto(editor, JSON.stringify(skipped, null, 2));
    await until(() => (checks().includes("Author") ? true : null), "skipped because of Author");
    expect(checks()).toContain("It would be skipped because of Author: it is by dependabot[bot], who is excluded");
    expect(run().disabled).toBe(true);

    await typeInto(editor, '{\n  "action": "opened"\n  "sender": {}\n}');
    await until(() => (checks().startsWith("Not valid JSON") ? true : null), "invalid");
    expect(checks()).toContain("Filters are checked once the JSON is valid.");
    expect(run().disabled).toBe(true);
  });
});

describe("the test event's helpers", () => {
  test("checkJson says the line and column of what breaks it", () => {
    expect(checkJson('{"a": 1}')).toEqual({ ok: true, value: { a: 1 } });
    const bad = checkJson('{\n  "a": 1\n  "b": 2\n}');
    expect(bad).toEqual({ ok: false, line: 3, column: 3, message: "Expected ',' or '}' after property value" });
    expect(checkJson('{"a": [1, 2,]}')).toMatchObject({ ok: false, line: 1, column: 13, message: "Unexpected token" });
    expect(checkJson('{"a": "x\n"}')).toMatchObject({ ok: false, line: 1, message: "Unterminated string" });
    expect(checkJson("{'a': 1}")).toMatchObject({ ok: false, line: 1, column: 2, message: "Expected a property name in double quotes" });
  });

  test("the template carries the event's main object and the common fields", () => {
    const pr = testTemplate("pull_request", "opened");
    expect(pr).toMatchObject({ action: "opened", repository: { full_name: "acme/web" }, sender: { login: "you" }, pull_request: { number: 1 } });
    expect(testTemplate("push", null)).toMatchObject({ ref: "refs/heads/main" });
    expect("action" in testTemplate("push", null)).toBe(false);
  });
});
