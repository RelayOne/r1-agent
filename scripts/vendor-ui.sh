#!/usr/bin/env bash
# scripts/vendor-ui.sh — vendor r1-server UI assets at build-tooling time.
#
# Per specs/r1-server-ui-v2-foundation.md §6 and the synthesized cluster
# specs/research/synthesized/ui-v2-retrofit.md (Strategy A — curl + per-file
# SRI shell script + committed blobs):
#
#   Run with no args:   re-fetch every blob, verify SRI, atomic-mv into place.
#                       Re-running with no version/SRI changes produces zero diff.
#   Run with --check:   verify each on-disk blob's SHA-384 matches the SRI table.
#                       No network access; this is the CI gate.
#
# SRI format is "sha384-" + base64(openssl dgst -sha384 -binary FILE). NOT
# sha384sum — the formats differ. See RT-VENDOR-SCRIPT-PATTERNS.md.

set -euo pipefail

ROOT=$(git rev-parse --show-toplevel 2>/dev/null || echo "$(cd "$(dirname "$0")/.." && pwd)")
VENDOR="$ROOT/cmd/r1-server/ui/web/vendor"

# Source URLs are jsdelivr (primary; reliable + IPFS-pinned per
# RT-VENDOR-SCRIPT-PATTERNS). unpkg de-prioritised after the March 2025
# 18-hour outage. Pinned versions per docs/decisions/index.md D-UI2-4.
declare -A URL=(
  ["htmx.min.js"]="https://cdn.jsdelivr.net/npm/htmx.org@2.0.4/dist/htmx.min.js"
  ["htmx-ext-sse.js"]="https://cdn.jsdelivr.net/npm/htmx-ext-sse@2.2.4/sse.js"
  ["three.module.js"]="https://cdn.jsdelivr.net/npm/three@0.170.0/build/three.module.min.js"
  ["three/addons/controls/OrbitControls.js"]="https://cdn.jsdelivr.net/npm/three@0.170.0/examples/jsm/controls/OrbitControls.js"
  ["three-spritetext.js"]="https://cdn.jsdelivr.net/npm/three-spritetext@1.9.5/dist/three-spritetext.mjs"
  ["3d-force-graph.js"]="https://cdn.jsdelivr.net/npm/3d-force-graph@1.77.0/dist/3d-force-graph.mjs"
  ["d3-force-3d.js"]="https://cdn.jsdelivr.net/npm/d3-force-3d@3.0.5/dist/d3-force-3d.js"
)

# SRI table — sha384 base64 of the on-disk binary content. Keep in sync with the
# committed blobs; cmd/r1-server/sri_test.go re-derives these and asserts equality.
declare -A SRI=(
  ["htmx.min.js"]="sha384-HGfztofotfshcF7+8n44JQL2oJmowVChPTg48S+jvZoztPfvwD79OC/LTtG6dMp+"
  ["htmx-ext-sse.js"]="sha384-QA9wXqexhwzXTuTvuF5QP82pddm3R2hy81UzXi7ioNTqNF2b75hlkkSGjafohhL3"
  ["three.module.js"]="sha384-IDC7sAMAIMB/TZ6dgKKPPAKZ2bXXXP8+FBMBC8cU319eBhKITx+PaalhfDkDNH28"
  ["three/addons/controls/OrbitControls.js"]="sha384-aJoe4qqS/DgF2jh9njAuvA6QIveJYoCuYOfYjdFY8P3eawzmc9bEQ40jq2TvOyuP"
  ["three-spritetext.js"]="sha384-lt/KVVv1fBxE0hqFEA6G3lcz4XHTqMGfUJVEueTbcYcaR6AK1ZeY33oV1XEIXEdY"
  ["3d-force-graph.js"]="sha384-7Taov5w4VASddPI/ouZAqBt9tN6eWq3NZTmltQHk9TszgaGncdmSOun1ExAadhXK"
  ["d3-force-3d.js"]="sha384-oWgY/VFCh4i+hboTQZVX9CoaFkXjLZxqEiS6/9vniNwfZupF1j4WAnBTjqA4yKNB"
)

compute_sri() {
  printf 'sha384-%s' "$(openssl dgst -sha384 -binary "$1" | openssl base64 -A)"
}

verify_one() {
  local rel=$1
  local got
  got=$(compute_sri "$VENDOR/$rel")
  if [[ "$got" != "${SRI[$rel]}" ]]; then
    printf 'SRI mismatch for %s\n  got:  %s\n  want: %s\n' "$rel" "$got" "${SRI[$rel]}" >&2
    return 1
  fi
}

fetch_one() {
  local rel=$1
  local url=${URL[$rel]}
  local dst="$VENDOR/$rel"
  local tmp
  tmp=$(mktemp)
  trap 'rm -f "$tmp"' RETURN
  if ! curl -fsSL "$url" -o "$tmp"; then
    printf 'fetch failed: %s\n' "$url" >&2
    return 1
  fi
  local got
  got=$(compute_sri "$tmp")
  if [[ "$got" != "${SRI[$rel]}" ]]; then
    printf 'SRI mismatch on fetch for %s\n  got:  %s\n  want: %s\n  (update SRI table if intentional)\n' "$rel" "$got" "${SRI[$rel]}" >&2
    return 1
  fi
  mkdir -p "$(dirname "$dst")"
  mv "$tmp" "$dst"
  printf 'ok  %s\n' "$rel"
}

mode=fetch
case "${1:-}" in
  --check) mode=check ;;
  -h|--help)
    cat <<'USAGE'
Usage: scripts/vendor-ui.sh [--check]

  (no flag)  Fetch every pinned vendor blob into cmd/r1-server/ui/web/vendor/.
             Verifies SRI on download; aborts on mismatch. Re-running with
             unchanged versions/SRIs produces no diff.
  --check    Verify each on-disk vendor blob against the SRI table. No network.
             Used by CI; matches cmd/r1-server/sri_test.go.
USAGE
    exit 0 ;;
  "") mode=fetch ;;
  *) printf 'unknown arg: %s\n' "$1" >&2; exit 64 ;;
esac

if [[ "$mode" == "check" ]]; then
  failed=0
  for rel in "${!SRI[@]}"; do
    verify_one "$rel" || failed=$((failed + 1))
  done
  if (( failed > 0 )); then
    printf '%d vendor blob(s) failed SRI check\n' "$failed" >&2
    exit 1
  fi
  printf 'all %d vendor blobs match SRI table\n' "${#SRI[@]}"
else
  for rel in "${!SRI[@]}"; do
    fetch_one "$rel"
  done
  printf 'all %d vendor blobs fetched + verified\n' "${#SRI[@]}"
fi
