/**
 * The checks report rounds are set on the Delivery page with the loop's
 * other bounds, saved as only what changed. Mounted in happy-dom against
 * the fixture client.
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

test("the checks report rounds are set on the Delivery page", async () => {
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
  const rounds = await until(() => container.querySelector<HTMLInputElement>("[data-testid=delivery-checksFixRounds]"), "the rounds field");
  expect(rounds.value).toBe("2");
  expect(container.textContent).toContain("Checks report rounds");

  await type(rounds, "1");
  await click(container.querySelector("[data-testid=delivery-save]")!);
  await until(() => client.patches.length > 0 || null, "the save");
  expect(client.patches).toEqual([{ delivery: { checksFixRounds: 1 } }]);

  await type(rounds, "-1");
  expect(container.querySelector<HTMLButtonElement>("[data-testid=delivery-save]")!.disabled).toBe(true);
});
