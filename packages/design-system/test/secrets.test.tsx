/**
 * SecretsChip, GivenTo, GivenToFieldset, OverriddenBy, SecretName,
 * PresetSecrets and SecretPicker: names and recipients only. Mounted in
 * happy-dom and driven by the events a browser sends; Secrets.module.css
 * maps its classes as written, so the struck class can be asserted.
 */

import { afterEach, describe, expect, mock, spyOn, test } from "bun:test";
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { cssModuleAsWritten } from "./cssModule.ts";
import type { SecretPick } from "../src/components/Secrets.tsx";

mock.module(...cssModuleAsWritten(new URL("../src/components/Secrets.module.css", import.meta.url)));

const { TooltipProvider } = await import("../src/primitives/Tooltip.tsx");
const { GivenTo, GivenToFieldset, OverriddenBy, PresetSecrets, SecretName, SecretPicker, SecretsChip, SECRETS_TIP_SOURCE } =
  await import("../src/components/Secrets.tsx");
// The class Secrets.module.css declares for a struck name, as the mock maps it.
const styles = { struck: "struck" };
const { accessibleName, allByRole, byRole } = await import("./queries.ts");

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
    // Glyph and words, never colour alone.
    expect(el.querySelector("[data-x=a] [data-testid=overridden-by] svg")).not.toBeNull();
  });
});

describe("SecretName", () => {
  test("mono; struck in the module's class when overridden; then From <org>, or an aside", async () => {
    const el = await mount(<>
      <span data-x="own"><SecretName name="GH_TOKEN" /></span>
      <span data-x="struck"><SecretName name="GH_TOKEN" struck from="Acme" /></span>
      <span data-x="aside"><SecretName name="GH_TOKEN" aside="platform has its own" /></span>
    </>);
    const name = (x: string) => el.querySelector(`[data-x=${x}] .ds-mono`)!;
    expect(name("own").className.split(" ")).not.toContain(styles["struck"]);
    expect(name("struck").className.split(" ")).toContain(styles["struck"]);
    expect(el.querySelector("[data-x=struck]")!.textContent).toBe("GH_TOKENFrom Acme");
    expect(el.querySelector("[data-x=struck] [data-source=organization] svg")).not.toBeNull();
    expect(el.querySelector("[data-x=aside] [data-testid=secret-aside]")!.textContent).toBe("platform has its own");
    expect(el.querySelector("[data-x=own]")!.textContent).toBe("GH_TOKEN");
  });
});

describe("PresetSecrets", () => {
  test("the lock glyph and what the role gets from each owner; none in its words", async () => {
    const el = await mount(<>
      <span data-x="a"><PresetSecrets role="Tester" given={[{ from: "Acme", names: ["NPM_TOKEN", "SENTRY_TOKEN"] }, { from: "platform", names: ["DATABASE_SEED_URL"] }]} /></span>
      <span data-x="b"><PresetSecrets role="Reviewer" given={[{ from: "Acme", names: [] }]} /></span>
      <span data-x="c"><PresetSecrets role="Investigator" given={[]} none="no secrets: it is not one of the agents a secret is given to" /></span>
    </>);
    expect(el.querySelector("[data-x=a]")!.textContent).toBe("Tester gets NPM_TOKEN, SENTRY_TOKEN from Acme · DATABASE_SEED_URL from platform");
    expect([...el.querySelectorAll("[data-x=a] code")].map((c) => c.textContent)).toEqual(["NPM_TOKEN", "SENTRY_TOKEN", "DATABASE_SEED_URL"]);
    expect(el.querySelector("[data-x=a] svg")).not.toBeNull();
    expect(el.querySelector("[data-x=b]")!.textContent).toBe("Reviewer gets no secrets");
    expect(el.querySelector("[data-x=c]")!.textContent).toBe("Investigator gets no secrets: it is not one of the agents a secret is given to");
  });

  test("an organisation and a project of one name are two groups, with no duplicate key", async () => {
    const errors: string[] = [];
    const spy = spyOn(console, "error").mockImplementation((...args: unknown[]) => void errors.push(args.map(String).join(" ")));
    try {
      const el = await mount(<PresetSecrets role="Tester" given={[{ from: "web", names: ["NPM_TOKEN"] }, { from: "web", names: ["SEED_URL"] }]} />);
      expect(el.textContent).toBe("Tester gets NPM_TOKEN from web · SEED_URL from web");
    } finally {
      spy.mockRestore();
    }
    expect(errors.filter((e) => e.includes("same key"))).toEqual([]);
  });
});

