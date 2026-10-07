/**
 * Which of the mockups' scenarios the app should answer from, if any.
 * Tiny on purpose: main.tsx reads it before deciding whether to load the
 * fixtures at all, so a production build carries none of them.
 */

export const SCENARIOS = ["a", "b", "c", "d", "e"] as const;
export type FixtureScenario = (typeof SCENARIOS)[number];

const KEY = "dude.fixtures";
/** The fixture task's run's state (`?run=`): paused, preview, aborted, failed, restarted, stalled. */
export const RUN_KEY = `${KEY}.run`;

/**
 * The scenario asked for: `?fixtures=b` (remembered), or what was remembered; `?fixtures=off` forgets. Null for the real API.
 * `?run=aborted` (failed, restarted, paused, preview, stalled; `off` forgets) puts the task's run in that state, remembered too.
 */
export function fixtureScenario(): FixtureScenario | null {
  const params = new URLSearchParams(window.location.search);
  const remember = (name: string, key: string) => {
    const asked = params.get(name);
    if (asked === null) return false;
    if (asked === "" || asked === "off") localStorage.removeItem(key);
    else localStorage.setItem(key, asked);
    return true;
  };
  // Both read before either is taken off the address.
  const fixtures = remember("fixtures", KEY);
  const run = remember("run", RUN_KEY);
  // The parameters have done their work; the hash is the app's own.
  if (fixtures || run) window.history.replaceState(null, "", `${window.location.pathname}${window.location.hash}`);
  const stored = localStorage.getItem(KEY);
  return stored && (SCENARIOS as readonly string[]).includes(stored) ? (stored as FixtureScenario) : null;
}
