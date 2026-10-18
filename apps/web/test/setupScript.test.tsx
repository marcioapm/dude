/**
 * A repository's setup script in Project settings → Repositories: the
 * `setup` mark after a name that has one, the row menu's Setup script…,
 * and the dialog — the server's byte limit, blank saved as none, the
 * server's refusal, hook-bypass warnings, and a reader's read-only view.
 * Mounted in happy-dom against the fixture client; text reaches the
 * editor as typing does, through CodeMirror's own DOM observer.
 */

import { afterEach, expect, test } from "bun:test";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import { SETUP_SCRIPT_MAX_BYTES, SETUP_SCRIPT_MESSAGE, type SettingsResponse } from "@dude/domain";
import { act, click, mount, settle, until } from "./dom.ts";
import { ApiError, type ProjectDetail, type Repository } from "../src/api/client.ts";
import { FixtureClient } from "../src/fixtures/client.ts";
import { PROJECT } from "../src/fixtures/data.ts";
import { ProjectSettingsScreen } from "../src/screens/ProjectSettingsScreen.tsx";
import { lintSetupScript } from "../src/setupScript.ts";

let mounted: Array<() => Promise<void>> = [];
afterEach(async () => {
  for (const unmount of mounted) await unmount();
  mounted = [];
  document.body.innerHTML = "";
});

const SCRIPT = "#!/usr/bin/env bash\nset -euo pipefail\nnpm ci --prefer-offline\n";

/** The fixture project, web-console with a script and sdk-js without; every PATCH recorded, refused when told to. */
class ScriptClient extends FixtureClient {
  patches: Array<{ id: string; changes: Record<string, unknown> }> = [];
  refuse: string | null = null;
  constructor(readonly canEdit: boolean, script: string | null = SCRIPT) {
    super("a");
    this.project = {
      ...PROJECT,
      repositories: PROJECT.repositories.map((r) => (r.name === "web-console" ? { ...r, setupScript: script } : { ...r, setupScript: null })),
    };
  }
  project: ProjectDetail;
  override getProject(): Promise<ProjectDetail> {
    return Promise.resolve(this.project);
  }
  override async projectSettings(id?: string): Promise<SettingsResponse> {
    const s = await super.projectSettings(id);
    return { ...s, canEdit: this.canEdit };
  }
  override updateRepository(id: string, changes: Partial<Omit<Repository, "id" | "projectId">>): Promise<Repository> {
    this.patches.push({ id, changes });
    if (this.refuse) return Promise.reject(new ApiError(400, "validation_failed", this.refuse));
    const repo = { ...this.project.repositories.find((r) => r.id === id)!, ...changes };
    this.project = { ...this.project, repositories: this.project.repositories.map((r) => (r.id === id ? repo : r)) };
    return Promise.resolve(repo);
  }
}

async function open(client: ScriptClient): Promise<HTMLElement> {
  const { container, unmount } = await mount(
    <TooltipProvider>
      <ToastProvider>
        <ProjectSettingsScreen client={client} projectId={PROJECT.id} projects={[]} admin={client.canEdit} page="repositories"
          onPage={() => {}} onChanged={() => {}} onBack={() => {}} onOrganization={() => {}} />
      </ToastProvider>
    </TooltipProvider>,
  );
  mounted.push(unmount);
  await until(() => container.querySelector("[data-testid=repository-row-sdk-js]"), "the repositories table");
  return container;
}

const press = (el: Element) =>
  act(async () => void el.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true, button: 0, pointerType: "mouse" })));

/** Opens the row menu of `name` and picks Setup script…; the dialog it opens. */
async function fromMenu(page: HTMLElement, name: string): Promise<HTMLElement> {
  await press(page.querySelector(`[aria-label="Actions for ${name}"]`)!);
  const item = await until(() => [...document.querySelectorAll<HTMLElement>("[role=menuitem]")].find((i) => i.textContent === "Setup script…"), "Setup script…");
  await click(item);
  return dialog();
}

