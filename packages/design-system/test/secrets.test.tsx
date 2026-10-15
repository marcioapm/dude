/**
 * SecretsChip, GivenTo and GivenToFieldset: names and recipients only.
 * Mounted in happy-dom and driven by the events a browser sends.
 */

import { afterEach, describe, expect, test } from "bun:test";
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { TooltipProvider } from "../src/primitives/Tooltip.tsx";
import { GivenTo, GivenToFieldset, SecretsChip, SECRETS_TIP_SOURCE } from "../src/components/Secrets.tsx";
import { allByRole } from "./queries.ts";

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
    const chip = el.querySelector("button")!;
    expect(chip.textContent).toBe("3 secrets");
    expect(chip.getAttribute("aria-label")).toBe("Project secrets: 3 secrets, SNOWFLAKE_PASSWORD, SNOWFLAKE_USER, SNOWFLAKE_ACCOUNT");
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
    expect(SECRETS_TIP_SOURCE).toBe("Given by project settings; values are never shown");
  });

  test("one is singular, none is nothing", async () => {
    const el = await mount(<><SecretsChip names={["ONE_KEY"]} /><SecretsChip names={[]} /></>);
    expect([...el.querySelectorAll("button")].map((b) => b.textContent)).toEqual(["1 secret"]);
  });
});

describe("GivenTo", () => {
  test("recipients as badges, none as Nobody", async () => {
    const el = await mount(<><span data-x="a"><GivenTo given={["Previews", "Tester"]} /></span><span data-x="b"><GivenTo given={[]} /></span></>);
    expect(el.querySelector("[data-x=a]")!.textContent).toBe("PreviewsTester");
    expect(el.querySelector("[data-x=b]")!.textContent).toBe("Nobody");
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
