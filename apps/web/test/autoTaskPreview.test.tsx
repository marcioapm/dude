/**
 * Automatic task previews (migration 272) in the web app: the project's
 * row in Settings → Servers → Branch previews (off by default; on, with a
 * calm hint pointing to Definitions when no server starts in previews, or
 * none is defined; saveable either way), and the task's own choice in the
 * Overview rail (Use project default, saying what that is, On, Off; what
 * is in effect). Mounted in happy-dom against the fixture client.
 */

import { afterEach, expect, test } from "bun:test";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import type { PreviewSettings, Recipe } from "@dude/domain";
import { click, mount, settle, until } from "./dom.ts";
import { accessibleName, allByRole, byRole } from "../../../packages/design-system/test/queries.ts";
import { FixtureClient } from "../src/fixtures/client.ts";
import { PROJECT, TASK_ID } from "../src/fixtures/data.ts";
import type { Task, TaskDetail, TaskFields } from "../src/api/client.ts";
import { PeopleProvider } from "../src/people.tsx";
import { ProjectSettingsScreen } from "../src/screens/ProjectSettingsScreen.tsx";
import { TaskScreen } from "../src/screens/TaskScreen.tsx";
import { AutoPreviewControl, autoPreviewInEffect } from "../src/screens/AutoPreviewControl.tsx";

let unmount: (() => Promise<void>) | undefined;
const remount = async () => { await unmount?.(); unmount = undefined; document.body.innerHTML = ""; };
afterEach(remount);

class Recording extends FixtureClient {
  saved: PreviewSettings[] = [];
  recipes: Recipe[] | null = null;
  override async projectServers() {
    const got = await super.projectServers();
    return this.recipes ? { ...got, servers: this.recipes } : got;
  }
  override async updatePreviewSettings(id: string, settings: PreviewSettings) {
    this.saved.push(settings);
    return super.updatePreviewSettings(id, settings);
  }
}

async function serversPage(client: FixtureClient) {
  const mounted = await mount(<TooltipProvider><ToastProvider>
    <ProjectSettingsScreen client={client} projectId={PROJECT.id} projects={[]} admin page="servers"
      onPage={() => {}} onChanged={() => {}} onBack={() => {}} onOrganization={() => {}} />
  </ToastProvider></TooltipProvider>);
  unmount = mounted.unmount;
  return until(() => mounted.container.querySelector<HTMLElement>("[data-testid=auto-task-preview-row]"), "the row");
}

async function pick(root: HTMLElement, label: string, option: string) {
  await click(byRole(root, "combobox", label));
  await until(() => allByRole(document.body, "option").length > 0 || undefined, "options");
  await click(byRole(document.body, "option", option));
}

const hint = (row: HTMLElement) => row.querySelector<HTMLElement>("[data-testid=auto-task-preview-hint]");

test("the project's automatic task previews are off by default, and turned on and off again from Servers", async () => {
  const client = new Recording("a");
  const row = await serversPage(client);
  expect(row.textContent).toContain("Automatic task previews");
  expect(row.textContent).toContain("when dude opens or reopens the task’s pull request");
  expect(byRole(row, "combobox", "Automatic task previews").textContent).toContain("Off");
  expect(hint(row)).toBeNull();

  await pick(row, "Automatic task previews", "On");
  await until(() => byRole(row, "combobox", "Automatic task previews").textContent?.includes("On") &&
    !byRole<HTMLButtonElement>(row, "combobox", "Automatic task previews").disabled || undefined, "saved on");
  // The fixture's web server starts in previews: nothing to point at.
  expect(hint(row)).toBeNull();
  await pick(row, "Automatic task previews", "Off");
  await until(() => client.saved.length === 2 || undefined, "saved off");
  // Saved with the other settings as they were, only this changed.
  expect(client.saved.map((s) => s.autoTaskPreview)).toEqual([true, false]);
  expect(client.saved[0]!.idleTimeoutMinutes).toBe(15);
});

test("on with no server starting in previews: a hint to Definitions, and it saves anyway", async () => {
  const client = new Recording("a");
  client.recipes = (await new FixtureClient("a").projectServers()).servers.map((r) => ({ ...r, autostartInPreviews: false }));
  const row = await serversPage(client);
  expect(hint(row)).toBeNull();
  await pick(row, "Automatic task previews", "On");
  const shown = await until(() => hint(row) ?? undefined, "the hint");
  expect(shown.textContent).toContain("Choose a server to start in previews");
  const link = byRole(shown, "link", "Definitions");
  expect(link.getAttribute("href")).toBe("#definitions");
  expect(document.getElementById("definitions")).not.toBeNull();
  expect(client.saved.at(-1)?.autoTaskPreview).toBe(true);
  // No warning, no refusal: a hint in the row's help.
  expect(row.querySelector("[role=alert]")).toBeNull();
});

test("on with no definitions at all: a hint to add one", async () => {
  const client = new Recording("a");
  client.recipes = [];
  await client.updatePreviewSettings(PROJECT.id, { ...(await client.projectServers()).previews, autoTaskPreview: true });
  const row = await serversPage(client);
  expect(byRole(row, "combobox", "Automatic task previews").textContent).toContain("On");
  expect(hint(row)?.textContent).toContain("Add a server that starts in previews");
});

class TaskRecording extends FixtureClient {
  changes: Array<Partial<TaskFields>> = [];
  override async updateTask(id: string, changes: Partial<TaskFields>): Promise<Task> {
    this.changes.push(changes);
    return { id } as Task;
  }
}

