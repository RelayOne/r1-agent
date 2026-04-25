// SPDX-License-Identifier: MIT
//
// Observability dashboard panel (R1D-9).
//
// KPI summary cards, latency histogram (per-provider), skill-invocation
// counts, error-rate timeline, time-range picker, CSV export, and a
// RelayGate cost-reconciliation banner. Charts are drawn with inline
// SVG to avoid a charting-library dependency at scaffold time.

import { invokeStub } from "../ipc-stub";
import type {
  ErrorTimelinePoint,
  ObsCsvExport,
  ObsKPIs,
  ObsRange,
  ProviderLatencyHistogram,
  RelayGateReconcileResult,
  SkillInvocationCount,
} from "../types/ipc";

const RANGES: { label: string; value: ObsRange }[] = [
  { label: "1h", value: "1h" },
  { label: "24h", value: "24h" },
  { label: "7d", value: "7d" },
  { label: "30d", value: "30d" },
];

interface PanelState {
  range: ObsRange;
}

export function renderPanel(root: HTMLElement): void {
  root.classList.add("r1-panel", "r1-panel-observability");
  root.innerHTML = `
    <header class="r1-panel-header">
      <h2>Observability</h2>
      <span class="r1-panel-subtitle">KPIs · latency · skills · errors · cost reconciliation</span>
    </header>
    <div class="r1-panel-body r1-obs-body">
      <div class="r1-obs-toolbar">
        <div class="r1-obs-range" role="tablist" aria-label="Time range">
          ${RANGES.map(
            (r, i) => `
              <button type="button" role="tab"
                aria-selected="${i === 2 ? "true" : "false"}"
                class="r1-obs-range-btn"
                data-range="${r.value}">${r.label}</button>
            `,
          ).join("")}
        </div>
        <button type="button" class="r1-btn" data-role="obs-export">Export CSV</button>
      </div>
      <div class="r1-obs-banner" data-role="obs-banner" hidden></div>
      <section class="r1-obs-kpis" data-role="obs-kpis">
        <p class="r1-empty">Loading KPIs&hellip;</p>
      </section>
      <section class="r1-obs-latency" data-role="obs-latency">
        <h3>Latency by provider</h3>
        <div class="r1-obs-latency-grid"><p class="r1-empty">Loading&hellip;</p></div>
      </section>
      <section class="r1-obs-skills" data-role="obs-skills">
        <h3>Skill invocation counts</h3>
        <ul class="r1-obs-skill-list"><li class="r1-empty">Loading&hellip;</li></ul>
      </section>
      <section class="r1-obs-errors" data-role="obs-errors">
        <h3>Error-rate timeline</h3>
        <div class="r1-obs-error-chart"><p class="r1-empty">Loading&hellip;</p></div>
      </section>
    </div>
  `;

  const state: PanelState = { range: "7d" };

  const rangeButtons = root.querySelectorAll<HTMLButtonElement>(".r1-obs-range-btn");
  rangeButtons.forEach((btn) => {
    btn.addEventListener("click", () => {
      const next = btn.dataset.range as ObsRange | undefined;
      if (!next || next === state.range) return;
      state.range = next;
      rangeButtons.forEach((b) => {
        b.setAttribute("aria-selected", b === btn ? "true" : "false");
      });
      void refresh(root, state);
    });
  });

  const exportBtn = root.querySelector<HTMLButtonElement>('[data-role="obs-export"]');
  if (exportBtn) exportBtn.addEventListener("click", () => void handleExport(state));

  void refresh(root, state);
}

async function refresh(root: HTMLElement, state: PanelState): Promise<void> {
  await Promise.all([
    loadKPIs(root, state),
    loadLatency(root, state),
    loadSkills(root, state),
    loadErrors(root, state),
    loadReconcile(root, state),
  ]);
}

async function loadKPIs(root: HTMLElement, state: PanelState): Promise<void> {
  const target = root.querySelector<HTMLElement>('[data-role="obs-kpis"]');
  if (!target) return;
  const kpis = await invokeStub<ObsKPIs>(
    "obs_kpis",
    "R1D-9",
    {
      range: state.range,
      total_sessions: 0,
      total_tokens: 0,
      total_cost_usd: 0,
      avg_latency_ms: 0,
      error_rate: 0,
      unique_skills: 0,
    },
    { range: state.range },
  );
  target.innerHTML = renderKPICards(kpis);
}

