/**
 * ModelPicker, mounted in happy-dom: what the chip says (following the
 * organisation, or chosen), what the menu offers and refuses and why, what
 * a pick hands back, and that a reader gets the chip with nothing to open.
 */

import { afterEach, describe, expect, test } from "bun:test";
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { ModelPicker, type ModelChoice, type PickerTier } from "../src/components/ModelPicker.tsx";
import { TooltipProvider } from "../src/primitives/Tooltip.tsx";
import { accessibleName, allByRole, byRole } from "./queries.ts";

let root: Root | null = null;
let host: HTMLElement | null = null;
afterEach(async () => {
  await act(async () => root?.unmount());
  host?.remove();
  root = host = null;
});

async function mount(el: React.ReactElement): Promise<HTMLElement> {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(<TooltipProvider>{el}</TooltipProvider>));
  return host;
}

const CLAUDE: PickerTier = { id: "mtr_claude", name: "Claude (High)", model: "claude-opus-5" };
const SOL: PickerTier = { id: "mtr_sol", name: "Sol", model: "gpt-6-sol" };
const TIERS = [CLAUDE, SOL];
const ORG = { tier: CLAUDE, harness: "claude-code" as const };
const DEFAULT: ModelChoice = { tier: null, harness: null };

async function openMenu(el: HTMLElement) {
  const chip = el.querySelector("[data-testid=model-picker]")!;
  await act(async () => void chip.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true, button: 0, pointerType: "mouse" })));
  return document.querySelector("[role=menu]")!;
}
const radios = () => allByRole(document, "menuitemradio");
// An item by the words it starts with: its label, then its model or why it is refused.
function radio(label: string): HTMLElement {
  const found = radios().filter((r) => accessibleName(r).startsWith(label));
  expect(found.length, label).toBe(1);
  return found[0]!;
}
const disabled = (el: HTMLElement) => el.getAttribute("aria-disabled") === "true";

