// R1 Desktop Vite config (R1D-2).
//
// Declares the five R1D-2 panel entry points alongside the main
// WebView bootstrap. `cargo tauri init` (R1D-1.1) may merge this with
// additional plugins (React, Tailwind, shadcn); keep the `rollupOptions
// .input` map intact across that merge so per-panel code-splitting
// remains available.
import { defineConfig } from "vite";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const root = dirname(fileURLToPath(import.meta.url));

export default defineConfig({
  clearScreen: false,
  server: {
    port: 5173,
    strictPort: true,
  },
  envPrefix: ["VITE_", "TAURI_"],
  build: {
    target: "esnext",
    sourcemap: true,
    rollupOptions: {
      input: {
        // Main composed app (loaded by index.html).
        main: resolve(root, "index.html"),
        // Per-panel entry points — usable by tests + future per-window
        // Tauri surfaces (e.g., detached cost dashboard).
        "panel-session-view": resolve(root, "src/panels/session-view.ts"),
        "panel-sow-tree": resolve(root, "src/panels/sow-tree.ts"),
        "panel-descent-ladder": resolve(root, "src/panels/descent-ladder.ts"),
        "panel-descent-evidence": resolve(root, "src/panels/descent-evidence.ts"),
        "panel-ledger-viewer": resolve(root, "src/panels/ledger-viewer.ts"),
        "panel-memory-inspector": resolve(root, "src/panels/memory-inspector.ts"),
        "panel-skill-catalog": resolve(root, "src/panels/skill-catalog.ts"),
        "panel-mcp-servers": resolve(root, "src/panels/mcp-servers.ts"),
        "panel-observability": resolve(root, "src/panels/observability.ts"),
        "panel-approval-queue": resolve(root, "src/panels/approval-queue.ts"),
        "panel-scheduler": resolve(root, "src/panels/scheduler.ts"),
        "panel-cost": resolve(root, "src/panels/cost-panel.ts"),
      },
    },
  },
  resolve: {
    alias: {
      "@panels": resolve(root, "src/panels"),
      "@types": resolve(root, "src/types"),
    },
    // Dedupe React + ReactDOM so the workspace package and the desktop
    // app share a single copy. Without this Vite would bundle React twice
    // (once for desktop, once via @r1/web-components) and break hooks.
    dedupe: ["react", "react-dom"],
  },
});
