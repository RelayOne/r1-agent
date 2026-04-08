# cloud-aws

> AWS deployment patterns covering compute, storage, networking, security, and cost optimization.

<!-- keywords: aws, lambda, ecs, rds, s3, cloudfront, terraform -->

## Compute Decision Matrix: ECS Fargate vs Lambda vs EC2

| Factor | ECS Fargate | Lambda | EC2 |
|---|---|---|---|
| Startup latency | 30-60s (tasks) | 100ms-10s (cold start) | Minutes (instances) |
| Max duration | Unlimited | 15 min | Unlimited |
| Scaling speed | Minutes | Seconds | Minutes |
| Cost model | Per vCPU-sec + GB-sec | Per request + GB-sec | Per hour (reserved cheaper) |
| Best for | Long-running services, APIs | Event-driven, bursty workloads | GPU, custom kernels, stateful |

1. Default to **Fargate** for web services and APIs -- no instance management, predictable scaling.
2. Use **Lambda** for event handlers (S3 triggers, SQS consumers, API Gateway backends under 29s).
3. Use **EC2** only when you need GPU, custom AMIs, or sustained high-CPU workloads where Reserved Instances save 40-60%.
4. Lambda cold starts: keep functions warm with provisioned concurrency for latency-sensitive paths.

## RDS Configuration

1. **Multi-AZ**: always enable for production. Automatic failover in 60-120s. Doubles cost but is non-negotiable.
2. **Read replicas**: up to 5 per primary. Route analytics and reporting queries to replicas.
3. **Parameter groups**: tune `max_connections`, `shared_buffers`, `work_mem` for your instance class.
4. **Storage**: use gp3 (baseline 3000 IOPS, cheaper than gp2) or io2 for write-heavy workloads.
5. Enable Performance Insights (free tier covers most instances) to identify slow queries.
6. Automated backups with 7-day retention minimum. Test restore quarterly.
7. Use IAM database authentication to eliminate password rotation overhead.

## S3 + CloudFront for Static Assets

1. S3 bucket with `BlockPublicAccess: true`. Serve exclusively through CloudFront OAC (Origin Access Control).
2. CloudFront cache behaviors: `/*` for static, `/api/*` forwarded to ALB origin with no caching.
3. Set `Cache-Control: public, max-age=31536000, immutable` on fingerprinted assets (main.a1b2c3.js).
4. Set `Cache-Control: no-cache` on `index.html` to pick up new asset references immediately.
5. Enable CloudFront Functions for redirects and header manipulation (cheaper than Lambda@Edge).
6. Use S3 Intelligent-Tiering for infrequently accessed uploads (user files, logs).

## VPC Networking

1. **CIDR**: use /16 for VPC (65K IPs), /24 per subnet. Plan for growth.
2. **Public subnets**: ALB, NAT Gateway, bastion (if needed). Auto-assign public IPs.
3. **Private subnets**: ECS tasks, RDS, ElastiCache. No direct internet access.
4. **NAT Gateway**: required for private subnets to reach internet (ECR pulls, external APIs). Deploy one per AZ for HA.
5. **Security groups**: stateful, allow by purpose. Web SG allows 443 from ALB SG. DB SG allows 5432 from App SG only.
6. VPC endpoints for S3, ECR, CloudWatch, and Secrets Manager reduce NAT costs and improve latency.
7. Use AWS PrivateLink for service-to-service communication within AWS.

## IAM Least-Privilege Patterns

1. Never use `*` in resource ARNs for production policies.
2. Use IAM roles (not access keys) for all AWS service interactions.
3. ECS task roles: one role per service, scoped to exactly the resources it needs.
4. Lambda execution roles: attach only the permissions for the specific DynamoDB table, S3 prefix, or SQS queue.
5. Use `aws iam access-analyzer` to identify unused permissions and generate least-privilege policies.
6. SCPs (Service Control Policies) at the organization level to prevent dangerous actions (e.g., disabling CloudTrail).
7. Require MFA for IAM console access and sensitive API calls.

## Cost Optimization

1. **Reserved Instances / Savings Plans**: commit to 1-year for 30% savings, 3-year for 50%. Start with compute Savings Plans (flexible across instance families).
2. **Spot Instances**: 60-90% discount for fault-tolerant batch jobs. Use Spot Fleet with diversified allocation.
3. **Right-sizing**: review CloudWatch CPU/memory utilization monthly. Downsize instances under 30% average utilization.
4. **S3 lifecycle policies**: transition to Glacier after 90 days, delete after 365 days for logs.
5. **NAT Gateway costs** add up fast ($0.045/GB). Use VPC endpoints for AWS service traffic.
6. **Unused resources**: schedule dev/staging environments to shut down outside business hours (Instance Scheduler).
7. Enable Cost Explorer and set budget alerts at 50%, 80%, 100% of expected monthly spend.

## Infrastructure as Code

1. **Terraform**: preferred for multi-cloud or team familiarity. Use remote state in S3 + DynamoDB locking.
2. **CDK**: preferred when team is already TypeScript/Python and wants IDE autocomplete and type safety.
3. Organize Terraform by layer: `networking/`, `data/`, `compute/`, `monitoring/`. Apply in dependency order.
4. Use modules for reusable patterns (VPC, ECS service, RDS cluster). Pin module versions.
5. Run `terraform plan` in CI on every PR. Apply only from CI/CD pipeline, never from developer machines.
6. Tag all resources with `Environment`, `Service`, `Owner`, `CostCenter` for billing and operational visibility.
7. Use `terraform import` to bring existing manually-created resources under management.
