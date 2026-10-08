/**
 * A project's key: the start of its tasks' keys (BILL-12), unique in its
 * organisation ignoring case (migration 095). People and agents name a
 * project's task by it alone, so two projects must never share one.
 */

/**
 * A key a person chooses: 2 to 6 letters or digits, the first a letter.
 * Every task-key parser takes `<key>-<number>` with a key of a letter then
 * letters or digits (orchestrator `delivery.taskKey`), so each such key parses.
 */
export const PROJECT_KEY = /^[A-Z][A-Z0-9]{1,5}$/;
export const PROJECT_KEY_MESSAGE = "key must be 2 to 6 letters or digits, starting with a letter";

/** The 409 for a key another project of the organisation has. */
export const projectKeyTakenMessage = (key: string, project: string) => `${key} is already the key of ${project}; pick another`;

/**
 * The keys dude tries for a project with `slug`, in order, when none is
 * chosen; the first one free is its key. Letters only count where the rule
 * says letters, and every candidate is upper case and PROJECT_KEY-shaped.
 *
 * 1. The slug's first four letters (billing-api: BILL). With fewer than two,
 *    its first letter and the letters or digits after it, four at most
 *    (a1: A1); with no letter at all, WI.
 * 2. The initials of the slug's words (split on "-"; a word that starts
 *    with a digit has none), four at most, padded to four with the letters
 *    after the last initial (billing-worker: BW + OR = BWOR).
 * 3. The first candidate followed by 2, 3 … 9 (BILL2 … BILL9).
 * 4. The first candidate followed by 10 … 99 (BILL10 … BILL99).
 *
 * Duplicates are dropped, so a one-word slug goes from BILL to BILL2.
 */
export function projectKeyCandidates(slug: string): string[] {
  const lower = slug.toLowerCase();
  const letters = lower.replace(/[^a-z]/g, "");
  let base = letters.slice(0, 4);
  if (base.length < 2) {
    const first = lower.search(/[a-z]/);
    base = first < 0 ? "wi" : lower.slice(first).replace(/[^a-z0-9]/g, "").slice(0, 4);
    if (base.length < 2) base = "wi";
  }
  base = base.toUpperCase();

  const out = [base];
  const words = lower.split("-").filter((w) => /^[a-z]/.test(w)).slice(0, 4);
  if (words.length > 0) {
    let initials = words.map((w) => w[0]).join("");
    initials += words.at(-1)!.slice(1).replace(/[^a-z]/g, "").slice(0, Math.max(0, 4 - initials.length));
    if (initials.length >= 2) out.push(initials.toUpperCase());
  }
  for (let n = 2; n <= 99; n++) out.push(`${base}${n}`);
  return [...new Set(out)];
}

/** The key dude gives a project with `slug` when none is chosen: its first candidate not `taken`; null when all are. */
export function deriveProjectKey(slug: string, taken: Iterable<string>): string | null {
  const used = new Set([...taken].map((k) => k.toUpperCase()));
  return projectKeyCandidates(slug).find((k) => !used.has(k)) ?? null;
}
