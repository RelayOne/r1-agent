# Vendored frontend dependencies

The files in this directory are pinned, content-addressed copies of upstream npm
packages, fetched at developer/release time by `scripts/vendor-ui.sh` and
committed to the repo. The r1-server binary embeds them via `//go:embed`, so
production runtime never needs the npm registry, jsdelivr, or any other CDN.
Changes to these files are reviewed in the same PR that bumps the SRI table in
`scripts/vendor-ui.sh` and the equality assertions in `cmd/r1-server/sri_test.go`.

This pattern is **Strategy A** from `specs/research/raw/RT-VENDOR-SCRIPT-PATTERNS.md`
— curl + per-file SRI shell script + committed blobs. The decision rationale is
recorded in `docs/decisions/index.md` D-UI2-4.

## Pinned versions

| File | Upstream package | Version | Source URL | License |
|---|---|---|---|---|
| `htmx.min.js` | [htmx.org](https://github.com/bigskysoftware/htmx) | 2.0.4 | jsdelivr `htmx.org@2.0.4/dist/htmx.min.js` | BSD-2-Clause |
| `htmx-ext-sse.js` | [htmx-ext-sse](https://github.com/bigskysoftware/htmx-extensions) | 2.2.4 | jsdelivr `htmx-ext-sse@2.2.4/sse.js` | BSD-2-Clause |
| `three.module.js` | [three](https://github.com/mrdoob/three.js) | 0.170.0 | jsdelivr `three@0.170.0/build/three.module.min.js` | MIT |
| `three/addons/controls/OrbitControls.js` | [three](https://github.com/mrdoob/three.js) | 0.170.0 | jsdelivr `three@0.170.0/examples/jsm/controls/OrbitControls.js` | MIT |
| `three-spritetext.js` | [three-spritetext](https://github.com/vasturiano/three-spritetext) | 1.9.5 | jsdelivr `three-spritetext@1.9.5/dist/three-spritetext.mjs` | MIT |
| `3d-force-graph.js` | [3d-force-graph](https://github.com/vasturiano/3d-force-graph) | 1.77.0 | jsdelivr `3d-force-graph@1.77.0/dist/3d-force-graph.mjs` | MIT |
| `d3-force-3d.js` | [d3-force-3d](https://github.com/vasturiano/d3-force-3d) | 3.0.5 | jsdelivr `d3-force-3d@3.0.5/dist/d3-force-3d.js` | ISC |

`htmx-ext-sse` is paired with `htmx` per [htmx#3337](https://github.com/bigskysoftware/htmx/issues/3337);
when bumping htmx, check the extension's compatibility note before bumping it
independently.

`d3-force-3d` does not ship a single-file ESM build at this version (its
`module` field points at `src/index.js`, which imports the rest of `src/`). We
vendor the UMD build (`dist/d3-force-3d.js`) and the Spec 2 web worker wraps
it with a small ESM shim. If a future version ships a single-file ESM build,
update the URL in `vendor-ui.sh` to use it.

`three.module.js` is the **minified** ESM build (`three.module.min.js` upstream,
renamed locally for the import-map declaration in `cmd/r1-server/ui/web/partials/import-map.html`).
The unminified form is 1.3 MB and pushes the vendor tree past the 250 KB
gzipped budget; the minified form lands at 167 KB gzipped.

## Subresource Integrity hashes

Each blob's `sha384` is recorded in `scripts/vendor-ui.sh` (the `SRI` array)
and re-derived at test time by `cmd/r1-server/sri_test.go`. Format is the
openssl base64 form:

```
sha384-$(openssl dgst -sha384 -binary FILE | openssl base64 -A)
```

**Do not** use `sha384sum` — its hex output is wrong for HTML SRI.

## How to regenerate

```bash
# (Re-)fetch every blob, verify SRI on download, atomic-mv into place.
# A no-op if all versions and SRIs are unchanged.
bash scripts/vendor-ui.sh

# Verify on-disk blobs against the SRI table without hitting the network.
# This is what CI runs.
bash scripts/vendor-ui.sh --check
```

## Bumping a pinned version

1. Edit `URL[<filename>]` in `scripts/vendor-ui.sh` to the new release URL.
2. Run `bash scripts/vendor-ui.sh`. The first run will fail on SRI mismatch;
   read the printed `got: sha384-...` line and copy it into the corresponding
   `SRI[<filename>]` entry.
3. Re-run `bash scripts/vendor-ui.sh` — should succeed.
4. Run `go test ./cmd/r1-server/...` — `sri_test.go` will pass once the table
   matches the on-disk content.
5. Update this README's version table.
6. Commit script + blob + README + sri_test changes together.

The atomic-`mv`-on-SRI-match flow guarantees the on-disk vendor tree is never
left in a partial state if the SRI check fails midway.

## Total budget

The vendor tree is gated by `cmd/r1-server/vendor_size_test.go` (added by
Spec 5) at ≤ 250 KB gzipped total. Current usage:

| File | Raw | Gzipped |
|---|---|---|
| `htmx.min.js` | 50K | 16K |
| `htmx-ext-sse.js` | 9K | 3K |
| `three.module.js` | 676K | 168K |
| `three/addons/controls/OrbitControls.js` | 32K | 7K |
| `three-spritetext.js` | 16K | 4K |
| `3d-force-graph.js` | 21K | 6K |
| `d3-force-3d.js` | 24K | 5K |
| **Total** | **828K** | **210K** |

(Numbers are approximate; CI test is the source of truth.)
