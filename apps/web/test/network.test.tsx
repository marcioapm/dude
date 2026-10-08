/**
 * Settings → Network, on an organisation and on a project: what each
 * shows, and what each control changes, against the fixture client, which
 * resolves the lists as the API does. Mounted in happy-dom; controls found
 * by role and label.
 */

import { afterEach, describe, expect, test } from "bun:test";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import type { SettingsPatch, SettingsResponse } from "@dude/domain";
import { act, click, mount, settle, type, until } from "./dom.ts";
import { byLabel, byRole } from "../../../packages/design-system/test/queries.ts";
import { FixtureClient } from "../src/fixtures/client.ts";
import { PROJECT, RUN_ID } from "../src/fixtures/data.ts";
import { RUN_KEY } from "../src/fixtures/scenario.ts";
import { RunScreen } from "../src/screens/RunScreen.tsx";
import { OrganizationSettingsScreen } from "../src/screens/OrganizationSettingsScreen.tsx";
import { ProjectSettingsScreen } from "../src/screens/ProjectSettingsScreen.tsx";

let mounted: Array<() => Promise<void>> = [];
afterEach(async () => {
  for (const unmount of mounted) await unmount();
  mounted = [];
  document.body.innerHTML = "";
});

/** Records the patches the page sent; read-only when asked, as a member's settings say. */
class Recording extends FixtureClient {
  patches: SettingsPatch[] = [];
  allowed: string[][] = [];
  readOnly = false;
  #view(s: SettingsResponse): SettingsResponse {
    return this.readOnly ? { ...s, canEdit: false } : s;
  }
  override async organizationSettings() {
    return this.#view(await super.organizationSettings());
  }
  override async projectSettings(projectId: string) {
    return this.#view(await super.projectSettings(projectId));
  }
  override async updateOrganizationSettings(patch: SettingsPatch) {
    this.patches.push(patch);
    return super.updateOrganizationSettings(patch);
  }
  override async updateProjectSettings(projectId: string, patch: SettingsPatch) {
    this.patches.push(patch);
    return super.updateProjectSettings(projectId, patch);
  }
  override async allowNames(projectId: string, names: string[]) {
    this.allowed.push(names);
    return super.allowNames(projectId, names);
  }
}

const chips = (scope: Element, testId: string) =>
  [...scope.querySelectorAll(`[data-testid=${testId}] [data-host]`)].map((c) => c.getAttribute("data-host"));
const page = (c: HTMLElement) => until(() => c.querySelector<HTMLElement>("[data-testid=network-page]"), "the Network page");

async function organisation(client = new Recording("a")) {
  const { container, unmount } = await mount(
    <TooltipProvider><ToastProvider>
      <OrganizationSettingsScreen client={client} me={null} people={[]} onPeopleChanged={() => {}} projects={[]} page="network" onPage={() => {}} />
    </ToastProvider></TooltipProvider>,
  );
  mounted.push(unmount);
  return { container: await page(container), client };
}

async function project(client = new Recording("a"), onOrganization: (p: string) => void = () => {}) {
  const { container, unmount } = await mount(
    <TooltipProvider><ToastProvider>
      <ProjectSettingsScreen client={client} projectId={PROJECT.id} projects={[]} admin page="network"
        onPage={() => {}} onChanged={() => {}} onBack={() => {}} onOrganization={onOrganization} />
    </ToastProvider></TooltipProvider>,
  );
  mounted.push(unmount);
  return { container: await page(container), client };
}

describe("an organisation's Network page", () => {
  test("lists its hosts, saves one typed and a preset's, and ticks what the list has", async () => {
    const { container, client } = await organisation();
    expect(chips(container, "network-egress")).toEqual(["github.com", "*.github.com", "objects.githubusercontent.com"]);
    expect(chips(container, "network-always")).toEqual(["llm.example", "dude’s tools"]);
    expect(container.querySelector("[data-preset=GitHub]")?.getAttribute("data-has")).toBe("true");
    const field = byLabel(container, "Add a host agents may reach") as HTMLInputElement;
    await type(field, "mirror.internal");
    await act(async () => void field.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true })));
    await settle(150);
    await click(container.querySelector("[data-preset=PyPI]")!);
    await settle(150);
    expect(client.patches.map((p) => p.network?.egress)).toEqual([
      ["github.com", "*.github.com", "objects.githubusercontent.com", "mirror.internal"],
      ["github.com", "*.github.com", "objects.githubusercontent.com", "mirror.internal", "pypi.org", "files.pythonhosted.org"],
    ]);
    expect(container.querySelector("[data-preset=PyPI]")?.getAttribute("data-has")).toBe("true");
  });

  test("a wildcard lux refuses stays in the field, saying why, and is not saved", async () => {
    const { container, client } = await organisation();
    const field = byLabel(container, "Add a host agents may reach") as HTMLInputElement;
    await type(field, "*.com");
    await act(async () => void field.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true })));
    expect(container.textContent).toContain("at least two labels");
    expect(client.patches).toEqual([]);
  });

  test("Anywhere writes * and says what it means; an empty list says agents reach only their model", async () => {
    const { container, client } = await organisation();
    expect(container.querySelector("[data-testid=network-anywhere]")).toBeNull();
    await click(container.querySelector("[data-testid=network-anywhere-switch]")!);
    await settle(150);
    expect(client.patches.at(-1)?.network?.egress?.[0]).toBe("*");
    expect(container.querySelector("[data-testid=network-anywhere]")?.textContent).toContain("Agents may reach any host");
    await click(container.querySelector("[data-testid=network-anywhere-switch]")!);
    await settle(150);
    for (const host of ["github.com", "*.github.com", "objects.githubusercontent.com"]) {
      await click(byRole(container, "button", `Remove ${host}`));
      await settle(120);
    }
    expect(container.querySelector("[data-testid=network-empty]")?.textContent).toContain("Agents can reach only their model.");
  });
});

