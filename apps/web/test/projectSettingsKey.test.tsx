/**
 * A project's settings show its key, read-only: the key is chosen when the
 * project is made and fixed after. Mounted in happy-dom against the
 * fixture client.
 */

import { afterEach, expect, test } from "bun:test";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import { mount, until } from "./dom.ts";
import { FixtureClient } from "../src/fixtures/client.ts";
import { PROJECT } from "../src/fixtures/data.ts";
import { ProjectSettingsScreen } from "../src/screens/ProjectSettingsScreen.tsx";

let mounted: Array<() => Promise<void>> = [];
afterEach(async () => {
  for (const unmount of mounted) await unmount();
  mounted = [];
});

test("General shows the project's key, which cannot be edited", async () => {
  const { container, unmount } = await mount(
    <TooltipProvider>
      <ToastProvider>
        <ProjectSettingsScreen client={new FixtureClient("a")} projectId={PROJECT.id} projects={[]} admin page="general"
          onPage={() => {}} onChanged={() => {}} onBack={() => {}} onOrganization={() => {}} />
      </ToastProvider>
    </TooltipProvider>,
  );
  mounted.push(unmount);
  const key = await until(() => container.querySelector<HTMLInputElement>("[data-testid=project-settings-key]"), "the Key field");
  expect(key.value).toBe("WEBC");
  expect(key.disabled).toBe(true);
  expect(container.querySelector(`label[for="${key.id}"]`)?.textContent).toBe("Key");
});
