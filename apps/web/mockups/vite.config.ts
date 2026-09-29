import { defineConfig, type Plugin } from "vite";
import react from "@vitejs/plugin-react";
import { fileURLToPath } from "node:url";

/*
 * Mockups: `bunx vite build --config mockups/vite.config.ts` writes one
 * self-contained HTML file per mockup into docs/design/mockups, so it opens
 * from disk with no server. Script and styles are inlined by the small
 * plugin below rather than a dependency.
 */

const inline = (): Plugin => ({
  name: "inline-into-html",
  enforce: "post",
  generateBundle(_, bundle) {
    const html = Object.values(bundle).find((f) => f.fileName.endsWith(".html"));
    if (!html || html.type !== "asset") return;
    let src = String(html.source);
    for (const [name, f] of Object.entries(bundle)) {
      if (f.type === "chunk") {
        src = src.replace(new RegExp(`<script type="module" crossorigin src="[^"]*${name}"></script>`), () => `<script type="module">${f.code.replace(/<\/script/g, "<\\/script")}</script>`);
        delete bundle[name];
      } else if (name.endsWith(".css")) {
        src = src.replace(new RegExp(`<link rel="stylesheet" crossorigin href="[^"]*${name}">`), () => `<style>${String(f.source)}</style>`);
        delete bundle[name];
      }
    }
    html.source = src;
  },
});

const mockup = process.env.MOCKUP ?? "memory";

export default defineConfig({
  root: fileURLToPath(new URL(`./${mockup}`, import.meta.url)),
  base: "./",
  plugins: [react(), inline()],
  build: {
    outDir: fileURLToPath(new URL(`../../../docs/design/mockups/${mockup}`, import.meta.url)),
    // The folder also holds the mockup's screenshots; only index.html is ours to replace.
    emptyOutDir: false,
    assetsInlineLimit: 100_000_000,
    cssCodeSplit: false,
    rollupOptions: { output: { inlineDynamicImports: true } },
  },
  server: { port: 5190 },
});