function renderKPICards(k: ObsKPIs): string {
  const cards: { label: string; value: string }[] = [
    { label: "Sessions", value: k.total_sessions.toLocaleString() },
    { label: "Tokens", value: k.total_tokens.toLocaleString() },
    { label: "Cost (USD)", value: `$${k.total_cost_usd.toFixed(2)}` },
    { label: "Avg latency", value: `${k.avg_latency_ms.toFixed(0)} ms` },
    { label: "Error rate", value: `${(k.error_rate * 100).toFixed(2)}%` },
    { label: "Skills used", value: k.unique_skills.toLocaleString() },
  ];
  return `
    <ul class="r1-obs-kpi-grid">
      ${cards
        .map(
          (c) => `
            <li class="r1-obs-kpi-card">
              <span class="r1-obs-kpi-label">${escapeHtml(c.label)}</span>
              <span class="r1-obs-kpi-value">${escapeHtml(c.value)}</span>
            </li>
          `,
        )
        .join("")}
    </ul>
  `;
}

async function loadLatency(root: HTMLElement, state: PanelState): Promise<void> {
  const target = root.querySelector<HTMLElement>('[data-role="obs-latency"] .r1-obs-latency-grid');
  if (!target) return;
  const histograms = await invokeStub<ProviderLatencyHistogram[]>(
    "obs_latency_histogram",
    "R1D-9",
    [],
    { range: state.range },
  );
  if (histograms.length === 0) {
    target.innerHTML = `<p class="r1-empty">No requests in this range.</p>`;
    return;
  }
  target.innerHTML = histograms.map(renderHistogram).join("");
}

function renderHistogram(h: ProviderLatencyHistogram): string {
  const max = h.buckets.reduce((m, b) => Math.max(m, b.count), 0);
  const bars = h.buckets
    .map((b) => {
      const pct = max === 0 ? 0 : (b.count / max) * 100;
      return `
        <div class="r1-obs-hist-bar">
          <span class="r1-obs-hist-bar-fill" style="height: ${pct.toFixed(1)}%"></span>
          <span class="r1-obs-hist-bar-label">${b.upper_ms}</span>
        </div>
      `;
    })
    .join("");
  return `
    <article class="r1-obs-hist-card">
      <header>
        <h4>${escapeHtml(h.provider)}</h4>
        <span class="r1-obs-hist-meta">
          n=${h.total_calls.toLocaleString()} · p50 ${h.p50_ms}ms · p95 ${h.p95_ms}ms · p99 ${h.p99_ms}ms
        </span>
      </header>
      <div class="r1-obs-hist">${bars}</div>
    </article>
  `;
}

async function loadSkills(root: HTMLElement, state: PanelState): Promise<void> {
  const list = root.querySelector<HTMLUListElement>('[data-role="obs-skills"] .r1-obs-skill-list');
  if (!list) return;
  const skills = await invokeStub<SkillInvocationCount[]>(
    "obs_skill_counts",
    "R1D-9",
    [],
    { range: state.range },
  );
  if (skills.length === 0) {
    list.innerHTML = `<li class="r1-empty">No skill invocations in this range.</li>`;
    return;
  }
  const max = skills.reduce((m, s) => Math.max(m, s.count), 0);
  list.innerHTML = skills
    .slice(0, 20)
    .map((s) => {
      const pct = max === 0 ? 0 : (s.count / max) * 100;
      const errPct = s.count === 0 ? 0 : (s.error_count / s.count) * 100;
      return `
        <li class="r1-obs-skill-row">
          <span class="r1-obs-skill-name">${escapeHtml(s.skill)}</span>
          <span class="r1-obs-skill-bar"><span class="r1-obs-skill-bar-fill" style="width: ${pct.toFixed(1)}%"></span></span>
          <span class="r1-obs-skill-count">${s.count.toLocaleString()}</span>
          <span class="r1-obs-skill-err">${errPct.toFixed(1)}% err</span>
        </li>
      `;
    })
    .join("");
}

