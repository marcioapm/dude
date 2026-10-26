/**
 * Organisation settings → Secrets against the fixture client: the list
 * (Given to, "Only automations that pick it", Picked by for admins, the
 * projects that have their own), Add with nothing ticked, Who gets it with
 * the "<project> has its own" note, and a member's read-only view.
 */

import { afterEach, describe, expect, test } from "bun:test";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import { act, click, mount, until } from "./dom.ts";
import { allByRole, byLabel, byRole } from "../../../packages/design-system/test/queries.ts";
import { FixtureClient } from "../src/fixtures/client.ts";
import { ORG_SECRETS_HELP, OrgSecretsPage } from "../src/screens/OrgSecrets.tsx";
import { NO_TALKERS_NOTE, ORG_NONE_TICKED } from "../src/screens/ProjectSecrets.tsx";

let mounted: Array<() => Promise<void>> = [];
afterEach(async () => {
  for (const unmount of mounted) await unmount();
  mounted = [];
  document.body.innerHTML = "";
});

class Named extends FixtureClient {
  override get organizationId() { return "org_example"; }
}

async function render(admin = true) {
  const client = new Named("a");
  const { container, unmount } = await mount(
    <TooltipProvider><ToastProvider><OrgSecretsPage client={client} orgName="Example" admin={admin} /></ToastProvider></TooltipProvider>);
  mounted.push(unmount);
  await until(() => container.querySelector("[data-testid=secrets]"), "the table");
  return { container, client };
}

const rows = (c: HTMLElement) => allByRole(byRole(c, "table", "Example's secrets"), "row").slice(1)
  .map((r) => allByRole(r, "cell").slice(0, 4).map((x) => x.textContent));
/** The rows once the dialog over them has closed (until then they are hidden from assistive technology). */
const rowsAfter = (c: HTMLElement) => (allByRole(document.body, "dialog").length ? [] : rows(c));
const press = (el: Element) =>
  act(async () => void el.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true, button: 0, pointerType: "mouse" })));
const dialog = () => until(() => allByRole(document.body, "dialog")[0], "the dialog");
async function typeInto(el: HTMLInputElement | HTMLTextAreaElement, text: string) {
  await act(async () => {
    Object.getOwnPropertyDescriptor(Object.getPrototypeOf(el), "value")!.set!.call(el, text);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

describe("Organisation settings → Secrets", () => {
  test("lists each with who gets it, which automations pick it and which projects have their own", async () => {
    const { container } = await render();
    expect(container.textContent).toContain(ORG_SECRETS_HELP("Example"));
    expect(allByRole(byRole(container, "table", "Example's secrets"), "columnheader").map((h) => h.textContent))
      .toEqual(["Name", "Given to", "Picked by", "Value", "Actions"]);
    expect(rows(container)).toEqual([
      ["GH_TOKENplatform has its own", "ImplementerFixer", "1 automation", "…Q8e2"],
      ["NPM_TOKEN", "PreviewsTesterImplementerFixerSimplifierReviewer", "—", "…x0Lm"],
      ["SEED_LLM_KEYweb-console has its own", "Tester", "—", "…Kq7w"],
      ["SENTRY_TOKEN", "TesterImplementer", "1 automation", "…7c1d"],
      ["STATUSPAGE_KEY", "Only automations that pick it", "1 automation", "…kk42"],
    ]);
    expect(container.textContent).toContain(NO_TALKERS_NOTE);
    expect(container.textContent).toContain("A project secret with the same name takes the place of Example's in that project");
  });

  test("a member reads names and recipients, with no Picked by, menu or Add", async () => {
    const { container } = await render(false);
    expect(allByRole(byRole(container, "table", "Example's secrets"), "columnheader").map((h) => h.textContent)).toEqual(["Name", "Given to", "Value"]);
    expect(allByRole(container, "button", "Add secret")).toHaveLength(0);
    expect(container.textContent).toContain("Only Example’s admins change these.");
  });

  test("Add takes a secret ticked for nobody, saying only automations that pick it get it", async () => {
    const { container, client } = await render();
    await click(byRole(container, "button", "Add secret"));
    const d = await dialog();
    expect(d.textContent).toContain("Add a secret to Example");
    expect(d.textContent).toContain("Ticks apply to every Example project's agents.");
    expect(allByRole(d, "checkbox").filter((b) => b.getAttribute("aria-checked") === "true")).toHaveLength(0);
    expect(d.textContent).toContain(ORG_NONE_TICKED);
    await typeInto(byLabel(d, "Name") as HTMLInputElement, "PAGER_KEY");
    await typeInto(byLabel(d, "Value") as HTMLInputElement, "pager-key-value-1234");
    await click(byRole(d, "button", "Add secret"));
    await until(() => rowsAfter(container).some((r) => r[0] === "PAGER_KEY") ? true : null, "the new row");
    expect((await client.organizationSecrets("org_example")).secrets.find((s) => s.name === "PAGER_KEY"))
      .toMatchObject({ recipients: [], hint: "1234" });
  });

  test("Who gets it says which projects have their own of the name", async () => {
    const { container, client } = await render();
    await press(byRole(container, "button", "Actions for GH_TOKEN"));
    await click(await until(() => allByRole(document.body, "menuitem", "Change who gets it…")[0], "the item"));
    const d = await dialog();
    expect(d.textContent).toContain("Who gets GH_TOKEN");
    expect(d.querySelector("[data-testid=overridden-in]")!.textContent)
      .toBe("platform has its own GH_TOKEN: agents there get that one, whatever is ticked here.");
    await click(byRole(d, "checkbox", "Reviewer"));
    await click(byRole(d, "button", "Save"));
    await until(() => rowsAfter(container).find((r) => r[0]?.startsWith("GH_TOKEN"))?.[1] === "ImplementerFixerReviewer" ? true : null, "the row's recipients");
    const now = (await client.organizationSecrets("org_example")).secrets.find((s) => s.name === "GH_TOKEN")!;
    expect(now.recipients).toEqual(["implementer", "fixer", "reviewer"]);
  });

  test("with no organisation known and /v1/me failing, it says so rather than loading forever", async () => {
    class Unknown extends FixtureClient {
      override get organizationId() { return undefined; }
      override me(): ReturnType<FixtureClient["me"]> { return Promise.reject(new Error("The server is unavailable.")); }
    }
    const { container, unmount } = await mount(
      <TooltipProvider><ToastProvider><OrgSecretsPage client={new Unknown("a")} orgName="Example" admin /></ToastProvider></TooltipProvider>);
    mounted.push(unmount);
    await until(() => container.textContent?.includes("The server is unavailable.") ? true : null, "the problem");
    expect(container.querySelector("[role=status]")?.textContent ?? "").not.toContain("Loading");
  });
});
