/**
 * ThemeProvider's initial density, rendered on the server with a stubbed
 * `localStorage`, and `useDensity` reading it. The effect that stamps
 * `data-density` and the setter need a DOM and are not covered here.
 */

import { afterEach, describe, expect, test } from "bun:test";
import { renderToStaticMarkup } from "react-dom/server";
import { ThemeProvider, useDensity, useTheme, type ThemeProviderProps } from "../src/theme.tsx";

function stubStorage(entries: Record<string, string>): string[] {
  const reads: string[] = [];
  const store = new Map(Object.entries(entries));
  Object.defineProperty(globalThis, "localStorage", {
    configurable: true,
    value: {
      getItem: (k: string) => {
        reads.push(k);
        return store.get(k) ?? null;
      },
      setItem: (k: string, v: string) => void store.set(k, v),
      removeItem: (k: string) => void store.delete(k),
    },
  });
  return reads;
}

afterEach(() => {
  delete (globalThis as { localStorage?: unknown }).localStorage;
});

function Probe() {
  return <output>{useTheme().density}</output>;
}

const densityOf = (props: Omit<ThemeProviderProps, "children"> = {}) =>
  renderToStaticMarkup(
    <ThemeProvider {...props}>
      <Probe />
    </ThemeProvider>,
  ).replace(/<\/?output>/g, "");

describe("useDensity", () => {
  test("reads the provider's density, not the root's attribute, which is left unstamped", () => {
    stubStorage({});
    document.documentElement.removeAttribute("data-density");
    function DensityProbe() {
      return <output>{useDensity()}</output>;
    }
    // Static rendering runs no effects, so <html> never gets data-density.
    const out = renderToStaticMarkup(<ThemeProvider defaultDensity="compact"><DensityProbe /></ThemeProvider>);
    expect(document.documentElement.hasAttribute("data-density")).toBe(false);
    expect(out).toBe("<output>compact</output>");
  });
});

describe("ThemeProvider initial density", () => {
  test("reads a stored compact", () => {
    stubStorage({ "dude.density": "compact" });
    expect(densityOf()).toBe("compact");
  });

  test("an unknown stored value falls back to comfortable", () => {
    stubStorage({ "dude.density": "bogus" });
    expect(densityOf()).toBe("comfortable");
  });

  test("defaultDensity applies when nothing is stored", () => {
    stubStorage({});
    expect(densityOf({ defaultDensity: "compact" })).toBe("compact");
  });

  test("densityStorageKey={null} ignores storage", () => {
    const reads = stubStorage({ "dude.density": "compact" });
    expect(densityOf({ densityStorageKey: null })).toBe("comfortable");
    expect(reads).not.toContain("dude.density");
  });

  test("a custom densityStorageKey is the one read", () => {
    stubStorage({ "dude.density": "comfortable", "app.d": "compact" });
    expect(densityOf({ densityStorageKey: "app.d" })).toBe("compact");
  });
});
