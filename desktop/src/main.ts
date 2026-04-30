// SPDX-License-Identifier: MIT
//
// R1 Desktop WebView entrypoint (R1D-2).
//
// Composes all shipped panel skeletons into a CSS grid. Layout:
//
//   +-------------------------------------+
//   | Session view (workspace+chat)       |  row 0 (full-width, R1D-2)
//   +-------------------------------------+
//   +------------------+------------------+
//   | SOW tree         | Descent ladder   |  row 1 (R1D-3)
//   +------------------+------------------+
//   | Ledger viewer    | Memory inspector |  row 2 (R1D-5/6)
//   +------------------+------------------+
//   | Skill catalog                       |  row 3 (full-width, R1D-4)
//   +-------------------------------------+
//   | MCP servers                         |  row 4 (full-width, R1D-8)
//   +-------------------------------------+
//   | Observability                       |  row 5 (full-width, R1D-9)
//   +-------------------------------------+
//   | Cost panel                          |  row 6 (full-width, R1D-9)
//   +-------------------------------------+
//
// Real Tauri bootstrap (Vite + React + shadcn/ui) lands in R1D-1.1.
// This file is intentionally framework-free so it works the moment
// `cargo tauri init` generates `main.rs` + `main.tsx`.

import { renderPanel as renderSessionView } from "./panels/session-view";
import { renderPanel as renderSowTree } from "./panels/sow-tree";
import { renderPanel as renderDescentLadder } from "./panels/descent-ladder";
import { renderPanel as renderLedgerViewer } from "./panels/ledger-viewer";
import { renderPanel as renderMemoryInspector } from "./panels/memory-inspector";
import { renderPanel as renderCostPanel } from "./panels/cost-panel";
import { renderPanel as renderSkillCatalog } from "./panels/skill-catalog";
import { renderPanel as renderMCPServers } from "./panels/mcp-servers";
import { renderPanel as renderObservability } from "./panels/observability";
import { renderPanel as renderApprovalQueue } from "./panels/approval-queue";
import { renderPanel as renderScheduler } from "./panels/scheduler";
import { mountDrawer as mountDescentEvidenceDrawer } from "./panels/descent-evidence";
import { mountNodeDrawer as mountLedgerNodeDrawer } from "./panels/ledger-node-drawer";
import {
  mountSettings,
  mountSettingsTrigger,
} from "./panels/settings";
import { mountOnboarding } from "./onboarding/onboarding";

type PanelEntry = {
  id: string;
  gridArea:
    | "session"
    | "sow"
    | "descent"
    | "ledger"
    | "memory"
    | "skills"
    | "mcp"
    | "obs"
    | "approvals"
    | "scheduler"
    | "cost";
  render: (root: HTMLElement) => void;
};

const PANELS: PanelEntry[] = [
  { id: "panel-session-view", gridArea: "session", render: renderSessionView },
  { id: "panel-sow-tree", gridArea: "sow", render: renderSowTree },
  { id: "panel-descent-ladder", gridArea: "descent", render: renderDescentLadder },
  { id: "panel-ledger-viewer", gridArea: "ledger", render: renderLedgerViewer },
  { id: "panel-memory-inspector", gridArea: "memory", render: renderMemoryInspector },
  { id: "panel-skill-catalog", gridArea: "skills", render: renderSkillCatalog },
  { id: "panel-mcp-servers", gridArea: "mcp", render: renderMCPServers },
  { id: "panel-observability", gridArea: "obs", render: renderObservability },
  { id: "panel-approval-queue", gridArea: "approvals", render: renderApprovalQueue },
  { id: "panel-scheduler", gridArea: "scheduler", render: renderScheduler },
  { id: "panel-cost", gridArea: "cost", render: renderCostPanel },
];

function mount(): void {
  const app = document.querySelector<HTMLElement>("#app");
  if (!app) {
    console.error("[r1-desktop] #app mount point missing from index.html");
    return;
  }

  if (window.localStorage.getItem("r1.onboarded") !== "1") {
    app.innerHTML = "";
    const host = document.createElement("div");
    host.id = "onboarding";
    app.appendChild(host);
    mountOnboarding(host);
    return;
  }

  app.classList.add("r1-app-grid");
  app.innerHTML = "";

  const toolbar = document.createElement("div");
  toolbar.className = "r1-app-toolbar";
  toolbar.style.gridArea = "toolbar";
  app.appendChild(toolbar);

  for (const panel of PANELS) {
    const section = document.createElement("section");
    section.id = panel.id;
    section.style.gridArea = panel.gridArea;
    app.appendChild(section);
    panel.render(section);
  }

  mountDescentEvidenceDrawer(document.body);
  mountLedgerNodeDrawer(document.body);
  mountSettings(document.body);
  mountSettingsTrigger(toolbar);
}

if (document.readyState === "loading") {
  document.addEventListener("DOMContentLoaded", mount, { once: true });
} else {
  mount();
}
