/**
 * Work done every so often only while someone can see the page: none
 * while it is hidden, once at once when it is shown again.
 *
 * On a fake clock: with real timers a 30 ms interval keeps ticking between
 * any two lines here, so "exactly once" after a show depended on how fast
 * the runner was.
 */

import { afterEach, beforeEach, describe, expect, jest, test } from "bun:test";
import { act, mount } from "./dom.ts";
import { useVisibleInterval } from "../src/hooks/useVisibleInterval.ts";

let mounted: Array<() => Promise<void>> = [];
let hidden = false;
Object.defineProperty(document, "hidden", { configurable: true, get: () => hidden });
Object.defineProperty(document, "visibilityState", { configurable: true, get: () => (hidden ? "hidden" : "visible") });
beforeEach(() => {
  jest.useFakeTimers();
});
afterEach(async () => {
  jest.useRealTimers();
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

/** Move the fake clock on by `ms`, the ticks it causes landing inside act. */
async function advance(ms: number) {
  await act(async () => {
    jest.advanceTimersByTime(ms);
  });
}

function Probe({ calls, every }: { calls: string[]; every: number }) {
  useVisibleInterval(() => calls.push(hidden ? "hidden" : "visible"), every);
  return null;
}

describe("useVisibleInterval", () => {
  test("runs on mount and on its interval while shown; not while hidden; once when shown again", async () => {
    const calls: string[] = [];
    const { unmount } = await mount(<Probe calls={calls} every={30} />);
    mounted.push(unmount);
    expect(calls.length).toBe(1);
    await advance(80);
    expect(calls.length).toBe(3);
    await setHidden(true);
    expect(calls.length).toBe(3);
    await advance(150);
    expect(calls.length).toBe(3);
    expect(calls).not.toContain("hidden");
    await setHidden(false);
    expect(calls.length).toBe(4);
    // Shown at t=230: two ticks by t=290 whether the interval kept its phase
    // (240, 270) or restarted on show (260, 290); one that waits 2*ms has one.
    await advance(60);
    expect(calls.length).toBe(6);
  });

  test("stops when unmounted, and a visibility change after does nothing", async () => {
    const calls: string[] = [];
    const { unmount } = await mount(<Probe calls={calls} every={30} />);
    mounted.push(unmount);
    await advance(50);
    await unmount();
    const at = calls.length;
    await setHidden(true);
    await setHidden(false);
    await advance(80);
    expect(calls.length).toBe(at);
  });
});
