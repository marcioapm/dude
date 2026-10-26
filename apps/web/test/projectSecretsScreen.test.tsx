/**
 * Project settings → Servers, as the screen mounts it against the fixture
 * client: the project's Secrets lists the organisation's after its own,
 * the one the project overrides struck through, and links to Organisation
 * settings → Secrets.
 */

import { afterEach, expect, test } from "bun:test";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import { click, mount, until } from "./dom.ts";
import { FixtureClient } from "../src/fixtures/client.ts";
import { PROJECT } from "../src/fixtures/data.ts";
import { ProjectSettingsScreen } from "../src/screens/ProjectSettingsScreen.tsx";

let mounted: Array<() => Promise<void>> = [];
afterEach(async () => {
  for (const unmount of mounted) await unmount();
  mounted = [];
  document.body.innerHTML = "";
});

test("the Servers page's Secrets lists the organisation's after the project's own, the overridden one struck", async () => {
  const opened: string[] = [];
  const { container, unmount } = await mount(
    <TooltipProvider><ToastProvider>
      <ProjectSettingsScreen client={new FixtureClient("a")} projectId={PROJECT.id} projects={[]} admin page="servers"
        onPage={() => {}} onChanged={() => {}} onBack={() => {}} onOrganization={(p) => opened.push(p)} />
    </ToastProvider></TooltipProvider>,
  );
  mounted.push(unmount);
  await until(() => container.querySelector("[data-testid=project-secrets] tr[data-source=organization]"), "the organisation's rows");
  const rows = [...container.querySelectorAll<HTMLElement>("[data-testid=project-secrets] tbody tr")];
  expect(rows.map((r) => `${r.getAttribute("data-source")}:${r.getAttribute("data-secret")}${r.getAttribute("data-overridden") ? " overridden" : ""}`)).toEqual([
    "project:DATABASE_SEED_URL",
    "project:SEED_LLM_KEY",
    "organization:GH_TOKEN",
    "organization:NPM_TOKEN",
    "organization:SEED_LLM_KEY overridden",
    "organization:SENTRY_TOKEN",
    "organization:STATUSPAGE_KEY",
  ]);
  const shadowed = rows.find((r) => r.getAttribute("data-overridden") === "true")!;
  expect(shadowed.textContent).toContain("Overridden by this project's SEED_LLM_KEY");
  expect(shadowed.querySelector("[data-testid=overridden-by] svg")).not.toBeNull();
  expect([...container.querySelectorAll("[data-testid=project-secrets] [data-struck=true]")].map((n) => n.textContent)).toEqual(["SEED_LLM_KEY"]);
  expect(shadowed.querySelector("[data-struck=true]")).not.toBeNull();
  const status = rows.find((r) => r.getAttribute("data-secret") === "STATUSPAGE_KEY")!;
  expect(status.textContent).toContain("From Example");
  expect(status.textContent).toContain("Only automations that pick it");
  await click(container.querySelector<HTMLElement>("[data-testid=org-secrets-link]")!);
  expect(opened).toEqual(["secrets"]);
});
