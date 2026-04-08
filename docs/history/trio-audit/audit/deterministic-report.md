# Deterministic Scan Report — 2026-04-01

## Summary
- **Files**: 155 (across 3 repos + .claude-config tooling)
- **Lines**: 33,053
- **Sections**: 47

## Findings by Repo (product code only, sections 9-46)

| Repo | Critical | High | Medium | Notes |
|------|----------|------|--------|-------|
| ember (sections 9-20) | 5 (FP) | 39 | 157 | Mostly HTML placeholder attrs, print stmts |
| flare (sections 21-26) | 6 (4 FP) | 1 | 2 | SQL $N placeholders flagged as "placeholder" |
| stoke (sections 27-46) | 12 (10 FP) | 24 | 27 | Scan patterns self-reference TODO/FIXME terms |
| **TOTAL** | **23** | **64** | **186** | Most criticals are false positives |

## False Positive Analysis
- 19/23 criticals are false positives: HTML `placeholder` attrs, SQL `$N` placeholder construction, and stoke's own scan patterns referencing "TODO/FIXME/placeholder" as detection targets
- True signals: 2 TODOs in flare integration tests (test scaffolding incomplete), 2 in stoke prompts (legitimate references)

## Security Surface
- **745 inputs** identified (env vars, route params, auth tokens, config)
- **22 DB-mapped inputs** (data flows from user input to database)
- **575 secrets/config entries** (env vars, auth headers, tokens)
- Ember has the largest attack surface (web app with auth, billing, GitHub OAuth)
- Flare has minimal surface (internal API, no direct user input)
- Stoke has moderate surface (CLI tool, subprocess execution, API keys)

## Key Signals for Semantic Scan
1. Ember: Auth/billing/OAuth flows need security review, SQL injection surface via db.ts
2. Flare: Integration tests are skeletal (TODOs), reconciler needs concurrency review
3. Stoke: Large main.go (2663 lines), subprocess execution paths, API key handling
4. Cross-repo: stoke/internal/compute/ember.go interfaces with ember — contract alignment needed
5. Cross-repo: flare/sdk/typescript — does ember use this? Integration check needed
