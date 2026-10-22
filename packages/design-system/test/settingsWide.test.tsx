/**
 * A settings page's column, read from the computed style with the shipped
 * stylesheets loaded: capped at 1100px, except a role's page (`wide`),
 * whose prompt fills the pane.
 */

import { afterAll, beforeAll, expect, mock, test } from "bun:test";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const asWritten = () => ({ default: new Proxy({}, { get: (_, key) => (typeof key === "string" ? key : undefined) }) });
mock.module("../src/components/Settings.module.css", asWritten);

const { SettingsLayout } = await import("../src/components/Settings.tsx");

const styleEls: HTMLStyleElement[] = [];
let root: Root | null = null;
let host: HTMLElement | null = null;

beforeAll(async () => {
  for (const path of ["tokens/tokens.css", "components/Settings.module.css"]) {
    const el = document.createElement("style");
    el.textContent = await Bun.file(new URL(`../src/${path}`, import.meta.url)).text();
    document.head.append(el);
    styleEls.push(el);
  }
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const layout = (wide: boolean) => (
    <SettingsLayout data-layout={wide ? "role" : "delivery"} wide={wide} scope={{ title: "Example" }} items={[{ id: "delivery", label: "Delivery" }]}
      current="delivery" onSelect={() => undefined}>
      <p>page</p>
    </SettingsLayout>
  );
  await act(async () => root!.render(<div>{layout(false)}{layout(true)}</div>));
});

afterAll(async () => {
  await act(async () => root?.unmount());
  host?.remove();
  for (const el of styleEls) el.remove();
});

const column = (which: string) => getComputedStyle(host!.querySelector(`[data-layout=${which}] .main`)!).maxWidth;

test("a settings page's column is capped at 1100px; a role's page is not", () => {
  expect(column("delivery")).toBe("1100px");
  expect(column("role")).toBe("none");
});
