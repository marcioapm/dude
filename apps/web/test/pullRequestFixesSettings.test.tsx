/**
 * Fixes on an open pull request on a project's Delivery page: a radio of
 * its two modes, direct chosen from the default, saved as only what
 * changed, Reset once set; and no such row on the organisation's page,
 * which has no value for it. Mounted in happy-dom against the fixture
 * client.
 */

import { afterEach, expect, test } from "bun:test";
import type { SettingsPatch, SettingsResponse } from "@dude/domain";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import { click, mount, until } from "./dom.ts";
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
  set: "direct" | "candidate" | null = null;
  override async projectSettings(projectId: string): Promise<SettingsResponse> {
    const s = await super.projectSettings(projectId);
    return this.set ? { ...s, delivery: { ...s.delivery, pullRequestFixes: { value: this.set, source: "project" } } } : s;
  }
  override async updateProjectSettings(projectId: string, patch: SettingsPatch): Promise<SettingsResponse> {
    this.patches.push(patch);
    if (patch.delivery && "pullRequestFixes" in patch.delivery) this.set = patch.delivery.pullRequestFixes ?? null;
    return this.projectSettings(projectId);
  }
}

const chosen = (group: HTMLElement) => group.querySelector("[role=radio][aria-checked=true]")?.getAttribute("data-value");

test("a project's Delivery page sets how fixes reach an open pull request, direct by default", async () => {
  const client = new Recording("a");
  const { container: c, unmount } = await mount(
    <TooltipProvider>
      <ToastProvider>
        <ProjectSettingsScreen client={client} projectId={PROJECT.id} projects={[]} admin page="delivery"
          onPage={() => {}} onChanged={() => {}} onBack={() => {}} onOrganization={() => {}} />
      </ToastProvider>
    </TooltipProvider>,
  );
  mounted.push(unmount);
  const group = await until(() => c.querySelector<HTMLElement>("[data-testid=delivery-pullRequestFixes]"), "the radio");
  expect(group.getAttribute("role")).toBe("radiogroup");
  expect([...group.querySelectorAll("[role=radio]")].map((o) => o.getAttribute("data-value"))).toEqual(["direct", "candidate"]);
  expect(chosen(group)).toBe("direct");
  await click(group.querySelector<HTMLElement>('[data-value="candidate"]')!);
  expect(chosen(group)).toBe("candidate");
  await click(c.querySelector<HTMLButtonElement>("[data-testid=delivery-save]")!);
  await until(() => client.patches.length > 0 || null, "the save");
  expect(client.patches).toEqual([{ delivery: { pullRequestFixes: "candidate" } }]);

  // Before the pull request is still its default: the one Reset is this row's.
  const reset = await until(() => c.querySelector<HTMLButtonElement>('[data-source="default"] button'), "Reset once changed");
  await click(reset);
  await until(() => (client.patches.length === 2 ? true : null), "the reset");
  expect(client.patches[1]).toEqual({ delivery: { pullRequestFixes: null } });
});

test("the organisation's Delivery page has no Fixes on an open pull request", async () => {
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
  expect(container.querySelectorAll("[data-testid=delivery-pullRequestFixes]").length).toBe(0);
  expect(container.querySelectorAll("[data-testid=delivery-maxPrFixReviewIterations]").length).toBe(1);
});
