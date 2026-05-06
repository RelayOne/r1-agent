// cmd/r1-server/ui/web/js/scrubber.js
//
// Spec 2 §3.3: vanilla-JS island bound to <input type="range" id="timeline-scrubber">.
// Reads `data-cursor` attributes from the waterfall rows (set server-side by
// Spec 3) and dispatches a 'graph:visibility' CustomEvent that graph.js
// listens for. No worker involvement — the worker is idle during scrub
// because positions are frozen at simulation cool-down.
//
// Loaded by base.html via <script type="module" src="/ui/web/js/scrubber.js">.

const SCRUBBER_SEL = '#timeline-scrubber';
const ROW_SEL = '[data-cursor]';

function readRows() {
  // Each row carries data-node-id + data-cursor (ISO 8601 timestamp,
  // monotonically increasing top-to-bottom in the waterfall).
  const out = [];
  for (const row of document.querySelectorAll(ROW_SEL)) {
    const nodeId = row.getAttribute('data-node-id') || '';
    const cursor = row.getAttribute('data-cursor') || '';
    if (!nodeId || !cursor) continue;
    const t = Date.parse(cursor);
    if (Number.isNaN(t)) continue;
    out.push({ nodeId, t });
  }
  // Sort by timestamp ascending so binary search works.
  out.sort((a, b) => a.t - b.t);
  return out;
}

// Build a Uint8Array indexed by row order: 1 = visible, 0 = hidden.
// Spec 2 §3.3: nodes whose created_at > cursor are scaled to 0.
function buildVisibility(rows, cursorMs) {
  const v = new Uint8Array(rows.length);
  for (let i = 0; i < rows.length; i++) v[i] = rows[i].t <= cursorMs ? 1 : 0;
  return v;
}

function dispatchVisibility(rows, cursorMs) {
  const visibility = buildVisibility(rows, cursorMs);
  const ev = new CustomEvent('graph:visibility', {
    detail: {
      kind: 'visibility',
      cursor: cursorMs,
      visibility,
      nodeIds: rows.map(r => r.nodeId),
    },
  });
  document.dispatchEvent(ev);
}

function bind() {
  const slider = document.querySelector(SCRUBBER_SEL);
  if (!slider) return;
  let rows = readRows();
  if (rows.length === 0) {
    // Re-poll after the first SSE batch lands. htmx swap fires the
    // `htmx:afterSwap` event on document.
    document.addEventListener('htmx:afterSwap', () => {
      rows = readRows();
      configureRange(slider, rows);
    }, { once: true });
  }
  configureRange(slider, rows);
  let raf = 0;
  slider.addEventListener('input', () => {
    if (raf) return; // Coalesce rapid drag events to one rAF tick.
    raf = requestAnimationFrame(() => {
      raf = 0;
      const cursorMs = parseInt(slider.value, 10);
      slider.setAttribute('data-state', cursorMs >= rows[rows.length - 1]?.t ? 'live' : 'rewind');
      dispatchVisibility(rows, cursorMs);
    });
  });
}

function configureRange(slider, rows) {
  if (rows.length === 0) return;
  const tMin = rows[0].t;
  const tMax = rows[rows.length - 1].t;
  slider.min = String(tMin);
  slider.max = String(tMax);
  slider.step = '1000'; // 1 second steps; coarse enough to be smooth.
  slider.value = String(tMax); // Default cursor: live (latest).
}

if (typeof document !== 'undefined') {
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', bind, { once: true });
  } else {
    bind();
  }
}

export { buildVisibility, readRows };
