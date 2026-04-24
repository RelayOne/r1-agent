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
        "panel-sow-tree": resolve(root, "src/panels/sow-tree.ts"),
        "panel-descent-ladder": resolve(root, "src/panels/descent-ladder.ts"),
        "panel-ledger-viewer": resolve(root, "src/panels/ledger-viewer.ts"),
        "panel-memory-inspector": resolve(root, "src/panels/memory-inspector.ts"),
        "panel-cost": resolve(root, "src/panels/cost-panel.ts"),
      },
    },
  },
  resolve: {
    alias: {
      "@panels": resolve(root, "src/panels"),
      "@types": resolve(root, "src/types"),
    },
  },
});
