// SPDX-License-Identifier: MIT
//
// Scheduler panel (R1D-10.3 — UI side).
//
// CRUD on recurring tasks: list with last/next-run timestamps, create
// + edit modal, enable/disable toggle, run-now button, delete with
// confirm. OS-level scheduling (launchd / Task Scheduler / systemd
// user units) is wired by the Rust host in R1D-10.4 — this panel only
// surfaces the desktop-side schedule registry.

import { invokeStub } from "../ipc-stub";
import type {
  ScheduleOkResult,
  ScheduleUpsertRequest,
  ScheduledTask,
} from "../types/ipc";

interface PanelState {
  tasks: ScheduledTask[];
  editing: ScheduledTask | null;
}

export function renderPanel(root: HTMLElement): void {
  root.classList.add("r1-panel", "r1-panel-scheduler");
  root.innerHTML = `
    <header class="r1-panel-header">
      <h2>Scheduler</h2>
      <span class="r1-panel-subtitle">recurring autonomous tasks</span>
    </header>
    <div class="r1-panel-body r1-scheduler-body">
      <div class="r1-scheduler-toolbar">
        <button type="button" class="r1-btn r1-btn-primary" data-role="schedule-new">New schedule</button>
      </div>
      <ul class="r1-scheduler-list" data-role="scheduler-list" aria-live="polite">
        <li class="r1-empty">Loading schedules&hellip;</li>
      </ul>
      <dialog class="r1-scheduler-dialog" data-role="scheduler-dialog">
        <form class="r1-scheduler-form" data-role="scheduler-form" method="dialog">
          <h3 data-role="scheduler-form-title">New schedule</h3>
          <input type="hidden" name="id">
          <label>Name <input type="text" name="name" required></label>
          <label>Cron <input type="text" name="cron" required></label>
          <label>Prompt <textarea name="prompt" rows="3" required></textarea></label>
          <label>Skill pack <input type="text" name="skill_pack"></label>
          <label>Provider <input type="text" name="provider"></label>
          <label>Budget (USD) <input type="number" step="0.01" min="0" name="budget_usd"></label>
          <label class="r1-scheduler-form-checkbox">
            <input type="checkbox" name="enabled" checked>
            Enabled
          </label>
          <div class="r1-modal-actions">
            <button type="button" class="r1-btn" data-role="scheduler-cancel">Cancel</button>
            <button type="submit" class="r1-btn r1-btn-primary">Save</button>
          </div>
        </form>
      </dialog>
    </div>
  `;

  const list = root.querySelector<HTMLUListElement>('[data-role="scheduler-list"]');
  const newBtn = root.querySelector<HTMLButtonElement>('[data-role="schedule-new"]');
  const dialog = root.querySelector<HTMLDialogElement>('[data-role="scheduler-dialog"]');
  const form = root.querySelector<HTMLFormElement>('[data-role="scheduler-form"]');
  const cancelBtn = root.querySelector<HTMLButtonElement>('[data-role="scheduler-cancel"]');
  if (!list || !newBtn || !dialog || !form || !cancelBtn) return;

  const state: PanelState = { tasks: [], editing: null };

  newBtn.addEventListener("click", () => openDialog(root, state, null));
  cancelBtn.addEventListener("click", () => dialog.close());

  form.addEventListener("submit", (ev) => {
    ev.preventDefault();
    const data = new FormData(form);
    const req: ScheduleUpsertRequest = {
      id: (String(data.get("id") ?? "") || undefined),
      name: String(data.get("name") ?? ""),
      cron: String(data.get("cron") ?? ""),
      prompt: String(data.get("prompt") ?? ""),
      skill_pack: String(data.get("skill_pack") ?? "") || undefined,
      provider: String(data.get("provider") ?? "") || undefined,
      budget_usd: data.get("budget_usd") ? Number(data.get("budget_usd")) : undefined,
      enabled: data.get("enabled") === "on",
    };
    void handleSave(root, state, req).then(() => {
      form.reset();
      dialog.close();
    });
  });

  list.addEventListener("click", (ev) => {
    const target = ev.target;
    if (!(target instanceof HTMLElement)) return;
    const row = target.closest<HTMLElement>(".r1-scheduler-row");
    if (!row) return;
    const id = row.dataset.scheduleId;
    if (!id) return;
    const role = target.dataset.role;
    if (role === "schedule-edit") {
      const t = state.tasks.find((x) => x.id === id);
      if (t) openDialog(root, state, t);
      return;
    }
    if (role === "schedule-delete") {
      void handleDelete(root, state, id);
      return;
    }
    if (role === "schedule-toggle") {
      const t = state.tasks.find((x) => x.id === id);
      if (t) void handleSave(root, state, { ...toUpsert(t), enabled: !t.enabled });
      return;
    }
    if (role === "schedule-run-now") {
      void handleRunNow(root, state, id);
      return;
    }
  });

  void refresh(root, state);
}

function toUpsert(t: ScheduledTask): ScheduleUpsertRequest {
  return {
    id: t.id,
    name: t.name,
    prompt: t.prompt,
    cron: t.cron,
    enabled: t.enabled,
    skill_pack: t.skill_pack,
    provider: t.provider,
    budget_usd: t.budget_usd,
  };
}

