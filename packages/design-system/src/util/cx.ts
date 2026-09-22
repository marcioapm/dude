/** Join class names, dropping falsy values. */
export function cx(...parts: ReadonlyArray<string | false | null | undefined>): string {
  let out = "";
  for (const p of parts) {
    if (p) out = out ? `${out} ${p}` : p;
  }
  return out;
}
