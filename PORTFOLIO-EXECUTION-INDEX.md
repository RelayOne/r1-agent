# PORTFOLIO-EXECUTION-INDEX.md

**Status:** final.
**Generated:** 2026-04-20 from `/home/claude/verify/findings.md`.
**Updated:** 2026-04-25 — R1 rename (Stoke → R1) complete; GitHub canonical repo is `RelayOne/r1-agent`.
**Purpose:** single flat task list covering all portfolio integration work.

> **R1 rename note (2026-04-25):** Product "Stoke" is now "R1". Repo path remains `/home/eric/repos/stoke/`
> during the local dual-accept window; canonical GitHub home is `github.com/RelayOne/r1-agent`.
> `work-stoke.md` is a pointer stub; canonical work-order is `work-r1.md`.
> All S1-S6 dual-accept phases merged; S5-3 README install-block merged 2026-04-25 (PR #4).
> Remaining: T-S7-1 (this propagation), T-R1P-* parity tasks.

## Portfolio products

| Product | Repo path | GitHub | Role |
|---|---|---|---|
| **R1** (was Stoke) | /home/eric/repos/stoke | github.com/RelayOne/r1-agent | open-source agent runtime + verification layer |
| CloudSwarm | /home/eric/repos/CloudSwarm | github.com/RelayOne/cloudswarm | managed R1 runtime + dashboard |
| TrustPlane (Truecom) | /home/eric/repos/TrustPlane | github.com/RelayOne/truecom | payment + identity + receipts |
| RelayGate (router-core) | /home/eric/repos/router-core | github.com/RelayOne/router-core | LLM router with HMAC receipt ingress |
| RelayOne | /home/eric/repos/RelayOne | github.com/RelayOne/relayone | LLM governance (enforcement gateway + control-plane) |
| Veritize (git-verity) | /home/eric/repos/git-verity | github.com/RelayOne/veritize | embedded RAG + hallucination detection |

## Three tracks

**Integration track (items 0-17, ~14 days)** — cross-product HTTP contract fixes. All 26 defects from `/home/claude/verify/findings.md`.

**Hardening track (items 18-22, ~11 days)** — prompt-injection defense per product, informed by CL4R1T4S corpus.

**Agent-platform track (items 23-27, ~28-38 days)** — R1 expanded to verified general-agent platform with CloudSwarm runtime + TrustPlane commerce.

## Flat task list

| # | Item | Repo | WORK file | Est. | Depends on |
|---|---|---|---|---|---|
| 0 | Integration smoke-test harness | portfolio | see index | 1-2d | — |
| 1 | RelayOne webhook framing + persist hotfix [P0 production] | RelayOne | WORK-relayone Tasks 1+2 | 1h | — |
| 2 | Router-core parser + systemd test | router-core | WORK-router-core Tasks 1+2 | 1-2h | — |
| 3 | TrustPlane commerce-read authz [privilege escalation] | TrustPlane | WORK-trustplane Task 1 | 3-4h | — |
| 4 | TrustPlane commerce primitive scope enforcement | TrustPlane | WORK-trustplane Task 2 | 4-6h | — |
| 5 | TrustPlane HMAC ingress routes | TrustPlane | WORK-trustplane Task 3 | 1.5-2d | 3+4 |
| 6 | TrustPlane WrapResponse correlation_id | TrustPlane | WORK-trustplane Task 4 | 30m | 5 |
| 7 | TrustPlane OpenAPI catch-up | TrustPlane | WORK-trustplane Task 5 | 1d | 3+4+5+6 |
| 8 | TrustPlane cleanup + docs | TrustPlane | WORK-trustplane Tasks 6+7 | 0.5-1d | 3-7 |
| 9 | Router-core release cycle v0.3/v0.4/v0.4.1 | router-core | WORK-router-core Tasks 3-7 | 1d | 2 |
| 10 | RelayOne TP URL + schema + auth rewrite | RelayOne | WORK-relayone Tasks 3-7 | 2-3d | 3+4+5+6 |
| 11 | RelayOne B-suite endpoints | RelayOne | WORK-relayone Task 8 | 1.5-2d | 10 |
| 12 | RelayOne cleanup + docs | RelayOne | WORK-relayone Tasks 9+10 | 1d | 1+10+11 |
| 13 | Veritize URL norm + CHANGELOG + CODEOWNERS + Dockerfile + cleanup | Veritize | WORK-verity Tasks 1-2, 4-5, 7-10 | 1d | — |
| 14 | Veritize B3 path alignment | Veritize | WORK-verity Task 3 | 30m | 11 |
| 15 | Veritize v0.1.0 tag | Veritize | WORK-verity Task 6 | 0.5d | 13 |
| 16 | Veritize v0.1.1 tag | Veritize | — | 30m | 14+15 |
| 17 | End-to-end smoke test | portfolio | — | 1h | 1-16 |
| 18 | R1 prompt-injection hardening | stoke | WORK-r1 Tasks 1-7 | 3-4d | — |
| 19 | RelayOne prompt-injection hardening | RelayOne | WORK-relayone Task 9 (hardening) | 3-4d | 10 |
| 20 | Veritize prompt-injection hardening | Veritize | WORK-verity Task 8 | 2-3d | 15 |
| 21 | TrustPlane prompt-injection hardening | TrustPlane | WORK-trustplane Task 6 | 1-2d | 8 |
| 22 | Router-core prompt-injection hardening | router-core | WORK-router-core Task 6 | 0.5d | 9 |
| 23 | R1 P0 hardening + S-0 foundation [CRITICAL PATH] | stoke | WORK-r1 Tasks 8-10 | 1.5d | — |
| 24 | R1 non-code task foundations | stoke | WORK-r1 Tasks 11-17 | ~5d | 23 |
| 25 | CloudSwarm CS-1..CS-4 | CloudSwarm | WORK-cloudswarm Tasks 1-6 | 1.5d | 23 (+ 24 for CS-3) |
| 26 | TrustPlane TP-1..TP-4 | TrustPlane | WORK-trustplane Task 7 | ~5d | 8 |
| 27 | R1 architecture phases + hireable agent | stoke | WORK-r1 Tasks 18-24 | 3-5w | 23+24+26 |
| 28 | r1-server visual execution trace dashboard | stoke | specs/r1-server.md (RS-1..RS-6) | ~2w | 23 (S-0 stream-to-file) |

**Calendar totals:** ~55 days solo / ~6-7 weeks at 3 engineers (r1-server adds ~2 weeks).

**Recommended immediate start:** item 23 (R1 S-0 + prompt hardening), 1.5 days, unblocks items 24-25 and parts of 27.
