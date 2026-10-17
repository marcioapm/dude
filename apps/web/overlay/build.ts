/**
 * Builds the preview overlay into public/feedback/overlay.js: one IIFE, no
 * imports at run time, minified, for pages dude does not own. Vite copies
 * public/ into the web app's build, where the backend serves
 * /feedback/overlay.js (apps/control-plane/src/api/web.ts).
 */

import { gzipSync } from "node:zlib";
import { join } from "node:path";
import { TOKENS_MODULE, overlayTokensModule } from "./build-tokens.ts";

const here = import.meta.dir;
const out = join(here, "..", "public", "feedback");
// The design system's tokens, regenerated first: a build never bundles a stale copy.
const tokens = overlayTokensModule();
if ((await Bun.file(TOKENS_MODULE).exists() ? await Bun.file(TOKENS_MODULE).text() : "") !== tokens) await Bun.write(TOKENS_MODULE, tokens);
const result = await Bun.build({
  entrypoints: [join(here, "main.ts")],
  outdir: out,
  naming: "overlay.js",
  format: "iife",
  target: "browser",
  minify: true,
  sourcemap: "none",
});
if (!result.success) {
  for (const log of result.logs) console.error(log);
  process.exit(1);
}
const bytes = await Bun.file(join(out, "overlay.js")).bytes();
console.log(`overlay.js: ${bytes.length} bytes, ${gzipSync(bytes).length} gzipped`);