describe("SecretPicker", () => {
  const ORG = [{ name: "GH_TOKEN", hint: "Q8e2" }, { name: "NPM_TOKEN", hint: "x0Lm" }];
  const PROJECT = { name: "platform", secrets: [{ name: "GH_TOKEN", hint: "pf91" }] };
  function Harness({ seen, project, disabled, initial }: { seen: SecretPick[][]; project?: typeof PROJECT; disabled?: boolean; initial?: SecretPick[] }) {
    const [v, setV] = useState<SecretPick[]>(initial ?? [{ source: "organization", name: "GH_TOKEN" }, { source: "project", name: "GH_TOKEN" }]);
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
    expect(shadowed.getAttribute("aria-checked")).toBe("false");
    expect(el.querySelector("[data-overridden=true]")!.textContent).toContain("Overridden by platform's GH_TOKEN");
    expect(el.querySelector("[data-overridden=true] .ds-mono")!.className.split(" ")).toContain(styles["struck"]);
    expect(el.querySelector("[data-overridden=true] [data-testid=overridden-by] svg")).not.toBeNull();
    expect(byRole(el, "checkbox", "GH_TOKEN (platform)").getAttribute("aria-checked")).toBe("true");
    await act(async () => byRole(el, "checkbox", "GH_TOKEN (platform)").click());
    expect(seen.at(-1)).toEqual([]);
  });

  test("an organisation pick the project overrides is the project's row ticked, saved as the project's, and unticking removes it", async () => {
    const seen: SecretPick[][] = [];
    const el = await mount(<Harness seen={seen} project={PROJECT} initial={[{ source: "organization", name: "GH_TOKEN" }]} />);
    expect(byRole(el, "checkbox", "GH_TOKEN (Acme)").getAttribute("aria-checked")).toBe("false");
    expect(byRole(el, "checkbox", "GH_TOKEN (platform)").getAttribute("aria-checked")).toBe("true");
    await act(async () => byRole(el, "checkbox", "NPM_TOKEN (Acme)").click());
    expect(seen.at(-1)).toEqual([{ source: "project", name: "GH_TOKEN" }, { source: "organization", name: "NPM_TOKEN" }]);
    await act(async () => byRole(el, "checkbox", "GH_TOKEN (platform)").click());
    expect(seen.at(-1)).toEqual([{ source: "organization", name: "NPM_TOKEN" }]);
  });

  test("none yet, and disabled for a reader", async () => {
    const el = await mount(<><SecretPicker orgName="Acme" org={[]} value={[]} onChange={() => {}} />
      <Harness seen={[]} disabled /></>);
    expect(el.textContent).toContain("None yet.");
    expect(allByRole(el, "checkbox").every((b) => b.hasAttribute("disabled"))).toBe(true);
  });
});

describe("the gallery's secrets blocks", () => {
  // Each block in both themes and both densities: four panes, every state drawn once per pane.
  async function block(id: string, next: string) {
    const { renderToStaticMarkup } = await import("react-dom/server");
    const { PaneDensityContext } = await import("../src/gallery/Frame.tsx");
    const { SettingsGallerySection } = await import("../src/gallery/sections/Settings.tsx");
    const h = renderToStaticMarkup(<TooltipProvider><PaneDensityContext.Provider value="both"><SettingsGallerySection mode="both" /></PaneDensityContext.Provider></TooltipProvider>);
    const b = h.slice(h.indexOf(`id="${id}"`), h.indexOf(`id="${next}"`));
    const panes = [...b.matchAll(/data-theme="(dark|light)"[^>]*data-density="(comfortable|compact)"/g)].map((m) => `${m[1]}/${m[2]}`);
    expect(panes.sort()).toEqual(["dark/comfortable", "dark/compact", "light/comfortable", "light/compact"]);
    return b.slice(b.indexOf("data-theme="));
  }
  const count = (h: string, re: RegExp) => (h.match(re) ?? []).length;

  test("#secrets-overridden: the struck row, its From marker and aside, the disabled picker row, and a 390 frame", async () => {
    const drawn = await block("secrets-overridden", "secrets-picker");
    // The table at full width and at 390, then the picker row: three struck names a pane.
    expect(count(drawn, /class="ds-mono struck"[^>]*>GH_TOKEN</g)).toBe(4 * 3);
    expect(count(drawn, /data-testid="overridden-by"><svg/g)).toBe(4 * 3);
    expect(count(drawn, />platform has its own</g)).toBe(4 * 2);
    expect(count(drawn, /style="width:390px/g)).toBe(4);
  });

  test("#secrets-picker: organisation-only, both lists, none yet, disabled, the preset line and 390", async () => {
    const drawn = await block("secrets-picker", "s-tiers");
    const t = drawn.replace(/<[^>]+>/g, "");
    expect(count(t, /None yet\./g)).toBe(4 * 2);
    expect(count(drawn, /data-overridden="true"/g)).toBe(4 * 2);
    expect(count(t, /Tester gets NPM_TOKEN, SENTRY_TOKEN from Acme · DATABASE_SEED_URL from platform/g)).toBe(4);
    expect(count(t, /Investigator gets no secrets: it is not one of the agents a secret is given to/g)).toBe(4);
    expect(count(drawn, /style="width:390px/g)).toBe(4);
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
