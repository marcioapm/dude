/**
 * The machine sizes pieces: NumberInput (steps, keys, bounds, what is
 * typed kept as typed), the Select's meta and description, and the bars
 * and chip a size is shown with. Mounted in happy-dom and driven by the
 * events a browser sends.
 */

import { afterEach, describe, expect, test } from "bun:test";
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { renderToStaticMarkup } from "react-dom/server";
import { NumberInput } from "../src/primitives/NumberInput.tsx";
import { Select } from "../src/primitives/Select.tsx";
import { TooltipProvider } from "../src/primitives/Tooltip.tsx";
import { FitBar, MachineChip, ProportionBar } from "../src/components/Machines.tsx";

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
  return host;
}

const seen: Array<number | null> = [];
function Stepper({ initial, step, min, max, error }: { initial: number | null; step: number; min?: number; max?: number; error?: string }) {
  const [v, setV] = useState<number | null>(initial);
  return (
    <NumberInput label="CPUs" unit="CPUs" value={v} step={step} min={min} max={max} error={error} hint="In steps of 0.5"
      onValueChange={(n) => {
        seen.push(n);
        setV(n);
      }} />
  );
}

const input = () => host!.querySelector("input")!;
const key = (k: string) => act(async () => void input().dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true })));
const click = (dir: "up" | "down") => act(async () => void host!.querySelector<HTMLButtonElement>(`[data-step="${dir}"]`)!.click());
const type = async (text: string) => {
  const el = input();
  const setter = Object.getOwnPropertyDescriptor(Object.getPrototypeOf(el), "value")!.set!;
  await act(async () => {
    setter.call(el, text);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
};

describe("NumberInput", () => {
  test("↑ and ↓ move it a step, − and + too, and neither passes a bound", async () => {
    seen.length = 0;
    await mount(<Stepper initial={1} step={0.5} min={0.5} max={2} />);
    await key("ArrowUp");
    expect(input().value).toBe("1.5");
    await click("up");
    expect(input().value).toBe("2");
    await key("ArrowUp");
    expect(input().value).toBe("2");
    expect(host!.querySelector<HTMLButtonElement>('[data-step="up"]')!.disabled).toBe(true);
    await key("ArrowDown");
    await click("down");
    await key("ArrowDown");
    expect(input().value).toBe("0.5");
    expect(seen).toEqual([1.5, 2, 2, 1.5, 1, 0.5]);
  });

  test("a step in tenths reads as tenths, not floating point", async () => {
    await mount(<Stepper initial={0.1} step={0.1} />);
    await key("ArrowUp");
    await key("ArrowUp");
    expect(input().value).toBe("0.3");
  });

  test("what is typed is kept as typed, and a step from off the grid lands on it", async () => {
    seen.length = 0;
    await mount(<Stepper initial={2} step={0.5} min={0.5} />);
    await type("2.3");
    expect(input().value).toBe("2.3");
    expect(seen.at(-1)).toBe(2.3);
    await key("ArrowUp");
    expect(input().value).toBe("2.5");
    await type("2.3");
    await key("ArrowDown");
    expect(input().value).toBe("2");
    await type("lots");
    expect(seen.at(-1)).toBeNull();
  });

  test("its anatomy: a spinbutton named by its label, the unit, and the error in place of the hint", async () => {
    await mount(<Stepper initial={2.3} step={0.5} error="Whole or half CPUs: 0.5, 1, 1.5…" />);
    const el = input();
    expect(el.getAttribute("role")).toBe("spinbutton");
    expect(host!.querySelector("label")!.htmlFor).toBe(el.id);
    expect(host!.textContent).toContain("CPUs");
    expect(el.getAttribute("aria-invalid")).toBe("true");
    const described = document.getElementById(el.getAttribute("aria-describedby")!)!;
    expect(described.textContent).toBe("Whole or half CPUs: 0.5, 1, 1.5…");
    expect(host!.textContent).not.toContain("In steps of 0.5");
    // The buttons are not tab stops: the keys do what they do.
    expect([...host!.querySelectorAll("button")].every((b) => b.tabIndex === -1)).toBe(true);
  });
});

describe("Select with meta", () => {
  test("rendered statically, the trigger carries its aria-label", () => {
    const h = renderToStaticMarkup(
      <Select aria-label="Machine" value="lg" options={[
        { value: "std", label: "Standard", meta: "2 CPUs · 8 GiB · 20 GiB" },
        { value: "lg", label: "Large", meta: "8 CPUs · 16 GiB · 80 GiB", description: "For builds" },
      ]} />,
    );
    expect(h).toContain('aria-label="Machine"');
  });

  test("open, each option shows its meta, a description under it, and the footer", async () => {
    await mount(
      <Select aria-label="Machine" defaultValue="std" footer={<span>Sizes are Acme’s</span>} options={[
        { value: "std", label: "Standard", meta: "2 CPUs · 8 GiB · 20 GiB" },
        { value: "lg", label: "Large", meta: "8 CPUs · 16 GiB · 80 GiB", description: "Follows whichever size is the default" },
      ]} />,
    );
    const trigger = host!.querySelector<HTMLButtonElement>("button")!;
    expect(trigger.textContent).toContain("Standard");
    expect(trigger.textContent).toContain("2 CPUs · 8 GiB · 20 GiB");
    await act(async () => {
      trigger.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true, button: 0, pointerType: "mouse" }));
    });
    const list = document.querySelector("[role=listbox]");
    expect(list).not.toBeNull();
    const large = document.querySelector('[data-value="lg"]')!;
    expect(large.textContent).toContain("8 CPUs · 16 GiB · 80 GiB");
    expect(large.textContent).toContain("Follows whichever size is the default");
    expect(document.body.textContent).toContain("Sizes are Acme’s");
  });
});

