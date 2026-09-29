/**
 * Which of the mockups' scenarios the app should answer from, if any.
 * Tiny on purpose: main.tsx reads it before deciding whether to load the
 * fixtures at all, so a production build carries none of them.
 */

export const SCENARIOS = ["a", "b", "c", "d", "e"] as const;
export type FixtureScenario = (typeof SCENARIOS)[number];

const KEY = "dude.fixtures";

/** The scenario asked for: `?fixtures=b` (remembered), or what was remembered; `?fixtures=off` forgets. Null for the real API. */
export function fixtureScenario(): FixtureScenario | null {
  const asked = new URLSearchParams(window.location.search).get("fixtures");
  if (asked !== null) {
    if (asked === "" || asked === "off") localStorage.removeItem(KEY);
    else localStorage.setItem(KEY, asked);
    // The parameter has done its work; the hash is the app's own.
    window.history.replaceState(null, "", `${window.location.pathname}${window.location.hash}`);
  }
  const stored = localStorage.getItem(KEY);
  return stored && (SCENARIOS as readonly string[]).includes(stored) ? (stored as FixtureScenario) : null;
}