function openDialog(root: HTMLElement, state: PanelState, task: ScheduledTask | null): void {
  const dialog = root.querySelector<HTMLDialogElement>('[data-role="scheduler-dialog"]');
  const form = root.querySelector<HTMLFormElement>('[data-role="scheduler-form"]');
  const title = root.querySelector<HTMLElement>('[data-role="scheduler-form-title"]');
  if (!dialog || !form || !title) return;
  state.editing = task;
  if (task) {
    title.textContent = `Edit ${task.name}`;
    setFieldValue(form, "id", task.id);
    setFieldValue(form, "name", task.name);
    setFieldValue(form, "cron", task.cron);
    setFieldValue(form, "prompt", task.prompt);
    setFieldValue(form, "skill_pack", task.skill_pack ?? "");
    setFieldValue(form, "provider", task.provider ?? "");
    setFieldValue(form, "budget_usd", task.budget_usd != null ? String(task.budget_usd) : "");
    setFieldChecked(form, "enabled", task.enabled);
  } else {
    title.textContent = "New schedule";
    form.reset();
  }
  dialog.showModal();
}

function setFieldValue(form: HTMLFormElement, name: string, value: string): void {
  const el = form.elements.namedItem(name);
  if (el instanceof HTMLInputElement || el instanceof HTMLTextAreaElement) {
    el.value = value;
  }
}

function setFieldChecked(form: HTMLFormElement, name: string, checked: boolean): void {
  const el = form.elements.namedItem(name);
  if (el instanceof HTMLInputElement) {
    el.checked = checked;
  }
}

async function refresh(root: HTMLElement, state: PanelState): Promise<void> {
  const tasks = await invokeStub<ScheduledTask[]>("schedule_list", "R1D-10", []);
  state.tasks = tasks;
  renderList(root, state);
}

function renderList(root: HTMLElement, state: PanelState): void {
  const list = root.querySelector<HTMLUListElement>('[data-role="scheduler-list"]');
  if (!list) return;
  if (state.tasks.length === 0) {
    list.innerHTML = `<li class="r1-empty">No schedules. Click "New schedule" to add one.</li>`;
    return;
  }
  list.innerHTML = state.tasks.map(renderRow).join("");
}

function renderRow(t: ScheduledTask): string {
  const status = t.last_status
    ? `<span class="r1-scheduler-status r1-scheduler-status-${t.last_status}">${t.last_status}</span>`
    : "";
  const next = t.next_run_at ? `next ${escapeHtml(t.next_run_at)}` : "not scheduled";
  const last = t.last_run_at ? `last ${escapeHtml(t.last_run_at)}` : "never run";
  return `
    <li class="r1-scheduler-row" data-schedule-id="${escapeHtml(t.id)}">
      <header class="r1-scheduler-row-head">
        <span class="r1-scheduler-name">${escapeHtml(t.name)}</span>
        <code class="r1-scheduler-cron">${escapeHtml(t.cron)}</code>
        ${status}
        <span class="r1-scheduler-toggle r1-scheduler-toggle-${t.enabled ? "on" : "off"}" data-role="schedule-toggle">
          ${t.enabled ? "enabled" : "paused"}
        </span>
      </header>
      <p class="r1-scheduler-prompt">${escapeHtml(t.prompt)}</p>
      <footer class="r1-scheduler-row-foot">
        <span class="r1-scheduler-meta">${last} · ${next}</span>
        <button type="button" class="r1-btn" data-role="schedule-run-now">Run now</button>
        <button type="button" class="r1-btn" data-role="schedule-edit">Edit</button>
        <button type="button" class="r1-btn r1-btn-danger" data-role="schedule-delete">Delete</button>
      </footer>
    </li>
  `;
}

async function handleSave(root: HTMLElement, state: PanelState, req: ScheduleUpsertRequest): Promise<void> {
  const method = req.id ? "schedule_update" : "schedule_create";
  const result = await invokeStub<ScheduleOkResult>(
    method,
    "R1D-10",
    { ok: true },
    req as unknown as Record<string, unknown>,
  );
  if (!result.ok) {
    alert(`Save failed for ${req.name}`);
    return;
  }
  await refresh(root, state);
}

async function handleDelete(root: HTMLElement, state: PanelState, id: string): Promise<void> {
  const target = state.tasks.find((t) => t.id === id);
  if (!target) return;
  if (!confirm(`Delete schedule "${target.name}"?`)) return;
  const result = await invokeStub<ScheduleOkResult>("schedule_delete", "R1D-10", { ok: true }, { id });
  if (!result.ok) {
    alert(`Delete failed for ${id}`);
    return;
  }
  await refresh(root, state);
}

async function handleRunNow(root: HTMLElement, state: PanelState, id: string): Promise<void> {
  const result = await invokeStub<ScheduleOkResult>("schedule_run_now", "R1D-10", { ok: true }, { id });
  if (!result.ok) {
    alert(`Run-now failed for ${id}`);
    return;
  }
  await refresh(root, state);
}

function escapeHtml(raw: string): string {
  return raw
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#39;");
}
