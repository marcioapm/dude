/**
 * A project's Delivery page sets the review loop's two convergence bounds,
 * each a number of rounds with its source, 0 to 20 (0 turns it off), saved as
 * only what changed. Mounted in happy-dom against the fixture client.
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

for (const [key, label] of [["maxStillRounds", "Unmet criterion rounds"], ["maxRegressionRounds", "Regression rounds"]] as const) {
  test(`${label} is set on the Delivery page within 0 to 20`, async () => {
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
    const field = await until(() => container.querySelector<HTMLInputElement>(`[data-testid=delivery-${key}]`), "the field");
    expect(field.value).toBe("2");
    expect(container.querySelector(`label[for="delivery-${key}"]`)?.textContent).toContain(label);
    const save = () => container.querySelector<HTMLButtonElement>("[data-testid=delivery-save]")!;
    for (const out of ["21", "-1"]) {
      await type(field, out);
      expect(save().disabled).toBe(true);
    }
    await type(field, "0");
    expect(save().disabled).toBe(false);
    await click(save());
    await until(() => client.patches.length > 0 || null, "the save");
    expect(client.patches).toEqual([{ delivery: { [key]: 0 } }]);
  });
}
