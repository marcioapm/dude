/**
 * Button's quiet danger kind, and RefusedButton: a control that can't be
 * used now and still says why.
 */

import { afterEach, describe, expect, test } from "bun:test";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { Button } from "../src/primitives/Button.tsx";
import { RefusedButton } from "../src/primitives/RefusedButton.tsx";
import { TooltipProvider } from "../src/primitives/Tooltip.tsx";

let root: Root | null = null;
let host: HTMLElement | null = null;
afterEach(async () => {
  await act(async () => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

async function mount(node: ReactNode): Promise<HTMLElement> {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => root!.render(<TooltipProvider>{node}</TooltipProvider>));
  return host;
}

describe("Button danger, quiet", () => {
  test("danger quiet is danger without the wash; quiet means nothing to other kinds or beside solid", async () => {
    const el = await mount(<>
      <Button variant="danger" quiet>Stop</Button>
      <Button variant="danger">Abort</Button>
      <Button variant="danger" quiet solid>Confirm</Button>
      <Button variant="secondary" quiet>Other</Button>
    </>);
    const [quiet, plain, solid, other] = [...el.querySelectorAll("button")].map((b) => [b.getAttribute("data-variant"), b.getAttribute("data-weight")]);
    expect(quiet).toEqual(["danger", "quiet"]);
    expect(plain).toEqual(["danger", null]);
    expect(solid).toEqual(["danger", "solid"]);
    expect(other).toEqual(["secondary", null]);
  });
});

describe("RefusedButton", () => {
  test("a focusable disabled button named by its label and described by its reason; the button inside takes no focus", async () => {
    const el = await mount(<RefusedButton reason="Only Ana, the owner, or an admin can do this" label="Stop" variant="danger" quiet leadingIcon="stop"
      data-testid="stop" />);
    const control = el.querySelector<HTMLElement>('[data-testid="stop"]')!;
    expect(control.getAttribute("role")).toBe("button");
    expect(control.getAttribute("aria-disabled")).toBe("true");
    expect(control.getAttribute("aria-label")).toBe("Stop");
    expect(control.tabIndex).toBe(0);
    expect(document.getElementById(control.getAttribute("aria-describedby")!)!.textContent).toBe("Only Ana, the owner, or an admin can do this");
    const inner = control.querySelector("button")!;
    expect([inner.disabled, inner.tabIndex, inner.textContent]).toEqual([true, -1, "Stop"]);
    expect([inner.getAttribute("data-variant"), inner.getAttribute("data-weight")]).toEqual(["danger", "quiet"]);
  });
});
