/**
 * ChoiceList: a radio group of a few ways to do something. One tab stop on
 * the chosen option; arrows move and choose, skipping an option that cannot
 * be chosen, which says why in place of its description.
 * Mounted in happy-dom, driven by the events a browser sends.
 */

import { afterEach, describe, expect, test } from "bun:test";
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { ChoiceList, type ChoiceOption } from "../src/primitives/ChoiceList.tsx";

let root: Root | null = null;
let host: HTMLElement | null = null;
afterEach(async () => {
  await act(async () => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

type Way = "resume" | "retry" | "restart";
const OPTIONS: ChoiceOption<Way>[] = [
  { value: "resume", label: "Resume", description: "The same agent goes on.", disabledReason: "No longer kept." },
  { value: "retry", label: "Try again", description: "A new agent, same branch." },
  { value: "restart", label: "Start over", description: "A new attempt." },
];

const chosen: Way[] = [];
function Harness() {
  const [value, setValue] = useState<Way>("retry");
  return <ChoiceList label="How" value={value} options={OPTIONS} onChange={(v) => { chosen.push(v); setValue(v); }} />;
}

async function render() {
  chosen.length = 0;
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => root!.render(<Harness />));
  return [...host.querySelectorAll<HTMLButtonElement>('[role="radio"]')];
}

const key = (el: HTMLElement, k: string) => act(async () => { el.dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true })); });

describe("ChoiceList", () => {
  test("is a named radio group whose chosen option is the one tab stop", async () => {
    const radios = await render();
    expect(host!.querySelector('[role="radiogroup"]')?.getAttribute("aria-label")).toBe("How");
    expect(radios.map((r) => r.getAttribute("aria-checked"))).toEqual(["false", "true", "false"]);
    expect(radios.map((r) => r.tabIndex)).toEqual([-1, 0, -1]);
  });

  test("an option that cannot be chosen says why, and a click does nothing", async () => {
    const radios = await render();
    expect(radios[0]!.getAttribute("aria-disabled")).toBe("true");
    expect(radios[0]!.textContent).toContain("No longer kept.");
    expect(radios[0]!.textContent).not.toContain("The same agent goes on.");
    await act(async () => radios[0]!.click());
    expect(chosen).toEqual([]);
  });

  test("arrows choose the next that can be chosen, wrapping past one that cannot", async () => {
    const radios = await render();
    await key(radios[1]!, "ArrowDown");
    expect(chosen).toEqual(["restart"]);
    await key(radios[2]!, "ArrowDown");
    expect(chosen).toEqual(["restart", "retry"]);
    await key(radios[1]!, "End");
    expect(chosen.at(-1)).toBe("restart");
    expect(document.activeElement).toBe(radios[2]!);
  });
});
