# kubernetes-operations

> Production Kubernetes operations covering resource management, deployments, PDBs, network policies, RBAC, secrets, and cost optimization

<!-- keywords: kubernetes, k8s, deployment, pod, container, helm, kubectl, gke, eks, aks, resources, networking, rbac, secrets, autoscaling -->

## When to Use
- Writing or reviewing Kubernetes manifests (Deployments, Services, PDBs, NetworkPolicies)
- Configuring resource requests/limits, probes, or autoscaling
- Setting up RBAC, service accounts, or secrets management
- Designing GitOps workflows with ArgoCD or Flux
- Optimizing cluster cost or debugging OOM/throttling issues

## When NOT to Use
- Stateless HTTP services that fit well on Cloud Run or Fly.io (fewer than 15 services, fewer than 10 engineers)
- Docker Compose or local development setups
- Serverless-only architectures with no container orchestration

## Behavioral Guidance

### Resource Management

**Set CPU requests but omit CPU limits for most workloads.** CPU is compressible -- limits cause CFS throttling that silently degrades latency. A multi-threaded app with a 0.5 vCPU limit consumes its quota in 12.5ms and is throttled for 87.5ms of the 100ms CFS period.

**Always set memory limits.** Memory is incompressible -- exceeding a limit triggers OOM kill, but without a limit a leak consumes all node memory.

```yaml
resources:
  requests:
    cpu: "250m"        # Based on P95 observed usage
    memory: "512Mi"
  limits:
    # cpu: intentionally omitted
    memory: "1Gi"      # ~2x request for headroom
```

**QoS classes:** Guaranteed (requests==limits for both CPU and memory) for payment processing, databases, latency-critical APIs. Burstable (requests < limits) for most workloads. Never deploy BestEffort (no resource spec) in production.

**VPA + HPA pitfall:** Never let VPA and HPA scale on the same metric. Use HPA on custom metrics (requests/sec, queue depth) and VPA on CPU/memory.

### Deployment Strategy

For zero-downtime critical APIs: `maxSurge: 1, maxUnavailable: 0`. Always pair with `minReadySeconds: 30` and `progressDeadlineSeconds: 600`.

**Pod termination race condition:** Endpoint removal is asynchronous -- pods may receive traffic after SIGTERM. Fix with a preStop sleep:

```yaml
spec:
  terminationGracePeriodSeconds: 60
  containers:
  - lifecycle:
      preStop:
        exec:
          command: ["/bin/sh", "-c", "sleep 20"]
```

`terminationGracePeriodSeconds` must exceed preStop time plus app shutdown time.

### Pod Disruption Budgets

**Prefer `maxUnavailable` over `minAvailable`** -- it adapts automatically when replica count changes via HPA.

**Always set `unhealthyPodEvictionPolicy: AlwaysAllow`** (GA in K8s 1.31). Default behavior deadlocks node drains when CrashLoopBackOff pods count against the disruption budget.

```yaml
apiVersion: policy/v1
kind: PodDisruptionBudget
spec:
  maxUnavailable: 1
  selector:
    matchLabels: { app: api-server }
  unhealthyPodEvictionPolicy: AlwaysAllow
```

### Network Policies

**Apply default-deny to every namespace first:**

```yaml
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: default-deny-all
spec:
  podSelector: {}
  policyTypes: [Ingress, Egress]
```

**Immediately add a DNS allow rule** for UDP/TCP port 53 to `kube-system/kube-dns`. Without it, all DNS resolution breaks.

CNI selection: Cilium for greenfield (L3-L7, FQDN egress, no sidecars). Calico for brownfield, BGP, or Windows. **Flannel does NOT support NetworkPolicy** -- policies are silently ignored.

### RBAC

- Use namespace-scoped `Role` + `RoleBinding` for 95% of cases
- Set `automountServiceAccountToken: false` on every ServiceAccount and pod that does not need API access
- One ServiceAccount per workload, never share the default SA
- Never grant `cluster-admin` to CI/CD
- Never use wildcard verbs (`*`) -- they implicitly grant `escalate`, `bind`, `impersonate`
- Test with: `kubectl auth can-i create deployments --as=system:serviceaccount:ns:sa -n ns`

### Secrets

- Enable encryption at rest (KMS v2 provider preferred)
- **Mount secrets as files, not environment variables.** Env vars require pod restarts for rotation; volume-mounted secrets auto-update within ~1 minute.
- Use External Secrets Operator for cloud secret manager sync (run 2+ replicas)
- For GitOps: SOPS + age/KMS over Sealed Secrets (diff-friendly, multi-cluster)
- Use Workload Identity to eliminate static credentials entirely

### GitOps

- ArgoCD for teams needing visual dashboard and multi-cluster management
- Flux for CLI-first, SOPS-native, edge/air-gapped environments
- **Folder-per-environment on single branch**, not branch-per-environment
- Enable `selfHeal: true` in ArgoCD production apps
- CI never needs cluster credentials -- GitOps controller pulls from Git

### Cost Optimization

- Spot/preemptible nodes save 60-90%; use PDBs and graceful SIGTERM handling
- Karpenter over Cluster Autoscaler on AWS (45-60s vs 3-4 min scale-up)
- Fewer larger nodes are more efficient (less per-node overhead)
- ARM nodes (Graviton/Ampere) are 20-40% cheaper for compatible workloads
- Deploy OpenCost from day one; enforce labels: `team`, `cost-center`, `environment`
- Average clusters waste 70% of requested CPU/memory; right-sizing delivers 30-50% savings

## Gotchas
- **`requests==limits` for Guaranteed QoS.** Both CPU and memory must have requests equal to limits. Omitting one makes it Burstable.
- **Default-deny network policies break DNS.** Always pair egress deny with an explicit DNS allow rule to kube-system.
- **`minAvailable` PDB footgun.** `minAvailable: 1` on a single-replica deployment blocks ALL voluntary disruptions -- node drains hang indefinitely, autoscaler cannot scale down.
- **`failurePolicy: Fail` on admission webhooks.** If the webhook is down, all matching API requests are rejected. Use `Ignore` for non-critical webhooks or ensure HA.
- **Cloud LB health checks bypass `terminationGracePeriodSeconds`.** Cloud load balancers have their own health check intervals that may route traffic to terminating pods. Use preStop hooks.
- **`hostPath` volumes are a security exit.** They grant container access to the host filesystem, bypassing namespace isolation. Block via admission policy.
- **Secrets in env vars leak.** They appear in `kubectl describe pod`, process listings, crash dumps, and child processes. Mount as files instead.
- **HPA targets must reference Deployment, not ReplicaSet.** Targeting a ReplicaSet breaks on rollout because a new RS is created each time.
- **`emptyDir` counts against ephemeral storage limits.** Large temp files in emptyDir can trigger pod eviction when `ephemeral-storage` limits are set.
- **Flannel silently ignores NetworkPolicy.** Policies are accepted by the API server but never enforced. Verify your CNI actually supports enforcement.
- **LimitRange does not validate its own defaults.** If `default` (limit) is less than `defaultRequest`, pods become unschedulable with a confusing error.
- **Liveness probes that check dependencies cause cascading failures.** When a database goes down, every pod restarts simultaneously, turning partial outage into total outage.
- **`latest` image tags make it impossible to know what is deployed.** Always use immutable tags or digests.
