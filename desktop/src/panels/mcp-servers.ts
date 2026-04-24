// SPDX-License-Identifier: MIT
//
// MCP servers panel (R1D-8).
//
// Lists configured MCP servers. Lets the operator add a new server,
// remove an existing one, run a connection-test handshake, and invoke
// individual tools through a generated form.

import { invokeStub } from "../ipc-stub";
import type {
  MCPAddRequest,
  MCPInvokeResult,
  MCPOkResult,
  MCPServer,
  MCPTestResult,
  MCPTool,
} from "../types/ipc";

interface PanelState {
  servers: MCPServer[];
  selectedID: string | null;
  testResults: Map<string, MCPTestResult>;
}

export function renderPanel(root: HTMLElement): void {
  root.classList.add("r1-panel", "r1-panel-mcp-servers");
  root.innerHTML = `
    <header class="r1-panel-header">
      <h2>MCP Servers</h2>
      <span class="r1-panel-subtitle">model context protocol — list, add, test, invoke</span>
    </header>
    <div class="r1-panel-body r1-mcp-body">
      <div class="r1-mcp-toolbar">
        <button type="button" class="r1-btn r1-btn-primary" data-role="mcp-add-open">Add server</button>
      </div>
      <ul class="r1-mcp-list" data-role="mcp-list" aria-live="polite">
        <li class="r1-empty">Loading servers&hellip;</li>
      </ul>
      <section class="r1-mcp-detail" data-role="mcp-detail" hidden>
        <header class="r1-mcp-detail-head">
          <h3 data-role="mcp-detail-name"></h3>
          <button type="button" class="r1-btn" data-role="mcp-detail-close">Close</button>
        </header>
        <div class="r1-mcp-detail-body" data-role="mcp-detail-body"></div>
      </section>
      <dialog class="r1-mcp-add-dialog" data-role="mcp-add-dialog">
        <form class="r1-mcp-add-form" data-role="mcp-add-form" method="dialog">
          <h3>Add MCP server</h3>
          <label>Name <input type="text" name="name" required></label>
          <label>URL <input type="text" name="url" required placeholder="http://localhost:3000 or stdio:./bin/server"></label>
          <label>Transport
            <select name="transport">
              <option value="stdio">stdio</option>
              <option value="sse">sse</option>
              <option value="http">http</option>
            </select>
          </label>
          <div class="r1-modal-actions">
            <button type="button" class="r1-btn" data-role="mcp-add-cancel">Cancel</button>
            <button type="submit" class="r1-btn r1-btn-primary">Add</button>
          </div>
        </form>
      </dialog>
    </div>
  `;

  const list = root.querySelector<HTMLUListElement>('[data-role="mcp-list"]');
  const detail = root.querySelector<HTMLElement>('[data-role="mcp-detail"]');
  const addBtn = root.querySelector<HTMLButtonElement>('[data-role="mcp-add-open"]');
  const dialog = root.querySelector<HTMLDialogElement>('[data-role="mcp-add-dialog"]');
  const form = root.querySelector<HTMLFormElement>('[data-role="mcp-add-form"]');
  const cancelBtn = root.querySelector<HTMLButtonElement>('[data-role="mcp-add-cancel"]');
  const detailClose = root.querySelector<HTMLButtonElement>('[data-role="mcp-detail-close"]');
  if (!list || !detail || !addBtn || !dialog || !form || !cancelBtn || !detailClose) return;

  const state: PanelState = {
    servers: [],
    selectedID: null,
    testResults: new Map(),
  };

  addBtn.addEventListener("click", () => dialog.showModal());
  cancelBtn.addEventListener("click", () => dialog.close());
  detailClose.addEventListener("click", () => closeDetail(detail));

  form.addEventListener("submit", (ev) => {
    ev.preventDefault();
    const data = new FormData(form);
    const req: MCPAddRequest = {
      name: String(data.get("name") ?? ""),
      url: String(data.get("url") ?? ""),
      transport: String(data.get("transport") ?? "stdio") as MCPAddRequest["transport"],
    };
    void handleAdd(root, state, req).then(() => {
      form.reset();
      dialog.close();
    });
  });

  list.addEventListener("click", (ev) => {
    const target = ev.target;
    if (!(target instanceof HTMLElement)) return;
    const row = target.closest<HTMLElement>(".r1-mcp-row");
    if (!row) return;
    const id = row.dataset.serverId;
    if (!id) return;
    const role = target.dataset.role;
    if (role === "mcp-remove") {
      ev.stopPropagation();
      void handleRemove(root, state, id);
      return;
    }
    if (role === "mcp-test") {
      ev.stopPropagation();
      void handleTest(root, state, id);
      return;
    }
    openDetail(root, state, id);
  });

  void loadServers(root, state);
}

