#!/usr/bin/env bash
# services/deploy.sh — deploy r1 SaaS surfaces to Cloud Run.
#
# Usage:
#   ./services/deploy.sh <env>          # one env: prod | staging | dev
#   ./services/deploy.sh all             # all three envs
#
# Per the standing GCP rules:
#   - region us-central1 (Tier 1; cheaper than northamerica-northeast2)
#   - min-instances 1 to avoid cold starts
#   - billing instance-based (verify in Console UI; gcloud sometimes
#     ignores --billing on create)
#   - secret naming: r1-<env>-shared-<KEY> for cross-service,
#                    r1-<env>-<service>-<KEY> for service-specific
#
# Each service gets its own Cloud Run service per env:
#   r1-coord-api-{prod,staging,dev}
#   r1-docs-{prod,staging,dev}
#   r1-downloads-cdn-{prod,staging,dev}
set -euo pipefail

PROJECT="relayone-488319"
REGION="us-central1"
REGISTRY="us-central1-docker.pkg.dev/$PROJECT/r1"

# Resolve the image tag per service. Each service is built independently
# by `gcloud builds submit ... --tag=...:<sha>` at different times, so
# the four services don't share one tag. We default to "the most recent
# tag we can find for each image"; operator can override per-service via
# TAG_<SVC> env (e.g. TAG_R1_COORD_API=244f87d8).
resolve_tag() {
  local svc="$1"
  local override_var="TAG_$(echo "$svc" | tr 'a-z-' 'A-Z_')"
  local override="${!override_var:-}"
  if [[ -n "$override" ]]; then
    echo "$override"
    return
  fi
  if [[ -n "${TAG:-}" ]]; then
    echo "$TAG"
    return
  fi
  # Pick the most recently-pushed tag for this image.
  gcloud artifacts docker images list "$REGISTRY/$svc" \
    --include-tags --sort-by=~UPDATE_TIME --limit=1 \
    --format='value(TAGS)' --project="$PROJECT" 2>/dev/null | head -1
}

deploy_one() {
  local svc="$1" env="$2"
  local name="$svc-$env"
  local tag="$(resolve_tag "$svc")"
  if [[ -z "$tag" ]]; then
    echo "  ! no image tag found for $svc; skipping" >&2
    return 1
  fi
  local image="$REGISTRY/$svc:$tag"

  local args=(
    run deploy "$name"
    --image="$image"
    --region="$REGION"
    --project="$PROJECT"
    --platform=managed
    --allow-unauthenticated
    --min-instances=1
    --max-instances=10
    --concurrency=80
    --cpu=1
    --memory=512Mi
    --port=8080
    --no-cpu-throttling
    --set-env-vars="R1_ENV=$env,R1_VERSION=$tag"
  )

  # Service-specific env / secret bindings.
  case "$svc" in
    r1-downloads-cdn)
      args+=(--set-env-vars="R1_BUCKET=relayone-488319-r1-releases")
      ;;
    r1-coord-api)
      # Coord API needs DB + JWT secret in every env.
      args+=(--set-secrets="DATABASE_URL=r1-$env-shared-DATABASE_URL:latest,AUTH_JWT_SECRET=r1-$env-shared-AUTH_JWT_SECRET:latest")
      ;;
    r1-admin)
      # Admin needs the coord-api URL so its widgets can hit /api/sessions etc.
      args+=(--set-env-vars="R1_COORD_API_URL=https://api.${env/prod/}r1.run")
      ;;
  esac

  echo ">> deploying $name ($image)"
  gcloud "${args[@]}"
}

ensure_bucket_iam() {
  # r1-downloads-cdn needs storage.objectViewer on the releases bucket.
  local svc_account
  svc_account="$(gcloud projects describe "$PROJECT" --format='value(projectNumber)')-compute@developer.gserviceaccount.com"
  gcloud storage buckets add-iam-policy-binding gs://relayone-488319-r1-releases \
    --member="serviceAccount:$svc_account" \
    --role="roles/storage.objectViewer" \
    --project="$PROJECT" \
    --quiet >/dev/null 2>&1 || true
}

main() {
  local target="${1:-all}"

  ensure_bucket_iam

  local envs=()
  if [[ "$target" == "all" ]]; then
    envs=(dev staging prod)
  else
    envs=("$target")
  fi

  for env in "${envs[@]}"; do
    for svc in r1-coord-api r1-docs r1-downloads-cdn r1-admin; do
      deploy_one "$svc" "$env"
    done
  done

  echo
  echo ">> deploy complete; smoke-checking /healthz on all services"
  for env in "${envs[@]}"; do
    for svc in r1-coord-api r1-docs r1-downloads-cdn r1-admin; do
      local url
      url="$(gcloud run services describe "$svc-$env" --region="$REGION" --project="$PROJECT" --format='value(status.url)')"
      printf "%-30s %-50s " "$svc-$env" "$url"
      # Cloud Run org policy intercepts /healthz on this project; use /livez.
      if curl -sSf -m 10 "$url/livez" >/dev/null 2>&1; then
        echo "OK ($url/livez)"
      else
        echo "FAIL ($url/livez)"
      fi
    done
  done
}

main "$@"
