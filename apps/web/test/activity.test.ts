/**
 * Whether someone is using the tab: shown, focused and given input within
 * ACTIVE_WINDOW_MS; and the API client says so on its requests only then.
 */

import { afterEach, describe, expect, setSystemTime, test } from "bun:test";
import { ACTIVE_WINDOW_MS, createActivity, installActivity } from "../src/activity.ts";
import { ApiClient } from "../src/api/client.ts";

class FakeDocument extends EventTarget {
  visibilityState = "visible";
  focused = true;
  hasFocus() {
    return this.focused;
  }
}

function setup(loadedAt = 0) {
  const doc = new FakeDocument();
  const win = new EventTarget();
  let now = loadedAt;
  const activity = createActivity(doc, loadedAt);
  activity.install(win, () => now);
  return { doc, win, activity, at: (t: number) => (now = t) };
}

describe("activity", () => {
  test("a page just opened is in use", () => {
    expect(setup(1_000).activity.isActive(1_000)).toBe(true);
  });

  test("hidden is not in use, whatever the input", () => {
    const { doc, win, activity, at } = setup();
    doc.visibilityState = "hidden";
    at(10);
    win.dispatchEvent(new Event("pointerdown"));
    expect(activity.isActive(10)).toBe(false);
  });

  test("not focused is not in use", () => {
    const { doc, activity } = setup();
    doc.focused = false;
    expect(activity.isActive(10)).toBe(false);
  });

  test("shown and focused, with no input for the window, is not in use; input brings it back", () => {
    const { win, activity, at } = setup();
    expect(activity.isActive(ACTIVE_WINDOW_MS - 1)).toBe(true);
    expect(activity.isActive(ACTIVE_WINDOW_MS)).toBe(false);
    const inputs = ["pointerdown", "keydown", "wheel", "touchstart"];
    for (const [i, type] of inputs.entries()) {
      // Each input well past the window of the one before.
      const t = at(ACTIVE_WINDOW_MS * (3 + 2 * i));
      expect([type, activity.isActive(t)]).toEqual([type, false]);
      win.dispatchEvent(new Event(type));
      expect([type, activity.isActive(t)]).toEqual([type, true]);
      expect([type, activity.isActive(t + ACTIVE_WINDOW_MS)]).toEqual([type, false]);
    }
  });

  test("automatic scrolling of a nested pane does not revive activity; initiating input does", () => {
    const pane = document.createElement("div");
    const nested = document.createElement("div");
    pane.append(nested);
    document.body.append(pane);
    let now = 0;
    const activity = createActivity(new FakeDocument(), now);
    activity.install(window, () => now);
    try {
      for (const type of ["wheel", "pointerdown", "keydown", "touchstart"]) {
        now += ACTIVE_WINDOW_MS + 1;
        expect(activity.isActive(now)).toBe(false);
        nested.dispatchEvent(new Event("scroll", { bubbles: false }));
        expect(activity.isActive(now)).toBe(false);
        nested.dispatchEvent(new Event(type, { bubbles: true }));
        expect(activity.isActive(now)).toBe(true);
      }
    } finally {
      pane.remove();
    }
  });

  test("focus again, or shown again, counts as input", () => {
    const { doc, win, activity, at } = setup();
    const later = at(ACTIVE_WINDOW_MS * 2);
    expect(activity.isActive(later)).toBe(false);
    win.dispatchEvent(new Event("focus"));
    expect(activity.isActive(later)).toBe(true);

    const after = at(ACTIVE_WINDOW_MS * 4);
    doc.visibilityState = "hidden";
    doc.dispatchEvent(new Event("visibilitychange"));
    doc.visibilityState = "visible";
    expect(activity.isActive(after)).toBe(false);
    doc.dispatchEvent(new Event("visibilitychange"));
    expect(activity.isActive(after)).toBe(true);
  });

  test("being hidden is not input", () => {
    const { doc, activity, at } = setup();
    const later = at(ACTIVE_WINDOW_MS * 2);
    doc.visibilityState = "hidden";
    doc.dispatchEvent(new Event("visibilitychange"));
    doc.visibilityState = "visible";
    expect(activity.isActive(later)).toBe(false);
  });

  test("installed twice, it listens once", () => {
    const added: string[] = [];
    const win = { addEventListener: (type: string) => void added.push(type) };
    const activity = createActivity(new FakeDocument(), 0);
    activity.install(win);
    const once = added.length;
    activity.install(win);
    expect(added.length).toBe(once);
  });
});

describe("the API client", () => {
  const originalFetch = globalThis.fetch;
  let hidden = false;
  Object.defineProperty(document, "visibilityState", { configurable: true, get: () => (hidden ? "hidden" : "visible") });
  afterEach(() => {
    globalThis.fetch = originalFetch;
    hidden = false;
    setSystemTime();
  });

  async function sent(): Promise<string | null> {
    let header: string | null = "unset";
    globalThis.fetch = (async (_input: RequestInfo | URL, init?: RequestInit) => {
      header = new Headers(init?.headers).get("x-dude-active");
      return Response.json({ people: [], you: "p" });
    }) as typeof fetch;
    await new ApiClient({ apiKey: "k" }).listPeople();
    return header;
  }

  test("says the person is here only while the tab is in use", async () => {
    installActivity();
    window.dispatchEvent(new Event("pointerdown"));
    expect(await sent()).toBe("1");
    hidden = true;
    expect(await sent()).toBeNull();
    hidden = false;
    setSystemTime(new Date(Date.now() + ACTIVE_WINDOW_MS + 1_000));
    expect(await sent()).toBeNull();
    window.dispatchEvent(new Event("keydown"));
    expect(await sent()).toBe("1");
  });
});
