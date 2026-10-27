/**
 * Inside a <form>, Radix's Checkbox and Select add a hidden, absolutely
 * positioned native control beside their button. With no positioned
 * ancestor its containing block is the initial one: it lands at the
 * control's document offset and makes the page taller than an app shell
 * that scrolls only its main pane. Each primitive's own root contains it.
 * Read from the computed style with the shipped stylesheet (happy-dom has no layout).
 */

import { afterEach, describe, expect, mock, test } from "bun:test";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { cssModuleAsWritten } from "./cssModule.ts";

const CHECKBOX_CSS = new URL("../src/primitives/Checkbox.module.css", import.meta.url);
const SELECT_CSS = new URL("../src/primitives/Select.module.css", import.meta.url);
mock.module(...cssModuleAsWritten(CHECKBOX_CSS));
mock.module(...cssModuleAsWritten(SELECT_CSS));

const { Checkbox } = await import("../src/primitives/Checkbox.tsx");
const { Select } = await import("../src/primitives/Select.tsx");

let root: Root | null = null;
let host: HTMLElement | null = null;
let style: HTMLStyleElement | null = null;
afterEach(async () => {
  await act(async () => root?.unmount());
  host?.remove();
  style?.remove();
  root = host = style = null;
});

async function mountInForm(css: URL, node: ReactNode): Promise<HTMLElement> {
  style = document.createElement("style");
  style.textContent = await Bun.file(css).text();
  document.head.append(style);
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(<form>{node}</form>));
  return host;
}

/** The element whose box an absolutely positioned `el` is placed against, or null for the initial containing block. */
function containingBlock(el: Element): Element | null {
  for (let at = el.parentElement; at; at = at.parentElement) {
    // happy-dom gives "" for a position no rule sets: the initial value, static.
    const position = getComputedStyle(at).position || "static";
    if (position !== "static") return at;
  }
  return null;
}

describe("a form control's hidden native input is contained by the primitive", () => {
  test("Checkbox", async () => {
    const page = await mountInForm(CHECKBOX_CSS, <Checkbox name="token" label="GH_TOKEN" />);
    const hidden = page.querySelector("input[type=checkbox][aria-hidden]")!;
    expect(hidden).not.toBeNull();
    expect(getComputedStyle(hidden).position).toBe("absolute");
    expect(containingBlock(hidden)).toBe(page.querySelector("form > div"));
  });

  test("Select", async () => {
    const page = await mountInForm(SELECT_CSS, <Select name="tz" label="Time zone" value="UTC"
      options={[{ value: "UTC", label: "UTC" }, { value: "Europe/Lisbon", label: "Lisbon" }]} />);
    const hidden = page.querySelector("select[aria-hidden]")!;
    expect(hidden).not.toBeNull();
    expect(getComputedStyle(hidden).position).toBe("absolute");
    expect(containingBlock(hidden)).toBe(page.querySelector("form > div"));
  });
});