describe("ModelPicker", () => {
  test("following the organisation: the effective pair, marked default, and named so", async () => {
    const el = await mount(<ModelPicker tiers={TIERS} organization={ORG} value={DEFAULT} onChange={() => undefined} />);
    const chip = byRole(el, "button", "Model: Claude (High) on Claude Code (organisation default)");
    expect(chip.textContent).toBe("Claude (High)· Claude Codedefault");
  });

  test("chosen: the chosen pair, no default mark", async () => {
    const el = await mount(<ModelPicker tiers={TIERS} organization={ORG} value={{ tier: SOL.id, harness: "codex" }} onChange={() => undefined} />);
    const chip = byRole(el, "button", "Model: Sol on Codex");
    expect(chip.textContent).toBe("Sol· Codex");
    expect(el.querySelector("[data-testid=model-picker-default]") === null).toBe(true);
  });

  test("the menu: two labelled groups, each led by the organisation's default, tiers with their models", async () => {
    const el = await mount(<ModelPicker tiers={TIERS} organization={{ tier: CLAUDE, harness: "opencode" }} value={DEFAULT} onChange={() => undefined} />);
    const menu = await openMenu(el);
    const groups = [...menu.querySelectorAll("[role=group]")].map((g) => accessibleName(g));
    expect(groups).toEqual(["Model tier", "Harness"]);
    expect(radios().map((r) => r.textContent)).toEqual([
      "Organisation default (Claude (High))claude-opus-5", "Claude (High)claude-opus-5", "Solgpt-6-sol",
      "Organisation default (OpenCode)", "OpenCode", "Claude Code",
      "CodexCodex takes an OpenAI model; Claude (High) requests claude-opus-5",
    ]);
    // The current choice is checked in each group.
    expect(radios().filter((r) => r.getAttribute("aria-checked") === "true").map((r) => r.textContent))
      .toEqual(["Organisation default (Claude (High))claude-opus-5", "Organisation default (OpenCode)"]);
  });

  test("a pair the harness cannot run is disabled and says why, in both groups", async () => {
    // On Claude Code (the organisation's): an OpenAI tier is refused.
    const el = await mount(<ModelPicker tiers={TIERS} organization={ORG} value={DEFAULT} onChange={() => undefined} />);
    await openMenu(el);
    expect(disabled(radio("Sol"))).toBe(true);
    expect(radio("Sol").textContent).toContain("Claude Code takes an Anthropic model (claude-…); Sol requests gpt-6-sol");
    expect(disabled(radio("Claude (High)"))).toBe(false);
    // On Claude's tier, Codex is refused; OpenCode and Claude Code are not.
    expect(disabled(radio("Codex"))).toBe(true);
    expect(radio("Codex").textContent).toContain("Codex takes an OpenAI model; Claude (High) requests claude-opus-5");
    expect(["OpenCode", "Claude Code"].map((h) => disabled(radio(h)))).toEqual([false, false]);
  });

  test("a disabled item cannot be picked", async () => {
    const picked: ModelChoice[] = [];
    const el = await mount(<ModelPicker tiers={TIERS} organization={ORG} value={DEFAULT} onChange={(c) => picked.push(c)} />);
    await openMenu(el);
    await act(async () => radio("Sol").click());
    expect(picked).toEqual([]);
  });

  test("picking hands back both halves; the menu stays open for the other; Organisation default sends null", async () => {
    const picked: ModelChoice[] = [];
    function Picking() {
      const [value, setValue] = useState<ModelChoice>(DEFAULT);
      return <ModelPicker tiers={TIERS} organization={{ tier: CLAUDE, harness: "opencode" }} value={value}
        onChange={(c) => { picked.push(c); setValue(c); }} />;
    }
    const el = await mount(<Picking />);
    await openMenu(el);
    await act(async () => radio("Sol").click());
    expect(document.querySelector("[role=menu]")).not.toBeNull();
    await act(async () => radio("Codex").click());
    // On Codex the organisation's Claude tier is refused, so the harness goes back first.
    expect(disabled(radio("Organisation default (Claude (High))"))).toBe(true);
    await act(async () => radio("Organisation default (OpenCode)").click());
    await act(async () => radio("Organisation default (Claude (High))").click());
    expect(picked).toEqual([
      { tier: SOL.id, harness: null },
      { tier: SOL.id, harness: "codex" },
      { tier: SOL.id, harness: null },
      { tier: null, harness: null },
    ]);
  });

  test("on a chosen pair, each item is judged against the pair's other half: on Sol and Codex, the Anthropic tier and the organisation's Claude Code are refused", async () => {
    const el = await mount(<ModelPicker tiers={TIERS} organization={ORG} value={{ tier: SOL.id, harness: "codex" }} onChange={() => undefined} />);
    await openMenu(el);
    // Codex cannot run claude-opus-5: the Claude tier, and the organisation's default tier (Claude), are refused.
    expect(disabled(radio("Claude (High)"))).toBe(true);
    expect(radio("Claude (High)").textContent).toContain("Codex takes an OpenAI model; Claude (High) requests claude-opus-5");
    expect(disabled(radio("Sol"))).toBe(false);
    // Claude Code cannot run Sol: the organisation's harness is refused, and says why.
    const orgHarness = radio("Organisation default (Claude Code)");
    expect(disabled(orgHarness)).toBe(true);
    expect(orgHarness.textContent).toContain("Claude Code takes an Anthropic model (claude-…); Sol requests gpt-6-sol");
    expect(disabled(radio("Codex"))).toBe(false);
  });

  test("a tier that names no model is refused, saying so", async () => {
    const empty: PickerTier = { id: "mtr_fast", name: "Fast", model: null };
    const picked: ModelChoice[] = [];
    const el = await mount(<ModelPicker tiers={[...TIERS, empty]} organization={ORG} value={DEFAULT} onChange={(c) => picked.push(c)} />);
    await openMenu(el);
    expect(disabled(radio("Fast"))).toBe(true);
    expect(radio("Fast").textContent).toBe("Fastnames no model yet");
    await act(async () => radio("Fast").click());
    expect(picked).toEqual([]);
  });

  test("a pair that no longer fits: an attention mark on the chip, and why under it, read with it", async () => {
    const why = "Codex takes an OpenAI model, but the tier Claude (High) requests claude-opus-5. Choose another harness or tier in the session's Model.";
    for (const readOnly of [false, true]) {
      const el = await mount(<ModelPicker tiers={TIERS} organization={ORG} value={{ tier: null, harness: "codex" }} misfit={why} readOnly={readOnly}
        onChange={() => undefined} />);
      const chip = el.querySelector<HTMLElement>("[data-testid=model-picker]")!;
      const described = chip.getAttribute("aria-describedby");
      expect(described && document.getElementById(described)?.textContent).toBe(why);
      expect(el.querySelector("[data-testid=model-picker-misfit]")).not.toBeNull();
      await act(async () => root?.unmount());
      host?.remove();
    }
    const fits = await mount(<ModelPicker tiers={TIERS} organization={ORG} value={DEFAULT} misfit={null} onChange={() => undefined} />);
    expect(fits.querySelector("[data-testid=model-picker]")!.hasAttribute("aria-describedby")).toBe(false);
    expect(fits.querySelector("[data-testid=model-picker-misfit]")).toBeNull();
  });

  test("a note atop the menu, when it cannot list everything yet", async () => {
    const el = await mount(<ModelPicker tiers={[]} organization={ORG} value={DEFAULT} menuNote="Could not load the tiers" onChange={() => undefined} />);
    const menu = await openMenu(el);
    expect(menu.textContent).toContain("Could not load the tiers");
  });

  test("Escape closes the menu and changes nothing", async () => {
    const picked: ModelChoice[] = [];
    const el = await mount(<ModelPicker tiers={TIERS} organization={ORG} value={DEFAULT} onChange={(c) => picked.push(c)} />);
    const menu = await openMenu(el);
    await act(async () => void menu.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })));
    expect(document.querySelector("[role=menu]") === null).toBe(true);
    expect(picked).toEqual([]);
  });

  test("read only: the chip's words and name, and nothing to press or open", async () => {
    const el = await mount(<ModelPicker tiers={TIERS} organization={ORG} value={{ tier: SOL.id, harness: "opencode" }} readOnly onChange={() => undefined} />);
    const chip = el.querySelector("[data-testid=model-picker]")!;
    expect(el.querySelectorAll("button").length).toBe(0);
    expect(chip.textContent).toContain("Model: Sol on OpenCode");
    await act(async () => void chip.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true, button: 0, pointerType: "mouse" })));
    expect(document.querySelector("[role=menu]") === null).toBe(true);
  });
});
