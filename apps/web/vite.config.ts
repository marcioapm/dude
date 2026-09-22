import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig({
  server: {
    port: 5180,
    // The control plane is a separate process in development. Proxying keeps
    // the app same-origin, which matters for the SSE stream and means the
    // deployed build needs no CORS configuration.
    proxy: {
      "/v1": {
        target: process.env.DUDE_CONTROL_PLANE ?? "http://localhost:3000",
        changeOrigin: true,
      },
      "/health": {
        target: process.env.DUDE_CONTROL_PLANE ?? "http://localhost:3000",
        changeOrigin: true,
      },
    },
  },
  build: {
    outDir: "dist",
    sourcemap: true,
  },
});
