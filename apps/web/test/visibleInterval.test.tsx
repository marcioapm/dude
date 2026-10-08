/**
 * Work done every so often only while someone can see the page: none
 * while it is hidden, once at once when it is shown again.
 */

import { afterEach, describe, expect, test } from "bun:test";
import { useState } from "react";
import { act, mount, settle } from "./dom.ts";
import { useVisibleInterval } from "../src/hooks/useVisibleInterval.ts";

let mounted: Array<() => Promise<void>> = [];
let hidden = false;
Object.defineProperty(document, "hidden", { configurable: true, get: () => hidden });
Object.defineProperty(document, "visibilityState", { configurable: true, get: () => (hidden ? "hidden" : "visible") });
afterEach(async () => {
  for (const unmount of mounted) await unmount();
  mounted = [];
  hidden = false;
});

async function setHidden(h: boolean) {
  hidden = h;
  await act(async () => {
    document.dispatchEvent(new Event("visibilitychange"));
  });
}

function Probe({ calls, every }: { calls: string[]; every: number }) {
  const [n] = useState(0);
  useVisibleInterval(() => calls.push(hidden ? "hidden" : "visible"), every);
  return <span>{n}</span>;
}

describe("useVisibleInterval", () => {
  test("runs on mount and on its interval while shown; not while hidden; once when shown again", async () => {
    const calls: string[] = [];
    const { unmount } = await mount(<Probe calls={calls} every={30} />);
    mounted.push(unmount);
    await settle(80);
    const shown = calls.length;
    expect(shown).toBeGreaterThanOrEqual(2);
    await setHidden(true);
    const atHide = calls.length;
    await settle(150);
    expect(calls.length).toBe(atHide);
    expect(calls).not.toContain("hidden");
    await setHidden(false);
    expect(calls.length).toBe(atHide + 1);
    await settle(80);
    expect(calls.length).toBeGreaterThan(atHide + 1);
  });

  test("stops when unmounted, and a visibility change after does nothing", async () => {
    const calls: string[] = [];
    const { unmount } = await mount(<Probe calls={calls} every={30} />);
    await settle(50);
    await unmount();
    const at = calls.length;
    await setHidden(true);
    await setHidden(false);
    await settle(80);
    expect(calls.length).toBe(at);
  });
});