describe("the bars and the chip", () => {
  test("a proportion bar sizes each part by its share, and names the reserved one", () => {
    const h = renderToStaticMarkup(
      <ProportionBar aria-label="32 GiB: 2 kept, two runs of 15" total="32 GiB" legend="Linux and the host · 2 GiB" segments={[
        { id: "host", value: 2, kind: "reserved" },
        { id: "a", value: 15, label: "Run A · asks 16 · gets 15" },
        { id: "b", value: 15, label: "Run B · asks 16 · gets 15" },
      ]} />,
    );
    expect(h).toContain('role="img"');
    expect(h).toContain("flex-grow:0.0625");
    expect(h).toContain("flex-grow:0.46875");
    expect(h).toContain("Run A · asks 16 · gets 15");
    expect(h).toContain("32 GiB");
  });

  test("a fit bar is a meter only when the host's size is known", () => {
    expect(renderToStaticMarkup(<FitBar share={0.5}>50% of a host</FitBar>)).toContain('aria-valuenow="50"');
    const unknown = renderToStaticMarkup(<FitBar share={null}>Unknown</FitBar>);
    expect(unknown).not.toContain("meter");
    expect(unknown).toContain("Unknown");
  });

  test("the chip names the machine to a screen reader, and its tooltip says where it came from", async () => {
    await mount(
      <TooltipProvider>
        <MachineChip name="XL" spec="16 CPUs · 48 GiB · 200 GiB" tooltip="From Checkout’s settings" data-testid="chip" />
      </TooltipProvider>,
    );
    const chip = host!.querySelector<HTMLButtonElement>('[data-testid="chip"]')!;
    expect(chip.getAttribute("aria-label")).toBe("Machine: XL, 16 CPUs · 48 GiB · 200 GiB");
    await act(async () => chip.focus());
    await act(async () => void (await new Promise((r) => setTimeout(r, 20))));
    expect(document.querySelector("[role=tooltip]")?.textContent).toContain("From Checkout’s settings");
  });
});
