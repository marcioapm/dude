/**
 * A role page's Machine field when the organisation's sizes cannot be
 * read: it still offers the inherited size. Mounted in happy-dom against
 * the fixture client.
 */

import { afterEach, expect, test } from "bun:test";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import { mount, until } from "./dom.ts";
import { ApiError } from "../src/api/client.ts";
import { FixtureClient } from "../src/fixtures/client.ts";
import { PROJECT } from "../src/fixtures/data.ts";
import { ProjectSettingsScreen } from "../src/screens/ProjectSettingsScreen.tsx";

let mounted: Array<() => Promise<void>> = [];
afterEach(async () => {
  for (const unmount of mounted) await unmount();
  mounted = [];
});

/** The fixture world whose sizes read fails, as the API's 5xx does. */
class NoSizesClient extends FixtureClient {
  override machineSizes(): ReturnType<FixtureClient["machineSizes"]> {
    return Promise.reject(new ApiError(503, "unavailable", "sizes are down"));
  }
}

test("sizes that fail to load leave the Machine field offering the inherited size", async () => {
  const { container, unmount } = await mount(
    <TooltipProvider>
      <ToastProvider>
        <ProjectSettingsScreen client={new NoSizesClient("a")} projectId={PROJECT.id} projects={[]} admin page="implementer"
          onPage={() => {}} onChanged={() => {}} onBack={() => {}} onOrganization={() => {}} />
      </ToastProvider>
    </TooltipProvider>,
  );
  mounted.push(unmount);
  const field = await until(() => container.querySelector<HTMLButtonElement>("[data-testid=role-machine]"), "the Machine select");
  expect(field.getAttribute("aria-label")).toBe("Machine");
  expect(field.disabled).toBe(false);
  expect(field.textContent).toBe("From Example");
  expect(container.textContent).not.toContain("Loading…");
});
