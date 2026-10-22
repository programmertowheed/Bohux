import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The bundle is embedded in the Go binary (ADR-0012) and served under /ui/. In
// development, API calls go to a local control plane.
export default defineConfig({
  base: "/ui/",
  plugins: [react()],
  build: { outDir: "dist", emptyOutDir: true, sourcemap: false },
  server: {
    proxy: {
      "/bohux.v1.": { target: process.env.BOHUX_API_URL ?? "http://127.0.0.1:7400", changeOrigin: true },
      // The terminal's socket keeps the page's Host, so the Origin check passes.
      "/sockets/": { target: process.env.BOHUX_API_URL ?? "http://127.0.0.1:7400", ws: true },
    },
  },
  test: { environment: "jsdom", globals: true },
});
