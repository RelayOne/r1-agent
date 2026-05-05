// SPDX-License-Identifier: MIT
// Vite config for r1-web (web-chat-ui spec).
//
// build.outDir points at internal/server/static/dist so the existing
// `//go:embed static` in internal/server/embed.go picks the SPA up
// without a Go-side change. base='/' for SPA from-root serving.
//
// Plugins:
// - @vitejs/plugin-react-swc — React with SWC (faster than Babel).
//
// Tailwind 3.4 is wired through PostCSS (see postcss.config.cjs); the
// `@tailwindcss/vite` plugin is a Tailwind v4 surface and is NOT
// loaded here per spec §Risks ("Vite 6 + Tailwind 3 compatibility:
// stay on v3 until shadcn migrates; do not mix").
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react-swc";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const root = dirname(fileURLToPath(import.meta.url));

export default defineConfig({
  base: "/",
  plugins: [react()],
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