async function loadServers(root: HTMLElement, state: PanelState): Promise<void> {
  const servers = await invokeStub<MCPServer[]>("mcp_list", "R1D-8", []);
  state.servers = servers;
  renderList(root, state);
}

function renderList(root: HTMLElement, state: PanelState): void {
  const list = root.querySelector<HTMLUListElement>('[data-role="mcp-list"]');
  if (!list) return;
  if (state.servers.length === 0) {
    list.innerHTML = `<li class="r1-empty">No MCP servers configured. Click "Add server" to register one.</li>`;
    return;
  }
  list.innerHTML = state.servers.map((s) => renderRow(s, state.testResults.get(s.id))).join("");
}

function renderRow(s: MCPServer, test?: MCPTestResult): string {
  const statusBadge = test
    ? test.ok
      ? `<span class="r1-mcp-badge r1-mcp-badge-ok">ok ${test.latency_ms}ms · ${test.tools.length} tools</span>`
      : `<span class="r1-mcp-badge r1-mcp-badge-err">${escapeHtml(test.message ?? "error")}</span>`
    : `<span class="r1-mcp-badge r1-mcp-badge-${s.status}">${s.status}</span>`;
  return `
    <li class="r1-mcp-row" data-server-id="${escapeHtml(s.id)}" tabindex="0" role="button">
      <div class="r1-mcp-row-main">
        <span class="r1-mcp-name">${escapeHtml(s.name)}</span>
        ${statusBadge}
      </div>
      <div class="r1-mcp-row-meta">
        <span class="r1-mcp-transport">${s.transport}</span>
        <span class="r1-mcp-url">${escapeHtml(s.url)}</span>
      </div>
      <div class="r1-mcp-row-actions">
        <button type="button" class="r1-btn" data-role="mcp-test">Test</button>
        <button type="button" class="r1-btn r1-btn-danger" data-role="mcp-remove">Remove</button>
      </div>
    </li>
  `;
}

async function handleAdd(root: HTMLElement, state: PanelState, req: MCPAddRequest): Promise<void> {
  const result = await invokeStub<MCPOkResult>("mcp_add", "R1D-8", { ok: true }, req as unknown as Record<string, unknown>);
  if (!result.ok) {
    alert(`Add failed for ${req.name}`);
    return;
  }
  await loadServers(root, state);
}

async function handleRemove(root: HTMLElement, state: PanelState, id: string): Promise<void> {
  const target = state.servers.find((s) => s.id === id);
  if (!target) return;
  if (!confirm(`Remove MCP server "${target.name}"?`)) return;
  const result = await invokeStub<MCPOkResult>("mcp_remove", "R1D-8", { ok: true }, { id });
  if (!result.ok) {
    alert(`Remove failed for ${id}`);
    return;
  }
  state.testResults.delete(id);
  if (state.selectedID === id) closeDetail(root.querySelector('[data-role="mcp-detail"]'));
  await loadServers(root, state);
}

async function handleTest(root: HTMLElement, state: PanelState, id: string): Promise<void> {
  const result = await invokeStub<MCPTestResult>(
    "mcp_test",
    "R1D-8",
    { ok: true, latency_ms: 0, protocol_version: "2024-11-05", tools: [] },
    { id },
  );
  state.testResults.set(id, result);
  renderList(root, state);
  if (state.selectedID === id) renderDetail(root, state, id);
}

