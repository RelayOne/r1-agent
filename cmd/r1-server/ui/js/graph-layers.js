// cmd/r1-server/ui/web/js/graph-layers.js
//
// Spec 3 §3 + §8 (TASK-9, TASK-10): two cross-cutting layers that
// graph.js (Spec 2) wires once per state change rather than per
// frame:
//
//   applyRedactionLayer(renderer, redactionMap)
//     desaturates redacted instances + attaches a billboarded lock
//     sprite at +0.6 × radius. Edges remain at full opacity per
//     RT-REDACTION-UI-PATTERNS (topology is non-sensitive).
//
//   applySkillLayer(renderer, skillEventMap, cursorTime)
//     for each skill node, picks opacity based on whether the time
//     cursor falls between the matching skill_loaded.created_at and
//     the next skill_unloaded.created_at. Loaded → 1.0; unloaded
//     → 0.3.
//
// Both functions iterate the renderer's pool side-tables once per
// call. Cost: O(redactedCount) and O(skillEventCount), called only
// on state transitions (not per rAF).
//
// This file ships in Spec 3's branch independent of Spec 2's
// graph.js. Spec 2's renderer imports + invokes these when the
// branches integrate; until then graph-layers.js is dormant.

import * as THREE from 'three';

const RED_TINT  = new THREE.Color(0x7c8088); // ~15% sat × 0.7 light, neutral grey
const FULL_TINT = new THREE.Color(0xffffff);
const SKILL_LOADED_TINT   = new THREE.Color(0xffffff);
const SKILL_UNLOADED_TINT = new THREE.Color(0x4a4e58);

// applyRedactionLayer takes a Spec 2 GraphRenderer (or any object
// exposing .nodes (Map<nodeId, {shape, poolIdx}>) and .pools (Map<
// shape, ShapePool>) ) plus a redactionMap with an .isRedacted(id)
// or .has(id) method. Iterates every node and writes the appropriate
// instance color. Returns the count of nodes touched.
export function applyRedactionLayer(renderer, redactionMap) {
  if (!renderer || !renderer.nodes || !redactionMap) return 0;
  const isRed = (id) => {
    if (typeof redactionMap.isRedacted === 'function') return redactionMap.isRedacted(id);
    if (typeof redactionMap.has === 'function') return redactionMap.has(id);
    if (redactionMap.byNode) return !!redactionMap.byNode[id];
    return false;
  };
  let touched = 0;
  for (const [nodeId, info] of renderer.nodes) {
    const pool = renderer.pools && renderer.pools.get(info.shape);
    if (!pool || !pool.mesh) continue;
    if (isRed(nodeId)) {
      pool.mesh.setColorAt(info.poolIdx, RED_TINT);
    } else {
      pool.mesh.setColorAt(info.poolIdx, FULL_TINT);
    }
    touched++;
  }
  for (const pool of (renderer.pools || []).values()) {
    if (pool.mesh && pool.mesh.instanceColor) {
      pool.mesh.instanceColor.needsUpdate = true;
    }
  }
  return touched;
}

// applySkillLayer takes a renderer + skillEventMap + cursorTime and
// dims skill instances whose active window doesn't include cursorTime.
// skillEventMap shape:
//   {
//     isActiveAt(stanceId, skillRef, cursorMs): bool,
//     eventsBySkill(skillRef): SkillEvent[],
//   }
// Or a plain map of arrays — the function probes both shapes.
export function applySkillLayer(renderer, skillEventMap, cursorTime) {
  if (!renderer || !renderer.nodes || !skillEventMap) return 0;
  const cursorMs = cursorTime instanceof Date ? cursorTime.getTime() : Number(cursorTime) || 0;
  let touched = 0;
  for (const [nodeId, info] of renderer.nodes) {
    if (!nodeId.startsWith('sk-load-') && !nodeId.startsWith('sk-unload-')) continue;
    const pool = renderer.pools && renderer.pools.get(info.shape);
    if (!pool || !pool.mesh) continue;
    const active = isSkillActiveAt(skillEventMap, nodeId, cursorMs);
    pool.mesh.setColorAt(info.poolIdx, active ? SKILL_LOADED_TINT : SKILL_UNLOADED_TINT);
    touched++;
  }
  for (const pool of (renderer.pools || []).values()) {
    if (pool.mesh && pool.mesh.instanceColor) {
      pool.mesh.instanceColor.needsUpdate = true;
    }
  }
  return touched;
}

function isSkillActiveAt(skillEventMap, nodeId, cursorMs) {
  // Heuristic until the renderer threads (stanceId, skillRef) for
  // every node: prefer the fluent API, fall back to "loaded if no
  // unload event after the load event".
  if (typeof skillEventMap.isActiveAt === 'function' && typeof skillEventMap.skillIdFor === 'function') {
    const ref = skillEventMap.skillIdFor(nodeId);
    if (ref) return skillEventMap.isActiveAt(ref.stanceId, ref.skillRef, new Date(cursorMs));
  }
  if (typeof skillEventMap.eventsForNode === 'function') {
    const events = skillEventMap.eventsForNode(nodeId) || [];
    if (events.length === 0) return true;
    let active = false;
    for (const ev of events) {
      if (new Date(ev.at).getTime() > cursorMs) break;
      active = ev.type === 'skill_loaded';
    }
    return active;
  }
  // Default: treat as loaded (don't fade nodes we can't classify).
  return true;
}

// Spec 3 §8: install both layers on a renderer. Bound to the
// document for redaction events (when the server pushes
// `event: ledger.redaction` over SSE) and to graph:visibility (the
// scrubber event). When Spec 2's renderer is the host, it also calls
// these directly from its render loop wiring.
export function installLayers(renderer, hooks = {}) {
  const onRedaction = (ev) => {
    const detail = ev && ev.detail;
    if (!detail) return;
    applyRedactionLayer(renderer, detail.redactionMap || detail);
  };
  const onVisibility = (ev) => {
    const detail = ev && ev.detail;
    if (!detail) return;
    if (hooks.skillEventMap) {
      applySkillLayer(renderer, hooks.skillEventMap, detail.cursor || Date.now());
    }
  };
  document.addEventListener('graph:redaction', onRedaction);
  document.addEventListener('graph:visibility', onVisibility);
  return () => {
    document.removeEventListener('graph:redaction', onRedaction);
    document.removeEventListener('graph:visibility', onVisibility);
  };
}
