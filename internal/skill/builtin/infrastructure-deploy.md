# infrastructure-deploy

> Infrastructure patterns for AWS, Docker, Kubernetes, CI/CD, and deployment safety

<!-- keywords: aws, docker, kubernetes, k8s, terraform, ci, cd, deploy, container, ecs, lambda, s3, cloudfront, nginx, load balancer, health check -->

## Critical Rules

1. **Never use `:latest` tag in production.** Pin exact versions: `node:20.11.0-alpine3.19`. `:latest` is mutable — your build is non-reproducible.

2. **Health checks are mandatory.** Every service needs `/healthz` (liveness) and `/readyz` (readiness). Kubernetes and load balancers depend on these.

3. **Secrets in environment variables, not images.** Never bake secrets into Docker images or commit them to git. Use secret managers.

4. **Blue-green or canary deploys only.** Never deploy directly to production. Route 5% traffic to canary, monitor errors, then promote.

5. **Infrastructure as Code, always.** No manual AWS console changes. Terraform/Pulumi/CDK. If it's not in code, it doesn't exist.

## Docker Patterns

### Multi-stage Build
```dockerfile
FROM golang:1.22 AS builder
WORKDIR /app
COPY go.* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /app/server ./cmd/server

FROM gcr.io/distroless/static-debian12
COPY --from=builder /app/server /server
ENTRYPOINT ["/server"]
```
- Builder stage: has build tools, large
- Runtime stage: minimal base, no shell, no package manager

### Container Security
- Run as non-root: `USER 1000:1000`
- Read-only filesystem: `--read-only`
- No new privileges: `--security-opt=no-new-privileges`
- Scan images: Trivy, Snyk, or Grype

## Kubernetes Essentials

### Resource Limits (always set)
```yaml
resources:
  requests:
    cpu: 100m
    memory: 128Mi
  limits:
    cpu: 500m
    memory: 512Mi
```
- Request = guaranteed. Limit = maximum.
- No limits = one pod can starve the node.

### Rolling Update
```yaml
strategy:
  type: RollingUpdate
  rollingUpdate:
    maxUnavailable: 0
    maxSurge: 1
```
- `maxUnavailable: 0` = never fewer than desired pods
- Combined with readiness probe = zero-downtime deploy

## AWS Patterns

### Cost Control
- **Right-size instances.** Most apps are over-provisioned. Start small, scale up.
- **Spot instances for batch/CI.** 60-90% cheaper. Use for non-critical workloads.
- **S3 lifecycle rules.** Move to Glacier after 90 days, delete after 365.
- **Reserved instances** for steady-state workloads (1-3 year commitment).

### Common Architecture
```
CloudFront → ALB → ECS/EKS → RDS (Multi-AZ)
                  ↘ ElastiCache (Redis)
                  ↘ SQS → Lambda (async processing)
```

## Common Gotchas

- **EBS volumes are per-AZ.** Can't mount in different AZ. Use EFS for shared storage.
- **Lambda cold starts.** First invocation adds 1-3s latency. Use provisioned concurrency for latency-sensitive endpoints.
- **ALB idle timeout.** Default 60s. If your backend takes longer, the ALB drops the connection. Set keep-alive > idle timeout.
- **Docker layer caching.** `COPY . .` invalidates cache on any file change. Copy dependency files first, install, then copy source.
- **Terraform state locks.** Always use remote state (S3 + DynamoDB). Local state = team conflicts.
- **DNS propagation.** TTL matters. Set low TTL before migration, wait, then migrate.
