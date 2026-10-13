/** Times and roles as the accounts screens say them: "in 12 days", "2 days ago", "11 October". */

const DAY = 86_400_000;

/** How far ahead an instant is, in whole days or hours: "in 12 days", "in 3 hours", "today". */
export function untilWords(iso: string, now = Date.now()): string {
  const ms = Date.parse(iso) - now;
  if (ms <= 0) return "now";
  const days = Math.round(ms / DAY);
  if (days >= 1) return days === 1 ? "tomorrow" : `in ${days} days`;
  const hours = Math.max(1, Math.round(ms / 3_600_000));
  return hours === 1 ? "in an hour" : `in ${hours} hours`;
}

/** How long ago: "today", "yesterday", "4 days ago", "3 weeks ago". */
export function agoWords(iso: string, now = Date.now()): string {
  const days = Math.floor((now - Date.parse(iso)) / DAY);
  if (days <= 0) return "today";
  if (days === 1) return "yesterday";
  if (days < 14) return `${days} days ago`;
  if (days < 60) return `${Math.floor(days / 7)} weeks ago`;
  return `${Math.floor(days / 30)} months ago`;
}

/** "11 October", in the reader's locale's month name. */
export function dayWords(iso: string): string {
  return new Date(iso).toLocaleDateString("en-GB", { day: "numeric", month: "long" });
}

/** "September 2026". */
export function monthWords(iso: string): string {
  return new Date(iso).toLocaleDateString("en-GB", { month: "long", year: "numeric" });
}

export { roleWord } from "@dude/design-system/components";

/** An absolute link to a place in this dude, for copying: `https://dude.acme.dev/#/invite/…`. */
export function absolute(hash: string, origin = window.location.origin): string {
  return `${origin}/${hash.startsWith("#") ? hash : `#${hash}`}`;
}