const dialog = () => until(() => document.querySelector<HTMLElement>("[data-testid=setup-script-dialog]")?.closest<HTMLElement>("[role=dialog]"), "the setup script dialog");
const editor = () => until(() => document.querySelector<HTMLElement>("[data-testid=setup-script-editor] .cm-content"), "the loaded editor");
const saveButton = () => document.querySelector<HTMLButtonElement>("[data-testid=setup-script-save]")!;

/** Replace the editor's text as typing would: its lines rewritten in the DOM, read back by CodeMirror. */
async function setText(text: string) {
  const content = await editor();
  await act(async () => {
    const lines = text.split("\n").map((l) => {
      const div = document.createElement("div");
      div.className = "cm-line";
      div.textContent = l;
      return div;
    });
    content.replaceChildren(...lines);
  });
  await settle(120);
}

test("a repository with a script carries a mark that names it; one without has none", async () => {
  const page = await open(new ScriptClient(true));
  const marks = [...page.querySelectorAll<HTMLButtonElement>("[data-testid=setup-script-mark]")];
  expect(marks.length).toBe(1);
  expect(marks[0]!.closest("[data-testid=repository-row-web-console]")).not.toBeNull();
  expect(marks[0]!.tagName).toBe("BUTTON");
  expect(marks[0]!.getAttribute("aria-label")).toBe("Edit web-console’s setup script, 3 lines");
});

test("Setup script… in the row menu opens the script; the mark opens the same dialog", async () => {
  const client = new ScriptClient(true);
  const page = await open(client);
  const d = await fromMenu(page, "sdk-js");
  expect(d.querySelector("h2")?.textContent).toBe("Setup script for sdk-js");
  expect(d.querySelector("[data-testid=size-meter]")?.textContent).toBe(`0 B of ${SETUP_SCRIPT_MAX_BYTES / 1024} KB`);
  // The help states the runtime's limit, not a guess.
  expect(d.textContent).toContain("Time limit: 15 minutes.");
  // An unchanged script has nothing to save.
  expect(saveButton().disabled).toBe(true);
  await click([...d.querySelectorAll("button")].find((b) => b.textContent === "Cancel")!);
  await settle();
  await click(page.querySelector("[data-testid=setup-script-mark]")!);
  const again = await dialog();
  expect(again.querySelector("h2")?.textContent).toBe("Setup script for web-console");
  expect((await editor()).textContent).toContain("npm ci --prefer-offline");
});

test("Save sends the script as written, and the row's mark follows", async () => {
  const client = new ScriptClient(true);
  const page = await open(client);
  await fromMenu(page, "sdk-js");
  await setText("make deps");
  expect(saveButton().disabled).toBe(false);
  await click(saveButton());
  await until(() => client.patches.length > 0 || null, "the PATCH");
  expect(client.patches).toEqual([{ id: "repo_sdk", changes: { setupScript: "make deps" } }]);
  const mark = await until(() => page.querySelector("[data-testid=repository-row-sdk-js] [data-testid=setup-script-mark]"), "sdk-js's mark");
  expect(mark.getAttribute("aria-label")).toBe("Edit sdk-js’s setup script, 1 line");
});

test("a script cleared to blank saves as none, and the mark goes", async () => {
  const client = new ScriptClient(true);
  const page = await open(client);
  await fromMenu(page, "web-console");
  await setText("   ");
  expect(saveButton().textContent).toBe("Remove script");
  await click(saveButton());
  await until(() => client.patches.length > 0 || null, "the PATCH");
  expect(client.patches).toEqual([{ id: "repo_wc", changes: { setupScript: null } }]);
  await until(() => page.querySelectorAll("[data-testid=setup-script-mark]").length === 0 || null, "the mark gone");
});

