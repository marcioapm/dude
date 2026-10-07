/**
 * A project's Delivery page sets the conductor's edit limit, beside the
 * conductor's other settings, in the same style: each a number with its
 * source, saved as only what changed. Mounted in happy-dom against the
 * fixture client.
 */

import { afterEach, expect, test } from "bun:test";
import type { SettingsPatch, SettingsResponse } from "@dude/domain";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import { click, mount, type, until } from "./dom.ts";
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

test("the conductor's edit limit is set on the Delivery page", async () => {
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
  const lines = await until(() => container.querySelector<HTMLInputElement>("[data-testid=delivery-conductorEditLines]"), "the lines field");
  const files = container.querySelector<HTMLInputElement>("[data-testid=delivery-conductorEditFiles]")!;
  expect([lines.value, files.value]).toEqual(["60", "3"]);
  expect(container.textContent).toContain("Lines the conductor may change");
  expect(container.textContent).toContain("Files the conductor may change");

  await type(lines, "40");
  await click(container.querySelector("[data-testid=delivery-save]")!);
  await until(() => client.patches.length > 0 || null, "the save");
  expect(client.patches).toEqual([{ delivery: { conductorEditLines: 40 } }]);

  // Not a whole number of at least one: not saved.
  await type(files, "0");
  expect(container.querySelector<HTMLButtonElement>("[data-testid=delivery-save]")!.disabled).toBe(true);
});