function openDetail(root: HTMLElement, state: PanelState, id: string): void {
  state.selectedID = id;
  renderDetail(root, state, id);
}

function closeDetail(detail: HTMLElement | null): void {
  if (!detail) return;
  detail.hidden = true;
}

function renderDetail(root: HTMLElement, state: PanelState, id: string): void {
  const detail = root.querySelector<HTMLElement>('[data-role="mcp-detail"]');
  const nameEl = root.querySelector<HTMLElement>('[data-role="mcp-detail-name"]');
  const body = root.querySelector<HTMLElement>('[data-role="mcp-detail-body"]');
  const server = state.servers.find((s) => s.id === id);
  if (!detail || !nameEl || !body || !server) return;
  detail.hidden = false;
  nameEl.textContent = server.name;
  const test = state.testResults.get(id);
  if (!test || test.tools.length === 0) {
    body.innerHTML = `<p class="r1-empty">Run "Test" to discover tools advertised by this server.</p>`;
    return;
  }
  body.innerHTML = test.tools.map((t) => renderToolBlock(server.id, t)).join("");
  body.querySelectorAll<HTMLFormElement>(".r1-mcp-tool-form").forEach((form) => {
    form.addEventListener("submit", (ev) => {
      ev.preventDefault();
      void handleInvoke(form, server.id, form.dataset.toolName ?? "");
    });
  });
}

function renderToolBlock(serverID: string, tool: MCPTool): string {
  const inputs = renderInputs(tool.input_schema);
  return `
    <article class="r1-mcp-tool" data-tool-name="${escapeHtml(tool.name)}">
      <header>
        <h4>${escapeHtml(tool.name)}</h4>
        <p>${escapeHtml(tool.description)}</p>
      </header>
      <form class="r1-mcp-tool-form" data-server-id="${escapeHtml(serverID)}" data-tool-name="${escapeHtml(tool.name)}">
        ${inputs}
        <button type="submit" class="r1-btn r1-btn-primary">Invoke</button>
      </form>
      <pre class="r1-mcp-tool-output" data-role="output" hidden></pre>
    </article>
  `;
}

function renderInputs(schema: Record<string, unknown>): string {
  const props = (schema as { properties?: Record<string, { type?: string; description?: string; enum?: string[] }> }).properties;
  if (!props) return `<p class="r1-mcp-tool-empty">No input parameters.</p>`;
  return Object.entries(props)
    .map(([key, prop]) => {
      const t = prop.type ?? "string";
      const desc = prop.description ? `<small>${escapeHtml(prop.description)}</small>` : "";
      if (Array.isArray(prop.enum)) {
        const opts = prop.enum.map((v) => `<option value="${escapeHtml(v)}">${escapeHtml(v)}</option>`).join("");
        return `<label>${escapeHtml(key)} ${desc}<select name="${escapeHtml(key)}">${opts}</select></label>`;
      }
      const inputType = t === "number" || t === "integer" ? "number" : "text";
      return `<label>${escapeHtml(key)} ${desc}<input type="${inputType}" name="${escapeHtml(key)}"></label>`;
    })
    .join("");
}

async function handleInvoke(form: HTMLFormElement, serverID: string, toolName: string): Promise<void> {
  const data = new FormData(form);
  const args: Record<string, unknown> = {};
  data.forEach((v, k) => {
    args[k] = v;
  });
  const out = form.querySelector<HTMLPreElement>('[data-role="output"]');
  if (out) {
    out.hidden = false;
    out.textContent = "Invoking…";
  }
  const result = await invokeStub<MCPInvokeResult>(
    "mcp_invoke_tool",
    "R1D-8",
    { ok: true, output: "", duration_ms: 0 },
    { server_id: serverID, tool_name: toolName, args },
  );
  if (!out) return;
  if (!result.ok) {
    out.textContent = `error: ${result.error?.message ?? "invoke failed"}`;
    return;
  }
  out.textContent = `(${result.duration_ms}ms) ${result.output || "(empty)"}`;
}

function escapeHtml(raw: string): string {
  return raw
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#39;");
}
