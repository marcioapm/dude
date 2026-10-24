/**
 * A NumberInput in a SettingRow and in a FormRow, read from the computed
 * style with the shipped stylesheets loaded (happy-dom has no layout; the
 * rendered widths are measured in a browser). Its field is a container, so
 * its intrinsic width would be 0: it is given one, room for − and + and a
 * few digits, which a shrinking SettingRow keeps. That size is not a floor:
 * a FormRow's 1fr tracks narrower than it (a phone dialog) shrink it.
 */

import { afterAll, beforeAll, expect, mock, test } from "bun:test";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { cssModuleAsWritten } from "./cssModule.ts";

mock.module(...cssModuleAsWritten(new URL("../src/primitives/NumberInput.module.css", import.meta.url)));
mock.module(...cssModuleAsWritten(new URL("../src/components/Settings.module.css", import.meta.url)));
mock.module(...cssModuleAsWritten(new URL("../src/primitives/Layout.module.css", import.meta.url)));

const { NumberInput } = await import("../src/primitives/NumberInput.tsx");
const { SettingRow } = await import("../src/components/Settings.tsx");
const { FormRow } = await import("../src/primitives/Layout.tsx");

const styleEls: HTMLStyleElement[] = [];
let root: Root | null = null;
let host: HTMLElement | null = null;

beforeAll(async () => {
  for (const path of ["tokens/tokens.css", "primitives/NumberInput.module.css", "components/Settings.module.css", "primitives/Layout.module.css"]) {
    const el = document.createElement("style");
    el.textContent = await Bun.file(new URL(`../src/${path}`, import.meta.url)).text();
    document.head.append(el);
    styleEls.push(el);
  }
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const none = () => undefined;
  await act(async () => root!.render(
    <>
      <SettingRow label="Expiry" help="Days without a run.">
        <NumberInput aria-label="Expiry in days" step={1} min={1} max={30} value={7} onValueChange={none} />
      </SettingRow>
      <FormRow data-testid="machine-size">
        <NumberInput label="CPUs" unit="CPUs" value={2} step={0.5} min={0.5} onValueChange={none} />
        <NumberInput label="Memory" unit="GiB" value={4} step={0.5} min={0.5} onValueChange={none} />
        <NumberInput label="Disk" unit="GiB" value={20} step={5} min={5} onValueChange={none} />
      </FormRow>
    </>,
  ));
});

afterAll(async () => {
  await act(async () => root?.unmount());
  host?.remove();
  for (const el of styleEls) el.remove();
});

const controlMd = () => getComputedStyle(document.documentElement).getPropertyValue("--ds-size-control-md").trim();

test("a NumberInput in a SettingRow has an intrinsic width of four controls' height", () => {
  const field = host!.querySelector(".rowControl .field");
  expect(field).not.toBeNull();
  expect(controlMd()).toMatch(/^\d+px$/);
  expect(getComputedStyle(field!).getPropertyValue("contain-intrinsic-inline-size")).toBe(`calc(${controlMd()} * 4)`);
});

test("NumberInputs in a FormRow have no minimum width a narrow track cannot shrink below", () => {
  const fields = [...host!.querySelectorAll("[data-testid=machine-size] > .field")];
  expect(fields.length).toBe(3);
  for (const field of fields) {
    const style = getComputedStyle(field);
    // happy-dom gives the value as written ("0"), a browser "0px".
    expect(["0", "0px"]).toContain(style.minWidth);
    expect(["", "auto", "0px"]).toContain(style.minInlineSize);
  }
});
