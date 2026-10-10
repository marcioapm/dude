/**
 * An ISO 8601 timestamp as milliseconds since the epoch, for ordering times
 * that come from different sources. Strings compare as instants only when
 * both are in the same zone: "16:42+01:00" sorts after "15:50Z" though it
 * is earlier. Unparseable text is NaN; `byInstant` puts it last.
 */
export function instant(at: string): number {
  return Date.parse(at);
}

/** A sort comparator, oldest first, on what `at` names in each. */
export function byInstant<T>(at: (x: T) => string): (a: T, b: T) => number {
  return (a, b) => {
    const x = instant(at(a));
    const y = instant(at(b));
    if (Number.isNaN(x) || Number.isNaN(y)) return Number(Number.isNaN(x)) - Number(Number.isNaN(y));
    return x - y;
  };
}
