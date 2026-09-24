import { useEffect, useState } from "react";

/**
 * A clock that ticks at `intervalMs` while `active`. Used for elapsed
 * times and countdowns. Never faster than once a second in this product —
 * sub-second live timers read as noise.
 */
export function useNow(active: boolean, intervalMs = 1000): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!active) return;
    setNow(Date.now());
    const id = setInterval(() => setNow(Date.now()), intervalMs);
    return () => clearInterval(id);
  }, [active, intervalMs]);
  return now;
}

export function toMs(v: string | number | Date | null | undefined): number | null {
  if (v === null || v === undefined) return null;
  const t = v instanceof Date ? v.getTime() : new Date(v).getTime();
  return Number.isNaN(t) ? null : t;
}

export interface ElapsedOptions {
  readonly startedAt?: string | number | Date | null | undefined;
  readonly endedAt?: string | number | Date | null | undefined;
  /** Static duration when `startedAt` is unknown. */
  readonly durationMs?: number | undefined;
  /** Still going: measure from `startedAt` to a ticking now. */
  readonly live: boolean;
}

/**
 * How long something took, or null when that is not known. Live it ticks
 * from `startedAt`; settled it needs both ends (or a `durationMs`). A
 * settled start with no end is *not* measured against the mount time — a
 * thought reloaded two days later would otherwise read "2d 3h".
 */
export function useElapsed({ startedAt, endedAt, durationMs, live }: ElapsedOptions): number | null {
  const now = useNow(live);
  const start = toMs(startedAt);
  if (start === null) return durationMs ?? null;
  if (live) return Math.max(0, now - start);
  const end = toMs(endedAt);
  if (end === null) return durationMs ?? null;
  return Math.max(0, end - start);
}
