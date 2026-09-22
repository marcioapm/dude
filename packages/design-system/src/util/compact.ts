/**
 * Remove keys whose value is `undefined`. Needed to hand optional props to
 * third-party components under `exactOptionalPropertyTypes`, where
 * `{ name: undefined }` is not assignable to `{ name?: string }`.
 */
export function compact<T extends object>(obj: T): { [K in keyof T]: Exclude<T[K], undefined> } {
  const out: Record<string, unknown> = {};
  for (const [k, v] of Object.entries(obj)) {
    if (v !== undefined) out[k] = v;
  }
  return out as { [K in keyof T]: Exclude<T[K], undefined> };
}