async function loadErrors(root: HTMLElement, state: PanelState): Promise<void> {
  const target = root.querySelector<HTMLElement>('[data-role="obs-errors"] .r1-obs-error-chart');
  if (!target) return;
  const points = await invokeStub<ErrorTimelinePoint[]>(
    "obs_error_timeline",
    "R1D-9",
    [],
    { range: state.range },
  );
  if (points.length === 0) {
    target.innerHTML = `<p class="r1-empty">No errors in this range.</p>`;
    return;
  }
  target.innerHTML = renderErrorTimeline(points);
}

function renderErrorTimeline(points: ErrorTimelinePoint[]): string {
  const w = 600;
  const h = 120;
  const pad = 24;
  const rates = points.map((p) => (p.total === 0 ? 0 : p.errors / p.total));
  const maxRate = Math.max(0.01, ...rates);
  const stepX = (w - pad * 2) / Math.max(1, points.length - 1);
  const xy = points
    .map((p, i) => {
      const rate = p.total === 0 ? 0 : p.errors / p.total;
      const x = pad + i * stepX;
      const y = h - pad - (rate / maxRate) * (h - pad * 2);
      return `${x.toFixed(1)},${y.toFixed(1)}`;
    })
    .join(" ");
  const dots = points
    .map((p, i) => {
      const rate = p.total === 0 ? 0 : p.errors / p.total;
      const x = pad + i * stepX;
      const y = h - pad - (rate / maxRate) * (h - pad * 2);
      const title = `${p.ts}: ${p.errors}/${p.total} (${(rate * 100).toFixed(2)}%)`;
      return `<circle cx="${x.toFixed(1)}" cy="${y.toFixed(1)}" r="2.5" fill="#ff8b8b"><title>${escapeHtml(title)}</title></circle>`;
    })
    .join("");
  return `
    <svg class="r1-obs-error-svg" viewBox="0 0 ${w} ${h}" role="img" aria-label="Error-rate timeline">
      <line x1="${pad}" y1="${h - pad}" x2="${w - pad}" y2="${h - pad}" stroke="#272c37" />
      <line x1="${pad}" y1="${pad}" x2="${pad}" y2="${h - pad}" stroke="#272c37" />
      <text x="${pad}" y="${pad - 6}" font-size="10" fill="#8a93a4">${(maxRate * 100).toFixed(2)}%</text>
      <polyline points="${xy}" fill="none" stroke="#ff8b8b" stroke-width="1.4" />
      ${dots}
    </svg>
  `;
}

async function loadReconcile(root: HTMLElement, state: PanelState): Promise<void> {
  const banner = root.querySelector<HTMLElement>('[data-role="obs-banner"]');
  if (!banner) return;
  const result = await invokeStub<RelayGateReconcileResult>(
    "obs_relaygate_reconcile",
    "R1D-9",
    {
      configured: false,
      match: true,
      desktop_cost_usd: 0,
      relaygate_cost_usd: 0,
      delta_usd: 0,
    },
    { range: state.range },
  );
  if (!result.configured) {
    banner.hidden = true;
    return;
  }
  banner.hidden = false;
  banner.classList.toggle("r1-obs-banner-warn", !result.match);
  banner.classList.toggle("r1-obs-banner-ok", result.match);
  banner.textContent = result.match
    ? `RelayGate B6 cost reconciliation: matched ($${result.desktop_cost_usd.toFixed(2)}).`
    : `RelayGate B6 cost mismatch: desktop $${result.desktop_cost_usd.toFixed(2)} vs RelayGate $${result.relaygate_cost_usd.toFixed(2)} (Δ $${result.delta_usd.toFixed(2)}). ${result.message ?? ""}`;
}

async function handleExport(state: PanelState): Promise<void> {
  const result = await invokeStub<ObsCsvExport>(
    "obs_export_csv",
    "R1D-9",
    {
      filename: `r1-observability-${state.range}.csv`,
      csv: "session_id,started_at,tokens,cost_usd,latency_ms,errors\n",
      row_count: 0,
    },
    { range: state.range },
  );
  const blob = new Blob([result.csv], { type: "text/csv" });
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = result.filename;
  document.body.appendChild(anchor);
  anchor.click();
  anchor.remove();
  URL.revokeObjectURL(url);
}

function escapeHtml(raw: string): string {
  return raw
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#39;");
}
