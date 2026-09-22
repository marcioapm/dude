/**
 * Formatting rules for the numbers operators read all day. One place, so a
 * cost in a table cell and a cost in a tile never disagree.
 */

export interface UsdOptions {
  /** Drop to the shortest form that still reads: `$1.2k`, `<$0.01`. */
  readonly compact?: boolean;
}

/**
 * Money.
 *   >= 1000  -> $1,234      (whole dollars; cents are noise at this scale)
 *   >= 1     -> $12.34
 *   >= 0.01  -> $0.42
 *   else     -> $0.0042     (session costs are often sub-cent)
 *   0        -> $0.00
 */
export function formatUsd(usd: number, opts: UsdOptions = {}): string {
  if (!Number.isFinite(usd)) return "—";
  const sign = usd < 0 ? "-" : "";
  const v = Math.abs(usd);
  if (v === 0) return "$0.00";
  if (opts.compact) {
    if (v >= 1_000_000) return `${sign}$${trimZeros((v / 1_000_000).toFixed(2))}M`;
    if (v >= 1000) return `${sign}$${trimZeros((v / 1000).toFixed(1))}k`;
    if (v < 0.01) return `${sign}<$0.01`;
  }
  if (v >= 1000) return `${sign}$${Math.round(v).toLocaleString("en-US")}`;
  if (v >= 1) return `${sign}$${v.toFixed(2)}`;
  if (v >= 0.01) return `${sign}$${v.toFixed(2)}`;
  return `${sign}$${v.toFixed(4)}`;
}

/**
 * Token counts.
 *   < 1000      -> 842
 *   < 1M        -> 12.4k
 *   else        -> 1.23M
 */
export function formatTokens(n: number, opts: { readonly exact?: boolean } = {}): string {
  if (!Number.isFinite(n)) return "—";
  if (opts.exact) return Math.round(n).toLocaleString("en-US");
  const v = Math.abs(n);
  const sign = n < 0 ? "-" : "";
  if (v < 1000) return `${sign}${Math.round(v)}`;
  if (v < 1_000_000) return `${sign}${trimZeros((v / 1000).toFixed(1))}k`;
  return `${sign}${trimZeros((v / 1_000_000).toFixed(2))}M`;
}

export interface DurationOptions {
  /** `short` = "3m 12s"; `clock` = "03:12"; `long` = "3 min 12 sec". */
  readonly style?: "short" | "clock" | "long";
}

/**
 * Elapsed time. Two significant units, never more.
 *   < 1s   -> 420ms
 *   < 60s  -> 42.1s
 *   < 1h   -> 3m 12s
 *   < 1d   -> 2h 04m
 *   else   -> 3d 2h
 */
export function formatDuration(ms: number, opts: DurationOptions = {}): string {
  if (!Number.isFinite(ms) || ms < 0) return "—";
  const style = opts.style ?? "short";
  const s = ms / 1000;
  if (style === "clock") {
    const h = Math.floor(s / 3600);
    const m = Math.floor((s % 3600) / 60);
    const sec = Math.floor(s % 60);
    return h > 0
      ? `${h}:${pad2(m)}:${pad2(sec)}`
      : `${pad2(m)}:${pad2(sec)}`;
  }
  const units =
    style === "long"
      ? { d: " day", h: " hr", m: " min", s: " sec", sep: " " }
      : { d: "d", h: "h", m: "m", s: "s", sep: " " };
  if (ms < 1000) return `${Math.round(ms)}ms`;
  if (s < 60) return `${s < 10 ? s.toFixed(1) : Math.round(s)}${units.s}`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}${units.m}${units.sep}${pad2(Math.floor(s % 60))}${units.s}`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}${units.h}${units.sep}${pad2(m % 60)}${units.m}`;
  const d = Math.floor(h / 24);
  return `${d}${units.d}${units.sep}${h % 24}${units.h}`;
}

export type TimestampStyle = "time" | "time-ms" | "datetime" | "date" | "relative";

/**
 * Timestamps. Event streams use `time-ms` (HH:MM:SS.mmm) because events
 * within one second are common and ordering matters.
 */
export function formatTimestamp(
  iso: string | number | Date,
  style: TimestampStyle = "time-ms",
  now: number = Date.now(),
): string {
  const d = iso instanceof Date ? iso : new Date(iso);
  if (Number.isNaN(d.getTime())) return "—";
  switch (style) {
    case "time":
      return `${pad2(d.getHours())}:${pad2(d.getMinutes())}:${pad2(d.getSeconds())}`;
    case "time-ms":
      return `${pad2(d.getHours())}:${pad2(d.getMinutes())}:${pad2(d.getSeconds())}.${String(
        d.getMilliseconds(),
      ).padStart(3, "0")}`;
    case "date":
      return `${d.getFullYear()}-${pad2(d.getMonth() + 1)}-${pad2(d.getDate())}`;
    case "datetime":
      return `${formatTimestamp(d, "date")} ${formatTimestamp(d, "time")}`;
    case "relative": {
      const delta = now - d.getTime();
      if (Math.abs(delta) < 5_000) return "just now";
      const suffix = delta >= 0 ? " ago" : " from now";
      return `${formatDuration(Math.abs(delta))}${suffix}`;
    }
  }
}

/** Short SHA / ID: first 7 chars for SHAs, first 8 for our IDs. */
export function shortId(id: string, len = 8): string {
  return id.length <= len ? id : id.slice(0, len);
}

/** 12.5% -> "+12.5%", -3 -> "-3.0%" */
export function formatPercent(fraction: number, digits = 1): string {
  if (!Number.isFinite(fraction)) return "—";
  const v = fraction * 100;
  const sign = v > 0 ? "+" : "";
  return `${sign}${v.toFixed(digits)}%`;
}

function pad2(n: number): string {
  return String(n).padStart(2, "0");
}

function trimZeros(s: string): string {
  return s.includes(".") ? s.replace(/\.?0+$/, "") : s;
}
