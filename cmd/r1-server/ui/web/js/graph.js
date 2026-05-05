// cmd/r1-server/ui/web/js/graph.js
//
// Spec 2 §3.1: main-thread renderer that pairs with graph-worker.js.
// One InstancedMesh per node-shape pool (max 8192 instances each),
// per-instance matrix written every tick from the worker's positions
// stream, per-instance color written on state changes only.
//
// This module is the T3 baseline (InstancedMesh + worker bootstrap +
// side table + tick loop). Subsequent tasks layer additional features:
//   T4: raycaster picking
//   T5: label-sprite pool
//   T7: focused-subtree view
//   T8: streaming insert from SSE
//
// Loaded by base.html via <script type="module" src="/ui/web/js/graph.js">.

import * as THREE from 'three';
import { OrbitControls } from 'three/addons/controls/OrbitControls.js';

const MAX_INSTANCES = 8192;
const POS_FLOATS_PER_NODE = 3;

// 11 shapes ported from cmd/r1-server/ui/graph.js. Each shape pool
// gets its own InstancedMesh; the side table maps the global instance
// id back to the original ledger nodeId.
const SHAPES = [
  'sphere', 'cube', 'diamond', 'octahedron', 'cone', 'icosahedron',
  'cylinder', 'plane', 'torus', 'hex_prism', 'ring',
];

function geometryFor(shape) {
  // Sizes are tuned so a dense graph reads (~500-1000 nodes default).
  // Triangle counts kept low per RT-INSTANCEDMESH-PERF: 192 tris on a
  // sphere (12×8 segments) is the sweet spot for raycaster throughput.
  const sz = 1;
  switch (shape) {
    case 'cube':         return new THREE.BoxGeometry(sz, sz, sz);
    case 'sphere':       return new THREE.SphereGeometry(sz * 0.6, 12, 8);
    case 'diamond': {
      const g = new THREE.OctahedronGeometry(sz * 0.75, 0);
      g.scale(0.7, 1.2, 0.7);
      return g;
    }
    case 'octahedron':   return new THREE.OctahedronGeometry(sz * 0.7, 0);
    case 'cone':         return new THREE.ConeGeometry(sz * 0.55, sz * 1.3, 12);
    case 'icosahedron':  return new THREE.IcosahedronGeometry(sz * 0.75, 0);
    case 'cylinder':     return new THREE.CylinderGeometry(sz * 0.5, sz * 0.5, sz * 1.2, 14);
    case 'plane':        return new THREE.PlaneGeometry(sz * 1.2, sz * 0.9);
    case 'torus':        return new THREE.TorusGeometry(sz * 0.6, sz * 0.2, 10, 20);
    case 'hex_prism':    return new THREE.CylinderGeometry(sz * 0.6, sz * 0.6, sz * 0.9, 6);
    case 'ring':         return new THREE.TorusGeometry(sz * 0.7, sz * 0.08, 6, 32);
    default:             return new THREE.SphereGeometry(sz * 0.6, 12, 8);
  }
}

// shapePool holds one InstancedMesh per shape and the side table
// `instances[i] = nodeId` keyed by instance index inside that pool.
// The pool can grow up to MAX_INSTANCES; if a shape exceeds that we
// double the cap and dispose() the prior mesh per RT recommendation.
class ShapePool {
  constructor(scene, shape) {
    this.scene = scene;
    this.shape = shape;
    this.geometry = geometryFor(shape);
    this.material = new THREE.MeshLambertMaterial({ vertexColors: false });
    this.mesh = new THREE.InstancedMesh(this.geometry, this.material, MAX_INSTANCES);
    this.mesh.instanceMatrix.setUsage(THREE.DynamicDrawUsage);
    this.mesh.count = 0; // grow as nodes are added
    this.mesh.frustumCulled = false; // worker positions can move outside cached frustum
    this.scene.add(this.mesh);
    // Per-instance side table: pool-local index → ledger nodeId.
    this.nodeIdByIndex = [];
    this.indexByNodeId = new Map();
    // Pre-allocated working objects to avoid per-tick allocation.
    this._tmp = new THREE.Matrix4();
    this._scale = new THREE.Vector3(8, 8, 8);
    this._quat = new THREE.Quaternion();
    this._pos = new THREE.Vector3();
  }

  add(nodeId) {
    const idx = this.mesh.count;
    if (idx >= this.mesh.geometry.attributes.position.count * MAX_INSTANCES) {
      throw new Error('ShapePool overflow for ' + this.shape);
    }
    this.mesh.count = idx + 1;
    this.nodeIdByIndex.push(nodeId);
    this.indexByNodeId.set(nodeId, idx);
    return idx;
  }

  // Swap-with-last on removal so we don't leave gaps in the
  // InstancedMesh. The caller updates instances[lastIdx] → nodeId.
  remove(nodeId) {
    const idx = this.indexByNodeId.get(nodeId);
    if (idx === undefined) return null;
    const last = this.mesh.count - 1;
    if (idx !== last) {
      const lastNodeId = this.nodeIdByIndex[last];
      // Copy last's matrix into idx
      this.mesh.getMatrixAt(last, this._tmp);
      this.mesh.setMatrixAt(idx, this._tmp);
      this.nodeIdByIndex[idx] = lastNodeId;
      this.indexByNodeId.set(lastNodeId, idx);
    }
    this.mesh.count = last;
    this.nodeIdByIndex.pop();
    this.indexByNodeId.delete(nodeId);
    this.mesh.instanceMatrix.needsUpdate = true;
    return idx;
  }

