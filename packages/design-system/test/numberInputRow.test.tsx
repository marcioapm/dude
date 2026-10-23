/**
 * A NumberInput in a SettingRow, read from the computed style with the
 * shipped stylesheets loaded: its field keeps a floor on its inline size,
 * so a shrinking row cannot collapse it to its − and + (a container's
 * intrinsic width is 0).
 */

import { afterAll, beforeAll, expect, mock, test } from "bun:test";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { cssModuleAsWritten } from "./cssModule.ts";

mock.module(...cssModuleAsWritten(new URL("../src/primitives/NumberInput.module.css", import.meta.url)));
mock.module(...cssModuleAsWritten(new URL("../src/components/Settings.module.css", import.meta.url)));

const { NumberInput } = await import("../src/primitives/NumberInput.tsx");
const { SettingRow } = await import("../src/components/Settings.tsx");

const styleEls: HTMLStyleElement[] = [];
let root: Root | null = null;
let host: HTMLElement | null = null;

beforeAll(async () => {
  for (const path of ["tokens/tokens.css", "primitives/NumberInput.module.css", "components/Settings.module.css"]) {
    const el = document.createElement("style");
    el.textContent = await Bun.file(new URL(`../src/${path}`, import.meta.url)).text();
    document.head.append(el);
    styleEls.push(el);
  }
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(
    <SettingRow label="Expiry" help="Days without a run.">
      <NumberInput aria-label="Expiry in days" step={1} min={1} max={30} value={7} onValueChange={() => undefined} />
    </SettingRow>,
  ));
});

afterAll(async () => {
  await act(async () => root?.unmount());
  host?.remove();
  for (const el of styleEls) el.remove();
});

test("a NumberInput in a SettingRow keeps a floor on its width: four controls' height", () => {
  const field = host!.querySelector(".rowControl .field");
  expect(field).not.toBeNull();
  const style = getComputedStyle(field!);
  const floor = style.minInlineSize || style.minWidth;
  const control = getComputedStyle(document.documentElement).getPropertyValue("--ds-size-control-md").trim();
  expect(control).toMatch(/^\d+px$/);
  expect(floor).toBe(`calc(${control} * 4)`);
});
