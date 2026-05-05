// SPDX-License-Identifier: MIT
// SPA entry point. Mounts a minimal landing shell so the
// scaffolding-phase build (items 1-15) produces a working bundle.
// Routes, ThemeProvider, daemon-store wiring are added by items
// 21-22 and 41 of the build checklist.
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import "@/styles/globals.css";

const rootEl = document.getElementById("root");
if (!rootEl) {
  throw new Error("r1-web: #root element missing from index.html");
}

createRoot(rootEl).render(
  <StrictMode>
    <main id="main" className="flex min-h-screen items-center justify-center bg-background text-foreground">
      <div className="text-center">
        <h1 className="text-2xl font-semibold">r1</h1>
        <p className="mt-2 text-sm text-muted-foreground">
          Web UI scaffolding ready. Components mount in subsequent build phases.
        </p>
      </div>
    </main>
  </StrictMode>,
);
