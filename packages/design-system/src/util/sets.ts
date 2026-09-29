/** A set with `key` in or out of it: for toggling a row open, ticking a choice. */
export function toggled<T>(set: ReadonlySet<T>, key: T, on: boolean = !set.has(key)): Set<T> {
  const next = new Set(set);
  if (on) next.add(key);
  else next.delete(key);
  return next;
}
