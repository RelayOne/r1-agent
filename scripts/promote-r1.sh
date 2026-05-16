#!/usr/bin/env bash
# scripts/promote-r1.sh
#
# Scriptable promotion helper for the hosted r1 SaaS surfaces.
# It does three things truthfully:
#   1. confirm that live Cloud Run traffic for an env matches the
#      expected branch tip for that env
#   2. open the gated promotion PR from dev -> staging
#   3. open the gated promotion PR from staging -> main
#
# It does NOT bypass branch protection or merge review requirements.
# Staging/main still require operator review in GitHub.

set -euo pipefail

REPO="${REPO:-RelayOne/r1-agent}"

usage() {
  cat <<'EOF'
Usage:
  bash scripts/promote-r1.sh confirm-live <dev|staging|prod>
  bash scripts/promote-r1.sh open-pr <staging|main>

Commands:
  confirm-live dev      verify live *.dev.r1.run versions match origin/dev
  confirm-live staging  verify live *.staging.r1.run versions match origin/staging
  confirm-live prod     verify live *.r1.run versions match origin/main

  open-pr staging       confirm live dev, then open or print the dev -> staging PR
  open-pr main          confirm live staging, then open or print the staging -> main PR
EOF
}

branch_for_env() {
  case "$1" in
    dev) echo "dev" ;;
    staging) echo "staging" ;;
    prod) echo "main" ;;
    *)
      echo "unknown env: $1" >&2
      return 1
      ;;
  esac
}

subdomain_for_env() {
  case "$1" in
    dev) echo "dev." ;;
    staging) echo "staging." ;;
    prod) echo "" ;;
    *)
      echo "unknown env: $1" >&2
      return 1
      ;;
  esac
}

expected_version_for_env() {
  local env="$1"
  local branch
  branch="$(branch_for_env "$env")"
  git fetch origin "$branch" >/dev/null 2>&1
  git rev-parse "origin/${branch}" | cut -c1-7
}

extract_version() {
  sed -n 's/.*"version":"\([^"]*\)".*/\1/p'
}

check_endpoint() {
  local label="$1" url="$2" want="$3"
  local body got
  body="$(curl -fsSL "$url")"
  got="$(printf '%s' "$body" | extract_version)"
  if [[ -z "$got" ]]; then
    echo "failed: ${label} did not return a version field (${url})" >&2
    echo "$body" >&2
    return 1
  fi
  if [[ "$got" != "$want" ]]; then
    echo "failed: ${label} returned version ${got}, expected ${want} (${url})" >&2
    return 1
  fi
  echo "ok: ${label} -> ${got}"
}

confirm_live() {
  local env="$1"
  local subdomain want
  subdomain="$(subdomain_for_env "$env")"
  want="$(expected_version_for_env "$env")"

  echo "==> confirming live ${env} against ${want}"
  check_endpoint "api" "https://api.${subdomain}r1.run/v1/version" "$want"
  check_endpoint "admin" "https://admin.${subdomain}r1.run/livez" "$want"
  check_endpoint "platform" "https://platform.${subdomain}r1.run/livez" "$want"
  check_endpoint "downloads" "https://downloads.${subdomain}r1.run/livez" "$want"
}

pr_url_for() {
  local base="$1" head="$2"
  gh pr list \
    --repo "$REPO" \
    --state open \
    --base "$base" \
    --head "$head" \
    --json url \
    --jq '.[0].url // ""'
}

open_promotion_pr() {
  local target="$1" base head title body url
  case "$target" in
    staging)
      confirm_live dev
      base="staging"
      head="dev"
      title="promote: dev -> staging"
      body=$'Promotion gate for `dev -> staging`.\n\nPrecondition satisfied:\n- `bash scripts/promote-r1.sh confirm-live dev` passed in the controller worktree.\n'
      ;;
    main)
      confirm_live staging
      base="main"
      head="staging"
      title="promote: staging -> main"
      body=$'Promotion gate for `staging -> main`.\n\nPrecondition satisfied:\n- `bash scripts/promote-r1.sh confirm-live staging` passed in the controller worktree.\n'
      ;;
    *)
      echo "unknown promotion target: $target" >&2
      return 1
      ;;
  esac

  url="$(pr_url_for "$base" "$head")"
  if [[ -n "$url" ]]; then
    echo "$url"
    return 0
  fi

  gh pr create \
    --repo "$REPO" \
    --base "$base" \
    --head "$head" \
    --title "$title" \
    --body "$body"
}

main() {
  if [[ $# -ne 2 ]]; then
    usage
    return 1
  fi

  case "$1" in
    confirm-live) confirm_live "$2" ;;
    open-pr) open_promotion_pr "$2" ;;
    *)
      usage
      return 1
      ;;
  esac
}

main "$@"
