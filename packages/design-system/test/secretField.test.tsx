/**
 * SecretField: a value written once. Masked until the eye shows it, kept
 * exactly as typed (newlines too), its length said beside the hint, and
 * password managers told to leave it alone. Mounted in happy-dom.
 */

import { afterEach, describe, expect, test } from "bun:test";
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { renderToStaticMarkup } from "react-dom/server";
import { SecretField, secretLength } from "../src/primitives/SecretField.tsx";
import { SettingRow } from "../src/components/Settings.tsx";

let root: Root | null = null;
let host: HTMLElement | null = null;
afterEach(async () => {
  await act(async () => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

const seen: string[] = [];
function Harness({ initial = "" }: { readonly initial?: string | undefined }) {
  const [v, setV] = useState(initial);
  return <SecretField label="Value" hint="Saved once." value={v} onChange={(next) => { seen.push(next); setV(next); }} />;
}

async function render(initial?: string) {
  seen.length = 0;
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => root!.render(<Harness initial={initial} />));
  return {
    area: host.querySelector("textarea")!,
    eye: host.querySelector<HTMLButtonElement>("button[aria-controls]")!,
    length: () => host!.querySelector('[data-testid="secret-length"]')?.textContent ?? "",
  };
}

const PEM = "-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBgkqhkiG9w0BAQEFAASC\nk3Lq9bF0rT2yVf8mWJxQ1s0Z\n-----END PRIVATE KEY-----";

describe("secretLength", () => {
  test("says how much a masked value holds", () => {
    expect(secretLength("")).toBe("");
    expect(secretLength("x")).toBe("1 character");
    expect(secretLength("sk-test-4b1d")).toBe("12 characters");
    expect(secretLength(PEM)).toBe(`4 lines · ${PEM.length} characters`);
    // A trailing newline is not a line of its own; it is a character.
    expect(secretLength("a\nb\n")).toBe("2 lines · 4 characters");
  });
});

describe("SecretField", () => {
  test("starts masked, and the eye shows and hides it, saying which", async () => {
    const { area, eye } = await render(PEM);
    expect(area.getAttribute("data-masked")).toBe("true");
    expect(eye.getAttribute("aria-label")).toBe("Show value");
    expect(eye.getAttribute("aria-pressed")).toBe("false");
    await act(async () => eye.click());
    expect(area.getAttribute("data-masked")).toBeNull();
    expect(eye.getAttribute("aria-label")).toBe("Hide value");
    expect(eye.getAttribute("aria-pressed")).toBe("true");
    await act(async () => eye.click());
    expect(area.getAttribute("data-masked")).toBe("true");
  });

  test("keeps the value as typed, newlines and spaces included, and says its length", async () => {
    const { length } = await render(PEM);
    expect(length()).toBe(`4 lines · ${PEM.length} characters`);
    expect(host!.textContent).toContain("Saved once.");
  });

  test("passes every change on as typed", async () => {
    const { area, length } = await render();
    expect(length()).toBe("");
    const typed = "  line one\nline two\n";
    await act(async () => {
      const set = Object.getOwnPropertyDescriptor(Object.getPrototypeOf(area), "value")!.set!;
      set.call(area, typed);
      area.dispatchEvent(new Event("input", { bubbles: true }));
    });
    expect(seen.at(-1)).toBe(typed);
    expect(length()).toBe(`2 lines · ${typed.length} characters`);
  });

  test("is left alone by password managers, autocorrect and spellcheck", async () => {
    const { area } = await render("x");
    expect(area.getAttribute("autocomplete")).toBe("off");
    expect(area.getAttribute("spellcheck")).toBe("false");
    expect(area.getAttribute("autocorrect")).toBe("off");
    expect(area.hasAttribute("data-1p-ignore")).toBe(true);
    expect(area.getAttribute("data-lpignore")).toBe("true");
  });

  test("an error replaces the hint and marks the field", () => {
    const h = renderToStaticMarkup(<SecretField label="Value" hint="Saved once." error="Enter a value." value="" onChange={() => {}} />);
    expect(h).toContain("Enter a value.");
    expect(h).not.toContain("Saved once.");
    expect(h).toContain('aria-invalid="true"');
  });
});

describe("SettingRow", () => {
  test("a block control takes its column whole", () => {
    const block = renderToStaticMarkup(<SettingRow label="Secrets" block><table /></SettingRow>);
    const inline = renderToStaticMarkup(<SettingRow label="Secrets"><table /></SettingRow>);
    expect(block).toMatch(/data-block="true"><table/);
    expect(inline).not.toContain("data-block");
  });
});
