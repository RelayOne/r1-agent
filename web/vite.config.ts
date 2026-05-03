// SPDX-License-Identifier: MIT
// Vite config for r1-web (web-chat-ui spec).
//
// build.outDir points at internal/server/static/dist so the existing
// `//go:embed static` in internal/server/embed.go picks the SPA up
// without a Go-side change. base='/' for SPA from-root serving.
//
// Plugins:
// - @vitejs/plugin-react-swc — React with SWC (faster than Babel).
// - @tailwindcss/vite (Tailwind 3.4 path) — registered via PostCSS in
//   postcss.config.cjs; we wire Tailwind through the vite plugin slot
//   for forward-compat with shadcn's preferred plumbing.
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react-swc";
import tailwindcss from "@tailwindcss/vite";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const root = dirname(fileURLToPath(import.meta.url));

export default defineConfig({
  base: "/",
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: {
      "@": resolve(root, "src"),
    },
  },
  server: {
    port: 5173,
    strictPort: true,
  },
  build: {
    outDir: "../internal/server/static/dist",
    emptyOutDir: true,
    target: "esnext",
    sourcemap: true,
  },
});
