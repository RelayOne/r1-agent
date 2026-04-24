// Scaffold Vite config. `cargo tauri init` (R1D-1.1) may overwrite with a
// richer config. Kept minimal here so the file exists at the expected path.
import { defineConfig } from "vite";

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
  },
});
