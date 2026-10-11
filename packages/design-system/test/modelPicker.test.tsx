/**
 * ModelPicker, mounted in happy-dom: what the chip says (following the
 * organisation, or chosen), what the menu offers (any harness with any
 * tier) and refuses (a tier naming no model), what a pick hands back, and
 * that a reader gets the chip with nothing to open.
 */

import { afterEach, describe, expect, test } from "bun:test";
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { ModelPicker, type ModelChoice, type PickerTier } from "../src/components/ModelPicker.tsx";
import { TooltipProvider } from "../src/primitives/Tooltip.tsx";
import { accessibleName, allByRole, byRole } from "./queries.ts";

let root: Root | null = null;
let host: HTMLElement | null = null;
async function unmount(): Promise<void> {
  await act(async () => root?.unmount());
  host?.remove();
  root = host = null;
}
afterEach(unmount);

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
      "Organisation default (OpenCode)", "OpenCode", "Claude Code", "Codex",
    ]);
    // The current choice is checked in each group.
    expect(radios().filter((r) => r.getAttribute("aria-checked") === "true").map((r) => r.textContent))
      .toEqual(["Organisation default (Claude (High))claude-opus-5", "Organisation default (OpenCode)"]);
  });

  test("any harness takes any tier: Codex on a Claude tier and Claude Code on a GPT one are offered and picked, with no note", async () => {
    const picked: ModelChoice[] = [];
    // On Claude Code with the Claude tier (the organisation's): Codex and Sol are offered as any other.
    const el = await mount(<ModelPicker tiers={TIERS} organization={ORG} value={DEFAULT} onChange={(c) => picked.push(c)} />);
    expect(el.querySelector("[data-testid=model-picker]")!.hasAttribute("aria-describedby")).toBe(false);
    await openMenu(el);
    expect(radios().filter(disabled).map((r) => r.textContent)).toEqual([]);
    expect(radio("Codex").textContent).toBe("Codex");
    expect(radio("Sol").textContent).toBe("Solgpt-6-sol");
    await act(async () => radio("Codex").click());
    await act(async () => radio("Sol").click());
    expect(picked).toEqual([{ tier: null, harness: "codex" }, { tier: SOL.id, harness: null }]);
    await unmount();

    // The reverse: on Sol and Codex, the Claude tier and the organisation's Claude Code.
    const chosen = await mount(<ModelPicker tiers={TIERS} organization={ORG} value={{ tier: SOL.id, harness: "codex" }} onChange={() => undefined} />);
    await openMenu(chosen);
    expect(radios().filter(disabled).map((r) => r.textContent)).toEqual([]);
    expect(radio("Claude (High)").textContent).toBe("Claude (High)claude-opus-5");
    expect(radio("Organisation default (Claude Code)").textContent).toBe("Organisation default (Claude Code)");
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
    await act(async () => radio("Organisation default (Claude (High))").click());
    await act(async () => radio("Organisation default (OpenCode)").click());
    expect(picked).toEqual([
      { tier: SOL.id, harness: null },
      { tier: SOL.id, harness: "codex" },
      { tier: null, harness: "codex" },
      { tier: null, harness: null },
    ]);
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

  test("a note atop the menu, when it cannot list everything yet, is the menu's description", async () => {
    const el = await mount(<ModelPicker tiers={[]} organization={ORG} value={DEFAULT} menuNote="Could not load the tiers" onChange={() => undefined} />);
    const menu = await openMenu(el);
    expect(menu.textContent).toContain("Could not load the tiers");
    // Menu mode reads items alone: the note reaches a screen reader only as what the menu says it is.
    const description = (menu.getAttribute("aria-describedby") ?? "").split(/\s+/).map((id) => document.getElementById(id)?.textContent).join(" ");
    expect(description).toBe("Could not load the tiers");
  });

  test("with no note, the menu has no description", async () => {
    const el = await mount(<ModelPicker tiers={TIERS} organization={ORG} value={DEFAULT} onChange={() => undefined} />);
    const menu = await openMenu(el);
    expect(menu.hasAttribute("aria-describedby")).toBe(false);
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
