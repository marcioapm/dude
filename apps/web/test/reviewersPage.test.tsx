/**
 * The Delivery page, on a project and on the organisation, lists every
 * reviewer as a checkbox, the documentation reviewer among them, unticked
 * unless required; ticking it saves it as a required reviewer at that level.
 * Mounted in happy-dom against the fixture client.
 */

import { afterEach, expect, test } from "bun:test";
import type { ReactNode } from "react";
import type { SettingsPatch, SettingsResponse } from "@dude/domain";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import { act, click, facetCheckbox, mount, until } from "./dom.ts";
import { FixtureClient } from "../src/fixtures/client.ts";
import { PROJECT } from "../src/fixtures/data.ts";
import { OrganizationSettingsScreen } from "../src/screens/OrganizationSettingsScreen.tsx";
import { ProjectSettingsScreen } from "../src/screens/ProjectSettingsScreen.tsx";

let mounted: Array<() => Promise<void>> = [];
afterEach(async () => {
  for (const unmount of mounted) await unmount();
  mounted = [];
});

class Recording extends FixtureClient {
  projectPatches: SettingsPatch[] = [];
  organizationPatches: SettingsPatch[] = [];
  override async updateProjectSettings(projectId: string, patch: SettingsPatch): Promise<SettingsResponse> {
    this.projectPatches.push(patch);
    return this.projectSettings(projectId);
  }
  override async updateOrganizationSettings(patch: SettingsPatch): Promise<SettingsResponse> {
    this.organizationPatches.push(patch);
    return this.organizationSettings();
  }
}

async function tickDocumentationAndSave(screen: ReactNode, saved: () => SettingsPatch[]) {
  const { container, unmount } = await mount(
    <TooltipProvider>
      <ToastProvider>{screen}</ToastProvider>
    </TooltipProvider>,
  );
  mounted.push(unmount);
  const form = await until(() => container.querySelector("[data-testid=delivery-form]"), "the delivery form");
  const documentation = facetCheckbox(form, "Documentation");
  expect(documentation.getAttribute("aria-checked")).toBe("false");

  await click(documentation);
  await click(container.querySelector("[data-testid=delivery-save]")!);
  await until(() => saved().length > 0 || null, "the save");
}

test("the project's Delivery page offers the documentation reviewer, off until ticked", async () => {
  const client = new Recording("a");
  await tickDocumentationAndSave(
    <ProjectSettingsScreen client={client} projectId={PROJECT.id} projects={[]} admin page="delivery"
      onPage={() => {}} onChanged={() => {}} onBack={() => {}} onOrganization={() => {}} />,
    () => client.projectPatches,
  );
  expect(client.projectPatches).toEqual([{ delivery: { requiredReviewers: ["correctness", "documentation"] } }]);
  expect(client.organizationPatches).toEqual([]);
});

test("the organisation's Delivery page offers the documentation reviewer, off until ticked", async () => {
  const client = new Recording("a");
  await tickDocumentationAndSave(
    <OrganizationSettingsScreen client={client} me={null} people={[]} onPeopleChanged={() => {}} projects={[]}
      page="delivery" onPage={() => {}} />,
    () => client.organizationPatches,
  );
  expect(client.organizationPatches).toEqual([{ delivery: { requiredReviewers: ["correctness", "documentation"] } }]);
  expect(client.projectPatches).toEqual([]);
});

// The second reviewer row is the reviewers every round runs: unticking
// correctness there, and listing the non-product paths, saves both.
test("the project's Delivery page saves the reviewers every round runs and the non-product paths", async () => {
  const client = new Recording("a");
  const { container, unmount } = await mount(
    <TooltipProvider>
      <ToastProvider>
        <ProjectSettingsScreen client={client} projectId={PROJECT.id} projects={[]} admin page="delivery"
          onPage={() => {}} onChanged={() => {}} onBack={() => {}} onOrganization={() => {}} />
      </ToastProvider>
    </TooltipProvider>,
  );
  mounted.push(unmount);
  const form = await until(() => container.querySelector("[data-testid=delivery-form]"), "the delivery form");
  const correctness = [...form.querySelectorAll<HTMLElement>('[role="checkbox"]')].filter((b) => b.parentElement?.textContent?.includes("Correctness"));
  expect(correctness.map((b) => b.getAttribute("aria-checked"))).toEqual(["true", "true"]);
  await click(correctness[1]!);
  const paths = form.querySelector<HTMLInputElement>("[data-testid=delivery-nonProductPaths]")!;
  expect(paths.value).toBe(".github/**, docs/**, **/*.md");
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(paths, ".github/**, infra/**,");
    paths.dispatchEvent(new Event("input", { bubbles: true }));
  });
  await click(container.querySelector("[data-testid=delivery-save]")!);
  await until(() => client.projectPatches.length > 0 || null, "the save");
  expect(client.projectPatches).toEqual([{ delivery: { alwaysReviewers: [], nonProductPaths: [".github/**", "infra/**"] } }]);
});
