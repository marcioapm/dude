import { afterEach, expect, test } from "bun:test";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import { type PreviewSettings } from "@dude/domain";
import { mount, click, until } from "./dom.ts";
import { byRole } from "../../../packages/design-system/test/queries.ts";
import { FixtureClient } from "../src/fixtures/client.ts";
import { PROJECT } from "../src/fixtures/data.ts";
import { ProjectSettingsScreen } from "../src/screens/ProjectSettingsScreen.tsx";

let unmount: (() => Promise<void>) | undefined;
afterEach(async () => { await unmount?.(); document.body.innerHTML = ""; });

class Recording extends FixtureClient {
  saved: PreviewSettings[] = [];
  override async updatePreviewSettings(id: string, settings: PreviewSettings) {
    this.saved.push(settings);
    return super.updatePreviewSettings(id, settings);
  }
}

test("preview form shows both defaults and saves each timeout without replacing the other", async () => {
  const client = new Recording("a");
  const mounted = await mount(<TooltipProvider><ToastProvider>
    <ProjectSettingsScreen client={client} projectId={PROJECT.id} projects={[]} admin page="servers"
      onPage={() => {}} onChanged={() => {}} onBack={() => {}} />
  </ToastProvider></TooltipProvider>);
  unmount = mounted.unmount;
  const page = await until(() => mounted.container.querySelector<HTMLElement>("[data-testid=preview-settings]"), "previews");
  expect(byRole(page, "combobox", "First wake timeout").textContent).toBe("15 minutes");
  expect(byRole(page, "combobox", "Wake timeout").textContent).toBe("5 minutes");
  for (const label of ["First wake timeout", "Wake timeout"]) {
    await click(byRole(page, "combobox", label));
    await click(await until(() => document.querySelector<HTMLElement>('[role=option][data-value="20"]') ?? [...document.querySelectorAll<HTMLElement>('[role=option]')].find(e => e.textContent === "20 minutes"), "20 minutes"));
    await until(() => byRole(page, "combobox", label).textContent === "20 minutes" && !byRole<HTMLButtonElement>(page, "combobox", label).disabled, "saved");
  }
  expect(client.saved.map(s => [s.firstWakeTimeoutMinutes, s.wakeTimeoutMinutes])).toEqual([[20, 5], [20, 20]]);
});
