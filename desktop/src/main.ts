// SPDX-License-Identifier: MIT
//
// R1 Desktop WebView entrypoint (R1D-2).
//
// Composes the five R1D-2 panel skeletons into a 3-row, 2-column CSS
// grid. Layout:
//
//   +------------------+------------------+
//   | SOW tree         | Descent ladder   |  row 1
//   +------------------+------------------+
//   | Ledger viewer    | Memory inspector |  row 2
//   +------------------+------------------+
//   | Cost panel                          |  row 3 (full-width)
//   +-------------------------------------+
//
// Real Tauri bootstrap (Vite + React + shadcn/ui) lands in R1D-1.1.
// This file is intentionally framework-free so it works the moment
// `cargo tauri init` generates `main.rs` + `main.tsx`.

import { renderPanel as renderSowTree } from "./panels/sow-tree";
import { renderPanel as renderDescentLadder } from "./panels/descent-ladder";
import { renderPanel as renderLedgerViewer } from "./panels/ledger-viewer";
import { renderPanel as renderMemoryInspector } from "./panels/memory-inspector";
import { renderPanel as renderCostPanel } from "./panels/cost-panel";
import { mountDrawer as mountDescentEvidenceDrawer } from "./panels/descent-evidence";

type PanelEntry = {
  id: string;
  gridArea: "sow" | "descent" | "ledger" | "memory" | "cost";
  render: (root: HTMLElement) => void;
};

const PANELS: PanelEntry[] = [
  { id: "panel-sow-tree", gridArea: "sow", render: renderSowTree },
  { id: "panel-descent-ladder", gridArea: "descent", render: renderDescentLadder },
  { id: "panel-ledger-viewer", gridArea: "ledger", render: renderLedgerViewer },
  { id: "panel-memory-inspector", gridArea: "memory", render: renderMemoryInspector },
  { id: "panel-cost", gridArea: "cost", render: renderCostPanel },
];

function mount(): void {
  const app = document.querySelector<HTMLElement>("#app");
  if (!app) {
    console.error("[r1-desktop] #app mount point missing from index.html");
    return;
  }

  app.classList.add("r1-app-grid");
  app.innerHTML = "";

  for (const panel of PANELS) {
    const section = document.createElement("section");
    section.id = panel.id;
    section.style.gridArea = panel.gridArea;
    app.appendChild(section);
    panel.render(section);
  }

  mountDescentEvidenceDrawer(document.body);
}

if (document.readyState === "loading") {
  document.addEventListener("DOMContentLoaded", mount, { once: true });
} else {
  mount();
}
