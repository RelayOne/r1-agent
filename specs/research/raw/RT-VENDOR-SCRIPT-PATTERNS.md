# RT-VENDOR-SCRIPT-PATTERNS — Build-time JS dependency vendoring for the air-gapped Go server

Status: research, raw notes
Date: 2026-05-05
Scope: inputs for `r1-server-ui-v2.md` §"embed.FS vendor files + vendor script". Target tree: `cmd/r1-server/ui/vendor/` (already contains `three.min.js`, `three-spritetext.min.js`, `3d-force-graph.min.js`; need to add `htmx.min.js`, `htmx-ext-sse.js`, optionally three-instancedmesh helpers). Goal: a one-time vendor procedure that pulls pinned versions, verifies integrity, and writes blobs the Go binary embeds at compile time. Runtime must never reach a CDN.

---

## Topic

Pattern + script for "vendor at build time, embed at compile time" of a small set of pinned JS chrome assets for a Go binary that uses `embed.FS`. Sub-questions:

1. Which of {curl + SRI shell script, npm install + copy, git submodule, esbuild bundle} fits a Go-binary embed.FS workflow best in 2026.
2. How to verify SRI hashes from a shell script (W3C SRI-2 base64 form, openssl recipe).
3. CDN URL stability: unpkg vs jsdelivr vs upstream GitHub release tarball, given an outage history reference (unpkg was down 18h in March 2025 [BlazingCDN]).
4. Whether the procedure can be run **once on a connected machine, blobs committed**, and CI never reaches the network.
5. Fitting the ≤250 KB gzipped budget for chrome assets (note: r1-server already vendors `three.min.js` ~660 KB un-gzipped → ~170 KB gzipped, plus 3d-force-graph ~660 KB un-gzipped — these are visualiser-only and exempt from the chrome budget; chrome here means htmx + htmx-ext-sse only).
6. Idempotency — re-running with the same pin set produces no diff.

---

## Strategies considered

| Strategy | Network at build (vendor step) | Network at runtime | SRI verification | Idempotent re-run | Fits Go embed.FS | Byte budget control | Notes |
|---|---|---|---|---|---|---|---|
| **A. Shell script with `curl` + per-file SRI check** | yes, vendor step only | **no** | yes, explicit `openssl dgst -sha384 -binary \| openssl base64 -A` compare | yes if pin map is the source of truth and script is `set -euo pipefail` with mtime/byte-size short-circuit | yes — writes plain files into `cmd/r1-server/ui/vendor/`, picked up by `//go:embed` | precise — one pin per file, no transitive bloat | Smallest moving parts. No `node_modules`. Matches existing layout. **Recommended.** |
| **B. `npm install` + `cp` from `node_modules/`** | yes, vendor step only | no | indirect — npm's `package-lock.json` integrity field is verified by npm, then we trust the copy step | partial — `npm install` is mostly idempotent with a lockfile, but `cp` can shuffle whitespace/perms; lockfile churn between npm versions | yes, but requires Node toolchain on the vendor machine | poor — pulls full package trees including non-dist files; needs explicit allowlist of files to copy | What the existing `vendor/README.md` proposes for the visualiser stack. Heavier toolchain footprint than (A). |
| **C. `git submodule` per upstream repo** | yes, on `git submodule update --init` | no | none built-in; would need a post-checkout SRI script anyway | yes, pinned by commit SHA | awkward — submodules pull entire upstream repos (Mb of source + tests), then we still cherry-pick `dist/*.min.js` | terrible — full repo per dep | Rejected: oversized clones, devs have to remember `--recurse-submodules`, no SRI semantics. |
| **D. `esbuild`/`bun build` bundle from npm sources** | yes, requires Node/Bun | no | none for the bundle as a whole; can hash the output but loses upstream-author signature | depends on bundler determinism (esbuild ≥0.20 is byte-stable for the same inputs; minor version bumps re-shape output) | yes — emits one file we embed | best — tree-shakes unused exports | Heaviest toolchain. Output cannot be cross-checked against an upstream-published SRI value, so we lose a real-world supply-chain check. Right answer if we end up writing custom JS; over-engineered for chrome of 4–5 pinned vendor blobs. |

