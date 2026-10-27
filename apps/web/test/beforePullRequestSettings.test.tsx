/**
 * Before the pull request on a project's Delivery page: the first row in
 * Review, a Select of its three choices with the picked one's line, its
 * source "Default · Open it" (Reset once changed), saved as only what
 * changed; and no such row on the organisation's page, which has no value
 * for it. Mounted in happy-dom against the fixture client.
 */

import { afterEach, expect, test } from "bun:test";
import type { SettingsPatch, SettingsResponse } from "@dude/domain";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import { act, click, mount, until } from "./dom.ts";
import { FixtureClient } from "../src/fixtures/client.ts";
import { PEOPLE, PROJECT } from "../src/fixtures/data.ts";
import { ProjectSettingsScreen } from "../src/screens/ProjectSettingsScreen.tsx";
import { OrganizationSettingsScreen } from "../src/screens/OrganizationSettingsScreen.tsx";

let mounted: Array<() => Promise<void>> = [];
afterEach(async () => {
  for (const unmount of mounted) await unmount();
  mounted = [];
});

class Recording extends FixtureClient {
  patches: SettingsPatch[] = [];
  set: "open" | "draft" | "ask" | null = null;
  override async projectSettings(projectId: string): Promise<SettingsResponse> {
    const s = await super.projectSettings(projectId);
    return this.set ? { ...s, delivery: { ...s.delivery, pullRequest: { value: this.set, source: "project" } } } : s;
  }
  override async updateProjectSettings(projectId: string, patch: SettingsPatch): Promise<SettingsResponse> {
    this.patches.push(patch);
    if (patch.delivery && "pullRequest" in patch.delivery) this.set = patch.delivery.pullRequest ?? null;
    return this.projectSettings(projectId);
  }
}

async function openProject(client: FixtureClient) {
  const { container, unmount } = await mount(
    <TooltipProvider>
      <ToastProvider>
        <ProjectSettingsScreen client={client} projectId={PROJECT.id} projects={[]} admin page="delivery"
          onPage={() => {}} onChanged={() => {}} onBack={() => {}} onOrganization={() => {}} />
      </ToastProvider>
    </TooltipProvider>,
  );
  mounted.push(unmount);
  return container;
}

test("a project's Delivery page sets what happens before the pull request, from its default", async () => {
  const client = new Recording("a");
  const c = await openProject(client);
  const trigger = await until(() => c.querySelector<HTMLElement>("[data-testid=delivery-pullRequest]"), "the Select");
  expect(c.querySelector('label[for="delivery-pullRequest"]')?.textContent).toContain("Before the pull request");
  expect(trigger.textContent).toContain("Open it");
  const source = c.querySelector<HTMLElement>('[data-source="default"]')!;
  expect(source.textContent).toBe("Default · Open it");
  expect(c.textContent).toContain("When review, simplify and the test pass, Deliver opens the pull request.");
  // The first row of Review.
  const first = c.querySelector("form label");
  expect(first?.getAttribute("for")).toBe("delivery-pullRequest");

  await act(async () => void trigger.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true, button: 0, pointerType: "mouse" })));
  const options = await until(() => (document.querySelectorAll("[role=option]").length === 3 ? document.querySelectorAll<HTMLElement>("[role=option]") : null), "three choices");
  expect([...options].map((o) => o.getAttribute("data-value"))).toEqual(["open", "draft", "ask"]);
  expect(options[1]!.textContent).toContain("CI still runs on most repositories");
  await click([...options].find((o) => o.getAttribute("data-value") === "ask")!);
  expect(c.textContent).toContain("Deliver stops for your review before the pull request");
  await click(c.querySelector<HTMLButtonElement>("[data-testid=delivery-save]")!);
  await until(() => client.patches.length > 0 || null, "the save");
  expect(client.patches).toEqual([{ delivery: { pullRequest: "ask" } }]);

  const reset = await until(() => c.querySelector<HTMLButtonElement>('[data-source="default"] button'), "Reset once changed");
  expect(reset.getAttribute("aria-label")).toBe("Reset to the default");
  await click(reset);
  await until(() => (client.patches.length === 2 ? true : null), "the reset");
  expect(client.patches[1]).toEqual({ delivery: { pullRequest: null } });
});

test("the organisation's Delivery page has no Before the pull request", async () => {
  const client = new FixtureClient("a");
  const { container, unmount } = await mount(
    <TooltipProvider>
      <ToastProvider>
        <OrganizationSettingsScreen client={client} me={PEOPLE.find((p) => p.role === "admin")!} people={PEOPLE}
          onPeopleChanged={() => {}} projects={[]} page="delivery" onPage={() => {}} />
      </ToastProvider>
    </TooltipProvider>,
  );
  mounted.push(unmount);
  await until(() => container.querySelector("[data-testid=delivery-form]"), "the Delivery page");
  expect(container.querySelectorAll("[data-testid=delivery-pullRequest]").length).toBe(0);
  expect(container.textContent).not.toContain("Before the pull request");
});
