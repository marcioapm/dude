/** An instant as milliseconds since the epoch, or null when absent or unparseable. */
export function toMs(v: string | number | Date | null | undefined): number | null {
  if (v === null || v === undefined) return null;
  const t = v instanceof Date ? v.getTime() : new Date(v).getTime();
  return Number.isNaN(t) ? null : t;
}