Conclusion: **Strategy A** wins on "least machinery, real SRI, cleanest air-gap story". The visualiser README's `npm install` recipe (Strategy B) stays valid for the heavy three.js stack because those packages don't publish per-file SRI on jsdelivr in a stable place — but for the chrome additions (htmx, htmx-ext-sse) Strategy A is cleaner and the upstream htmx project publishes SRI hashes in its release announcements. We can mix: A for files that have published SRI, B as a fallback for those that don't.

---

## SRI mechanics (W3C SRI-2, MDN)

- Format: `<algo>-<base64(<digest>)>` where `<algo>` ∈ `sha256 | sha384 | sha512`. Multiple hashes can be space-separated; the strongest one the browser supports wins. [W3C SRI-2, MDN]
- **Base64, not hex.** `sha384sum file.js` prints hex — wrong format for SRI. Correct generation:
  ```bash
  openssl dgst -sha384 -binary file.js | openssl base64 -A
  ```
  And to emit the full SRI string in one line:
  ```bash
  printf 'sha384-%s\n' "$(openssl dgst -sha384 -binary file.js | openssl base64 -A)"
  ```
  [Transloadit, Userpilot KB, MDN]
- `sha384` is the MDN/W3C-recommended default (collision margin + size balance). [MDN]
- For shell verification: capture expected hash in a manifest, compute observed hash, fail with `exit 1` on mismatch. **Do not** parse hex `sha384sum` output and try to convert — encode/decode mismatches are a known footgun. Stick with `openssl`.
- Reproducibility: SRI hashes are stable for a given immutable file. unpkg/jsdelivr serve immutable per-version URLs (`@2.0.4`, never `@latest`), so once captured, the hash never changes. If it does, that's a tampering signal — exit non-zero.

---

## CDN URL stability

