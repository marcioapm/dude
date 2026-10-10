/** Which modifier the platform's shortcuts use, as a key cap reads: ⌘ on Apple devices, Ctrl elsewhere. */
export function modKey(): "⌘" | "Ctrl" {
  if (typeof navigator === "undefined") return "Ctrl";
  const platform = (navigator as Navigator & { userAgentData?: { platform?: string } }).userAgentData?.platform ?? navigator.platform ?? "";
  return /mac|iphone|ipad|ipod/i.test(platform || navigator.userAgent) ? "⌘" : "Ctrl";
}

/**
 * `key` typed as a single-key shortcut: outside a field and not already
 * handled. `e.key` is the character typed, so Shift or Option producing it
 * on another layout still counts; only ⌘ and Ctrl (not AltGr, which is
 * Ctrl+Alt) make it a command instead.
 */
export function isBareKey(e: KeyboardEvent, key: string): boolean {
  if (e.key !== key || e.metaKey || (e.ctrlKey && !e.altKey) || e.defaultPrevented) return false;
  const el = e.target as HTMLElement | null;
  return !(el && (el.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(el.tagName)));
}
