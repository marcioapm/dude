/**
 * The model tier pieces: the suggestions under a field that takes any name,
 * and the session header's chip. Mounted in happy-dom and driven by the
 * events a browser sends.
 */

import { afterEach, describe, expect, test } from "bun:test";
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { TooltipProvider } from "../src/primitives/Tooltip.tsx";
import { NameChips, TierChip } from "../src/components/Tiers.tsx";

let root: Root | null = null;
let host: HTMLElement | null = null;
afterEach(async () => {
  await act(async () => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

async function mount(node: React.ReactNode) {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => root!.render(node));
}

describe("NameChips", () => {
  function Picker() {
    const [value, setValue] = useState("b");
    return <><NameChips label="Names" names={["a", "b", "c"]} value={value} onPick={setValue} /><output>{value}</output></>;
  }

  test("the chosen name is pressed; picking another moves it", async () => {
    await mount(<Picker />);
    const chips = [...host!.querySelectorAll<HTMLButtonElement>("[role=group] button")];
    expect(chips.map((c) => c.getAttribute("aria-pressed"))).toEqual(["false", "true", "false"]);
    await act(async () => chips[2]!.click());
    expect(host!.querySelector("output")?.textContent).toBe("c");
    expect(chips.map((c) => c.getAttribute("aria-pressed"))).toEqual(["false", "false", "true"]);
  });
});

describe("TierChip", () => {
  test("names the tier and the model to a screen reader, and its tooltip opens on focus", async () => {
    await mount(<TooltipProvider><TierChip tier="Coder" model="claude-opus-5-5" tooltip="What dude asked for" data-testid="chip" /></TooltipProvider>);
    const chip = host!.querySelector<HTMLButtonElement>('[data-testid="chip"]')!;
    expect(chip.getAttribute("aria-label")).toBe("Model: Coder, requests claude-opus-5-5");
    await act(async () => chip.focus());
    await act(async () => void (await new Promise((r) => setTimeout(r, 20))));
    expect(document.querySelector("[role=tooltip]")?.textContent).toContain("What dude asked for");
  });

  test("with no tier, the model alone", async () => {
    await mount(<TierChip model="llm-anthropic/claude-sonnet-5" data-testid="chip" />);
    const chip = host!.querySelector<HTMLButtonElement>('[data-testid="chip"]')!;
    expect(chip.getAttribute("aria-label")).toBe("Model: llm-anthropic/claude-sonnet-5");
    expect(chip.textContent).toBe("llm-anthropic/claude-sonnet-5");
  });
});
