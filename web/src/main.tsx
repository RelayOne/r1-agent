// SPDX-License-Identifier: MIT
// SPA entry point. Mounts the <App> tree (item 41/55 routing +
// providers). Closes the audit finding that web/src/main.tsx rendered
// only a static landing line — every shipped chat / lanes / settings
// component was dark code with no path from the bundle entry.
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import "@/styles/globals.css";
import { App } from "@/App";

const rootEl = document.getElementById("root");
if (!rootEl) {
  throw new Error("r1-web: #root element missing from index.html");
}

createRoot(rootEl).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
