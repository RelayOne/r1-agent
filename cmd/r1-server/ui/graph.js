// r1-server — 3D ledger visualizer (RS-4 item 20).
//
// Loads the ledger DAG from /api/session/:id/ledger and renders it
// via 3d-force-graph (which internally uses Three.js + forcegraph).
// Node geometry / colour map, edge line styles, and all interactions
// (click / hover / filter / search / time-scrubber / mission cluster)
// live in this single file. No framework — just the exported
// ForceGraph3D global from the CDN script and vanilla DOM.
//
// Architecture:
//   detectWebGL  — feature-test; on failure we bail to the 2D banner
//   loadData     — fetch ledger, parse, derive helpers (byId, types)
//   styleMap     — declarative table of per-node-type shape + colour
//                 and per-edge-type line style
//   buildScene   — construct the ForceGraph3D instance
//   buildUI      — wire filter / search / scrubber / panel / tooltip
//   applyView    — recompute displayed nodes/edges from current UI
//                 state (filter + search + scrubber) and hand them
//                 to the forcegraph
//
// Only modern browsers (ES2020+). We intentionally avoid transpile
// tooling — this file is delivered verbatim from the embed.FS.

(function () {
  'use strict';

  // --- state ----------------------------------------------------------
  const state = {
    instanceID: null,
    nodes: [],       // full, unfiltered list (ordered by created_at)
    edges: [],       // full list
    byId: new Map(), // id -> node
    byType: new Map(), // type -> count
    selectedTypes: new Set(), // empty = all
    search: '',
    scrubMax: 0,     // index inclusive
    cluster: true,   // cluster by mission_id
    graph: null,     // ForceGraph3D instance
    panel: null,
    tooltipEl: null,
    sessionMeta: null,
  };

  // --- small helpers --------------------------------------------------
  function $(sel) { return document.querySelector(sel); }

  function esc(s) {
    return String(s == null ? '' : s).replace(
      /[&<>"]/g,
      (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c])
    );
  }

  function parseSessionID() {
    const m = window.location.pathname.match(/^\/session\/([^/]+)\/graph\/?$/);
    return m ? decodeURIComponent(m[1]) : null;
  }

  function setConn(ok, text) {
    const el = $('#conn-status');
    if (!el) return;
    el.textContent = text;
    el.className = ok ? 'ok' : 'err';
  }

  // --- WebGL detect ---------------------------------------------------
  // We probe once on boot. 3d-force-graph does its own WebGLRenderer
  // construction but swallows failures silently; we want a clear
  // fallback path so the user knows what's happening.
  function detectWebGL() {
    try {
      const canvas = document.createElement('canvas');
      const gl = canvas.getContext('webgl2') || canvas.getContext('webgl');
      if (!gl) return { ok: false, reason: 'No WebGL context available.' };
      if (!window.ForceGraph3D) {
        return {
          ok: false,
          reason: 'Three.js / 3d-force-graph failed to load from the CDN. ' +
                  'Check your network or refresh.',
        };
      }
      return { ok: true };
    } catch (e) {
      return { ok: false, reason: 'WebGL probe threw: ' + e.message };
    }
  }

  function showFallback(reason) {
    const fb = $('#fallback');
    const graphDiv = $('#graph');
    const reasonEl = $('#fallback-reason');
    if (reasonEl) reasonEl.textContent = reason;
    if (fb) fb.hidden = false;
    if (graphDiv) graphDiv.style.display = 'none';
  }

  // --- style map -----------------------------------------------------
  // Maps ledger node_type -> Three.js geometry factory + colour.
  // Geometry sizes are tuned so a dense graph (~500 nodes) still
  // reads — sphere is the reference at r=4.
  const NODE_STYLES = {
    task: { color: '#4a90e2', shape: 'cube', size: 6 },
    decision_internal: { color: '#9b7edb', shape: 'sphere', size: 4 },
    decision_repo: { color: '#7b4eb0', shape: 'sphere', size: 6 },
    verification_evidence: { color: '#7bd88f', shape: 'diamond', size: 5 },
    verification_evidence_pass: { color: '#7bd88f', shape: 'diamond', size: 5 },
    verification_evidence_fail: { color: '#e6634f', shape: 'diamond', size: 5 },
    hitl_request: { color: '#e6a55c', shape: 'octahedron', size: 5, filled: false },
    hitl_response: { color: '#e6a55c', shape: 'octahedron', size: 5, filled: true },
    escalation: { color: '#e6634f', shape: 'cone', size: 6 },
    judge_verdict: { color: '#e6c34f', shape: 'icosahedron', size: 6 },
    research_request: { color: '#4ec3c3', shape: 'cylinder', size: 5, filled: false },
    research_report: { color: '#4ec3c3', shape: 'cylinder', size: 5, filled: true },
    agree: { color: '#7bd88f', shape: 'sphere', size: 2.5 },
    dissent: { color: '#e6634f', shape: 'sphere', size: 2.5 },
    draft: { color: '#8a91a3', shape: 'plane', size: 6 },
    loop: { color: '#e6e05c', shape: 'torus', size: 5 },
    skill: { color: '#4ecfd8', shape: 'hex_prism', size: 5 },
    supervisor_state_checkpoint: { color: '#e6e8ef', shape: 'ring', size: 5 },
  };

  const DEFAULT_STYLE = { color: '#8a91a3', shape: 'sphere', size: 3 };

  // For verification nodes the pass/fail colour comes from the raw
  // payload, not the node_type alone. This normaliser returns an
  // adjusted type key that NODE_STYLES already has an entry for.
  function normalizeType(node) {
    if (node.type === 'verification_evidence') {
      try {
        const raw = node.raw && JSON.parse(node.raw);
        if (raw && (raw.result === 'fail' || raw.passed === false)) {
          return 'verification_evidence_fail';
        }
      } catch (_) { /* ignore */ }
      return 'verification_evidence_pass';
    }
    return node.type;
  }

  function styleFor(node) {
    return NODE_STYLES[normalizeType(node)] || DEFAULT_STYLE;
  }

  // Edge styling — 3d-force-graph doesn't expose dashed WebGL lines
  // cheaply (they require custom shader mats), so we approximate with
  // colour + width + particle direction. A "dashed" link gets a
  // particle stream; a "zigzag" falls back to red particles with
  // wider separation.
  const EDGE_STYLES = {
    supersedes: { color: '#cfd2d9', width: 1.6, particles: 0, dashed: false },
    depends_on: { color: '#6aa3e6', width: 1.2, particles: 4, dashed: true },
    contradicts: { color: '#e6634f', width: 1.4, particles: 6, dashed: true },
    extends:    { color: '#7bd88f', width: 1.3, particles: 0, dashed: false },
    references: { color: '#b6b9c3', width: 0.8, particles: 2, dashed: true },
    resolves:   { color: '#e6c34f', width: 2.6, particles: 0, dashed: false },
    distills:   { color: '#9b7edb', width: 0.9, particles: 0, dashed: false },
  };

  const DEFAULT_EDGE = { color: '#666', width: 1, particles: 0, dashed: false };

  function edgeStyle(type) { return EDGE_STYLES[type] || DEFAULT_EDGE; }

  // --- geometry factories --------------------------------------------
  // Each returns a fresh THREE.Mesh. We reuse materials per colour to
  // avoid GPU material blowup on large graphs.
  const MAT_CACHE = new Map();
  function mat(color, filled = true) {
    const key = color + (filled ? ':filled' : ':wire');
    let m = MAT_CACHE.get(key);
    if (m) return m;
    m = new THREE.MeshLambertMaterial({
      color,
      wireframe: !filled,
      transparent: true,
      opacity: filled ? 0.9 : 0.85,
    });
    MAT_CACHE.set(key, m);
    return m;
  }

  function makeMesh(node) {
    const s = styleFor(node);
    const c = s.color;
    const sz = s.size;
    const filled = s.filled !== false;
    let geom;
    switch (s.shape) {
      case 'cube': geom = new THREE.BoxGeometry(sz, sz, sz); break;
      case 'sphere': geom = new THREE.SphereGeometry(sz * 0.6, 16, 12); break;
      case 'diamond': {
        // Bipyramid via OctahedronGeometry; stretched so it reads as
        // a diamond rather than a generic d8.
        geom = new THREE.OctahedronGeometry(sz * 0.75, 0);
        geom.scale(1, 1.5, 1);
        break;
      }
      case 'octahedron': geom = new THREE.OctahedronGeometry(sz * 0.7, 0); break;
      case 'cone': geom = new THREE.ConeGeometry(sz * 0.55, sz * 1.3, 12); break;
      case 'icosahedron': geom = new THREE.IcosahedronGeometry(sz * 0.75, 0); break;
      case 'cylinder': geom = new THREE.CylinderGeometry(sz * 0.5, sz * 0.5, sz * 1.2, 14); break;
      case 'plane': geom = new THREE.PlaneGeometry(sz * 1.2, sz * 0.9); break;
      case 'torus': geom = new THREE.TorusGeometry(sz * 0.6, sz * 0.2, 10, 20); break;
      case 'hex_prism': geom = new THREE.CylinderGeometry(sz * 0.6, sz * 0.6, sz * 0.9, 6); break;
      case 'ring': geom = new THREE.TorusGeometry(sz * 0.7, sz * 0.08, 6, 32); break;
      default: geom = new THREE.SphereGeometry(sz * 0.6, 12, 8);
    }
    const mesh = new THREE.Mesh(geom, mat(c, filled));
    // PlaneGeometry defaults to xy-plane; rotate so it's visible from
    // the default camera orbit instead of edge-on.
    if (s.shape === 'plane') mesh.rotation.x = -Math.PI / 2;
    return mesh;
  }

  // --- data loading --------------------------------------------------
  async function loadData() {
    const id = state.instanceID;
    const r = await fetch(`/api/session/${encodeURIComponent(id)}/ledger`, {
      headers: { 'Accept': 'application/json' },
    });
    if (!r.ok) throw new Error(`ledger fetch ${r.status}`);
    const snap = await r.json();
    state.nodes = (snap.nodes || []).map((n, i) => {
      // 3d-force-graph wants plain objects; we graft display helpers.
      const rawStr = typeof n.raw === 'string' ? n.raw : JSON.stringify(n.raw);
      return {
        id: n.id,
        type: n.type || 'unknown',
        mission_id: n.mission_id || '',
        created_at: n.created_at || '',
        created_by: n.created_by || '',
        parent_hash: n.parent_hash || '',
        raw: rawStr,
        _order: i,
        _searchBlob: (rawStr + ' ' + n.type + ' ' + (n.created_by || '')).toLowerCase(),
      };
    });
    state.edges = (snap.edges || []).map((e) => ({
      id: e.id,
      source: e.from,
      target: e.to,
      type: e.type || '',
    }));
    state.byId = new Map(state.nodes.map((n) => [n.id, n]));
    state.byType = new Map();
    for (const n of state.nodes) {
      state.byType.set(n.type, (state.byType.get(n.type) || 0) + 1);
    }
    state.scrubMax = state.nodes.length;
  }

  async function loadSessionMeta() {
    try {
      const r = await fetch(`/api/session/${encodeURIComponent(state.instanceID)}`);
      if (r.ok) state.sessionMeta = await r.json();
    } catch (_) { /* non-fatal */ }
  }

  // --- scene ---------------------------------------------------------
  function buildScene() {
    const container = $('#graph');
    // 3d-force-graph exports a factory function.
    state.graph = ForceGraph3D()(container)
      .backgroundColor('#0f1115')
      .nodeThreeObject(makeMesh)
      .nodeLabel(() => '')        // we do our own tooltip
      .linkColor((l) => edgeStyle(l.type).color)
      .linkWidth((l) => edgeStyle(l.type).width)
      .linkOpacity(0.7)
      .linkDirectionalArrowLength((l) => (l.type === 'supersedes' ? 4 : 2))
      .linkDirectionalArrowRelPos(0.95)
      .linkDirectionalParticles((l) => edgeStyle(l.type).particles)
      .linkDirectionalParticleSpeed(0.006)
      .linkDirectionalParticleWidth(1.6)
      .onNodeClick(openPanel)
      .onNodeHover(onHover)
      .onBackgroundClick(closePanel);

    // Cluster-by-mission nudges: add a weak radial force per mission.
    // 3d-force-graph accepts custom d3 forces via .d3Force.
    state.graph.d3Force('charge').strength(-60);
    applyClusterForce();

    window.addEventListener('resize', () => {
      state.graph
        .width(container.clientWidth)
        .height(container.clientHeight);
    });
  }

  function applyClusterForce() {
    if (!state.graph) return;
    // d3Force is provided by 3d-force-graph itself, not a separate
    // d3 global — it delegates to the internal d3-force simulation.
    // Remove any prior custom force, then re-add if clustering is on.
    state.graph.d3Force('cluster', null);
    if (!state.cluster) return;
    // Build mission -> anchor position (points on a circle in xz).
    const missions = Array.from(new Set(state.nodes.map((n) => n.mission_id).filter(Boolean)));
    if (missions.length < 2) return;
    const anchors = new Map();
    const R = 300;
    missions.forEach((m, i) => {
      const t = (i / missions.length) * Math.PI * 2;
      anchors.set(m, { x: Math.cos(t) * R, y: 0, z: Math.sin(t) * R });
    });
    // Custom force: nudge each node toward its mission anchor.
    const force = (alpha) => {
      for (const n of state.nodes) {
        const a = anchors.get(n.mission_id);
        if (!a) continue;
        const k = 0.04 * alpha;
        n.vx = (n.vx || 0) + (a.x - (n.x || 0)) * k;
        n.vy = (n.vy || 0) + (a.y - (n.y || 0)) * k;
        n.vz = (n.vz || 0) + (a.z - (n.z || 0)) * k;
      }
    };
    force.initialize = () => {};
    state.graph.d3Force('cluster', force);
  }

  // --- tooltip + side panel -----------------------------------------
  function onHover(node) {
    const tip = state.tooltipEl;
    if (!node) { tip.hidden = true; return; }
    tip.innerHTML = `
      <div class="tt-type">${esc(node.type)}</div>
      <div class="tt-meta">${esc(node.id.slice(0, 16))}</div>
      <div class="tt-meta">created_at: ${esc(node.created_at)}</div>
      <div class="tt-meta">created_by: ${esc(node.created_by || '—')}</div>
      ${node.mission_id ? `<div class="tt-meta">mission: ${esc(node.mission_id)}</div>` : ''}
    `;
    tip.hidden = false;
  }

  function positionTooltip(ev) {
    const tip = state.tooltipEl;
    if (!tip || tip.hidden) return;
    const pad = 14;
    let x = ev.clientX + pad;
    let y = ev.clientY + pad;
    const rect = tip.getBoundingClientRect();
    if (x + rect.width > window.innerWidth) x = ev.clientX - rect.width - pad;
    if (y + rect.height > window.innerHeight) y = ev.clientY - rect.height - pad;
    tip.style.left = x + 'px';
    tip.style.top = y + 'px';
  }

  function openPanel(node) {
    if (!node) return;
    const panel = state.panel;
    const detail = $('#node-detail');
    const rawEl = $('#node-raw');
    let prettyRaw = node.raw;
    try { prettyRaw = JSON.stringify(JSON.parse(node.raw), null, 2); } catch (_) { /* leave */ }
    detail.innerHTML = `
      <dt>ID</dt><dd><code>${esc(node.id)}</code></dd>
      <dt>Type</dt><dd>${esc(node.type)}</dd>
      <dt>Mission</dt><dd>${esc(node.mission_id || '—')}</dd>
      <dt>Created</dt><dd>${esc(node.created_at || '—')}</dd>
      <dt>By</dt><dd>${esc(node.created_by || '—')}</dd>
      <dt>Parent</dt><dd><code>${esc(node.parent_hash || '—')}</code></dd>
    `;
    rawEl.textContent = prettyRaw;
    panel.hidden = false;
  }

  function closePanel() {
    if (state.panel) state.panel.hidden = true;
  }

  // --- filter / search / scrubber UI --------------------------------
  function buildUI() {
    state.panel = $('#sidepanel');
    state.tooltipEl = $('#tooltip');
    $('#close-panel').addEventListener('click', closePanel);
    window.addEventListener('mousemove', positionTooltip);

    // Header + stream link
    $('#sess-id').textContent = state.instanceID;
    const streamURL = `/session/${encodeURIComponent(state.instanceID)}`;
    $('#stream-link').href = streamURL;
    const fbStream = $('#fallback-stream');
    if (fbStream) fbStream.href = streamURL;

    // Filter checkboxes — one per observed type, ordered by frequency.
    const filterBox = $('#type-filter');
    const types = Array.from(state.byType.entries()).sort((a, b) => b[1] - a[1]);
    state.selectedTypes = new Set(types.map(([t]) => t));
    filterBox.innerHTML = types.map(([t, count]) => {
      const swatch = (NODE_STYLES[t] || DEFAULT_STYLE).color;
      return `<label>
        <input type="checkbox" data-type="${esc(t)}" checked>
        <span class="swatch" style="background:${esc(swatch)}"></span>
        ${esc(t)} <span style="color:var(--fg-muted)">(${count})</span>
      </label>`;
    }).join('');
    filterBox.addEventListener('change', (ev) => {
      const cb = ev.target;
      if (!cb || cb.tagName !== 'INPUT') return;
      const t = cb.getAttribute('data-type');
      if (cb.checked) state.selectedTypes.add(t);
      else state.selectedTypes.delete(t);
      applyView();
    });

    // Search
    const search = $('#search-box');
    let searchTimer = null;
    search.addEventListener('input', () => {
      clearTimeout(searchTimer);
      searchTimer = setTimeout(() => {
        state.search = search.value.trim().toLowerCase();
        applyView();
      }, 120);
    });

    // Scrubber — 0..N (N = total nodes). Store as a percentage of N
    // so the <input type="range"> resolution is stable for any size.
    const scrubber = $('#time-scrubber');
    const label = $('#time-label');
    scrubber.addEventListener('input', () => {
      const pct = Number(scrubber.value);
      state.scrubMax = Math.max(0, Math.round(state.nodes.length * (pct / 100)));
      label.textContent = pct + '%';
      applyView();
    });

    // Cluster toggle
    $('#cluster-by-mission').addEventListener('change', (ev) => {
      state.cluster = !!ev.target.checked;
      applyClusterForce();
      // Kick the simulation so the cluster force actually moves things.
      if (state.graph) state.graph.d3ReheatSimulation();
    });

    // Reset camera
    $('#reset-cam').addEventListener('click', () => {
      if (state.graph) state.graph.zoomToFit(800);
    });
  }

  // applyView recomputes the set of visible nodes + edges from the
  // current filter/search/scrubber state and hands them to the
  // forcegraph. O(N+E) per call; fine for ledgers under ~10k nodes.
  function applyView() {
    const visible = new Set();
    for (let i = 0; i < Math.min(state.scrubMax, state.nodes.length); i++) {
      const n = state.nodes[i];
      if (!state.selectedTypes.has(n.type)) continue;
      if (state.search && !n._searchBlob.includes(state.search)) continue;
      visible.add(n.id);
    }
    const nodes = state.nodes.filter((n) => visible.has(n.id));
    const edges = state.edges.filter((e) => {
      const from = typeof e.source === 'object' ? e.source.id : e.source;
      const to = typeof e.target === 'object' ? e.target.id : e.target;
      return visible.has(from) && visible.has(to);
    });
    if (state.graph) {
      state.graph.graphData({ nodes, links: edges });
    }
  }

  // --- boot ----------------------------------------------------------
  async function boot() {
    state.instanceID = parseSessionID();
    if (!state.instanceID) {
      showFallback('No session ID in URL — expected /session/:id/graph.');
      return;
    }
    $('#sess-id').textContent = state.instanceID;

    const webgl = detectWebGL();
    if (!webgl.ok) {
      $('#stream-link').href = `/session/${encodeURIComponent(state.instanceID)}`;
      const fbStream = $('#fallback-stream');
      if (fbStream) fbStream.href = `/session/${encodeURIComponent(state.instanceID)}`;
      showFallback(webgl.reason);
      return;
    }

    setConn(true, 'loading…');
    try {
      await Promise.all([loadData(), loadSessionMeta()]);
    } catch (e) {
      setConn(false, 'ledger load failed: ' + e.message);
      showFallback('Failed to load ledger: ' + e.message);
      return;
    }
    if (!state.nodes.length) {
      setConn(true, 'empty ledger');
    } else {
      setConn(true, `${state.nodes.length} node${state.nodes.length === 1 ? '' : 's'} · ` +
        `${state.edges.length} edge${state.edges.length === 1 ? '' : 's'}`);
    }

    buildScene();
    buildUI();
    applyView();
    // Fit camera once layout has settled a touch.
    setTimeout(() => { if (state.graph) state.graph.zoomToFit(600); }, 1200);
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', boot);
  } else {
    boot();
  }
})();