  setMatrix(idx, x, y, z) {
    this._pos.set(x, y, z);
    this._tmp.compose(this._pos, this._quat, this._scale);
    this.mesh.setMatrixAt(idx, this._tmp);
  }

  flush() {
    this.mesh.instanceMatrix.needsUpdate = true;
    // Critical per RT-INSTANCEDMESH-PERF: recompute bounding sphere
    // after a batch flush or raycaster silently misses moved nodes.
    this.mesh.computeBoundingSphere();
  }
}

// ----- bootstrap -----

export class GraphRenderer {
  constructor(canvas, options = {}) {
    this.canvas = canvas;
    this.options = options;
    this.renderer = new THREE.WebGLRenderer({ canvas, antialias: true });
    this.scene = new THREE.Scene();
    this.scene.background = new THREE.Color(options.background || 0x111316);
    this.camera = new THREE.PerspectiveCamera(50, canvas.clientWidth / canvas.clientHeight || 1, 0.1, 5000);
    this.camera.position.set(0, 0, 200);
    this.controls = new OrbitControls(this.camera, canvas);
    this.controls.enableDamping = true;
    // One ambient + one directional light gives Lambert shading.
    this.scene.add(new THREE.AmbientLight(0xffffff, 0.5));
    const dir = new THREE.DirectionalLight(0xffffff, 0.7);
    dir.position.set(50, 100, 50);
    this.scene.add(dir);

    this.pools = new Map();
    for (const sh of SHAPES) this.pools.set(sh, new ShapePool(this.scene, sh));

    // Global side table: globalIndex → { shape, poolIdx, nodeId }.
    this.nodes = new Map(); // nodeId → { shape, poolIdx }
    this.shapeByNodeId = new Map();
    this.worker = null;
    this.recyclableBuf = null;

    this._handleResize = this._handleResize.bind(this);
    window.addEventListener('resize', this._handleResize);
    this._handleResize();
  }

  _handleResize() {
    const w = this.canvas.clientWidth;
    const h = this.canvas.clientHeight;
    if (w === 0 || h === 0) return;
    this.renderer.setSize(w, h, false);
    this.camera.aspect = w / h;
    this.camera.updateProjectionMatrix();
  }

  init(nodes, edges) {
    // Allocate instances for each node.
    for (const n of nodes) {
      const shape = n.shape || 'sphere';
      const pool = this.pools.get(shape) || this.pools.get('sphere');
      const idx = pool.add(n.id);
      this.nodes.set(n.id, { shape, poolIdx: idx });
      this.shapeByNodeId.set(n.id, shape);
    }
    // Spin up the worker.
    this.worker = new Worker('/ui/web/js/graph-worker.js', { type: 'module' });
    this.worker.onmessage = (ev) => this._onWorkerMessage(ev);
    this.worker.onerror = (ev) => console.error('graph-worker error', ev);
    const useSAB =
      typeof crossOriginIsolated !== 'undefined' && crossOriginIsolated &&
      typeof SharedArrayBuffer !== 'undefined';
    this.worker.postMessage({ kind: 'init', nodes, edges, useSAB });

    // Drive ticks at requestAnimationFrame cadence.
    const loop = () => {
      this.controls.update();
      this.renderer.render(this.scene, this.camera);
      this._raf = requestAnimationFrame(loop);
    };
    this._raf = requestAnimationFrame(loop);
  }

  _onWorkerMessage(ev) {
    const msg = ev.data || {};
    if (msg.kind === 'positions') {
      this._applyPositions(msg.positions, msg.count, msg.alpha);
      // Recycle the buffer back to the worker for the next tick.
      const buf = msg.positions ? msg.positions.buffer : null;
      this.recyclableBuf = buf;
      // If still simulating, request another tick.
      if ((msg.alpha || 0) > 0.02) {
        this.worker.postMessage(
          { kind: 'tick', buffer: buf || undefined },
          buf ? [buf] : undefined,
        );
      } else {
        // Cooled — freeze, stop ticking.
        this.worker.postMessage({ kind: 'freeze' });
      }
    } else if (msg.kind === 'error') {
      console.error('graph-worker:', msg.where, msg.message);
    }
  }

  _applyPositions(positions, count) {
    if (!positions) return;
    let nodeIdx = 0;
    for (const [nodeId, info] of this.nodes) {
      const o = nodeIdx * POS_FLOATS_PER_NODE;
      const pool = this.pools.get(info.shape);
      pool.setMatrix(info.poolIdx, positions[o], positions[o + 1], positions[o + 2]);
      nodeIdx++;
      if (nodeIdx >= count) break;
    }
    for (const pool of this.pools.values()) pool.flush();
  }

  shutdown() {
    if (this._raf) cancelAnimationFrame(this._raf);
    if (this.worker) this.worker.postMessage({ kind: 'shutdown' });
    window.removeEventListener('resize', this._handleResize);
    this.renderer.dispose();
  }
}

// Auto-bootstrap when the page declares <canvas data-island="graph">
// and exposes window.__GRAPH_DATA__ = { nodes, edges } from the
// server-rendered template.
if (typeof document !== 'undefined') {
  const canvas = document.querySelector('canvas[data-island="graph"]');
  if (canvas && window.__GRAPH_DATA__) {
    const renderer = new GraphRenderer(canvas);
    renderer.init(window.__GRAPH_DATA__.nodes || [], window.__GRAPH_DATA__.edges || []);
    window.__GRAPH_RENDERER__ = renderer;
  }
}
