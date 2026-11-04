/**
 * Fixes on an open pull request: a project's Delivery page and the
 * organisation's offer the narrow loop's bound. Mounted in happy-dom
 * against the fixture client.
 */

import { afterEach, expect, test } from "bun:test";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import { mount, until } from "./dom.ts";
import { FixtureClient } from "../src/fixtures/client.ts";
import { PEOPLE, PROJECT } from "../src/fixtures/data.ts";
import { ProjectSettingsScreen } from "../src/screens/ProjectSettingsScreen.tsx";
import { OrganizationSettingsScreen } from "../src/screens/OrganizationSettingsScreen.tsx";

let mounted: Array<() => Promise<void>> = [];
afterEach(async () => {
  for (const unmount of mounted) await unmount();
  mounted = [];
});

test("a project's Delivery page offers the pull request fix review rounds", async () => {
  const client = new FixtureClient("a");
  const { container: c, unmount } = await mount(
    <TooltipProvider>
      <ToastProvider>
        <ProjectSettingsScreen client={client} projectId={PROJECT.id} projects={[]} admin page="delivery"
          onPage={() => {}} onChanged={() => {}} onBack={() => {}} onOrganization={() => {}} />
      </ToastProvider>
    </TooltipProvider>,
  );
  mounted.push(unmount);
  await until(() => c.querySelector("[data-testid=delivery-form]"), "the Delivery page");
  expect(c.querySelectorAll("[data-testid=delivery-maxPrFixReviewIterations]").length).toBe(1);
});

test("the organisation's Delivery page offers the pull request fix review rounds", async () => {
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
  expect(container.querySelectorAll("[data-testid=delivery-maxPrFixReviewIterations]").length).toBe(1);
});
