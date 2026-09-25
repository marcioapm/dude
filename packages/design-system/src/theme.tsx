import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from "react";
import type { ThemeMode } from "./tokens/themes.ts";
import { DEFAULT_DENSITY, isDensity, type Density } from "./tokens/density.ts";

export type ThemePreference = ThemeMode | "system";

export interface ThemeContextValue {
  /** What the user asked for. */
  readonly preference: ThemePreference;
  /** What is actually applied right now. */
  readonly resolved: ThemeMode;
  readonly setPreference: (p: ThemePreference) => void;
  readonly reducedMotion: boolean;
  readonly setReducedMotion: (v: boolean | null) => void;
  readonly density: Density;
  readonly setDensity: (d: Density) => void;
}

const ThemeContext = createContext<ThemeContextValue | null>(null);

export interface ThemeProviderProps {
  readonly defaultPreference?: ThemePreference | undefined;
  /** Persist to this storage key. Pass null to disable. */
  readonly storageKey?: string | null | undefined;
  readonly defaultDensity?: Density | undefined;
  /** Persist density to this storage key. Pass null to disable. */
  readonly densityStorageKey?: string | null | undefined;
  /** Element to stamp `data-theme` and `data-density` on; defaults to the document root. */
  readonly target?: HTMLElement | null | undefined;
  readonly children?: ReactNode;
}

function readMedia(query: string): boolean {
  return typeof window !== "undefined" && typeof window.matchMedia === "function" ? window.matchMedia(query).matches : false;
}

/**
 * Applies the theme and density by stamping `data-theme` / `data-density` /
 * `data-reduced-motion` on the root element. The CSS honours OS preferences
 * and defaults to comfortable on its own; this provider is only needed for
 * an in-app override and to expose the resolved values to JS (canvas charts,
 * native menus in Tauri).
 */
export function ThemeProvider({
  defaultPreference = "system",
  storageKey = "dude.theme",
  defaultDensity = DEFAULT_DENSITY,
  densityStorageKey = "dude.density",
  target,
  children,
}: ThemeProviderProps) {
  const [preference, setPreferenceState] = useState<ThemePreference>(() => {
    if (storageKey && typeof localStorage !== "undefined") {
      const v = localStorage.getItem(storageKey);
      if (v === "light" || v === "dark" || v === "system") return v;
    }
    return defaultPreference;
  });
  const [density, setDensityState] = useState<Density>(() => {
    if (densityStorageKey && typeof localStorage !== "undefined") {
      const v = localStorage.getItem(densityStorageKey);
      if (isDensity(v)) return v;
    }
    return defaultDensity;
  });
  const [systemDark, setSystemDark] = useState(() => readMedia("(prefers-color-scheme: dark)"));
  const [systemReduced, setSystemReduced] = useState(() => readMedia("(prefers-reduced-motion: reduce)"));
  const [reducedOverride, setReducedOverride] = useState<boolean | null>(null);

  useEffect(() => {
    if (typeof window === "undefined" || typeof window.matchMedia !== "function") return;
    const dark = window.matchMedia("(prefers-color-scheme: dark)");
    const reduced = window.matchMedia("(prefers-reduced-motion: reduce)");
    const onDark = (e: MediaQueryListEvent) => setSystemDark(e.matches);
    const onReduced = (e: MediaQueryListEvent) => setSystemReduced(e.matches);
    dark.addEventListener("change", onDark);
    reduced.addEventListener("change", onReduced);
    return () => {
      dark.removeEventListener("change", onDark);
      reduced.removeEventListener("change", onReduced);
    };
  }, []);

  // Dark is the design default, so an unknown system preference resolves dark.
  const resolved: ThemeMode = preference === "system" ? (systemDark ? "dark" : readMedia("(prefers-color-scheme: light)") ? "light" : "dark") : preference;
  const reducedMotion = reducedOverride ?? systemReduced;

  useEffect(() => {
    const el = target ?? (typeof document !== "undefined" ? document.documentElement : null);
    if (!el) return;
    el.setAttribute("data-theme", resolved);
    el.setAttribute("data-density", density);
    if (reducedOverride !== null) el.setAttribute("data-reduced-motion", String(reducedOverride));
    else el.removeAttribute("data-reduced-motion");
  }, [resolved, density, reducedOverride, target]);

  const setPreference = useCallback(
    (p: ThemePreference) => {
      setPreferenceState(p);
      if (storageKey && typeof localStorage !== "undefined") localStorage.setItem(storageKey, p);
    },
    [storageKey],
  );

  const setDensity = useCallback(
    (d: Density) => {
      setDensityState(d);
      if (densityStorageKey && typeof localStorage !== "undefined") localStorage.setItem(densityStorageKey, d);
    },
    [densityStorageKey],
  );

  const value = useMemo<ThemeContextValue>(
    () => ({ preference, resolved, setPreference, reducedMotion, setReducedMotion: setReducedOverride, density, setDensity }),
    [preference, resolved, setPreference, reducedMotion, density, setDensity],
  );

  return <ThemeContext.Provider value={value}>{children}</ThemeContext.Provider>;
}

export function useTheme(): ThemeContextValue {
  const v = useContext(ThemeContext);
  if (!v) throw new Error("useTheme must be used inside <ThemeProvider>");
  return v;
}
