/** Which modifier the platform's shortcuts use, as a key cap reads: ⌘ on Apple devices, Ctrl elsewhere. */
export function modKey(): "⌘" | "Ctrl" {
  if (typeof navigator === "undefined") return "Ctrl";
  const platform = (navigator as Navigator & { userAgentData?: { platform?: string } }).userAgentData?.platform ?? navigator.platform ?? "";
  return /mac|iphone|ipad|ipod/i.test(platform || navigator.userAgent) ? "⌘" : "Ctrl";
}
