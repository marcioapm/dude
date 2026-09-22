import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import { fileURLToPath } from "node:url";

/**
 * Gallery dev server / build. Root is the gallery folder so the package
 * itself has no build output; consumers import source.
 */
export default defineConfig({
  root: fileURLToPath(new URL("./src/gallery", import.meta.url)),
  base: "./",
  plugins: [react()],
  css: {
    modules: {
      // Readable class names so the gallery's DOM is inspectable.
      generateScopedName: "ds-[name]__[local]",
    },
  },
  build: {
    outDir: fileURLToPath(new URL("./dist/gallery", import.meta.url)),
    emptyOutDir: true,
    target: "es2022",
  },
  server: { port: 5199, strictPort: false },
});
