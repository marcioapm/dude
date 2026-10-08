/**
 * Input's error action: the one-click fix shown beside a field's error.
 * Mounted in happy-dom and clicked as a browser would.
 */

import { afterEach, expect, test } from "bun:test";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { Input } from "../src/primitives/Input.tsx";

let root: Root | null = null;
let host: HTMLElement | null = null;
afterEach(async () => {
  await act(async () => root?.unmount());
  host?.remove();
  root = host = null;
});

async function mount(node: React.ReactNode) {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => root!.render(node));
  return host;
}

test("an error with an action shows both; the action's click is the caller's, and it does not submit the form", async () => {
  const clicks: string[] = [];
  let submitted = 0;
  const h = await mount(
    <form onSubmit={(e) => { e.preventDefault(); submitted++; }}>
      <Input label="Key" defaultValue="BILL" error="BILL is already the key of Billing API; pick another"
        errorAction={{ label: "Use BLED", onClick: () => clicks.push("BLED"), "data-testid": "fix" }} />
    </form>,
  );
  const input = h.querySelector("input")!;
  const described = h.querySelector(`#${CSS.escape(input.getAttribute("aria-describedby")!)}`)!;
  expect(described.textContent).toBe("BILL is already the key of Billing API; pick another");
  expect(input.getAttribute("aria-invalid")).toBe("true");
  const fix = h.querySelector<HTMLButtonElement>("[data-testid=fix]")!;
  expect(fix.textContent).toBe("Use BLED");
  expect(fix.type).toBe("button");
  await act(async () => fix.click());
  expect(clicks).toEqual(["BLED"]);
  expect(submitted).toBe(0);
});

test("an action without an error, or with only a hint, is not shown", async () => {
  const action = { label: "Use BLED", onClick: () => undefined, "data-testid": "fix" };
  let h = await mount(<Input label="Key" errorAction={action} />);
  expect(h.querySelectorAll("[data-testid=fix]").length).toBe(0);
  await act(async () => root!.render(<Input label="Key" hint="Starts its tasks' keys" errorAction={action} />));
  h = host!;
  expect(h.querySelectorAll("[data-testid=fix]").length).toBe(0);
  expect(h.textContent).toContain("Starts its tasks' keys");
});
