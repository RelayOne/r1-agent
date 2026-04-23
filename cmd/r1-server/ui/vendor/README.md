# r1-server UI vendor directory

This directory holds offline copies of the third-party ESM libraries the 3D
ledger visualiser (`cmd/r1-server/ui/graph.html`) depends on. Shipping the
libraries from disk (rather than fetching them from a CDN at page-load)
satisfies work-stoke TASK 15 AC #6 — "No CDN dependency — works offline".

## Why this directory is mostly empty in the repo

The library blobs are deliberately NOT committed to the Stoke source tree:

1. Three.js alone ships a ~600 KB minified ESM bundle; the full
   `3d-force-graph` stack (Three.js + three-spritetext + 3d-force-graph +
   d3-force-3d + OrbitControls) weighs in at ~1.6 MB minified.
2. Different operators have different redistribution / licence-audit
   requirements. Checking pre-built blobs into the source tree bypasses
   those checks.
3. The files can be regenerated from pinned versions with a single
   `npm install` step (see below), so the repo stays reproducible without
   the blobs.

At runtime, `cmd/r1-server/vendor_check.go` logs a WARNING on server
start-up if the expected files are missing, pointing operators here.

## Expected files and versions

The `graph.html` shell (once it is switched from CDN `<script>` tags to
`<script type="module">` imports) expects the following paths under this
directory:

| Path                            | Package                       | Pinned version |
| ------------------------------- | ----------------------------- | -------------- |
| `three.module.js`               | `three/build/three.module.js` | `0.160.0`      |
| `OrbitControls.js`              | `three/examples/jsm/controls/OrbitControls.js` | `0.160.0` |
| `three-spritetext.module.js`    | `three-spritetext`            | `1.8.2`        |
| `3d-force-graph.module.js`      | `3d-force-graph`              | `1.73.0`       |
| `d3-force-3d.module.js`         | `d3-force-3d`                 | `3.0.5`        |

The Go self-check only verifies `three.module.js`; it is the sentinel for
the whole set. If it is present the assumption is that the operator ran
the vendoring procedure below and the rest of the tree is intact.

## How to populate this directory

```bash
# In a scratch workspace (NOT inside the repo):
npm init -y
npm install three@0.160.0 three-spritetext@1.8.2 \
            3d-force-graph@1.73.0 d3-force-3d@3.0.5

# Copy the ESM entry-points here:
cp node_modules/three/build/three.module.js                   cmd/r1-server/ui/vendor/
cp node_modules/three/examples/jsm/controls/OrbitControls.js  cmd/r1-server/ui/vendor/
cp node_modules/three-spritetext/dist/three-spritetext.module.js \
   cmd/r1-server/ui/vendor/
cp node_modules/3d-force-graph/dist/3d-force-graph.module.js  cmd/r1-server/ui/vendor/
cp node_modules/d3-force-3d/dist/d3-force-3d.esm.js \
   cmd/r1-server/ui/vendor/d3-force-3d.module.js
```

After the files are in place, rebuild the server:

```bash
go build ./cmd/r1-server
```

The embed directive in `cmd/r1-server/ui.go` picks them up automatically
because `//go:embed ui/*` walks every file under `ui/`.

## Consuming the vendored modules from graph.html

Once the blobs are present, switch `graph.html`'s `<script src="...cdn...">`
tags to an `<script type="importmap">` block plus a module entry-point.
Sketch:

```html
<script type="importmap">
{
  "imports": {
    "three":           "/ui/vendor/three.module.js",
    "three-spritetext":"/ui/vendor/three-spritetext.module.js",
    "3d-force-graph":  "/ui/vendor/3d-force-graph.module.js",
    "d3-force-3d":     "/ui/vendor/d3-force-3d.module.js"
  }
}
</script>
<script type="module" src="/ui/graph.js"></script>
```

`graph.js` then swaps its implicit global usage (`window.THREE`,
`window.ForceGraph3D`) for explicit `import` statements at the top of the
file. This file-swap is deliberately left as a follow-up commit — mixing a
CDN → vendor cut-over with library-blob vendoring makes rollback noisy.

## Web-Worker layout

A sibling file `cmd/r1-server/ui/graph-worker.js` implements an off-main-
thread force-layout loop. It posts `{positions}` deltas back to
`graph.js` at the end of each simulation tick, freeing the main thread to
keep rendering at 60 fps on dense graphs (>2 k nodes). The worker
currently runs with a self-contained gravity + repulsion integrator;
swapping it for the `d3-force-3d` module is gated on the vendor files
above being in place.
