/**
 * A CSS module as a bundler gives it to the component: each class the
 * stylesheet declares (comments aside), named as written, and nothing for a
 * name it does not declare. Under bun test a CSS module maps to {}; a test
 * that reads computed style needs the class names, so it mocks the module
 * with this. A module mock lasts for the whole `bun test` process, so other
 * files' tests see it too: returning only declared classes means an element
 * whose class the stylesheet does not declare (Markdown's `<p>`) still
 * renders without one there.
 */

import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

/** The module's path to mock it by, and the mock. */
export function cssModuleAsWritten(url: URL): [string, () => { default: Record<string, string | undefined> }] {
  const path = fileURLToPath(url);
  const css = readFileSync(path, "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  const declared = new Set([...css.matchAll(/\.(-?[_a-zA-Z][\w-]*)/g)].map((m) => m[1]));
  return [path, () => ({ default: new Proxy({}, { get: (_, key) => (typeof key === "string" && declared.has(key) ? key : undefined) }) })];
}
