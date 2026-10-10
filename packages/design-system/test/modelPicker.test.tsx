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
import { accessibleName, byRole } from "./queries.ts";

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
const radios = () => [...document.querySelectorAll("[role=menuitemradio]")] as HTMLElement[];
const radio = (id: string) => document.querySelector(`[data-testid="rowmenu-${id}"]`) as HTMLElement;

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
    expect(radios().filter((r) => r.getAttribute("aria-checked") === "true").map((r) => r.dataset["testid"]))
      .toEqual(["rowmenu-tier-default", "rowmenu-harness-default"]);
  });

  test("a pair the harness cannot run is disabled and says why, in both groups", async () => {
    // On Claude Code (the organisation's): an OpenAI tier is refused.
    const el = await mount(<ModelPicker tiers={TIERS} organization={ORG} value={DEFAULT} onChange={() => undefined} />);
    await openMenu(el);
    expect(radio(SOL.id).hasAttribute("data-disabled")).toBe(true);
    expect(radio(SOL.id).textContent).toContain("Claude Code takes an Anthropic model; Sol requests gpt-6-sol");
    expect(radio(CLAUDE.id).hasAttribute("data-disabled")).toBe(false);
    // On Claude's tier, Codex is refused; OpenCode and Claude Code are not.
    expect(radio("codex").hasAttribute("data-disabled")).toBe(true);
    expect(radio("codex").textContent).toContain("Codex takes an OpenAI model; Claude (High) requests claude-opus-5");
    expect(["opencode", "claude-code"].map((h) => radio(h).hasAttribute("data-disabled"))).toEqual([false, false]);
  });

  test("a disabled item cannot be picked", async () => {
    const picked: ModelChoice[] = [];
    const el = await mount(<ModelPicker tiers={TIERS} organization={ORG} value={DEFAULT} onChange={(c) => picked.push(c)} />);
    await openMenu(el);
    await act(async () => radio(SOL.id).click());
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
    await act(async () => radio(SOL.id).click());
    expect(document.querySelector("[role=menu]")).not.toBeNull();
    await act(async () => radio("codex").click());
    // On Codex the organisation's Claude tier is refused, so the harness goes back first.
    expect(radio("tier-default").hasAttribute("data-disabled")).toBe(true);
    await act(async () => radio("harness-default").click());
    await act(async () => radio("tier-default").click());
    expect(picked).toEqual([
      { tier: SOL.id, harness: null },
      { tier: SOL.id, harness: "codex" },
      { tier: SOL.id, harness: null },
      { tier: null, harness: null },
    ]);
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
    expect(chip.hasAttribute("data-readonly")).toBe(true);
    await act(async () => void chip.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true, button: 0, pointerType: "mouse" })));
    expect(document.querySelector("[role=menu]") === null).toBe(true);
  });
});