- **Pin to a version tag, never to a major or to `latest`.** The htmx-ext-sse PR threads explicitly call out that `@latest` URLs flap when extensions are split into separate npm packages. [bigskysoftware/htmx#3337, htmx 2.0 blog]
- **jsdelivr > unpkg for stability.** unpkg had an 18-hour outage in March 2025 and is single-network; jsdelivr is multi-CDN with sub-50 ms p50 and >99.99 % uptime. freeCodeCamp migrated unpkg → jsdelivr for this reason. [BlazingCDN, freeCodeCamp PR #59291]
- **For long-term archival, prefer the upstream GitHub release tarball** (`https://github.com/<org>/<repo>/releases/download/v<ver>/<artifact>`). These are immutable, signed by the GitHub release pipeline, and survive npm registry churn. Fallback to jsdelivr (`https://cdn.jsdelivr.net/npm/<pkg>@<ver>/dist/<file>`), then unpkg.
- Concretely for our four files: htmx releases publish `htmx.min.js` + SRI in the release notes ("htmx 2.0.0 has been released" template) — use the GitHub release URL as primary. three-spritetext / 3d-force-graph publish only via npm — use jsdelivr as primary.

---

## Air-gapped CI: vendor once, commit, never re-fetch

Yes, this is the right model.

- The vendor script runs on a **connected developer machine** (or a one-shot connected bastion in regulated environments) and writes blobs into `cmd/r1-server/ui/vendor/`.
- Those blobs **are committed to the repo**. The existing `vendor/README.md` argued against committing because of size; for chrome (htmx + htmx-ext-sse, ~50 KB combined) that argument doesn't apply. For the heavy three.js stack the existing `vendor_check.go` "log a WARNING if missing" pattern stays, but we should commit the chrome files.
- CI (`go build ./cmd/r1`) runs **without network** and only consumes the embedded blobs. `//go:embed ui/*` walks the tree at compile time; missing files cause a clear compile error, not a runtime regression.
- The pin manifest (`tools/vendor-js/manifest.txt` or inline in the script) is the authority. CI can run the script in `--check` mode (no downloads, just verify SRI of on-disk files against manifest) as a guard against tampering. This is the pattern from "Idempotent shell scripts" [arslan.io, metaist/idempotent-bash].
- Downside of committing: license audit. Mitigation: include upstream `LICENSE` text in the same directory, and document each blob's provenance in `vendor/MANIFEST.md`.

---

## Byte-budget analysis (chrome only)

Spec says ≤250 KB gzipped for chrome. Per-file rough numbers for the additions:

| File | Pinned ver | Min size | Gzip est. |
|---|---|---|---|
| `htmx.min.js` | 2.0.4 | ~50 KB | ~17 KB |
| `htmx-ext-sse.js` (min) | 2.2.4 | ~3 KB | ~1.5 KB |
| **chrome subtotal** | | **~53 KB** | **~18.5 KB** |

Comfortably under 250 KB gzipped. Headroom to add `htmx-ext-response-targets`, `htmx-ext-loading-states`, or Alpine.js without revisiting the budget.

(For reference, the visualiser stack `three.min.js` + `3d-force-graph.min.js` + `three-spritetext.min.js` is ~1.34 MB un-gzipped / ~370 KB gzipped. Spec correctly classifies this as visualiser-only, separately budgeted.)

---

## Idempotency design

Three rules, lifted from arslan.io and metaist/idempotent-bash:

1. **Manifest is the source of truth.** A flat text file with `<filename> <url> <sri>` per line. The script reads it; it does not contain version numbers in shell logic.
2. **Short-circuit on hash match.** For each entry: if the file exists AND its SRI matches the manifest, skip download. Otherwise download to a temp file, verify SRI, atomically `mv` into place.
3. **Atomic writes only.** `curl -fsSL --output "$tmp"` then `mv "$tmp" "$dest"`. Never write directly to `$dest` — a partial write on Ctrl-C leaves corrupt content that passes a "file exists" check on re-run. [Lloyd Atkinson, arslan.io]

Re-running with no manifest changes produces zero diffs (no mtime touch, no byte change). Bumping a pin: change the manifest line, re-run, single-file diff lands.

---

## Recommendation

A bash script `tools/vendor-js/vendor.sh` driven by a manifest, invoked manually by maintainers and in `--check` mode by CI.

```bash
#!/usr/bin/env bash
# tools/vendor-js/vendor.sh
# Vendor pinned JS chrome into cmd/r1-server/ui/vendor/ with SRI verification.
# Idempotent: re-running with unchanged manifest produces no diff.
# Usage:
#   tools/vendor-js/vendor.sh           # download missing/changed files
#   tools/vendor-js/vendor.sh --check   # verify on-disk SRI, no network (CI mode)

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
DEST="$ROOT/cmd/r1-server/ui/vendor"
MANIFEST="$ROOT/tools/vendor-js/manifest.txt"
MODE="${1:-fetch}"

mkdir -p "$DEST"

sri_of() {
  printf 'sha384-%s' "$(openssl dgst -sha384 -binary "$1" | openssl base64 -A)"
}

while IFS=$'\t' read -r name url want_sri; do
  [[ -z "$name" || "$name" == \#* ]] && continue
  dest="$DEST/$name"

  if [[ -f "$dest" ]]; then
    have_sri="$(sri_of "$dest")"
    if [[ "$have_sri" == "$want_sri" ]]; then
      echo "ok    $name"
      continue
    fi
    echo "stale $name (have=$have_sri want=$want_sri)"
    [[ "$MODE" == "--check" ]] && { echo "FAIL: $name SRI mismatch" >&2; exit 1; }
  else
    echo "miss  $name"
    [[ "$MODE" == "--check" ]] && { echo "FAIL: $name missing"      >&2; exit 1; }
  fi

  tmp="$(mktemp)"
  trap 'rm -f "$tmp"' EXIT
  curl -fsSL --proto '=https' --tlsv1.2 --output "$tmp" "$url"
  got_sri="$(sri_of "$tmp")"
  if [[ "$got_sri" != "$want_sri" ]]; then
    echo "FAIL: $name SRI mismatch after download (got=$got_sri want=$want_sri)" >&2
    exit 1
  fi
  mv "$tmp" "$dest"
  trap - EXIT
  echo "fetch $name"
done < "$MANIFEST"

echo "vendor: ok"
```

Companion manifest `tools/vendor-js/manifest.txt` (TAB-separated, `#` comments). Format: `<filename>\t<url>\t<sri>`.

Notes on the script:

- `set -euo pipefail` + `--proto '=https' --tlsv1.2` + atomic `mv` are the three non-negotiables.
- `openssl dgst | openssl base64 -A` is portable across macOS / Linux / BSD without GNU coreutils. `-A` keeps the base64 on one line (default wraps at 76 cols).
- `--check` mode is what CI runs (`go test ./...` then `tools/vendor-js/vendor.sh --check`) — no network, just verify what's committed matches the manifest.
- Trap clears the tmp file if `curl` aborts; the destination is never partially written.
- The manifest file is the single source of pin truth; bumping a version is a one-line PR.

---

## Pinned versions to vendor for r1-server-ui-v2

Manifest entries (SRI placeholders to be filled by running the script once with `--regenerate`, or by copying from the upstream htmx release announcement):

```
# tools/vendor-js/manifest.txt
# <filename>            <url>                                                                              <sri-sha384>
htmx.min.js             https://cdn.jsdelivr.net/npm/htmx.org@2.0.4/dist/htmx.min.js                       sha384-HGfztofotfshcF7+8n44JQL2oJmowVChPTg48S+jvZoztPfvwD79OC/LTtG6dMp+
htmx-ext-sse.js         https://cdn.jsdelivr.net/npm/htmx-ext-sse@2.2.4/dist/sse.js                       sha384-PLACEHOLDER_GENERATE_ON_FIRST_RUN
three.min.js            https://cdn.jsdelivr.net/npm/three@0.170.0/build/three.min.js                     sha384-PLACEHOLDER_GENERATE_ON_FIRST_RUN
three-spritetext.min.js https://cdn.jsdelivr.net/npm/three-spritetext@1.9.5/dist/three-spritetext.min.js  sha384-PLACEHOLDER_GENERATE_ON_FIRST_RUN
3d-force-graph.min.js   https://cdn.jsdelivr.net/npm/3d-force-graph@1.77.0/dist/3d-force-graph.min.js     sha384-PLACEHOLDER_GENERATE_ON_FIRST_RUN
```

Pin choices:

- **htmx 2.0.4** — htmx 2.x stable line, current as of 2025; the SRI hash above is the one published by htmx for unpkg `@2.0.4` and identical for jsdelivr because the file bytes are the same. [bigskysoftware/htmx#3439, devtoolbox.dedyn.io]
- **htmx-ext-sse 2.2.4** — paired with htmx 2.0.x per htmx#3337 (peer-dep on htmx 2.0.2+, works fine with 2.0.4+). The 2.x extension URL pattern is `htmx-ext-sse@<ver>/dist/sse.js`, **not** the legacy `htmx.org/dist/ext/sse.js`. [htmx.org/extensions/sse, htmx 2.0 blog]
- **three 0.170.0** — first 2025 LTS-style line; existing vendor blob is `three.min.js` (matches the global `THREE` build, not the ESM `three.module.js`); keep that style for the visualiser. (Existing `vendor/README.md` references `0.160.0` for the ESM path — bump in a separate PR if/when the ESM cut-over lands.)
- **three-spritetext 1.9.5** — current 2025 line; the existing vendor file dates from 2024 (1.8.x). Compatible with three 0.170.0 (the r161 breakage cited in vasturiano/three-spritetext#43 is long fixed).
- **3d-force-graph 1.77.0** — late-2025 release on the 1.7x line; latest 1.79.1 also acceptable but 1.77.0 has been deployed longer. Pin matches the existing vendor blob's API surface.

First-run procedure (one-time, on a connected machine):

```bash
# 1. Run with PLACEHOLDER hashes — script will fail on mismatch and print the
#    actual computed hash for each file. Copy those into the manifest.
tools/vendor-js/vendor.sh || true

# 2. Open manifest.txt, replace each PLACEHOLDER with the printed sha384-...
#    value reported by the failing run.

# 3. Re-run; this time it should print "fetch <name>" for each file and exit 0.
tools/vendor-js/vendor.sh

# 4. Commit cmd/r1-server/ui/vendor/*.js + manifest.txt.
git add cmd/r1-server/ui/vendor tools/vendor-js
git commit -m "chore(vendor): pin chrome JS deps with SRI"
```

CI then runs `tools/vendor-js/vendor.sh --check` as part of the gate. Air-gapped builds need nothing else — `//go:embed ui/*` consumes the on-disk blobs.

Optional: `three-instancedmesh` helpers are not a separate npm package; instanced-mesh is built into three.js core (`THREE.InstancedMesh`). No additional vendor file needed unless we adopt a separate helper library — re-evaluate when we have a concrete name.

---

## Sources

Accessed 2026-05-05.

- [W3C Subresource Integrity Level 2 spec](https://www.w3.org/TR/sri-2/)
- [MDN — Subresource Integrity](https://developer.mozilla.org/en-US/docs/Web/Security/Defenses/Subresource_Integrity)
- [Transloadit — Verify CDN integrity with sha384sum in browsers](https://transloadit.com/devtips/verify-cdn-integrity-with-sha384sum-in-browsers/)
- [SRI Hash Generator (srihash.org)](https://srihash.org/)
- [Userpilot KB — SRI](https://docs.userpilot.com/developer/security/sri)
- [BlazingCDN — jsDelivr vs unpkg vs cdnjs (2025 outage data)](https://blog.blazingcdn.com/en-us/jsdelivr-vs-unpkg-vs-cdnjs-best-free-cdn-for-open-source-projects)
- [BlazingCDN — Choose the best JS CDN (Skypack/jsDelivr/unpkg)](https://medium.com/@blazingcdn/choose-the-best-js-cdn-compare-npm-alternatives-skypack-jsdelivr-unpkg-bdc981985345)
- [jsDelivr — Migrate from unpkg to jsDelivr](https://www.jsdelivr.com/unpkg)
- [freeCodeCamp PR #59291 — replace unpkg with jsdelivr & cdnjs](https://github.com/freeCodeCamp/freeCodeCamp/pull/59291)
- [htmx.org — SSE extension docs](https://htmx.org/extensions/sse/)
- [htmx 2.0 release blog](https://htmx.org/posts/2024-06-17-htmx-2-0-0-is-released/)
- [bigskysoftware/htmx#3337 — htmx-ext-sse peer-dep on htmx 2.0.x](https://github.com/bigskysoftware/htmx/issues/3337)
- [bigskysoftware/htmx#3439 — possible outdated SSE integrity value](https://github.com/bigskysoftware/htmx/issues/3439)
- [bigskysoftware/htmx#1971 — wrong sha384 integrity hash](https://github.com/bigskysoftware/htmx/issues/1971)
- [htmx-ext-sse on jsDelivr](https://www.jsdelivr.com/package/npm/htmx-ext-sse)
- [vasturiano/3d-force-graph (GitHub)](https://github.com/vasturiano/3d-force-graph)
- [3d-force-graph on npm](https://www.npmjs.com/package/3d-force-graph)
- [three-spritetext on npm](https://www.npmjs.com/package/three-spritetext)
- [vasturiano/three-spritetext#43 — three.js r161 compat](https://github.com/vasturiano/three-spritetext/issues/43)
- [three.js r170 release](https://github.com/mrdoob/three.js/releases/tag/r170)
- [three.js r180 release](https://github.com/mrdoob/three.js/releases/tag/r180)
- [arslan.io — How to write idempotent Bash scripts](https://arslan.io/2019/07/03/how-to-write-idempotent-bash-scripts/)
- [metaist/idempotent-bash](https://github.com/metaist/idempotent-bash)
- [Lloyd Atkinson — Frictions and complexities of "simple" scripts](https://www.lloydatkinson.net/posts/2024/frictions-and-complexities-of-simple-bash-scripts/)
- [oneuptime — Go Vendoring](https://oneuptime.com/blog/post/2026-01-23-go-vendoring/view)
- [VictoriaMetrics — Vendoring, or `go mod vendor`](https://victoriametrics.com/blog/vendoring-go-mod-vendor/)
- [Zhimin Wen — Preparing an Offline Golang Development Environment](https://zhimin-wen.medium.com/preparing-an-offline-golang-development-environment-612981cad5e0)
- [InfraGap — Air-Gapped Development Environments](https://infragap.com/air-gapped/)
- [DevToolbox — htmx: The Complete Guide for 2026](https://devtoolbox.dedyn.io/blog/htmx-complete-guide)
