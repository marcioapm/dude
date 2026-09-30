/**
 * When closing a form would lose writing worth asking about. A few words
 * are cheap to type again and a confirmation for them is noise; past
 * `DISCARD_GUARD_WORDS` in the fields that changed since the form opened,
 * closing asks first.
 */

export const DISCARD_GUARD_WORDS = 20;

export function wordCount(text: string): number {
  const t = text.trim();
  return t ? t.split(/\s+/).length : 0;
}

/**
 * Words in the fields that differ from what they opened with, or 0 when
 * the change is not worth a confirmation: nothing changed, or no more than
 * `DISCARD_GUARD_WORDS` words in what did.
 */
export function unsavedWords(opened: ReadonlyArray<string>, now: ReadonlyArray<string>): number {
  let words = 0;
  now.forEach((text, i) => {
    if (text !== (opened[i] ?? "")) words += wordCount(text);
  });
  return words > DISCARD_GUARD_WORDS ? words : 0;
}