test("over the server's byte limit, counted in UTF-8, the meter is danger and Save is disabled", async () => {
  const page = await open(new ScriptClient(true));
  const d = await fromMenu(page, "sdk-js");
  // Each é is two bytes: half the limit in characters is exactly the limit.
  await setText("é".repeat(SETUP_SCRIPT_MAX_BYTES / 2));
  expect(d.querySelector("[data-testid=size-meter]")?.getAttribute("data-level")).toBe("attention");
  expect(saveButton().disabled).toBe(false);
  await setText("é".repeat(SETUP_SCRIPT_MAX_BYTES / 2) + "#");
  expect(d.querySelector("[data-testid=size-meter]")?.getAttribute("data-level")).toBe("danger");
  expect(d.querySelector("[data-testid=setup-script-invalid]")?.textContent).toBe(
    "16 KB is over the 16 KB limit. Keep long files in the repository and have the script use them.");
  expect(saveButton().disabled).toBe(true);
});

test("the server's refusal is a danger callout in the dialog, which stays open", async () => {
  const client = new ScriptClient(true);
  client.refuse = `request body failed validation: setupScript: ${SETUP_SCRIPT_MESSAGE}`;
  const page = await open(client);
  const d = await fromMenu(page, "sdk-js");
  await setText("echo hi");
  await click(saveButton());
  const callout = await until(() => d.querySelector<HTMLElement>("[data-tone=danger][role=alert]"), "the refusal");
  expect(callout.textContent).toContain(SETUP_SCRIPT_MESSAGE);
  expect(document.querySelector("[data-testid=setup-script-dialog]")).not.toBeNull();
});

test("a line that bypasses hooks is a warning that does not block Save", async () => {
  const page = await open(new ScriptClient(true));
  const d = await fromMenu(page, "sdk-js");
  await setText("git config core.hooksPath scripts/hooks\ngit commit --no-verify -m x\nHUSKY=0 npm ci\n# core.hooksPath in a comment is fine");
  expect(d.querySelector("[data-testid=setup-script-bypass-count]")?.textContent).toBe("3 lines bypass hooks");
  expect(d.querySelectorAll("[data-testid=setup-script-warnings] li").length).toBe(3);
  expect(saveButton().disabled).toBe(false);
});

test("lintSetupScript marks each bypass at its words, and only outside a comment line", () => {
  expect(lintSetupScript("cd a && git config core.hooksPath h").map(({ line, from, to, severity }) => ({ line, from, to, severity })))
    .toEqual([{ line: 1, from: 19, to: 33, severity: "warning" }]);
  expect(lintSetupScript("x\n  HUSKY=0 npm ci").map((d) => [d.line, d.from, d.to])).toEqual([[2, 2, 9]]);
  expect(lintSetupScript("git push --no-verify")).toHaveLength(1);
  expect(lintSetupScript("  # git config core.hooksPath x")).toEqual([]);
  expect(lintSetupScript("HUSKY=1 npm ci\necho --no-verifying")).toEqual([]);
});

test("a reader gets no row menu, and the mark opens the script read-only with Close", async () => {
  const client = new ScriptClient(false);
  const page = await open(client);
  expect(page.querySelectorAll('[aria-label^="Actions for"]').length).toBe(0);
  const mark = page.querySelector<HTMLButtonElement>("[data-testid=setup-script-mark]")!;
  expect(mark.getAttribute("aria-label")).toBe("Read web-console’s setup script, 3 lines");
  await click(mark);
  const d = await dialog();
  const content = await editor();
  expect(content.getAttribute("contenteditable")).toBe("false");
  expect(d.textContent).toContain("read-only");
  expect(d.querySelector("[data-testid=setup-script-save]")).toBeNull();
  await click(d.querySelector("[data-testid=setup-script-close]")!);
  await settle();
  expect(document.querySelector("[data-testid=setup-script-dialog]")).toBeNull();
  expect(client.patches).toEqual([]);
});
