/**
 * Words with a branch or base in them, as the design system sets an
 * identifier: plain text, and the name in mono (`ds-mono`).
 */

import type { Words } from "./pullRequests.ts";

export function MonoWords({ words }: { words: Words }) {
  return <>{words.map((w, i) => (typeof w === "string" ? w : <span key={i} className="ds-mono">{w.mono}</span>))}</>;
}
