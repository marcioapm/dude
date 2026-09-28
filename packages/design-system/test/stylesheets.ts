/**
 * Reading the product's stylesheets for the rules the design system holds
 * them to (radius, borders, raw values). Not a test file: the guards share it.
 */

import { Glob } from "bun";

export const ROOT = `${import.meta.dir}/../../..`;

/** Every stylesheet the product ships, the design system's and the web app's. */
export function stylesheets(): string[] {
  const out: string[] = [];
  for (const dir of ["packages/design-system/src", "apps/web/src"]) {
    for (const path of new Glob("**/*.css").scanSync(`${ROOT}/${dir}`)) {
      if (path.endsWith("tokens.css")) continue;
      out.push(`${dir}/${path}`);
    }
  }
  return out.sort();
}

/** `[selector, property, value]` for every declaration whose property matches, comments removed. */
export function declarations(css: string, prop: RegExp): Array<[string, string, string]> {
  const out: Array<[string, string, string]> = [];
  const clean = css.replace(/\/\*[\s\S]*?\*\//g, "");
  for (const m of clean.matchAll(/([^{}]*)\{([^{}]*)\}/g)) {
    const selector = m[1]!.trim().replace(/\s+/g, " ");
    // One declaration per `;`-separated part: a property named inside a
    // value (`transition: border-color 120ms, …`) is not a declaration.
    for (const part of m[2]!.split(";")) {
      const d = /^\s*([a-z-]+)\s*:\s*([\s\S]+?)\s*$/.exec(part);
      if (d && prop.test(d[1]!)) out.push([selector, d[1]!, d[2]!.replace(/\s+/g, " ")]);
    }
  }
  return out;
}