describe("a project's Network page", () => {
  test("shows its organisation's hosts read-only, From the organisation with the way there, and its own removable", async () => {
    const went: string[] = [];
    const { container, client } = await project(new Recording("a"), (p) => went.push(p));
    expect(chips(container, "network-org-egress")).toEqual(["github.com", "*.github.com", "objects.githubusercontent.com"]);
    expect(container.querySelector("[data-testid=network-org-egress] button")).toBeNull();
    expect(container.querySelector("[data-testid=network-from]")?.textContent).toContain("From Example · change in Example’s settings");
    await click(byRole(container, "button", "change in Example’s settings"));
    expect(went).toEqual(["network"]);
    // GitHub is the organisation's already: ticked here too; npm adds to the project's.
    expect(container.querySelector("[data-preset=GitHub]")?.getAttribute("data-has")).toBe("true");
    await click(container.querySelector("[data-preset=npm]")!);
    await settle(150);
    expect(client.patches).toEqual([{ network: { egress: ["registry.npmjs.org"] } }]);
    expect(chips(container, "network-egress")).toEqual(["registry.npmjs.org"]);
    expect(byRole(container, "button", "Remove registry.npmjs.org")).toBeTruthy();
  });

  test("only its own list is an override, with what it overrides and Reset", async () => {
    const { container, client } = await project();
    expect(container.textContent).not.toContain("Overridden");
    await click(container.querySelector("[data-testid=network-only-switch]")!);
    await settle(150);
    expect(client.patches).toEqual([{ network: { mode: "only" } }]);
    expect(container.textContent).toContain("OverriddenExample: 3 hosts");
    expect(container.querySelector("[data-testid=network-org-egress]")).toBeNull();
    await click(byRole(container, "button", "Reset"));
    await settle(150);
    expect(client.patches.at(-1)).toEqual({ network: { mode: "add", egress: [] } });
    expect(container.textContent).not.toContain("Overridden");
  });

  test("Refused recently lists what agents were refused; Allow adds it, and it goes", async () => {
    const { container, client } = await project();
    const row = await until(() => container.querySelector("[data-testid=network-refused-row]"), "the refused row");
    expect([...row.querySelectorAll("[data-refused]")].map((r) => r.getAttribute("data-refused"))).toEqual(["files.pythonhosted.org", "registry.npmjs.org"]);
    await click(row.querySelector("[data-refused='registry.npmjs.org'] button")!);
    await settle(200);
    expect(client.allowed).toEqual([["registry.npmjs.org"]]);
    expect(chips(container, "network-egress")).toEqual(["registry.npmjs.org"]);
    await until(() => container.querySelector("[data-refused='registry.npmjs.org']") === null || null, "the allowed host to go");
    await click(byRole(container, "button", "Allow all 1"));
    await settle(200);
    expect(container.querySelector("[data-testid=network-refused-row]")).toBeNull();
  });

  test("a member who may not change it sees everything, disabled", async () => {
    const client = new Recording("a");
    client.readOnly = true;
    await client.updateProjectSettings(PROJECT.id, { network: { egress: ["proxy.golang.org"] } });
    client.patches = [];
    const { container } = await project(client);
    expect(chips(container, "network-egress")).toEqual(["proxy.golang.org"]);
    expect(container.querySelector("[data-testid=network-page] input")).toBeNull();
    expect([...container.querySelectorAll<HTMLButtonElement>("[data-preset]")].every((b) => b.disabled)).toBe(true);
    expect(container.querySelector<HTMLButtonElement>("[data-testid=network-only-switch]")!.disabled).toBe(true);
    const row = await until(() => container.querySelector("[data-testid=network-refused-row]"), "the refused row");
    expect(row.querySelector("button")).toBeNull();
  });
});

describe("in a Run", () => {
  /** The fixture Run whose uv sync was refused files.pythonhosted.org. */
  function refusedRun() {
    localStorage.setItem(RUN_KEY, "refused");
    return new Recording("a");
  }
  async function run(client: Recording) {
    const { container, unmount } = await mount(<TooltipProvider><ToastProvider><RunScreen client={client} runId={RUN_ID} onBack={() => {}} /></ToastProvider></TooltipProvider>);
    mounted.push(async () => {
      await unmount();
      localStorage.clear();
    });
    return container;
  }

  test("a call whose output names a host lux refused says so under it; Allow adds it for the project", async () => {
    const client = refusedRun();
    const container = await run(client);
    const note = await until(() => container.querySelector("[data-testid=network-refused]"), "the note");
    expect(note.closest("[data-tool]")?.getAttribute("data-tool")).toBe("bash");
    expect(note.textContent).toContain("Network refused: files.pythonhosted.org is not on web-console’s list, nor Example’s.");
    // One note, on the call that names it: not on the others.
    expect(container.querySelectorAll("[data-testid=network-refused]").length).toBe(1);
    await click(byRole(note as HTMLElement, "button", "Allow for web-console"));
    await until(() => container.querySelector("[data-testid=network-allowed]"), "Allowed");
    expect(client.allowed).toEqual([["files.pythonhosted.org"]]);
    expect(container.querySelector("[data-testid=network-allow]")).toBeNull();
  });

  test("someone who may not change the project's list gets the note and Settings, no Allow", async () => {
    const client = refusedRun();
    client.readOnly = true;
    const container = await run(client);
    const note = await until(() => container.querySelector("[data-testid=network-refused]"), "the note");
    expect([...note.querySelectorAll("button")].map((b) => b.textContent)).toEqual(["Settings"]);
  });
});
