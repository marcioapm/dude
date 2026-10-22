/**
 * A project's Delivery page lists every reviewer as a checkbox, the
 * documentation reviewer among them, unticked unless required; ticking it
 * saves it as a required reviewer. Mounted in happy-dom against the fixture
 * client.
 */

import { afterEach, expect, test } from "bun:test";
import type { SettingsPatch, SettingsResponse } from "@dude/domain";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import { click, facetCheckbox, mount, until } from "./dom.ts";
import { FixtureClient } from "../src/fixtures/client.ts";
import { PROJECT } from "../src/fixtures/data.ts";
import { ProjectSettingsScreen } from "../src/screens/ProjectSettingsScreen.tsx";

let mounted: Array<() => Promise<void>> = [];
afterEach(async () => {
  for (const unmount of mounted) await unmount();
  mounted = [];
});

class Recording extends FixtureClient {
  patches: SettingsPatch[] = [];
  override async updateProjectSettings(projectId: string, patch: SettingsPatch): Promise<SettingsResponse> {
    this.patches.push(patch);
    return this.projectSettings(projectId);
  }
}

test("the Delivery page offers the documentation reviewer, off until ticked", async () => {
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
  const documentation = facetCheckbox(form, "Documentation");
  expect(documentation.getAttribute("aria-checked")).toBe("false");

  await click(documentation);
  await click(container.querySelector("[data-testid=delivery-save]")!);
  await until(() => client.patches.length > 0 || null, "the save");
  expect(client.patches).toEqual([{ delivery: { requiredReviewers: ["correctness", "documentation"] } }]);
});
