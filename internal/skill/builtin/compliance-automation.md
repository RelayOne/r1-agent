# compliance-automation

> SOC 2, ISO 27001, and GDPR compliance evidence collection embedded in engineering workflows

<!-- keywords: soc2, soc 2, iso 27001, compliance, audit, evidence, gdpr, hipaa, pci, prowler, vanta, drata, policy, access review -->

## Core Principle

Compliance evidence should be a byproduct of good engineering practices, not a separate workstream. Teams embedding controls into CI/CD pipelines reduce audit preparation from 500+ hours to under 170 hours and automate 70-80% of SOC 2 Type II evidence collection.

## SOC 2 Engineering Controls

### CC6 Access Controls (68% of qualified audit opinions)

- **SCIM integration** between HRIS and IdP for automated deprovisioning within 24 hours of termination
- **Quarterly access reviews:** 3-week cycle -- Week 1 extract data, Week 2 review/approve, Week 3 remediate
- **JIT access** (Azure PIM, ConductorOne) provides superior audit trails over standing privileges
- Auditors sample 25-40 instances per control across the full observation period

### CC8 Change Management (maps to PR workflows)

- **Branch protection:** Required PR reviews (1+ approver), dismiss stale reviews on new commits, required status checks, restrict force pushes
- **CODEOWNERS** for sensitive paths (auth, infrastructure, CI/CD configs)
- **Ticket linking:** Branch naming `feature/JIRA-1234-desc`, CI check validates PR references a ticket
- **Separation of duties:** Code author cannot be sole approver. Small teams use detective controls: mandatory peer review + post-deployment audit
- **Emergency changes:** Document P1/P2 process. Alert if emergency changes exceed 5% of total

### CC7 System Operations

- Centralized logging via SIEM (Splunk, Datadog, ELK)
- Vulnerability scanning with realistic SLAs you can actually meet (30-day high is better than 7-day missed half the time)

## Infrastructure Scanning Stack

- **Prowler:** 584 checks for AWS across 41 frameworks (CIS, SOC 2, PCI, HIPAA, NIST). Industry standard for open-source cloud scanning.
- **Checkov:** IaC scanning for Terraform, CloudFormation, Kubernetes, Dockerfile
- **Trivy:** Container images + IaC misconfiguration in a single binary
- **AWS Config:** `Operational-Best-Practices-For-SOC-2` conformance pack with dozens of SOC 2-mapped rules
- **OPA/Rego:** Policy-as-code for custom compliance rules as CI gates

## Encryption Evidence (automated)

- **At rest:** AWS Config rules (`ENCRYPTED_VOLUMES`, `s3-bucket-server-side-encryption-enabled`, `rds-storage-encrypted`) with auto-remediation
- **In transit:** Weekly TLS scans via testssl.sh or SSLyze against endpoint inventory
- **Key management:** CloudTrail logs filtered for `RotateKey`, `CreateKey`, `EnableKeyRotation`
- **Certificates:** cert-manager + Let's Encrypt for automated lifecycle. Daily expiry checks.
- **Service mesh:** Istio `PeerAuthentication` in STRICT mode for automatic mTLS

## Incident Response (auditor-grade)

Automated timeline generation tools (incident.io, Rootly, FireHydrant) capture Slack activity into chronological timelines. Key timestamps auditors need: detection, triage, declaration, containment, eradication, recovery, PIR completion.

Post-incident reviews within 48-72 hours with: executive summary, severity, impact, timeline, root cause (Five Whys), action items with owners and deadlines. Auditors check that action items are tracked to completion.

Evidence preservation: immutable storage (S3 Object Lock) for incident logs, retain for full audit observation period. Annual tabletop exercise with cross-functional participants is required.

## Vulnerability Management SLAs

| Severity | Conservative SLA | CISA Guidance |
|----------|-----------------|---------------|
| Critical | 3-5 days | 15 calendar days |
| High | 14-30 days | 30 calendar days |
| Medium | 60-90 days | -- |
| Low | Next maintenance | -- |

Set SLAs you can meet 100% of the time. Auditors check compliance rate against your stated policy.

## Audit Log Retention (use longest applicable)

- PCI DSS: 12 months (3 months immediately available)
- HIPAA: 6 years
- SOX: 7 years
- GDPR: Proportional (no fixed period)
- NIS 2: 18 months for security incidents

## First SOC 2 Timeline

9-15 months end-to-end. Costs: $30K-$80K audit fees + $5K-$25K/year platform. With automation, internal effort drops to 110-170 hours. Access control issues account for ~68% of qualified opinions -- automate SCIM deprovisioning first.

## Drift Detection

Catches when cloud resources diverge from IaC. Run daily minimum in production. Classify acceptable drift (auto-scaling) vs unauthorized (manual console changes). Use immutable infrastructure patterns to minimize drift surface.
