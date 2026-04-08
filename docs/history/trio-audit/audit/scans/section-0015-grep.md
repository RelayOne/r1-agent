# Deterministic Scan
## Findings (critical:0 high:1 medium:20)
- [high] ./ember/devbox/src/routes/credits.ts:45 — Hardcoded localhost:   const appUrl = process.env.APP_URL || "http://localhost:3000";
- [medium] ./ember/devbox/src/routes/credits.ts:11 — TypeScript any: const stripe = new Stripe(process.env.STRIPE_SECRET_KEY!, { apiVersion: "2024-04-10" as any });
- [medium] ./ember/devbox/src/routes/credits.ts:24 — TypeScript any:   const user = c.get("user") as any;
- [medium] ./ember/devbox/src/routes/credits.ts:27 — TypeScript any:   const packageId = (body as any).packageId;
- [medium] ./ember/devbox/src/routes/credits.ts:73 — TypeScript any:   const user = c.get("user") as any;
- [medium] ./ember/devbox/src/routes/github.ts:16 — TypeScript any: const app = new Hono<{ Variables: { user: any; session: any; requestId: string } }>();
- [medium] ./ember/devbox/src/routes/github.ts:30 — TypeScript any:   } catch (e: any) {
- [medium] ./ember/devbox/src/routes/github.ts:38 — TypeScript any:   const user = c.get("user") as any;
- [medium] ./ember/devbox/src/routes/github.ts:73 — TypeScript any:     const user = c.get("user") as any;
- [medium] ./ember/devbox/src/routes/github.ts:79 — TypeScript any:     const ghUser = await ghRes.json() as any;
- [medium] ./ember/devbox/src/routes/github.ts:121 — TypeScript any:   const user = c.get("user") as any;
- [medium] ./ember/devbox/src/routes/github.ts:135 — TypeScript any:   const repos = (await res.json()) as any[];
- [medium] ./ember/devbox/src/routes/github.ts:146 — TypeScript any:   const user = c.get("user") as any;
- [medium] ./ember/devbox/src/routes/github.ts:155 — TypeScript any:   const orgs = (await res.json()) as any[];
- [medium] ./ember/devbox/src/routes/github.ts:160 — TypeScript any:   const user = c.get("user") as any;
- [medium] ./ember/devbox/src/routes/github.ts:170 — TypeScript any:   const repos = (await res.json()) as any[];
- [medium] ./ember/devbox/src/routes/github.ts:195 — TypeScript any:   const user = c.get("user") as any;
- [medium] ./ember/devbox/src/routes/github.ts:206 — TypeScript any:       const ghUser = await ghRes.json() as any;
- [medium] ./ember/devbox/src/routes/github.ts:222 — TypeScript any:     repos: repos.map((r: any) => ({
- [medium] ./ember/devbox/src/routes/github.ts:236 — TypeScript any:   const user = c.get("user") as any;
- [medium] ./ember/devbox/src/routes/github.ts:246 — TypeScript any:       const ghUser = await ghRes.json() as any;

