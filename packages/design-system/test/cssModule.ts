/**
 * A CSS module as a bundler gives it to the component: each class the
 * stylesheet declares, named as written, and nothing for a name it does not
 * declare. Under bun test a CSS module maps to {}; mocking it with this keeps
 * the class names a computed-style test needs, and — since a module mock lasts
 * for the whole `bun test` process — never hands another file's tests a class
 * the real module would not (a `<p>` stays a bare `<p>`).
 */

import { readFileSync } from "node:fs";

export function cssModuleAsWritten(url: URL) {
  const declared = new Set([...readFileSync(url, "utf8").matchAll(/\.(-?[_a-zA-Z][\w-]*)/g)].map((m) => m[1]));
  return () => ({ default: new Proxy({}, { get: (_, key) => (typeof key === "string" && declared.has(key) ? key : undefined) }) });
}
