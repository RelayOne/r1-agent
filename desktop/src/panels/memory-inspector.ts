// SPDX-License-Identifier: MIT
//
// Memory-bus inspector panel (R1D-2).
//
// Renders the five canonical memory scopes as tabs, each with its
// entry-count badge (0 at scaffold time). The `memory.list_scopes`
// call is issued through `invokeStub` with a TODO R1D-6 tag.
//
// Per-row drill-down + write/read history lands in R1D-6.3.

import { invokeStub } from "../ipc-stub";
import { ALL_MEMORY_SCOPES } from "../types/ipc-const";
import type {
  MemoryListScopesResult,
  MemoryScope,
} from "../types/ipc";

export function renderPanel(root: HTMLElement): void {
  root.classList.add("r1-panel", "r1-panel-memory-inspector");
  root.innerHTML = `
    <header class="r1-panel-header">
      <h2>Memory Bus</h2>
      <span class="r1-panel-subtitle">5-scope key/value inspector</span>
    </header>
    <div class="r1-panel-body">
      <ul class="r1-memory-scopes" data-role="memory-scopes" role="tablist">
        ${ALL_MEMORY_SCOPES.map(renderScopeRow).join("")}
      </ul>
    </div>
  `;

  const scopes = root.querySelector<HTMLUListElement>(
    '[data-role="memory-scopes"]',
  );
  if (!scopes) return;

  void loadScopes(scopes);
}

async function loadScopes(container: HTMLUListElement): Promise<void> {
  const result = await invokeStub<MemoryListScopesResult>(
    "memory_list_scopes",
    "R1D-6",
    { scopes: [] },
  );
  // Scaffold renders every canonical scope with a count of zero
  // regardless of what the stub returns; once the real body lands
  // (R1D-6.2), this becomes a per-scope key/value table.
  void result;
  applyZeroCounts(container);
}

function applyZeroCounts(container: HTMLUListElement): void {
  for (const scope of ALL_MEMORY_SCOPES) {
    const badge = container.querySelector<HTMLSpanElement>(
      `[data-scope="${scope}"] .r1-scope-count`,
    );
    if (badge) badge.textContent = "0";
  }
}

function renderScopeRow(scope: MemoryScope): string {
  return `
    <li class="r1-memory-scope" data-scope="${scope}" role="tab" tabindex="0">
      <span class="r1-scope-name">${scope}</span>
      <span class="r1-scope-count" aria-label="entry count">&mdash;</span>
    </li>
  `;
}
