// r1-server MVP dashboard app.js
//
// Single-page app, vanilla fetch + DOM. Two routes:
//   /             — instance list
//   /session/:id  — live-tailing stream view
//
// Routing is path-based (via history API). app.js listens for
// popstate and hijacks click on same-origin anchors so the shell
// HTML never reloads. Polling refreshes on intervals — no SSE yet.

const app = document.getElementById('app');
const connStatus = document.getElementById('conn-status');

const STATUS_ICON = {
  running: { icon: '🟢', cls: 'status-running' },
  completed: { icon: '⚫', cls: 'status-completed' },
  crashed: { icon: '🔴', cls: 'status-crashed' },
  failed: { icon: '🔴', cls: 'status-failed' },
  paused: { icon: '⏸', cls: 'status-paused' },
};

// -- Fetch helpers -----------------------------------------------------
async function getJSON(path) {
  const r = await fetch(path, { headers: { 'Accept': 'application/json' } });
  if (!r.ok) throw new Error(`GET ${path} → ${r.status}`);
  return r.json();
}

function markConn(ok, text) {
  connStatus.textContent = text;
  connStatus.className = ok ? 'ok' : 'err';
}

// -- Formatting helpers ------------------------------------------------
function elapsed(since) {
  if (!since) return '—';
  const ms = Date.now() - new Date(since).getTime();
  if (ms < 1000) return '<1s';
  const s = Math.floor(ms / 1000);
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ${s % 60}s`;
  const h = Math.floor(m / 60);
  return `${h}h ${m % 60}m`;
}

function esc(s) {
  return String(s == null ? '' : s).replace(
    /[&<>"]/g, (c) => ({ '&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;' }[c]));
}

// -- Views -------------------------------------------------------------
async function renderList() {
  app.innerHTML = `<section><h2>Instances</h2><div id="list"></div></section>`;
  const refresh = async () => {
    try {
      const { sessions, count } = await getJSON('/api/sessions');
      markConn(true, `${count} session${count === 1 ? '' : 's'}`);
      const box = document.getElementById('list');
      if (!box) return; // route switched away mid-refresh
      if (!count) {
        box.innerHTML = `<p class="empty">No Stoke instances discovered yet.<br>
          r1-server scans $HOME and common code dirs every 60s.</p>`;
        return;
      }
      const rows = sessions.map((s) => {
        const { icon, cls } = STATUS_ICON[s.status] || { icon: '❓', cls: '' };
        const idEnc = encodeURIComponent(s.instance_id);
        return `<tr>
          <td class="status ${cls}">${icon}</td>
          <td class="id"><code>${esc(s.instance_id)}</code></td>
          <td><a href="/session/${idEnc}"
                 data-route="/session/${esc(s.instance_id)}">${esc(s.repo_root || '—')}</a></td>
          <td>${esc(s.mode || '—')}</td>
          <td>${esc(s.model || '—')}</td>
          <td>${esc(s.sow_name || '—')}</td>
          <td>${elapsed(s.started_at)}</td>
          <td class="links">
            <a href="/session/${idEnc}"
               data-route="/session/${esc(s.instance_id)}">[Stream]</a>
            <a href="/session/${idEnc}/graph">[Graph]</a>
          </td>
        </tr>`;
      }).join('');
      box.innerHTML = `
        <table class="sessions">
          <thead><tr>
            <th></th><th>Instance</th><th>Repo</th>
            <th>Mode</th><th>Model</th><th>SOW</th><th>Elapsed</th><th>Views</th>
          </tr></thead>
          <tbody>${rows}</tbody>
        </table>`;
    } catch (e) {
      markConn(false, e.message);
    }
  };
  await refresh();
  listRefresh.id = setInterval(refresh, 5000);
}

const listRefresh = { id: null };
const detailRefresh = { id: null, lastEventID: 0 };

async function renderDetail(id) {
  app.innerHTML = `<section>
    <div class="session-meta" id="meta"></div>
    <div class="controls">
      <label>Filter <select id="type-filter"><option value="">all</option></select></label>
      <label><input type="checkbox" id="autoscroll" checked> auto-scroll</label>
    </div>
    <div class="events" id="events"></div>
  </section>`;

  const typeFilter = document.getElementById('type-filter');
  const autoscroll = document.getElementById('autoscroll');
  const eventsBox = document.getElementById('events');

  const knownTypes = new Set();
  let rendered = 0;

  async function refreshMeta() {
    try {
      const row = await getJSON(`/api/session/${encodeURIComponent(id)}`);
      const { icon, cls } = STATUS_ICON[row.status] || { icon: '❓', cls: '' };
      document.getElementById('meta').innerHTML = `
        <dl>
          <dt>Status</dt><dd class="${cls}">${icon} ${esc(row.status)}</dd>
          <dt>Instance</dt><dd><code>${esc(row.instance_id)}</code></dd>
          <dt>Repo</dt><dd>${esc(row.repo_root)}</dd>
          <dt>Mode</dt><dd>${esc(row.mode || '—')}</dd>
          <dt>Model</dt><dd>${esc(row.model || '—')}</dd>
          <dt>SOW</dt><dd>${esc(row.sow_name || '—')}</dd>
          <dt>Started</dt><dd>${esc(row.started_at)} (${elapsed(row.started_at)} ago)</dd>
          <dt>Stream</dt><dd><code>${esc(row.stream_file || '—')}</code></dd>
          <dt>Views</dt><dd><a href="/session/${encodeURIComponent(row.instance_id)}/graph">[Graph]</a></dd>
        </dl>`;
      markConn(true, row.status);
    } catch (e) {
      markConn(false, e.message);
      document.getElementById('meta').innerHTML =
        `<div class="error-banner">Session ${esc(id)} not found: ${esc(e.message)}</div>`;
    }
  }

  async function refreshEvents() {
    try {
      const payload = await getJSON(
        `/api/session/${encodeURIComponent(id)}/events?after=${detailRefresh.lastEventID}&limit=500`);
      const fresh = payload.events || [];
      if (!fresh.length) return;
      const wanted = typeFilter.value;
      for (const ev of fresh) {
        detailRefresh.lastEventID = Math.max(detailRefresh.lastEventID, ev.id);
        if (!knownTypes.has(ev.event_type)) {
          knownTypes.add(ev.event_type);
          const opt = document.createElement('option');
          opt.value = ev.event_type || '';
          opt.textContent = ev.event_type || '(untyped)';
          typeFilter.appendChild(opt);
        }
        if (wanted && ev.event_type !== wanted) continue;
        const cls = 'evt-' + (ev.event_type || 'untyped').replace(/[^a-z0-9]/gi, '-');
        const card = document.createElement('div');
        card.className = 'event ' + cls;
        card.innerHTML = `<div class="evt-head">
          #${ev.id} · <span class="evt-type">${esc(ev.event_type || '(untyped)')}</span>
          · ${esc(ev.timestamp)}
        </div><pre>${esc(JSON.stringify(ev.data, null, 2))}</pre>`;
        eventsBox.appendChild(card);
        rendered++;
      }
      if (autoscroll.checked) {
        window.scrollTo({ top: document.body.scrollHeight, behavior: 'auto' });
      }
    } catch (e) {
      markConn(false, e.message);
    }
  }

  typeFilter.addEventListener('change', () => {
    // Rebuild filtered view from cursor zero on filter change.
    eventsBox.innerHTML = '';
    detailRefresh.lastEventID = 0;
    rendered = 0;
    refreshEvents();
  });

  await refreshMeta();
  await refreshEvents();
  detailRefresh.id = setInterval(async () => {
    await refreshMeta();
    await refreshEvents();
  }, 2000);
}

// -- Router ------------------------------------------------------------
function stopRefreshers() {
  if (listRefresh.id) { clearInterval(listRefresh.id); listRefresh.id = null; }
  if (detailRefresh.id) { clearInterval(detailRefresh.id); detailRefresh.id = null; }
  detailRefresh.lastEventID = 0;
}

function render() {
  stopRefreshers();
  const p = window.location.pathname;
  if (p === '/' || p === '/index.html') {
    renderList();
    return;
  }
  const m = p.match(/^\/session\/([^/]+)\/?$/);
  if (m) {
    renderDetail(decodeURIComponent(m[1]));
    return;
  }
  app.innerHTML = `<section><h2>Not found</h2>
    <p><a href="/" data-route="/">Back to instance list</a></p></section>`;
}

document.addEventListener('click', (ev) => {
  const a = ev.target.closest('a[data-route]');
  if (!a) return;
  ev.preventDefault();
  history.pushState(null, '', a.getAttribute('href'));
  render();
});

window.addEventListener('popstate', render);

render();
