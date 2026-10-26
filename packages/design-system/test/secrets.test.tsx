/**
 * SecretsChip, GivenTo and GivenToFieldset: names and recipients only.
 * Mounted in happy-dom and driven by the events a browser sends.
 */

import { afterEach, describe, expect, test } from "bun:test";
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { TooltipProvider } from "../src/primitives/Tooltip.tsx";
import { GivenTo, GivenToFieldset, OverriddenBy, SecretPicker, SecretsChip, SECRETS_TIP_SOURCE, type SecretPick } from "../src/components/Secrets.tsx";
import { accessibleName, allByRole, byRole } from "./queries.ts";

let root: Root | null = null;
let host: HTMLElement | null = null;
afterEach(async () => {
  await act(async () => root?.unmount());
  host?.remove();
  document.body.innerHTML = "";
  root = null;
  host = null;
});

async function mount(node: React.ReactNode) {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => root!.render(<TooltipProvider>{node}</TooltipProvider>));
  return host;
}

describe("SecretsChip", () => {
  test("names the count, and its tip lists the names and where they came from", async () => {
    const el = await mount(<SecretsChip names={["SNOWFLAKE_PASSWORD", "SNOWFLAKE_USER", "SNOWFLAKE_ACCOUNT"]} />);
    const chip = byRole<HTMLButtonElement>(el, "button", "Secrets: 3 secrets, SNOWFLAKE_PASSWORD, SNOWFLAKE_USER, SNOWFLAKE_ACCOUNT");
    expect(chip.textContent).toBe("3 secrets");
    await act(async () => void chip.focus());
    const tip = await (async () => {
      for (let i = 0; i < 50; i++) {
        const t = allByRole(document.body, "tooltip")[0];
        if (t) return t;
        await act(async () => new Promise((r) => setTimeout(r, 10)));
      }
      throw new Error("no tooltip");
    })();
    expect(tip.textContent).toBe("SNOWFLAKE_PASSWORDSNOWFLAKE_USERSNOWFLAKE_ACCOUNT" + SECRETS_TIP_SOURCE);
    expect(SECRETS_TIP_SOURCE).toBe("Given by project and organisation settings; values are never shown");
  });

  test("one is singular, none is nothing", async () => {
    const el = await mount(<><SecretsChip names={["ONE_KEY"]} /><SecretsChip names={[]} /></>);
    expect(allByRole(el, "button").map(accessibleName)).toEqual(["Secrets: 1 secret, ONE_KEY"]);
    expect(byRole(el, "button", "Secrets: 1 secret, ONE_KEY").textContent).toBe("1 secret");
  });
});

describe("GivenTo", () => {
  test("recipients as badges, none as Nobody", async () => {
    const el = await mount(<><span data-x="a"><GivenTo given={["Previews", "Tester"]} /></span><span data-x="b"><GivenTo given={[]} /></span></>);
    expect(el.querySelector("[data-x=a]")!.textContent).toBe("PreviewsTester");
    expect(el.querySelector("[data-x=b]")!.textContent).toBe("Nobody");
  });

  test("none says what an empty list means where Nobody would be false", async () => {
    const el = await mount(<GivenTo given={[]} none="Only automations that pick it" />);
    expect(el.textContent).toBe("Only automations that pick it");
  });
});

describe("OverriddenBy", () => {
  test("says which secret replaces this one, by default this project's", async () => {
    const el = await mount(<><span data-x="a"><OverriddenBy name="GH_TOKEN" /></span><span data-x="b"><OverriddenBy name="GH_TOKEN" by="platform's" /></span></>);
    expect(el.querySelector("[data-x=a]")!.textContent).toBe("Overridden by this project's GH_TOKEN");
    expect(el.querySelector("[data-x=b]")!.textContent).toBe("Overridden by platform's GH_TOKEN");
    expect(el.querySelector("[data-x=a] code")!.textContent).toBe("GH_TOKEN");
  });
});