type RowTask = Pick<TaskDetail, "id" | "autoTaskPreview" | "projectAutoTaskPreview"> & { status?: TaskDetail["status"] };

async function taskRow(client: FixtureClient, given: RowTask) {
  const task = { status: "running" as const, ...given };
  const mounted = await mount(<TooltipProvider><ToastProvider>
    <AutoPreviewControl client={client} task={task} onChanged={() => {}} />
  </ToastProvider></TooltipProvider>);
  unmount = mounted.unmount;
  return mounted.container.querySelector<HTMLElement>("[data-testid=task-auto-preview]")!;
}

// The in-effect line, the end of the choice's hint.
const effect = (row: HTMLElement) => /Effective: .*$/.exec(row.textContent ?? "")?.[0] ?? "";

test("a task following its project says which way the project goes, and what is in effect", async () => {
  for (const project of [false, true]) {
    const row = await taskRow(new TaskRecording("a"), { id: "wi_1", autoTaskPreview: null, projectAutoTaskPreview: project });
    const select = byRole(row, "combobox", "Automatic preview");
    expect(select.textContent).toBe(`Use project default (${project ? "On" : "Off"})`);
    expect(effect(row)).toBe(`Effective: ${project ? "On" : "Off"} · project default`);
    expect(row.textContent).toContain("when it opens or reopens the pull request");
    expect(row.textContent).toContain("A preview someone asks for still starts.");
    await click(select);
    const options = await until(() => { const o = allByRole(document.body, "option"); return o.length ? o : undefined; }, "options");
    expect(options.map(accessibleName)).toEqual([`Use project default (${project ? "On" : "Off"})`, "On", "Off"]);
    await unmount?.();
    unmount = undefined;
    document.body.innerHTML = "";
  }
});

test("a task's own On or Off wins over its project's, and each choice is saved as true, false or null", async () => {
  const off = await taskRow(new TaskRecording("a"), { id: "wi_1", autoTaskPreview: false, projectAutoTaskPreview: true });
  expect(byRole(off, "combobox", "Automatic preview").textContent).toBe("Off");
  expect(effect(off)).toBe("Effective: Off · task override");
  await unmount?.();
  document.body.innerHTML = "";

  const client = new TaskRecording("a");
  const row = await taskRow(client, { id: "wi_1", autoTaskPreview: true, projectAutoTaskPreview: false });
  expect(byRole(row, "combobox", "Automatic preview").textContent).toBe("On");
  expect(effect(row)).toBe("Effective: On · task override");
  await pick(row, "Automatic preview", "Off");
  await until(() => client.changes.length === 1 || undefined, "saved");
  expect(effect(row)).toBe("Effective: Off · task override");
  await pick(row, "Automatic preview", "Use project default (Off)");
  await until(() => client.changes.length === 2 || undefined, "saved");
  expect(effect(row)).toBe("Effective: Off · project default");
  expect(client.changes).toEqual([{ autoTaskPreview: false }, { autoTaskPreview: null }]);
});

test("on a task that is done the choice is disabled, saying why; a failed or aborted one can come back, so it still takes it", async () => {
  const done = await taskRow(new TaskRecording("a"), { id: "wi_1", status: "done", autoTaskPreview: true, projectAutoTaskPreview: false });
  expect(byRole<HTMLButtonElement>(done, "combobox", "Automatic preview: On. The task is done.").disabled).toBe(true);
  expect(done.textContent).toContain("The task is done.");
  await remount();
  for (const status of ["failed", "aborted"] as const) {
    const client = new TaskRecording("a");
    const row = await taskRow(client, { id: "wi_1", status, autoTaskPreview: true, projectAutoTaskPreview: false });
    await pick(row, "Automatic preview", "Off");
    await until(() => client.changes.length === 1 || undefined, "saved");
    expect(client.changes).toEqual([{ autoTaskPreview: false }]);
    await remount();
  }
});

class TaskOverview extends TaskRecording {
  constructor(private patch: Partial<TaskDetail>) {
    super("a");
  }
  override async getTask(id: string): Promise<TaskDetail> {
    return { ...(await super.getTask(id)), ...this.patch } as TaskDetail;
  }
}

test("the task's Overview shows the choice in its This task block, and saves it", async () => {
  const client = new TaskOverview({ status: "running", autoTaskPreview: null, projectAutoTaskPreview: true });
  const mounted = await mount(<TooltipProvider><ToastProvider><PeopleProvider client={client}>
    <TaskScreen client={client} taskId={TASK_ID} onOpenRun={() => {}} onBack={() => {}} />
  </PeopleProvider></ToastProvider></TooltipProvider>);
  unmount = mounted.unmount;
  const block = await until(() => mounted.container.querySelector<HTMLElement>("section[aria-label='This task']"), "This task");
  await settle();
  const row = await until(() => block.querySelector<HTMLElement>("[data-testid=task-auto-preview]"), "the automatic preview choice");
  expect(byRole(row, "combobox", "Automatic preview").textContent).toBe("Use project default (On)");
  expect(effect(row)).toBe("Effective: On · project default");
  await pick(row, "Automatic preview", "Off");
  await until(() => client.changes.length === 1 || undefined, "saved");
  expect(client.changes).toEqual([{ autoTaskPreview: false }]);
});

test("in effect: the task's own choice, else the project's", () => {
  expect([autoPreviewInEffect(null, true), autoPreviewInEffect(null, false), autoPreviewInEffect(false, true), autoPreviewInEffect(true, false)])
    .toEqual([true, false, false, true]);
});
