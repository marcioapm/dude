import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

/*
 * The control plane is a separate process. Proxying keeps the app
 * same-origin, which matters for the SSE stream and means the deployed build
 * needs no CORS configuration. Shared by `dev` and `preview`, so the test
 * suite can serve the production build against its own control plane.
 */
const target = process.env.DUDE_CONTROL_PLANE ?? "http://localhost:3000";
const proxy = {
  "/v1": { target, changeOrigin: true },
  "/health": { target, changeOrigin: true },
};

export default defineConfig({
  plugins: [react()],
  server: { port: 5180, proxy },
  preview: { port: Number(process.env.DUDE_WEB_PORT ?? 5181), strictPort: true, proxy },
  build: {
    outDir: "dist",
    sourcemap: true,
  },
});