describe("SecretPicker", () => {
  const ORG = [{ name: "GH_TOKEN", hint: "Q8e2" }, { name: "NPM_TOKEN", hint: "x0Lm" }];
  const PROJECT = { name: "platform", secrets: [{ name: "GH_TOKEN", hint: "pf91" }] };
  function Harness({ seen, project, disabled }: { seen: SecretPick[][]; project?: typeof PROJECT; disabled?: boolean }) {
    const [v, setV] = useState<SecretPick[]>([{ source: "organization", name: "GH_TOKEN" }, { source: "project", name: "GH_TOKEN" }]);
    return <SecretPicker orgName="Acme" org={ORG} project={project} value={v} disabled={disabled} onChange={(n) => { seen.push(n); setV(n); }} />;
  }

  test("an organisation automation: Acme's group alone, each box named with its level and described by its last 4", async () => {
    const seen: SecretPick[][] = [];
    const el = await mount(<Harness seen={seen} />);
    expect(allByRole(el, "group").map(accessibleName)).toEqual(["Acme's"]);
    expect(allByRole(el, "checkbox").map(accessibleName)).toEqual(["GH_TOKEN (Acme)", "NPM_TOKEN (Acme)"]);
    expect(byRole(el, "checkbox", "GH_TOKEN (Acme)").getAttribute("aria-checked")).toBe("true");
    expect(el.textContent).toContain("…x0Lm");
    await act(async () => byRole(el, "checkbox", "NPM_TOKEN (Acme)").click());
    expect(seen.at(-1)).toContainEqual({ source: "organization", name: "NPM_TOKEN" });
  });

  test("a project automation: both groups; the organisation's name the project holds is disabled and says why", async () => {
    const seen: SecretPick[][] = [];
    const el = await mount(<Harness seen={seen} project={PROJECT} />);
    expect(allByRole(el, "group").map(accessibleName)).toEqual(["Acme's", "platform's"]);
    const shadowed = byRole(el, "checkbox", "GH_TOKEN (Acme)");
    expect(shadowed.hasAttribute("disabled")).toBe(true);
    // Never shown ticked, though the value still names it.
    expect(shadowed.getAttribute("aria-checked")).toBe("false");
    expect(el.querySelector("[data-overridden=true]")!.textContent).toContain("Overridden by platform's GH_TOKEN");
    expect(byRole(el, "checkbox", "GH_TOKEN (platform)").getAttribute("aria-checked")).toBe("true");
    await act(async () => byRole(el, "checkbox", "GH_TOKEN (platform)").click());
    expect(seen.at(-1)).toEqual([{ source: "organization", name: "GH_TOKEN" }]);
  });

  test("none yet, and disabled for a reader", async () => {
    const el = await mount(<><SecretPicker orgName="Acme" org={[]} value={[]} onChange={() => {}} />
      <Harness seen={[]} disabled /></>);
    expect(el.textContent).toContain("None yet.");
    expect(allByRole(el, "checkbox").every((b) => b.hasAttribute("disabled"))).toBe(true);
  });
});

describe("GivenToFieldset", () => {
  const OPTIONS = [
    { id: "previews", label: "Previews", description: "Servers and setup scripts of branch previews" },
    { id: "tester", label: "Tester", description: "Runs tests" },
    { id: "reviewer", label: "Reviewer", description: "Reviews the changes" },
  ];
  function Harness({ seen }: { seen: string[][] }) {
    const [v, setV] = useState<string[]>(["previews"]);
    return <GivenToFieldset options={OPTIONS} value={v} hint="After you save." error={v.length ? undefined : "Give it to at least one"}
      onChange={(n) => { seen.push(n); setV(n); }} />;
  }

  test("ticks keep the options' order, and none says the error in place of the hint", async () => {
    const seen: string[][] = [];
    const el = await mount(<Harness seen={seen} />);
    const boxes = () => allByRole(el, "checkbox");
    expect(el.querySelector("legend")!.textContent).toBe("Given to");
    expect(el.textContent).toContain("After you save.");
    await act(async () => boxes()[2]!.click());
    await act(async () => boxes()[1]!.click());
    expect(seen.at(-1)).toEqual(["previews", "tester", "reviewer"]);
    for (const b of [0, 1, 2]) await act(async () => boxes()[b]!.click());
    expect(seen.at(-1)).toEqual([]);
    expect(el.textContent).toContain("Give it to at least one");
    expect(el.textContent).not.toContain("After you save.");
    expect(el.querySelector("fieldset")!.getAttribute("aria-invalid")).toBe("true");
  });
});
