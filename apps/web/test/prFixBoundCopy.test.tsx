/**
 * The pull request fix bounds say what happens when they run out, true with
 * a conductor and without one: the conductor decides and may ask the owner;
 * with no conductor, the owner decides.
 */

import { afterEach, expect, test } from "bun:test";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import { mount, until } from "./dom.ts";
import { FixtureClient } from "../src/fixtures/client.ts";
import { PROJECT } from "../src/fixtures/data.ts";
import { ProjectSettingsScreen } from "../src/screens/ProjectSettingsScreen.tsx";
import { GithubBehaviour } from "../src/screens/GithubSettings.tsx";

let mounted: Array<() => unknown> = [];
afterEach(async () => {
  for (const unmount of mounted) await unmount();
  mounted = [];
});

// A setting's help: the <small> of the nearest row around its field.
function helpOf(field: HTMLElement): string | null | undefined {
  for (let el = field.parentElement; el; el = el.parentElement) {
    const help = el.querySelector("small");
    if (help) return help.textContent;
  }
  return undefined;
}

test("Pull request fix rounds says the conductor decides, or the owner without one", async () => {
  const { container, unmount } = await mount(
    <TooltipProvider>
      <ToastProvider>
        <ProjectSettingsScreen client={new FixtureClient("a")} projectId={PROJECT.id} projects={[]} admin page="delivery"
          onPage={() => {}} onChanged={() => {}} onBack={() => {}} onOrganization={() => {}} />
      </ToastProvider>
    </TooltipProvider>,
  );
  mounted.push(unmount);
  const field = await until(() => container.querySelector<HTMLElement>("[data-testid=delivery-maxPrFixIterations]"), "the field");
  expect(helpOf(field)).toBe(
    "Fixes for pull request comments before the conductor decides how to go on, and may ask the owner; with no conductor, the owner is asked.");
});

test("Fix rounds per pull request says the conductor decides, or the owner without one", async () => {
  const { container, unmount } = await mount(<ToastProvider><GithubBehaviour client={new FixtureClient("a")} admin /></ToastProvider>);
  mounted.push(unmount);
  const field = await until(() => container.querySelector<HTMLElement>("[data-testid=setting-fix-rounds]"), "the field");
  expect(helpOf(field)).toBe(
    "Then the conductor decides how it goes on, and may ask the owner; with no conductor, the owner decides. 0 for no limit.");
});
