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
TAG="${TAG:-$(git -C "$(dirname "$0")/.." rev-parse --short HEAD)}"
REGISTRY="us-central1-docker.pkg.dev/$PROJECT/r1"

deploy_one() {
  local svc="$1" env="$2"
  local name="$svc-$env"
  local image="$REGISTRY/$svc:$TAG"

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
    --set-env-vars="R1_ENV=$env,R1_VERSION=$TAG"
  )

  # Service-specific env / secret bindings.
  case "$svc" in
    r1-downloads-cdn)
      args+=(--set-env-vars="R1_BUCKET=relayone-488319-r1-releases")
      ;;
    r1-coord-api)
      # Coord API needs DB access in prod; mount the secret as env var.
      if [[ "$env" == "prod" || "$env" == "staging" || "$env" == "dev" ]]; then
        args+=(--set-secrets="DATABASE_URL=r1-$env-shared-DATABASE_URL:latest")
      fi
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
    for svc in r1-coord-api r1-docs r1-downloads-cdn; do
      deploy_one "$svc" "$env"
    done
  done

  echo
  echo ">> deploy complete; smoke-checking /healthz on all services"
  for env in "${envs[@]}"; do
    for svc in r1-coord-api r1-docs r1-downloads-cdn; do
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
