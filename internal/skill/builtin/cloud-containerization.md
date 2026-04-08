# cloud-containerization

> Container and orchestration best practices covering Docker builds, Kubernetes deployments, and operational patterns.

<!-- keywords: docker, kubernetes, k8s, helm, container, orchestration -->

## Multi-Stage Docker Builds

1. Use multi-stage builds to separate build dependencies from runtime image.
```dockerfile
FROM golang:1.22 AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /server ./cmd/server

FROM gcr.io/distroless/static-debian12
COPY --from=builder /server /server
ENTRYPOINT ["/server"]
```
2. Use `distroless` or `alpine` base images. A Go static binary on distroless is ~5 MB vs ~900 MB on `golang:latest`.
3. Order Dockerfile instructions from least to most frequently changing to maximize layer caching.
4. Copy dependency manifests first, install, then copy source -- dependency layers cache independently.
5. Pin base image digests in production: `FROM golang:1.22@sha256:abc123...` to prevent supply chain drift.
6. Run as non-root: `USER 65534` (nobody) in the final stage.
7. Use `.dockerignore` to exclude `.git/`, `node_modules/`, test fixtures, and IDE files.

## Kubernetes Deployment Strategies

1. **Rolling update** (default): gradually replaces pods. Set `maxUnavailable: 0` and `maxSurge: 25%` for zero-downtime.
2. **Blue-green**: deploy new version alongside old, switch traffic atomically via service selector. Requires 2x resources during transition.
3. **Canary**: route a percentage of traffic to the new version. Use Argo Rollouts or Flagger for automated promotion.
4. Set `minReadySeconds: 10` to prevent marking pods as ready before they have handled real traffic.
5. Use `PodDisruptionBudget` to ensure minimum availability during voluntary disruptions (node drain, cluster upgrade).

```yaml
apiVersion: policy/v1
kind: PodDisruptionBudget
spec:
  minAvailable: "50%"
  selector:
    matchLabels:
      app: my-service
```

## Helm Chart Best Practices

1. Use `values.yaml` for defaults, environment-specific overrides in `values-prod.yaml`, `values-staging.yaml`.
2. Template only what varies. Do not template every field -- it makes charts unreadable.
3. Use `helm diff` plugin to review changes before `helm upgrade`.
4. Pin chart versions in `Chart.lock`. Run `helm dependency update` in CI.
5. Include NOTES.txt to print post-install instructions (endpoint URL, credentials location).
6. Validate templates in CI: `helm template . | kubectl apply --dry-run=server -f -`.

## Resource Limits and Requests

1. **Requests** = guaranteed allocation for scheduling. **Limits** = maximum before OOM kill or CPU throttling.
2. Set memory request == limit to avoid OOM surprises. Containers exceeding memory limits are killed immediately.
3. Set CPU request to observed P50 usage, CPU limit to 2-4x request (or omit limit for burstable).
4. Use Vertical Pod Autoscaler (VPA) in recommendation mode to right-size based on actual usage.
5. Namespace-level `ResourceQuota` prevents a single team from consuming the entire cluster.
6. `LimitRange` sets defaults so no pod runs without resource constraints.

## Health Checks

1. **Liveness probe**: restart the pod if it hangs. Check internal health (not downstream dependencies).
   - `httpGet /healthz`, `periodSeconds: 10`, `failureThreshold: 3`.
2. **Readiness probe**: remove from service if unable to handle traffic. Check critical dependencies.
   - `httpGet /readyz`, `periodSeconds: 5`, `failureThreshold: 3`.
3. **Startup probe**: give slow-starting apps time to initialize before liveness kicks in.
   - `httpGet /healthz`, `periodSeconds: 5`, `failureThreshold: 30` (allows up to 150s startup).
4. Never point liveness probes at downstream services -- a slow database should not cause a restart cascade.
5. Readiness probes should return 503 during graceful shutdown (handle SIGTERM, stop accepting new requests).

## Horizontal Pod Autoscaler

```yaml
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
spec:
  minReplicas: 2
  maxReplicas: 20
  metrics:
    - type: Resource
      resource:
        name: cpu
        target:
          type: Utilization
          averageUtilization: 70
  behavior:
    scaleDown:
      stabilizationWindowSeconds: 300
```

1. Scale on CPU for compute-bound services, custom metrics (queue depth, request latency) for I/O-bound.
2. Set `stabilizationWindowSeconds` for scale-down to prevent flapping (5 minutes is a good default).
3. Minimum 2 replicas in production for high availability across zones.
4. Use KEDA for event-driven autoscaling (SQS queue depth, Kafka consumer lag).

## Secret Management

1. Never store secrets in ConfigMaps, environment variables in manifests, or Helm values files.
2. **External Secrets Operator**: syncs secrets from AWS Secrets Manager, Vault, or GCP Secret Manager into Kubernetes Secrets.
3. **Sealed Secrets**: encrypt secrets client-side, safe to commit to Git. Controller decrypts in-cluster.
4. Rotate secrets without pod restart: mount secrets as volumes (auto-updated) instead of env vars.
5. Use workload identity (IRSA on EKS, Workload Identity on GKE) to authenticate pods to cloud secret stores.

## Service Mesh Considerations

1. **When to adopt**: service-to-service mTLS requirement, fine-grained traffic management, or observability gaps.
2. **Istio**: feature-rich (traffic shaping, fault injection, observability). Higher resource overhead (~100 MB per sidecar).
3. **Linkerd**: lightweight, simpler operations, lower overhead (~20 MB per proxy). Good default choice.
4. Start with mTLS and observability. Add traffic management (retries, timeouts, circuit breaking) incrementally.
5. Sidecar injection adds latency (~1ms per hop). Measure before and after to confirm acceptable impact.
6. Consider ambient mesh (Istio ambient mode) to avoid sidecar overhead for simple mTLS use cases.
